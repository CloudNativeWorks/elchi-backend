package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"golang.org/x/sync/errgroup"

	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/clickhouse"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/security"
)

// InventoryHandler exposes the read-only API surface that backs the
// "API Inventory" UI. All write paths to MongoDB `api_inventory` and
// ClickHouse `api_events_*` are owned by elchi-collector — this
// handler only reads. Schema is the contract pinned by
// elchi-collector/docs/schema.md.
type InventoryHandler struct {
	Context *db.AppContext
	Logger  *logger.Logger
}

// NewInventoryHandler constructs an InventoryHandler bound to the
// shared AppContext (Mongo + optional ClickHouse client).
func NewInventoryHandler(ctx *db.AppContext, log *logger.Logger) *InventoryHandler {
	return &InventoryHandler{Context: ctx, Logger: log}
}

const (
	inventoryCollection = "api_inventory"

	// Detail endpoint guards. ClickHouse OFFSET/scan cost grows with the
	// time window, so cap the request to a sensible maximum and clamp
	// page sizes that would otherwise pull megabytes per request.
	detailMaxRangeDays   = 7
	detailDefaultRangeMs = int64(time.Hour / time.Millisecond) // 1h
	detailMaxLimit       = 500
	detailDefaultLimit   = 50

	listDefaultLimit = 20
	listMaxLimit     = 100

	// mongoQueryTimeout caps every MongoDB roundtrip from these
	// handlers. The request context alone only covers client cancels —
	// without an explicit deadline, a slow / backed-up cluster keeps
	// the goroutine and HTTP socket pinned until Mongo finally
	// answers. 15s is comfortably above healthy p99 (single-digit ms)
	// while small enough to fail fast under degradation.
	mongoQueryTimeout = 15 * time.Second
)

// listFilter mirrors PaginationRequest's role for the inventory schema
// (root-level fields, not nested under `general`). Only fields with a
// MongoDB index get a dedicated query parameter — the rest are derived
// from the indexed columns or trimmed by the caller via
// limit+offset.
type listFilter struct {
	ListenerName   string
	Host           string
	Method         []string
	NormalizedPath string
	Protocol       []string
	RiskFlags      []string
	EndpointCats   []string
	PIICategories  []string
	MinRiskScore   int
	MaxRiskScore   int
	LastSeenFrom   time.Time
	LastSeenTo     time.Time
	SortBy         string
	SortOrder      int // 1 / -1
	Limit          int64
	Offset         int64
	// CH-cross filters. When ANY of these is set, the handler hits
	// ClickHouse first to discover the (listener_name,
	// normalized_path) pairs that match in the time window, and
	// feeds the result into the MongoDB query as a `$or` set.
	// Country / ASN read tags[geo.*]; SourceIP / UserAgent are the
	// raw columns shipped by the collector starting from migration
	// 005 (default-on, empty when the collector opts out for
	// compliance). Time window defaults to the raw retention cap
	// (7d) when From/To not supplied via last_seen_*.
	Country   string
	ASN       string
	SourceIP  string
	UserAgent string
}

