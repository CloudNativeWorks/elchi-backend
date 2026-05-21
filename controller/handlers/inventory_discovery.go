package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"golang.org/x/sync/errgroup"

	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/clickhouse"
)

// ----- shared knobs -----

// writeMethods enumerates the HTTP verbs that mutate state. Auth
// coverage defaults to flagging only unauthenticated *writes* because
// public GETs are typically intentional (health, docs, marketing
// pages); a missing auth check on POST/PUT/DELETE/PATCH is almost
// always a misconfig.
var writeMethods = []string{"POST", "PUT", "DELETE", "PATCH"}

// discoveryWindowDefault / Max bound the "new endpoint" lookback
// window. 24h is the operator-friendly default; allowing more than
// 90d would invite full collection scans on busy projects without a
// meaningful security signal (anything older than 90d is not "new").
const (
	discoveryWindowDefault = 24 * time.Hour
	discoveryWindowMax     = 90 * 24 * time.Hour

	// zombieMinSeenDefault filters out endpoints that were never
	// popular to begin with — without this every one-shot probe path
	// shows up as a zombie candidate, drowning the genuine cleanup
	// targets in noise. 1000 is a sane floor for "this was real
	// traffic", tunable via ?min_seen_count=N.
	zombieMinSeenDefault = int64(1000)

	// piiSampleLimit caps the example paths returned per PII
	// category. Five is enough for an at-a-glance UI badge and
	// keeps the response compact even when hundreds of endpoints
	// surface a given category.
	piiSampleLimit = 5
)

// parseWindowDuration parses a "24h", "7d", "30d" shorthand for the
// discoveries endpoint. Returns the configured default when missing
// or unparseable so the call still succeeds with sensible behaviour;
// the cap defends against month-long scans dressed up as queries.
func parseWindowDuration(s string, def, max time.Duration) time.Duration {
	if s == "" {
		return def
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		if d > max {
			return max
		}
		return d
	}
	// Accept "7d" / "30d" too — time.ParseDuration rejects 'd'.
	if strings.HasSuffix(s, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil && n > 0 {
			d := time.Duration(n) * 24 * time.Hour
			if d > max {
				return max
			}
			return d
		}
	}
	return def
}

// ---------------------------------------------------------------------
// 1. Yeni Endpoint Discovery
// ---------------------------------------------------------------------

// ListDiscoveries handles GET /api/v3/inventory/discoveries.
//
// Returns endpoints whose `first_seen` falls inside the requested
// window — the "new APIs shipped without you knowing" report.
//
//	?project=<id>           required
//	?window=24h|7d|30d|72h  optional (default 24h, max 90d)
//	?listener_name=<name>   optional prefix filter
//	?limit=&offset=         standard pagination
//
// Sort: first_seen DESC (newest first) so the dashboard's top rows
// are always the freshest discoveries.
func (h *InventoryHandler) ListDiscoveries(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), mongoQueryTimeout)
	defer cancel()

	userDetails, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return
	}
	projectID := authorization.ExtractProjectFromRequest(c)
	if projectID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project query parameter is required"})
		return
	}
	if err := authorization.ValidateRequestProject(ctx, h.Context.Client, userDetails, projectID); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	window := parseWindowDuration(c.Query("window"), discoveryWindowDefault, discoveryWindowMax)
	cutoff := time.Now().UTC().Add(-window)

	limit, offset := parseLimitOffset(c, listDefaultLimit, listMaxLimit)

	filter := bson.M{
		"project_id": projectID,
		"confirmed":  true, // new-discovery feed shows real endpoints only
		"first_seen": bson.M{"$gte": cutoff},
	}
	if v := strings.TrimSpace(c.Query("listener_name")); v != "" {
		filter["listener_name"] = bson.M{"$regex": "^" + escapeRegex(v)}
	}

	// first_seen DESC default — newest discoveries on top. All four
	// sortable fields are backed by a project-scoped compound index
	// (project_first_seen / project_last_seen / project_seen_count /
	// project_riskscore_lastseen).
	sortBy, sortOrder := parseSort(c,
		[]string{"first_seen", "last_seen", "seen_count", "max_risk_score"},
		"first_seen")

	coll := h.Context.Client.Collection(inventoryCollection)
	total, items, err := countAndFind(ctx, coll, filter, options.Find().
		SetLimit(limit).
		SetSkip(offset).
		SetSort(bson.D{{Key: sortBy, Value: sortOrder}}))
	if err != nil {
		h.Logger.Errorf("discoveries query failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query discoveries"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":         items,
		"window":       window.String(),
		"window_start": cutoff,
		"count":        len(items),
		"total_count":  total,
		"limit":        limit,
		"offset":       offset,
		"sort_by":      sortBy,
		"sort_order":   sortOrderLabel(sortOrder),
	})
}

// sortOrderLabel turns the internal Mongo direction back into the
// public `asc`/`desc` token so the response echoes what was applied.
func sortOrderLabel(order int) string {
	if order == 1 {
		return "asc"
	}
	return "desc"
}

// ---------------------------------------------------------------------
// 3. Auth Coverage Report
// ---------------------------------------------------------------------

// AuthCoverage handles GET /api/v3/inventory/auth-coverage.
//
// Surfaces two distinct authentication problems via ?mode:
//
//	unauthenticated (default) — auth_observed=false: no authenticated
//	  request was ever seen on this endpoint. A missing auth check.
//	inconsistent              — auth_observed=true AND
//	  noauth_observed=true: the endpoint served BOTH authenticated and
//	  unauthenticated traffic. This is the collector's `auth_inconsistent`
//	  signal — almost always a misconfig (a route that should require
//	  auth but has a code path / cache / health-check bypass).
//
// Query params:
//
//	?project=<id>            required
//	?mode=unauthenticated|inconsistent  optional (default unauthenticated)
//	?include_read=true       optional (adds GET/HEAD/OPTIONS)
//	?listener_name=<name>    optional prefix filter
//	?limit=&offset=          standard pagination
//
// Sort: seen_count DESC — high-traffic problem endpoints get fixed first.
func (h *InventoryHandler) AuthCoverage(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), mongoQueryTimeout)
	defer cancel()

	userDetails, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return
	}
	projectID := authorization.ExtractProjectFromRequest(c)
	if projectID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project query parameter is required"})
		return
	}
	if err := authorization.ValidateRequestProject(ctx, h.Context.Client, userDetails, projectID); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	limit, offset := parseLimitOffset(c, listDefaultLimit, listMaxLimit)

	mode := "unauthenticated"
	if c.Query("mode") == "inconsistent" {
		mode = "inconsistent"
	}

	filter := bson.M{"project_id": projectID, "confirmed": true} // auth posture over real endpoints only
	// auth_observed / noauth_observed are written as BSON booleans by
	// elchi-collector. The CH `api_events_raw.auth_observed` column is
	// a uint8, so a future collector revision could switch the
	// inventory fields to 0/1 — `$in: [false, 0]` / `[true, 1]` match
	// both encodings. A plain `0`/`1` would silently match nothing
	// today (Mongo treats int and bool as distinct values).
	switch mode {
	case "inconsistent":
		// Both authed AND un-authed traffic seen — the misconfig case.
		filter["auth_observed"] = bson.M{"$in": bson.A{true, 1}}
		filter["noauth_observed"] = bson.M{"$in": bson.A{true, 1}}
	default:
		// No authenticated request ever observed.
		filter["auth_observed"] = bson.M{"$in": bson.A{false, 0}}
	}
	if c.Query("include_read") != "true" {
		filter["method"] = bson.M{"$in": writeMethods}
	}
	if v := strings.TrimSpace(c.Query("listener_name")); v != "" {
		filter["listener_name"] = bson.M{"$regex": "^" + escapeRegex(v)}
	}

	// seen_count DESC default — fix the loudest problem endpoint first.
	sortBy, sortOrder := parseSort(c,
		[]string{"seen_count", "last_seen", "max_risk_score"},
		"seen_count")

	coll := h.Context.Client.Collection(inventoryCollection)
	total, items, err := countAndFind(ctx, coll, filter, options.Find().
		SetLimit(limit).
		SetSkip(offset).
		SetSort(bson.D{{Key: sortBy, Value: sortOrder}}))
	if err != nil {
		h.Logger.Errorf("auth coverage query failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query auth coverage"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":        items,
		"count":       len(items),
		"total_count": total,
		"limit":       limit,
		"offset":      offset,
		"mode":        mode,
		"scope":       methodsScope(c),
		"sort_by":     sortBy,
		"sort_order":  sortOrderLabel(sortOrder),
	})
}