// ListInventoryListeners handles GET /api/v3/inventory/listeners.
//
// Returns one row per (listener_name, project_id) tuple — the
// project's discovered API surface bucketed by listener, which is
// what the "API Discovery" left-nav table renders. Each row
// aggregates the underlying api_inventory documents for that
// listener:
//
//	listener_name           — group key
//	project_id              — group key
//	normalized_path_count   — distinct normalized_path count under this listener
//	hostnames               — distinct host values (deduped, capped at 32 in the response)
//	risk_flags              — union of every risk_flag observed across the group
//	last_seen               — max(last_seen) across the group
//	status_dist             — merged status code histogram (sum per code)
//
// Project query parameter is REQUIRED — without it we'd otherwise
// scan/leak across projects. authorization.ValidateRequestProject
// enforces the user's access to the requested project (Owner bypass;
// Admin in base_project; Editor/Viewer in user.Projects).
//
// Pagination + optional filters share the parseInventoryListFilter
// shape so the UI can pass the same query knobs as the flat list.
func (h *InventoryHandler) ListInventoryListeners(c *gin.Context) {
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

	f := parseInventoryListFilter(c)

	matchStage := bson.M{"project_id": projectID}
	applyInventoryListFilter(matchStage, f)

	// Pipeline:
	//   1. $match  — project + optional flat filters; uses
	//                project_last_seen / project_riskscore_lastseen
	//                indexes for the project_id leg.
	//   2. $group  — bucket by (listener_name, project_id). $addToSet
	//                dedupes hosts; $max gives last_seen; status_dist
	//                is reduced into a single map below via $project.
	//   3. $project — derive normalized_path_count, slice hostnames
	//                 to maxHostsPerGroup, dedupe risk_flags, merge
	//                 status_dist maps via $reduce + $objectToArray.
	//   4. $facet  — total_count + paginated data in one roundtrip
	//                so the UI's "X of Y listeners" badge stays cheap.
	//                $facet is heavier than two queries on huge sets,
	//                but project_last_seen index keeps us in the
	//                hundreds-of-thousands ceiling (cardinality cap is
	//                100K endpoints per collector replica per schema.md).
	listenerName := strings.TrimSpace(c.Query("listener_name"))
	if listenerName != "" {
		// listener_name on the grouped endpoint is a prefix filter
		// applied AFTER group so the operator can search across
		// listeners without hitting the slow regex on raw docs.
		// Sanitised by SanitizeSearchInput inside applyInventoryListFilter
		// already; we re-anchor here for the post-group $match.
		if san, ok := security.SanitizeSearchInput(listenerName); ok && san != "" {
			matchStage["listener_name"] = bson.M{"$regex": "^" + san}
		}
	}

	// Sort whitelist for the grouped result. Both fields exist in the
	// $project output ahead of the $sort stage: last_seen is carried
	// through, normalized_path_count is the $size of distinct_paths.
	// Anything else falls back to last_seen.
	listenerSortBy, listenerSortOrder := parseSort(c,
		[]string{"last_seen", "normalized_path_count"}, "last_seen")

	const maxHostsPerGroup = 32
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: matchStage}},
		{{Key: "$group", Value: bson.M{
			"_id": bson.M{
				"listener_name": "$listener_name",
				"project_id":    "$project_id",
			},
			"distinct_paths": bson.M{"$addToSet": "$normalized_path"},
			"hostnames":      bson.M{"$addToSet": "$host"},
			"risk_flags":     bson.M{"$push": "$risk_flags"},
			"last_seen":      bson.M{"$max": "$last_seen"},
			"status_dists":   bson.M{"$push": "$status_dist"},
		}}},
		// $project intentionally leaves `status_dists` as the raw
		// array-of-maps and merges it Go-side after the cursor decode.
		// The previous in-pipeline merge ($reduce + $setUnion +
		// $arrayToObject + nested $reduce-with-$convert) is O(N×K) per
		// group and has run into TypeMismatch / 16 MB doc-size limits
		// on heterogeneously-typed collector writes. Doing the same sum
		// over a `map[string]int64` is microseconds of CPU, removes the
		// pipeline-cost cliff, and tolerates string-vs-int bucket
		// values without explicit BSON `$convert`s.
		{{Key: "$project", Value: bson.M{
			"_id":                   0,
			"listener_name":         "$_id.listener_name",
			"project_id":            "$_id.project_id",
			"normalized_path_count": bson.M{"$size": bson.M{"$ifNull": bson.A{"$distinct_paths", bson.A{}}}},
			// Hostnames: $addToSet already deduped; strip empty strings
			// (collector writes "" for TCP traffic / Host-less HTTP) and
			// cap to maxHostsPerGroup so a noisy listener can't blow up
			// the response payload.
			"hostnames": bson.M{"$slice": bson.A{
				bson.M{"$filter": bson.M{
					"input": bson.M{"$ifNull": bson.A{"$hostnames", bson.A{}}},
					"as":    "h",
					"cond": bson.M{"$and": bson.A{
						bson.M{"$ne": bson.A{"$$h", ""}},
						bson.M{"$ne": bson.A{"$$h", nil}},
					}},
				}},
				maxHostsPerGroup,
			}},
			// risk_flags: $push gathered an array-of-arrays; reduce +
			// $setUnion gives us the unique flag set across the group.
			"risk_flags": bson.M{"$reduce": bson.M{
				"input":        "$risk_flags",
				"initialValue": bson.A{},
				"in": bson.M{"$setUnion": bson.A{
					"$$value",
					bson.M{"$ifNull": bson.A{"$$this", bson.A{}}},
				}},
			}},
			"last_seen":    1,
			"status_dists": 1,
		}}},
		{{Key: "$sort", Value: bson.M{listenerSortBy: listenerSortOrder}}},
		{{Key: "$facet", Value: bson.M{
			"data": []bson.M{
				{"$skip": f.Offset},
				{"$limit": f.Limit},
			},
			"meta": []bson.M{
				{"$count": "total"},
			},
		}}},
	}

	coll := h.Context.Client.Collection(inventoryCollection)
	// SetAllowDiskUse(true): the $group stage can buffer per-listener
	// arrays (status_dists, hostnames, distinct_paths) that exceed
	// the in-memory aggregation budget on busy projects. Letting the
	// server spill to disk trades a touch of latency for survival on
	// edge cases — the alternative is a hard error from the planner
	// when a single listener has tens of thousands of inventory rows.
	cur, err := coll.Aggregate(ctx, pipeline, options.Aggregate().SetAllowDiskUse(true))
	if err != nil {
		h.Logger.Errorf("inventory listeners aggregate failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to aggregate listener summary"})
		return
	}
	defer cur.Close(ctx)

	type facetResult struct {
		Data []bson.M `bson:"data"`
		Meta []struct {
			Total int64 `bson:"total"`
		} `bson:"meta"`
	}
	var facets []facetResult
	if err := cur.All(ctx, &facets); err != nil {
		h.Logger.Errorf("inventory listeners decode failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to decode listener summary"})
		return
	}

	var (
		data  []bson.M
		total int64
	)
	if len(facets) > 0 {
		data = facets[0].Data
		if len(facets[0].Meta) > 0 {
			total = facets[0].Meta[0].Total
		}
	}
	if data == nil {
		data = []bson.M{}
	}
	// Go-side status_dist merge: collapse each row's array-of-maps
	// (collected via $push in the group stage) into a single
	// `status_dist: {code -> count}` value. Tolerant of
	// heterogeneously-typed bucket values (collector wrote Long for
	// some docs and numeric-string for others) — see toInt64Loose.
	for _, row := range data {
		row["status_dist"] = mergeStatusDists(row["status_dists"])
		delete(row, "status_dists")
	}
	resp := buildPaginationResponse(data, total, f.Limit, f.Offset)
	resp["sort_by"] = listenerSortBy
	resp["sort_order"] = sortOrderLabel(listenerSortOrder)
	c.JSON(http.StatusOK, resp)
}