// methodsScope returns the human-readable description of which
// methods AuthCoverage filtered to, so the UI can render a chip /
// caption matching the data.
func methodsScope(c *gin.Context) string {
	if c.Query("include_read") == "true" {
		return "all_methods"
	}
	return "write_methods"
}

// ---------------------------------------------------------------------
// 4. Bot/Scanner Heatmap
// ---------------------------------------------------------------------

// BotScannerHeatmap handles GET /api/v3/inventory/bot-scanner.
//
// Returns the bot/scanner traffic distribution across endpoints over
// the requested time window — backed by ClickHouse, scoped via
// `tags['ua.kind'] IN ('bot','scanner')`. Each row is a
// (path, family, kind) cell suitable for a UI heatmap.
//
//	?project=<id>            required
//	?listener_name=<name>    optional exact-match
//	?from=&to=               optional (default 24h, max 7d)
//	?top=N                   optional (default 50, max 200)
//
// 503 when ClickHouse is not configured — same contract as the rest
// of the CH-backed inventory endpoints.
func (h *InventoryHandler) BotScannerHeatmap(c *gin.Context) {
	if h.Context.Clickhouse == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "clickhouse not configured; bot/scanner heatmap unavailable"})
		return
	}

	userDetails, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return
	}
	projectID := authorization.ExtractProjectFromRequest(c)
	if projectID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project query parameter is required"})
		return
	}
	parentCtx := c.Request.Context()
	if err := authorization.ValidateRequestProject(parentCtx, h.Context.Client, userDetails, projectID); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	from, to, perr := parseTimeWindow(c, detailDefaultRangeMs, detailMaxRangeDays)
	if perr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": perr.Error()})
		return
	}

	top := 0
	if v := c.Query("top"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			top = n
		}
	}

	hits, qerr := h.Context.Clickhouse.QueryBotScannerHeatmap(parentCtx, clickhouse.BotScannerFilter{
		ProjectID:    projectID,
		ListenerName: strings.TrimSpace(c.Query("listener_name")),
		From:         from,
		To:           to,
		Top:          top,
	})
	if qerr != nil {
		h.Logger.Errorf("bot/scanner heatmap failed: %v", qerr)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to query bot/scanner heatmap"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":  hits,
		"count": len(hits),
		"from":  from,
		"to":    to,
	})
}

// ---------------------------------------------------------------------
// 5. PII Endpoint Inventory
// ---------------------------------------------------------------------