// mergeStatusDists folds the array-of-maps emitted by the $push stage
// into a single {code -> count} histogram. Driver decodes the field
// as `bson.A` of `bson.M`; anything else (nil, wrong type) yields an
// empty map so the JSON response never carries `null` for this key.
func mergeStatusDists(v any) map[string]int64 {
	out := map[string]int64{}
	arr, ok := v.(bson.A)
	if !ok {
		return out
	}
	for _, entry := range arr {
		dist, ok := entry.(bson.M)
		if !ok {
			continue
		}
		for code, raw := range dist {
			out[code] += toInt64Loose(raw)
		}
	}
	return out
}

// toInt64Loose coerces the mixed numeric types the Mongo driver hands
// back (int32 from a BSON `int`, int64 from a BSON `long`, float64
// from a `double`, plus the legacy string-encoded counts some
// collector versions emitted) into a single int64. Unparseable values
// fall back to 0 so a single corrupt bucket cannot crash the
// pipeline.
func toInt64Loose(v any) int64 {
	switch n := v.(type) {
	case int32:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	case string:
		if i, err := strconv.ParseInt(n, 10, 64); err == nil {
			return i
		}
	}
	return 0
}

// ListInventory handles GET /api/v3/inventory.
//
// Auth: project-scoped via authorization.ValidateRequestProject (Owner
// bypass; Admin in base_project; Editor/Viewer in user.Projects).
// MongoDB filter is `project_id` (collector schema) — there are no
// per-resource permissions on inventory rows.
func (h *InventoryHandler) ListInventory(c *gin.Context) {
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

	f := parseInventoryListFilter(c)

	mongoFilter := bson.M{"project_id": projectID}
	applyInventoryListFilter(mongoFilter, f)

	// CH-side cross filter: country / asn / source_ip / user_agent
	// aren't stored on api_inventory docs (only on the per-event
	// tags + raw columns in ClickHouse). When ANY is set, we
	// round-trip to ClickHouse first to discover which
	// (listener_name, normalized_path) tuples have matching traffic
	// in the time window, then $or those into MongoDB.
	if f.Country != "" || f.ASN != "" || f.SourceIP != "" || f.UserAgent != "" {
		if h.Context.Clickhouse == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "country/asn/source_ip/user_agent filters require clickhouse; backend is currently disabled",
			})
			return
		}
		from, to := resolveGeoFilterTimeWindow(f)
		keys, gerr := h.Context.Clickhouse.DiscoverInventoryKeys(ctx, clickhouse.InventoryDiscoveryFilter{
			ProjectID: projectID,
			Country:   f.Country,
			ASN:       f.ASN,
			SourceIP:  f.SourceIP,
			UserAgent: f.UserAgent,
			From:      from,
			To:        to,
		})
		if gerr != nil {
			h.Logger.Errorf("inventory list discovery failed: %v", gerr)
			c.JSON(http.StatusBadGateway, gin.H{"error": "failed to resolve ch-cross filter"})
			return
		}
		if len(keys) == 0 {
			// No matching traffic in the window — short-circuit with
			// an empty page so the UI shows "no results" cleanly.
			c.JSON(http.StatusOK, buildPaginationResponse([]bson.M{}, 0, f.Limit, f.Offset))
			return
		}
		// Compose a per-tuple $or array; using $in on either single
		// field alone would match cross-listener noise.
		ors := make([]bson.M, 0, len(keys))
		for _, k := range keys {
			ors = append(ors, bson.M{
				"listener_name":   k.ListenerName,
				"normalized_path": k.NormalizedPath,
			})
		}
		mongoFilter["$or"] = ors
	}

	coll := h.Context.Client.Collection(inventoryCollection)

	// Count and data fetch are independent — run them concurrently to
	// halve the wall-time. errgroup propagates the first error and
	// cancels the sibling so a slow leg can't drag a fast response
	// down.
	findOpts := options.Find().
		SetLimit(f.Limit).
		SetSkip(f.Offset).
		SetSort(bson.D{{Key: f.SortBy, Value: f.SortOrder}})

	var (
		total int64
		items = []bson.M{}
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		n, err := coll.CountDocuments(gctx, mongoFilter)
		if err != nil {
			return fmt.Errorf("count: %w", err)
		}
		total = n
		return nil
	})
	g.Go(func() error {
		cur, err := coll.Find(gctx, mongoFilter, findOpts)
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
		h.Logger.Errorf("inventory list failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query inventory"})
		return
	}

	c.JSON(http.StatusOK, buildPaginationResponse(items, total, f.Limit, f.Offset))
}

// GetInventoryItem handles GET /api/v3/inventory/:id.
// Returns the single api_inventory document scoped to the requesting
// user's project (defence in depth — :id alone shouldn't leak
// cross-project data).
func (h *InventoryHandler) GetInventoryItem(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), mongoQueryTimeout)
	defer cancel()

	userDetails, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return
	}

	item, status, errMsg := h.fetchInventoryItem(ctx, c.Param("id"), userDetails)
	if errMsg != "" {
		c.JSON(status, gin.H{"error": errMsg})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": item})
}

// GetInventoryEvents handles GET /api/v3/inventory/:id/events.
// Pulls raw rows from ClickHouse `api_events_raw` filtered by the
// inventory row's (project_id, listener_name, normalized_path).
// Required: ?from / ?to (ISO-8601). Defaults: last 1h.
func (h *InventoryHandler) GetInventoryEvents(c *gin.Context) {
	// Parent context drives the ClickHouse query (which has its own
	// CLICKHOUSE_QUERY_TIMEOUT_SEC). The Mongo fetch + auth check
	// uses a tighter deadline below so a stalled inventory lookup
	// can't eat into the ClickHouse budget.
	ctx := c.Request.Context()

	if h.Context.Clickhouse == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "clickhouse not configured; inventory_detail unavailable"})
		return
	}

	userDetails, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return
	}

	mongoCtx, mongoCancel := context.WithTimeout(ctx, mongoQueryTimeout)
	item, status, errMsg := h.fetchInventoryItem(mongoCtx, c.Param("id"), userDetails)
	mongoCancel()
	if errMsg != "" {
		c.JSON(status, gin.H{"error": errMsg})
		return
	}

	from, to, perr := parseTimeWindow(c, detailDefaultRangeMs, detailMaxRangeDays)
	if perr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": perr.Error()})
		return
	}

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", strconv.Itoa(detailDefaultLimit)))
	if limit <= 0 {
		limit = detailDefaultLimit
	}
	if limit > detailMaxLimit {
		limit = detailMaxLimit
	}
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if offset < 0 {
		offset = 0
	}

	// `fields=full` returns every documented column (headers map,
	// tags map, TLS metadata, gRPC internals); default `core`
	// trims the response to the ~half of columns the inventory list
	// UI actually renders. Unknown values fall through to core.
	fields := "core"
	if v := strings.ToLower(strings.TrimSpace(c.Query("fields"))); v == "full" {
		fields = "full"
	}

	// `?sample=true` pins the query to the inventory document's
	// sample_event_ids[] — the cross-database drill-down from an
	// endpoint card straight to representative raw events. Those
	// IDs can be older than the default 1h window, so we widen
	// from/to to the raw-retention cap (detailMaxRangeDays) and let
	// the event_id IN (...) filter do the narrowing.
	var eventIDs []string
	if c.Query("sample") == "true" {
		eventIDs = asStringSlice(item, "sample_event_ids")
		if len(eventIDs) == 0 {
			// Nothing to drill into — return an empty page cleanly
			// rather than running an unbounded scan.
			c.JSON(http.StatusOK, buildPaginationResponse([]clickhouse.RawEvent{}, 0, int64(limit), int64(offset)))
			return
		}
		to = time.Now().UTC()
		from = to.Add(-time.Duration(detailMaxRangeDays) * 24 * time.Hour)
	}

	// Sort: whitelist of columns the events table can order by.
	// Unknown sort_by silently falls back to ts (the query builder's
	// rawEventsOrderBy applies the same guard — this copy is just so
	// the response can echo the value actually applied).
	sortBy := "ts"
	switch c.Query("sort_by") {
	case "duration_ms", "status_code", "risk_score":
		sortBy = c.Query("sort_by")
	}
	sortOrder := "desc"
	if c.Query("sort_order") == "asc" {
		sortOrder = "asc"
	}

	filter := clickhouse.RawEventsFilter{
		ProjectID:      asString(item, "project_id"),
		ListenerName:   asString(item, "listener_name"),
		NormalizedPath: asString(item, "normalized_path"),
		Method:         c.Query("method"),
		RequestID:      c.Query("request_id"),
		EventIDs:       eventIDs,
		From:           from,
		To:             to,
		Limit:          limit,
		Offset:         offset,
		Fields:         fields,
		SortBy:         sortBy,
		SortOrder:      sortOrder,
		// Opt-in for the count() roundtrip — ClickHouse count() over
		// the raw events table can be expensive on busy listeners.
		// UIs that don't render total pages should omit this.
		IncludeTotal: c.Query("include_total") == "true",
	}
	if v := c.Query("status_min"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 599 {
			filter.StatusMin = uint16(n)
		}
	}
	if v := c.Query("status_max"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 599 {
			filter.StatusMax = uint16(n)
		}
	}
	// Risk filters — keep events carrying any of the listed
	// risk_flags / pii_categories, or scoring at/above min_risk_score.
	// splitCSV caps each list at maxCSVValues (32).
	if v := c.Query("risk_flag"); v != "" {
		filter.RiskFlags = splitCSV(v)
	}
	if v := c.Query("pii_category"); v != "" {
		filter.PIICategories = splitCSV(v)
	}
	if v := c.Query("min_risk_score"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 255 {
			filter.MinRiskScore = uint8(n)
		}
	}
	// Reject inverted ranges up front — otherwise ClickHouse builds a
	// guaranteed-empty WHERE (`status_code >= X AND <= Y` with X>Y)
	// and the caller sees an empty page without knowing why.
	if filter.StatusMin > 0 && filter.StatusMax > 0 && filter.StatusMin > filter.StatusMax {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status_min must be <= status_max"})
		return
	}

	events, total, err := h.Context.Clickhouse.QueryRawEvents(ctx, filter)
	if err != nil {
		h.Logger.Errorf("clickhouse raw events query failed: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to query events"})
		return
	}

	resp := buildPaginationResponse(events, total, int64(limit), int64(offset))
	resp["sort_by"] = sortBy
	resp["sort_order"] = sortOrder
	c.JSON(http.StatusOK, resp)
}