// PIIInventory handles GET /api/v3/inventory/pii.
//
// Groups endpoints by the `pii_categories` field — collector tags
// each event with the PII types it detected in headers/payload, and
// the inventory document carries the union of categories observed
// over the endpoint's lifetime.
//
// Response shape favours the UI's "GDPR overview" panel: per
// category we emit an aggregate count and a small sample of paths.
// The flat list of every PII-bearing endpoint is also returned so
// the UI can render a secondary table without a second roundtrip.
//
//	?project=<id>           required
//	?listener_name=<name>   optional prefix filter
//	?limit=&offset=         pagination for the flat endpoints list
func (h *InventoryHandler) PIIInventory(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), mongoQueryTimeout)
	defer cancel()

	userDetails, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return
	}
	projectID := authorization.ExtractProjectFromRequest(c)
	if projectID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project query parameter is required"})
		return
	}
	if err := authorization.ValidateRequestProject(ctx, h.Context.Client, userDetails, projectID); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	limit, offset := parseLimitOffset(c, listDefaultLimit, listMaxLimit)

	filter := bson.M{
		"project_id": projectID,
		"confirmed":  true, // PII inventory over real endpoints only
		// $not + $size:0 matches both missing-field AND empty-array
		// documents on Mongo — `{pii_categories: {$ne: []}}` alone
		// would still return docs with the field absent on older
		// schemas.
		"pii_categories": bson.M{"$exists": true, "$not": bson.M{"$size": 0}},
	}
	if v := strings.TrimSpace(c.Query("listener_name")); v != "" {
		filter["listener_name"] = bson.M{"$regex": "^" + escapeRegex(v)}
	}

	coll := h.Context.Client.Collection(inventoryCollection)

	// Two-pass strategy: a cheap `$sortByCount` over the unwound
	// categories gives us counts per category without buffering any
	// sample arrays, and per-category sample fetches run in parallel
	// (one Find per category, capped at piiSampleLimit rows).
	//
	// The previous one-shot pipeline used `$push` to collect every
	// endpoint per category, then `$slice` to keep only 5 — which
	// risked blowing the 16 MB BSON pipeline-doc limit on busy
	// projects (50 K endpoints × ~150 bytes ≈ 7.5 MB per category,
	// across N categories the total spilled to disk under
	// allowDiskUse and burned hundreds of MB of working set).
	categoryCounts, err := h.piiCategoryCounts(ctx, coll, filter)
	if err != nil {
		h.Logger.Errorf("pii categories aggregation failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to aggregate pii categories"})
		return
	}
	categories, err := h.piiCategorySamples(ctx, coll, filter, categoryCounts)
	if err != nil {
		h.Logger.Errorf("pii category samples failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch pii category samples"})
		return
	}

	// last_seen DESC default for the flat endpoints list. The
	// per-category `categories` block stays count-sorted (sortByCount).
	sortBy, sortOrder := parseSort(c,
		[]string{"last_seen", "seen_count", "max_risk_score"},
		"last_seen")

	total, items, err := countAndFind(ctx, coll, filter, options.Find().
		SetLimit(limit).
		SetSkip(offset).
		SetSort(bson.D{{Key: sortBy, Value: sortOrder}}))
	if err != nil {
		h.Logger.Errorf("pii endpoints query failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query pii endpoints"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"categories":  categories,
		"endpoints":   items,
		"count":       len(items),
		"total_count": total,
		"limit":       limit,
		"offset":      offset,
		"sort_by":     sortBy,
		"sort_order":  sortOrderLabel(sortOrder),
	})
}

// piiCategoryCount carries one row from the categories aggregate.
// Exported field names match the JSON contract the UI consumes.
type piiCategoryCount struct {
	Category      string `bson:"_id" json:"category"`
	EndpointCount int64  `bson:"count" json:"endpoint_count"`
}