// GetInventoryStats handles GET /api/v3/inventory/:id/stats.
// Reads aggregate metrics from `api_events_1{m,h,d}` for the inventory
// row. Required: ?granularity=1m|1h|1d, ?from, ?to (ISO-8601).
func (h *InventoryHandler) GetInventoryStats(c *gin.Context) {
	// Same shape as GetInventoryEvents — parent ctx for ClickHouse,
	// short-lived ctx for the Mongo fetch (applied via
	// fetchInventoryItemTimed).
	ctx := c.Request.Context()

	if h.Context.Clickhouse == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "clickhouse not configured; inventory_detail unavailable"})
		return
	}

	userDetails, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return
	}

	item, status, errMsg := h.fetchInventoryItem(ctx, c.Param("id"), userDetails)
	if errMsg != "" {
		c.JSON(status, gin.H{"error": errMsg})
		return
	}

	gran := c.Query("granularity")
	switch gran {
	case "1m", "1h", "1d":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "granularity must be one of 1m, 1h, 1d"})
		return
	}

	// The 1d rollup aggregates without normalized_path (schema.md);
	// silently dropping the filter would surface incorrect "all paths
	// merged" data as if it were path-scoped. Reject explicitly so the
	// UI can disable the path field for 1d granularity instead of
	// guessing why a filter has no effect.
	pathFromItem := asString(item, "normalized_path")
	if gran == "1d" && pathFromItem != "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "granularity 1d does not support path-scoped queries; use 1m or 1h, or open stats from a path-agnostic context",
		})
		return
	}

	from, to, perr := parseTimeWindow(c, detailDefaultRangeMs, detailMaxRangeDays)
	if perr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": perr.Error()})
		return
	}

	filter := clickhouse.RollupFilter{
		Granularity:    gran,
		ProjectID:      asString(item, "project_id"),
		ListenerName:   asString(item, "listener_name"),
		NormalizedPath: pathFromItem,
		Method:         c.Query("method"),
		From:           from,
		To:             to,
	}

	buckets, err := h.Context.Clickhouse.QueryRollup(ctx, filter)
	if err != nil {
		h.Logger.Errorf("clickhouse rollup query failed: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to query stats"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": buckets, "count": len(buckets)})
}

// GeoSummary handles GET /api/v3/inventory/geo.
//
// Scope:
//   - ?project=<id>                  required, drives project isolation
//   - &listener_name=<name>          optional, narrows to one listener
//   - &inventory_id=<oid>            optional, narrows to one endpoint
//     (overrides listener_name; cross-
//     project re-auth via fetchInventoryItem)
//   - &from=&to=                     optional, ISO-8601 (7d cap)
//   - &top=N                         per-dimension Top-N (1..50, default 10)
//   - &method=<GET|POST|...>         optional single-method filter
//   - &include_series=true           optional, appends time_series payload
//   - &series_granularity=1m|1h|1d   only honoured with include_series
//
// Response: GeoSummary with the dimensions documented in models.go.
func (h *InventoryHandler) GeoSummary(c *gin.Context) {
	if h.Context.Clickhouse == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "clickhouse not configured; geo summary unavailable"})
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
	// Parent context drives ClickHouse work (its own
	// CLICKHOUSE_QUERY_TIMEOUT_SEC applies inside the sub-queries).
	// Mongo lookup for inventory_id uses a short ctx below so a slow
	// lookup can't eat into the ClickHouse budget.
	parentCtx := c.Request.Context()
	if err := authorization.ValidateRequestProject(parentCtx, h.Context.Client, userDetails, projectID); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	filter := clickhouse.GeoFilter{
		ProjectID:    projectID,
		ListenerName: strings.TrimSpace(c.Query("listener_name")),
		Method:       strings.TrimSpace(c.Query("method")),
		// Raw IP / UA aggregates are two extra full scans of
		// api_events_raw — kept opt-in so the default dashboard call
		// runs five parallel queries instead of seven.
		IncludeRawDims: c.Query("include_raw_dims") == "true",
	}
	if v := c.Query("top"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Top = n
		}
	}

	// Optional drill into a specific endpoint. Mongo fetch happens
	// under a short-lived context; auth check inside fetchInventoryItem
	// re-validates project ownership so the operator can't peek into
	// other projects' inventory by guessing an ObjectID.
	if invID := strings.TrimSpace(c.Query("inventory_id")); invID != "" {
		mongoCtx, mongoCancel := context.WithTimeout(parentCtx, mongoQueryTimeout)
		item, status, errMsg := h.fetchInventoryItem(mongoCtx, invID, userDetails)
		mongoCancel()
		if errMsg != "" {
			c.JSON(status, gin.H{"error": errMsg})
			return
		}
		filter.ListenerName = asString(item, "listener_name")
		filter.NormalizedPath = asString(item, "normalized_path")
	}

	from, to, perr := parseTimeWindow(c, detailDefaultRangeMs, detailMaxRangeDays)
	if perr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": perr.Error()})
		return
	}
	filter.From = from
	filter.To = to

	summary, err := h.Context.Clickhouse.QueryGeoSummary(parentCtx, filter)
	if err != nil {
		h.Logger.Errorf("geo summary failed: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to query geo summary"})
		return
	}

	// Optional time series block. We do this after the summary so
	// the country Top-N list (which determines the series labels) is
	// already in hand — keeps the two payloads coherent.
	if c.Query("include_series") == "true" {
		granularity := strings.TrimSpace(c.Query("series_granularity"))
		if granularity == "" {
			granularity = pickAutoGranularity(filter.From, filter.To)
		}
		// DOS guard: cap the bucket count per granularity so a
		// hostile / careless ?series_granularity=1m + 7-day window
		// can't materialise 10k+ buckets × N series in memory.
		// Limits intentionally match what's actually renderable on
		// a chart — anything beyond is noise the UI can't display.
		if err := validateSeriesWindow(granularity, filter.From, filter.To); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		topCountries := make([]string, 0, len(summary.TopCountries))
		for _, ct := range summary.TopCountries {
			topCountries = append(topCountries, ct.Code)
		}
		series, serr := h.Context.Clickhouse.QueryGeoTimeSeries(parentCtx, filter, granularity, topCountries)
		if serr != nil {
			// Series failure shouldn't kill the dashboard — surface
			// it on the side and let the UI render the snapshot.
			h.Logger.Warnf("geo time series failed (omitting from response): %v", serr)
		} else {
			summary.TimeSeries = series
		}
	}

	c.JSON(http.StatusOK, gin.H{"data": summary})
}

// validateSeriesWindow caps the time window per granularity so the
// time-series payload stays bounded:
//
//	1m → ≤ 6h   (max 360 buckets)
//	1h → ≤ 7d   (max 168 buckets, matches raw retention)
//	1d → ≤ 90d  (max 90 buckets; raw retention is still 7d so longer
//	             windows just yield zero-filled buckets, which is OK
//	             for charts but useful when an operator points to an
//	             extended retention pool later)
//
// Without this guard an operator passing
// ?series_granularity=1m&from=…&to=…+7d would materialise 10080
// buckets × ~15 series = millions of int64 in memory.
func validateSeriesWindow(granularity string, from, to time.Time) error {
	span := to.Sub(from)
	var max time.Duration
	switch granularity {
	case "1m":
		max = 6 * time.Hour
	case "1h":
		max = 7 * 24 * time.Hour
	case "1d":
		max = 90 * 24 * time.Hour
	default:
		return fmt.Errorf("series_granularity must be one of 1m, 1h, 1d")
	}
	if span > max {
		return fmt.Errorf("series_granularity=%s allows a max window of %s; requested %s", granularity, max, span)
	}
	return nil
}

// pickAutoGranularity chooses a sensible bucket size when the caller
// asks for include_series=true but omits series_granularity:
//
//	≤ 2h  → 1m
//	≤ 48h → 1h
//	>     → 1d
//
// Keeps the bucket count bounded (≤ 120 at 1m, ≤ 48 at 1h, ≤ 7 at 1d
// for the 7-day window cap) so charts stay readable.
func pickAutoGranularity(from, to time.Time) string {
	span := to.Sub(from)
	switch {
	case span <= 2*time.Hour:
		return "1m"
	case span <= 48*time.Hour:
		return "1h"
	default:
		return "1d"
	}
}

// ---------------------------------------------------------------------
// helpers (private)
// ---------------------------------------------------------------------

// fetchInventoryItem loads one document by its hex ObjectID and
// enforces project-scope auth via the central authorization helper.
// Returns (item, statusCode, errMsg) so callers can short-circuit
// with a single conditional.
func (h *InventoryHandler) fetchInventoryItem(ctx context.Context, idHex string, userDetails models.UserDetails) (bson.M, int, string) {
	objID, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		return nil, http.StatusBadRequest, "invalid inventory id"
	}

	coll := h.Context.Client.Collection(inventoryCollection)
	var item bson.M
	if err := coll.FindOne(ctx, bson.M{"_id": objID}).Decode(&item); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, http.StatusNotFound, "inventory item not found"
		}
		h.Logger.Errorf("inventory findone failed: %v", err)
		return nil, http.StatusInternalServerError, "failed to fetch inventory item"
	}

	projectID := asString(item, "project_id")
	if projectID == "" {
		return nil, http.StatusInternalServerError, "inventory item missing project_id"
	}
	if err := authorization.ValidateRequestProject(ctx, h.Context.Client, userDetails, projectID); err != nil {
		return nil, http.StatusForbidden, err.Error()
	}
	return item, 0, ""
}