// piiCategoryCounts runs the lightweight first pass: $match + $unwind
// + $sortByCount yields the per-category endpoint counts WITHOUT
// pulling any sample documents into pipeline memory. Result ordering
// is high-count first so the UI gets the dominant categories at the
// top of its panel.
func (h *InventoryHandler) piiCategoryCounts(ctx context.Context, coll *mongo.Collection, filter bson.M) ([]piiCategoryCount, error) {
	pipeline := []bson.M{
		{"$match": filter},
		{"$unwind": "$pii_categories"},
		{"$sortByCount": "$pii_categories"},
	}
	cur, err := coll.Aggregate(ctx, pipeline, options.Aggregate().SetAllowDiskUse(true))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := []piiCategoryCount{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// piiCategorySample is one sample row attached to a category in the
// final response shape.
type piiCategorySample struct {
	ListenerName   string `bson:"listener_name"   json:"listener_name"`
	NormalizedPath string `bson:"normalized_path" json:"normalized_path"`
	Method         string `bson:"method"          json:"method"`
}

// piiCategorySamples fans out per-category sample fetches in
// parallel; each Find pulls at most piiSampleLimit rows sorted by
// last_seen DESC. Compared with the previous one-shot aggregation
// the memory ceiling drops from O(endpoints × samples) to O(N
// categories × piiSampleLimit) — five rows per category, regardless
// of how big the underlying inventory is.
//
// errgroup keeps the wall time at max(individual fetch); the
// (typically small, <20) category count means we never exhaust the
// Mongo connection pool here.
func (h *InventoryHandler) piiCategorySamples(ctx context.Context, coll *mongo.Collection, baseFilter bson.M, counts []piiCategoryCount) ([]bson.M, error) {
	if len(counts) == 0 {
		return []bson.M{}, nil
	}
	type catWithSamples struct {
		category piiCategoryCount
		samples  []piiCategorySample
	}
	results := make([]catWithSamples, len(counts))

	g, gctx := errgroup.WithContext(ctx)
	for i, cc := range counts {
		i, cc := i, cc
		g.Go(func() error {
			// Per-category filter: clone the base and pin pii_categories
			// to the single category we're sampling for. Cloning protects
			// against the loop racing on a shared bson.M.
			perCat := bson.M{}
			for k, v := range baseFilter {
				perCat[k] = v
			}
			perCat["pii_categories"] = cc.Category

			cur, err := coll.Find(gctx, perCat, options.Find().
				SetLimit(int64(piiSampleLimit)).
				SetSort(bson.D{{Key: "last_seen", Value: -1}}).
				SetProjection(bson.M{
					"_id":             0,
					"listener_name":   1,
					"normalized_path": 1,
					"method":          1,
				}))
			if err != nil {
				return fmt.Errorf("find samples for %q: %w", cc.Category, err)
			}
			defer cur.Close(gctx)
			var samples []piiCategorySample
			if err := cur.All(gctx, &samples); err != nil {
				return fmt.Errorf("decode samples for %q: %w", cc.Category, err)
			}
			results[i] = catWithSamples{category: cc, samples: samples}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Preserve the input ordering (high-count first) so the UI's
	// panel renders categories sorted by impact.
	out := make([]bson.M, 0, len(results))
	for _, r := range results {
		samples := r.samples
		if samples == nil {
			samples = []piiCategorySample{}
		}
		out = append(out, bson.M{
			"category":       r.category.Category,
			"endpoint_count": r.category.EndpointCount,
			"samples":        samples,
		})
	}
	return out, nil
}

// ---------------------------------------------------------------------
// 8. Zombie API Detection
// ---------------------------------------------------------------------

// ListZombies handles GET /api/v3/inventory/zombies.
//
// Returns endpoints that have gone quiet — `last_seen` older than
// the inactive cutoff (default 30d) AND historically popular
// (`seen_count >= min_seen_count`, default 1000). The combination
// surfaces cleanup candidates the operator actually cares about:
// orphan paths that were once serving real traffic.
//
//	?project=<id>           required
//	?inactive_days=N        optional (default 30, max 365)
//	?min_seen_count=N       optional (default 1000)
//	?listener_name=<name>   optional prefix filter
//	?limit=&offset=         standard pagination
//
// Sort: seen_count DESC — the loudest-once-now-silent endpoint is the
// strongest deprecation candidate.
func (h *InventoryHandler) ListZombies(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), mongoQueryTimeout)
	defer cancel()

	userDetails, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return
	}
	projectID := authorization.ExtractProjectFromRequest(c)
	if projectID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project query parameter is required"})
		return
	}
	if err := authorization.ValidateRequestProject(ctx, h.Context.Client, userDetails, projectID); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	inactiveDays := 30
	if v := c.Query("inactive_days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 365 {
				n = 365
			}
			inactiveDays = n
		}
	}
	cutoff := time.Now().UTC().Add(-time.Duration(inactiveDays) * 24 * time.Hour)

	minSeen := zombieMinSeenDefault
	if v := c.Query("min_seen_count"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			minSeen = n
		}
	}

	limit, offset := parseLimitOffset(c, listDefaultLimit, listMaxLimit)

	filter := bson.M{
		"project_id": projectID,
		"confirmed":  true, // zombie = stale REAL endpoints, not dormant scanner probes
		"last_seen":  bson.M{"$lt": cutoff},
		"seen_count": bson.M{"$gte": minSeen},
	}
	if v := strings.TrimSpace(c.Query("listener_name")); v != "" {
		filter["listener_name"] = bson.M{"$regex": "^" + escapeRegex(v)}
	}

	// seen_count DESC default — the loudest-once-now-silent endpoint
	// is the strongest deprecation candidate.
	sortBy, sortOrder := parseSort(c,
		[]string{"seen_count", "last_seen", "max_risk_score"},
		"seen_count")

	coll := h.Context.Client.Collection(inventoryCollection)
	total, items, err := countAndFind(ctx, coll, filter, options.Find().
		SetLimit(limit).
		SetSkip(offset).
		SetSort(bson.D{{Key: sortBy, Value: sortOrder}}))
	if err != nil {
		h.Logger.Errorf("zombies query failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query zombies"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":           items,
		"count":          len(items),
		"total_count":    total,
		"inactive_days":  inactiveDays,
		"inactive_since": cutoff,
		"min_seen_count": minSeen,
		"limit":          limit,
		"offset":         offset,
		"sort_by":        sortBy,
		"sort_order":     sortOrderLabel(sortOrder),
	})
}