func parseInventoryListFilter(c *gin.Context) listFilter {
	f := listFilter{
		Limit:     listDefaultLimit,
		Offset:    0,
		SortBy:    "last_seen",
		SortOrder: -1, // newest first
	}
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 {
		if v > listMaxLimit {
			v = listMaxLimit
		}
		f.Limit = int64(v)
	}
	if v, err := strconv.Atoi(c.Query("offset")); err == nil && v >= 0 {
		f.Offset = int64(v)
	}

	f.ListenerName = c.Query("listener_name")
	f.Host = c.Query("host")
	f.NormalizedPath = c.Query("normalized_path")

	if v := c.Query("method"); v != "" {
		f.Method = splitCSV(v)
	}
	if v := c.Query("protocol"); v != "" {
		f.Protocol = splitCSV(v)
	}
	if v := c.Query("risk_flag"); v != "" {
		f.RiskFlags = splitCSV(v)
	}
	if v := c.Query("endpoint_category"); v != "" {
		f.EndpointCats = splitCSV(v)
	}
	if v := c.Query("pii_category"); v != "" {
		f.PIICategories = splitCSV(v)
	}
	if v, err := strconv.Atoi(c.Query("min_risk_score")); err == nil {
		f.MinRiskScore = v
	}
	if v, err := strconv.Atoi(c.Query("max_risk_score")); err == nil {
		f.MaxRiskScore = v
	}
	if v := c.Query("last_seen_from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.LastSeenFrom = t
		}
	}
	if v := c.Query("last_seen_to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.LastSeenTo = t
		}
	}

	// Sort whitelist: every entry must be backed by a project-scoped
	// compound index so a busy project can't trigger a collection
	// scan. Coverage:
	//   last_seen      → project_last_seen
	//   max_risk_score → project_riskscore_lastseen
	//   seen_count     → project_seen_count
	//   latency_max_ms → project_latency_max
	// All four are ensured on startup (pkg/db/db.go CompoundIndices).
	// A listener_name / method / risk_flag filter applied alongside
	// the sort means the index only partially covers the query (ESR),
	// but the project_id leg still prunes and the typical call is
	// project-scoped — acceptable. Unknown sort_by falls through to
	// last_seen (no 400).
	switch c.Query("sort_by") {
	case "max_risk_score":
		f.SortBy = "max_risk_score"
	case "seen_count":
		f.SortBy = "seen_count"
	case "latency_max_ms":
		f.SortBy = "latency_max_ms"
	case "last_seen", "":
		f.SortBy = "last_seen"
	}
	if c.Query("sort_order") == "asc" {
		f.SortOrder = 1
	}
	f.Country = strings.TrimSpace(c.Query("country"))
	f.ASN = strings.TrimSpace(c.Query("asn"))
	f.SourceIP = strings.TrimSpace(c.Query("source_ip"))
	f.UserAgent = strings.TrimSpace(c.Query("user_agent"))
	return f
}

func applyInventoryListFilter(filter bson.M, f listFilter) {
	// String filters use ANCHORED prefix regex (`^pattern`) so MongoDB
	// can drive the query off an index instead of falling back to a
	// collection scan. Case-insensitivity disables index use entirely
	// on regex queries, so we drop the `i` option too — the collector
	// normalises listener_name/host/path values at write time, making
	// case-sensitive prefix match the cheapest correct semantic.
	// SanitizeSearchInput already escapes regex metacharacters, so the
	// anchored fragment is safe to splice in.
	if v := f.ListenerName; v != "" {
		if san, ok := security.SanitizeSearchInput(v); ok && san != "" {
			filter["listener_name"] = bson.M{"$regex": "^" + san}
		}
	}
	if v := f.Host; v != "" {
		if san, ok := security.SanitizeSearchInput(v); ok && san != "" {
			filter["host"] = bson.M{"$regex": "^" + san}
		}
	}
	if v := f.NormalizedPath; v != "" {
		if san, ok := security.SanitizeSearchInput(v); ok && san != "" {
			filter["normalized_path"] = bson.M{"$regex": "^" + san}
		}
	}
	if len(f.Method) > 0 {
		filter["method"] = bson.M{"$in": f.Method}
	}
	if len(f.Protocol) > 0 {
		filter["protocol"] = bson.M{"$in": f.Protocol}
	}
	if len(f.RiskFlags) > 0 {
		filter["risk_flags"] = bson.M{"$in": f.RiskFlags}
	}
	if len(f.EndpointCats) > 0 {
		filter["endpoint_categories"] = bson.M{"$in": f.EndpointCats}
	}
	if len(f.PIICategories) > 0 {
		filter["pii_categories"] = bson.M{"$in": f.PIICategories}
	}
	if f.MinRiskScore > 0 || f.MaxRiskScore > 0 {
		rng := bson.M{}
		if f.MinRiskScore > 0 {
			rng["$gte"] = f.MinRiskScore
		}
		if f.MaxRiskScore > 0 {
			rng["$lte"] = f.MaxRiskScore
		}
		filter["max_risk_score"] = rng
	}
	if !f.LastSeenFrom.IsZero() || !f.LastSeenTo.IsZero() {
		rng := bson.M{}
		if !f.LastSeenFrom.IsZero() {
			rng["$gte"] = f.LastSeenFrom
		}
		if !f.LastSeenTo.IsZero() {
			rng["$lte"] = f.LastSeenTo
		}
		filter["last_seen"] = rng
	}
}

// parseTimeWindow extracts ?from and ?to (ISO-8601) and applies a
// max-range guard so a buggy / malicious caller cannot scan months of
// raw events. When `to` is missing it defaults to now; when `from` is
// missing it defaults to `to - defaultRangeMs`.
//
// Both outputs are normalised to UTC. time.Parse(RFC3339) keeps the
// input's zone (Location pointer) on the result; mixing that with
// UTC values later caused map-key bugs in the geo time-series merge
// where two time.Time values describing the same instant compared
// unequal. Canonicalising here keeps downstream code zone-agnostic.
func parseTimeWindow(c *gin.Context, defaultRangeMs int64, maxRangeDays int) (from, to time.Time, err error) {
	now := time.Now().UTC()
	if v := c.Query("to"); v != "" {
		t, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			return time.Time{}, time.Time{}, errors.New("invalid 'to' timestamp; expected RFC3339")
		}
		to = t.UTC()
	} else {
		to = now
	}
	if v := c.Query("from"); v != "" {
		t, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			return time.Time{}, time.Time{}, errors.New("invalid 'from' timestamp; expected RFC3339")
		}
		from = t.UTC()
	} else {
		from = to.Add(-time.Duration(defaultRangeMs) * time.Millisecond)
	}
	if !to.After(from) {
		return time.Time{}, time.Time{}, errors.New("'from' must be earlier than 'to'")
	}
	if to.Sub(from) > time.Duration(maxRangeDays)*24*time.Hour {
		return time.Time{}, time.Time{}, errors.New("time range exceeds maximum (7 days)")
	}
	return from, to, nil
}

// buildPaginationResponse mirrors models.PaginationResponse shape so
// the UI can use the same pagination component for every list endpoint.
// Guards against limit<=0 to keep this safe for any future caller —
// today's call sites all force a positive default but a careless
// refactor would otherwise panic with division by zero.
func buildPaginationResponse(data any, total int64, limit, offset int64) gin.H {
	count := lengthOf(data)
	if limit <= 0 {
		limit = 1
	}
	currentPage := int(offset/limit) + 1
	totalPages := int(total / limit)
	if total%limit != 0 {
		totalPages++
	}
	return gin.H{
		"data":         data,
		"count":        count,
		"total_count":  total,
		"total_pages":  totalPages,
		"limit":        limit,
		"offset":       offset,
		"current_page": currentPage,
		"has_next":     currentPage < totalPages,
		"has_prev":     currentPage > 1,
	}
}

// maxCSVValues caps every comma-separated query parameter (method,
// protocol, risk_flag, endpoint_category, pii_category). Without a cap
// a caller can splice an arbitrarily large `?method=A,B,C,...` into
// MongoDB's `$in` operator and blow up planner cost; 32 is well past
// any legitimate UI selector and small enough that the resulting `$in`
// stays cheap.
const maxCSVValues = 32

// resolveGeoFilterTimeWindow picks the time window for the
// inventory-list geo cross filter. Order of precedence:
//  1. explicit last_seen_from / last_seen_to (if both set)
//  2. last_seen_from + now
//  3. now - 7d → now (clamps to the ClickHouse raw retention default)
//
// Keeps the window tight by default so the path-discovery query
// stays fast even on busy projects.
func resolveGeoFilterTimeWindow(f listFilter) (time.Time, time.Time) {
	now := time.Now().UTC()
	to := now
	from := now.Add(-7 * 24 * time.Hour)
	if !f.LastSeenFrom.IsZero() {
		from = f.LastSeenFrom
	}
	if !f.LastSeenTo.IsZero() {
		to = f.LastSeenTo
	}
	if !to.After(from) {
		to = from.Add(time.Hour)
	}
	return from, to
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t == "" {
			continue
		}
		out = append(out, t)
		if len(out) >= maxCSVValues {
			break
		}
	}
	return out
}

func asString(m bson.M, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// asStringSlice extracts a []string from a bson.M field. The Mongo
// driver decodes a BSON array as `bson.A` ([]any); each element is
// type-asserted to string and non-string / empty entries are
// skipped. Capped at maxCSVValues so a pathologically long
// sample_event_ids array can't bloat the downstream `IN (...)`
// clause. Returns an empty (non-nil) slice when the field is
// absent or not an array.
func asStringSlice(m bson.M, key string) []string {
	raw, ok := m[key].(bson.A)
	if !ok {
		return []string{}
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok || s == "" {
			continue
		}
		out = append(out, s)
		if len(out) >= maxCSVValues {
			break
		}
	}
	return out
}

func lengthOf(v any) int {
	switch x := v.(type) {
	case []bson.M:
		return len(x)
	case []clickhouse.RawEvent:
		return len(x)
	case []clickhouse.RollupBucket:
		return len(x)
	}
	return 0
}