// ---------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------

// parseSort resolves ?sort_by / ?sort_order against a per-endpoint
// whitelist. An unknown or missing sort_by silently falls back to
// defaultField — the same forgiving contract the inventory list
// endpoint uses (never a 400 for a bad sort param). Returns the Mongo
// sort direction: -1 for desc (default) or 1 for asc.
//
// The whitelist is the index-safety guard: only fields backed by a
// project-scoped compound index should appear in `allowed`, otherwise
// a caller could trigger an unbounded in-memory sort on a large
// project.
func parseSort(c *gin.Context, allowed []string, defaultField string) (string, int) {
	field := defaultField
	if v := strings.TrimSpace(c.Query("sort_by")); v != "" {
		for _, a := range allowed {
			if v == a {
				field = v
				break
			}
		}
	}
	order := -1
	if c.Query("sort_order") == "asc" {
		order = 1
	}
	return field, order
}

// parseLimitOffset is the standard pagination knob parser, clamped
// to the inventory list ceilings. Lives here rather than in
// inventory.go so the discovery endpoints stay self-contained.
func parseLimitOffset(c *gin.Context, def, max int) (int64, int64) {
	limit := int64(def)
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 {
		if v > max {
			v = max
		}
		limit = int64(v)
	}
	offset := int64(0)
	if v, err := strconv.Atoi(c.Query("offset")); err == nil && v >= 0 {
		offset = int64(v)
	}
	return limit, offset
}

// countAndFind runs CountDocuments and Find in parallel — same
// pattern ListInventory uses. errgroup.WithContext gives proper
// fail-fast: the first error cancels gctx so the sibling Mongo call
// returns immediately instead of running to the parent's 15s ceiling
// while we already know the response will be a 500. Wall time stays
// at max(count, find) on the happy path.
func countAndFind(ctx context.Context, coll *mongo.Collection, filter bson.M, opts *options.FindOptions) (int64, []bson.M, error) {
	var (
		total int64
		items = []bson.M{}
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		n, err := coll.CountDocuments(gctx, filter)
		if err != nil {
			return fmt.Errorf("count: %w", err)
		}
		total = n
		return nil
	})
	g.Go(func() error {
		cur, err := coll.Find(gctx, filter, opts)
		if err != nil {
			return fmt.Errorf("find: %w", err)
		}
		defer cur.Close(gctx)
		if err := cur.All(gctx, &items); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		return nil
	})
	if err := g.Wait(); err != nil {
		return 0, nil, err
	}
	return total, items, nil
}

// escapeRegex sanitises a user-supplied prefix against regex
// metacharacters. The inventory list endpoint uses
// security.SanitizeSearchInput for the same purpose; this helper
// stays local so the discovery endpoints don't have to import the
// security package's whole surface just for one call.
func escapeRegex(s string) string {
	// Reuse the same regex metas the security package guards
	// against. Order matters: escape \ first.
	const meta = `\.+*?()|[]{}^$`
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if strings.ContainsRune(meta, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
