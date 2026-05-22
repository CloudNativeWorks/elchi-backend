package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"golang.org/x/sync/errgroup"

	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/inventory"
)

// API Inventory drift / change detection.
//
// api_inventory is upsert-only (no history), so field-level change
// detection needs a stored baseline to diff against. pkg/inventory's
// scheduler captures daily snapshots into api_inventory_snapshots; these
// handlers diff the LIVE inventory against the most recent snapshot
// at-or-before a caller-supplied `since`, and expose snapshot
// management.

const inventorySnapshotsCollection = "api_inventory_snapshots"

const (
	// driftDefaultSince is the lookback when ?since is omitted.
	driftDefaultSince = 24 * time.Hour
	// driftMaxSinceWindow caps a relative ?since to keep the baseline
	// lookup bounded.
	driftMaxSinceWindow = 90 * 24 * time.Hour

	// snapshotCreateTimeout bounds a manual $merge snapshot — generous
	// vs mongoQueryTimeout because the aggregation touches every
	// confirmed operation of the project.
	snapshotCreateTimeout = 2 * time.Minute

	// Zombie-resurrection bounds: an operation whose baseline last_seen
	// was already stale (≥30d quiet) but is now fresh again (seen within
	// 7d) — a deprecated path that came back to life.
	zombieResurrectStaleAfter  = 30 * 24 * time.Hour
	zombieResurrectFreshWithin = 7 * 24 * time.Hour
)

// drift signal type identifiers (also the JSON `type` value).
const (
	signalNewOperation       = "new_operation"
	signalRemovedOperation   = "removed_operation"
	signalNewMethod          = "new_method"
	signalAuthDowngrade      = "auth_downgrade"
	signalNewPIICategory     = "new_pii_category"
	signalNewRiskFlag        = "new_risk_flag"
	signalRiskIncrease       = "risk_increase"
	signalStatusRegress      = "status_regress"
	signalZombieResurrection = "zombie_resurrection"
)

// signalModeMap maps each signal type to the ?mode bucket that selects
// it. mode=all returns every signal.
var signalModeMap = map[string]string{
	signalNewOperation:       "new",
	signalNewMethod:          "new",
	signalRemovedOperation:   "removed",
	signalAuthDowngrade:      "auth",
	signalNewPIICategory:     "pii",
	signalNewRiskFlag:        "risk",
	signalRiskIncrease:       "risk",
	signalStatusRegress:      "status",
	signalZombieResurrection: "zombie",
}

// driftSignal is one detected change between baseline and live.
type driftSignal struct {
	Type           string    `json:"type"`
	ListenerName   string    `json:"listener_name"`
	Protocol       string    `json:"protocol"`
	Host           string    `json:"host"`
	Method         string    `json:"method"`
	NormalizedPath string    `json:"normalized_path"`
	GRPCService    string    `json:"grpc_service,omitempty"`
	GRPCMethod     string    `json:"grpc_method,omitempty"`
	LastSeen       time.Time `json:"last_seen"`
	Detail         gin.H     `json:"detail,omitempty"`
}

// ListChanges handles GET /api/v3/inventory/changes.
//
// Diffs the live api_inventory against the baseline snapshot
// at-or-before ?since and returns the field-level changes.
//
//	?project=<id>   required
//	?since=<RFC3339|24h|7d|30d>  baseline anchor (default 24h ago)
//	?mode=all|new|removed|auth|pii|risk|status|zombie  signal filter
//	?limit=&offset=  pagination over the (in-memory) signal list
//
// 404 when no snapshot exists at-or-before the requested time — drift
// needs a baseline; the operator must take one (or wait for the daily
// scheduler) first.
func (h *InventoryHandler) ListChanges(c *gin.Context) {
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

	sinceTime := parseSince(c.Query("since"))
	mode := strings.TrimSpace(c.Query("mode"))
	if mode == "" {
		mode = "all"
	}

	snapColl := h.Context.Client.Collection(inventorySnapshotsCollection)

	// 1. Resolve the baseline: newest snapshot at-or-before `since`.
	var baselineMeta struct {
		SnapshotID string    `bson:"snapshot_id"`
		SnapshotAt time.Time `bson:"snapshot_at"`
	}
	err = snapColl.FindOne(ctx,
		bson.M{"project_id": projectID, "snapshot_at": bson.M{"$lte": sinceTime}},
		options.FindOne().
			SetSort(bson.D{{Key: "snapshot_at", Value: -1}}).
			SetProjection(bson.D{{Key: "snapshot_id", Value: 1}, {Key: "snapshot_at", Value: 1}}),
	).Decode(&baselineMeta)
	if errors.Is(err, mongo.ErrNoDocuments) {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "no baseline snapshot at or before the requested time",
			"since": sinceTime,
			"hint":  "take a snapshot (POST /api/v3/inventory/snapshots) or wait for the daily scheduler",
		})
		return
	}
	if err != nil {
		h.Logger.Errorf("drift baseline lookup failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to resolve baseline snapshot"})
		return
	}

	// 2 + 3. Baseline (snapshot form: status_codes) and live (inventory
	// form: status_dist) operation sets fetched in parallel — two
	// independent full scans, so running them concurrently keeps the
	// wall time at max() rather than the sum under the shared deadline.
	var baselineDocs, currentDocs []bson.M
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		docs, ferr := driftFindAll(gctx, snapColl,
			bson.M{"project_id": projectID, "snapshot_id": baselineMeta.SnapshotID},
			options.Find().SetProjection(driftProjection(true)))
		if ferr != nil {
			return fmt.Errorf("baseline fetch: %w", ferr)
		}
		baselineDocs = docs
		return nil
	})
	g.Go(func() error {
		docs, ferr := driftFindAll(gctx, h.Context.Client.Collection(inventoryCollection),
			bson.M{"project_id": projectID, "confirmed": true},
			options.Find().SetProjection(driftProjection(false)))
		if ferr != nil {
			return fmt.Errorf("current fetch: %w", ferr)
		}
		currentDocs = docs
		return nil
	})
	if err := g.Wait(); err != nil {
		h.Logger.Errorf("drift fetch failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch inventory for diff"})
		return
	}

	// 4. Diff + 5. mode filter.
	signals := filterSignalsByMode(diffInventory(baselineDocs, currentDocs), mode)

	// Newest-changed first.
	sort.Slice(signals, func(i, j int) bool { return signals[i].LastSeen.After(signals[j].LastSeen) })

	limit, offset := parseLimitOffset(c, listDefaultLimit, listMaxLimit)
	total := int64(len(signals))
	page := paginateSignals(signals, limit, offset)

	c.JSON(http.StatusOK, gin.H{
		"data":                 page,
		"count":                len(page),
		"total_count":          total,
		"since":                sinceTime,
		"baseline_snapshot_id": baselineMeta.SnapshotID,
		"baseline_snapshot_at": baselineMeta.SnapshotAt,
		"mode":                 mode,
		"limit":                limit,
		"offset":               offset,
	})
}

// ListSnapshots handles GET /api/v3/inventory/snapshots.
//
// Lists the available snapshots for a project (snapshot_id +
// snapshot_at + operation count), newest first — backs the UI's `since`
// picker.
func (h *InventoryHandler) ListSnapshots(c *gin.Context) {
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

	pipeline := []bson.M{
		{"$match": bson.M{"project_id": projectID}},
		{"$group": bson.M{
			"_id":             "$snapshot_id",
			"snapshot_at":     bson.M{"$first": "$snapshot_at"},
			"operation_count": bson.M{"$sum": 1},
		}},
		{"$sort": bson.M{"snapshot_at": -1}},
		{"$limit": 100},
		{"$project": bson.M{
			"_id":             0,
			"snapshot_id":     "$_id",
			"snapshot_at":     1,
			"operation_count": 1,
		}},
	}
	cur, err := h.Context.Client.Collection(inventorySnapshotsCollection).Aggregate(ctx, pipeline)
	if err != nil {
		h.Logger.Errorf("list snapshots failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list snapshots"})
		return
	}
	defer cur.Close(ctx)
	out := []bson.M{}
	if err := cur.All(ctx, &out); err != nil {
		h.Logger.Errorf("list snapshots decode failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to decode snapshots"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": out, "count": len(out)})
}

// CreateSnapshot handles POST /api/v3/inventory/snapshots.
//
// Manually captures a snapshot of the project's confirmed inventory —
// the "freeze a baseline before/after a release" path. Admin/Owner only
// (same gate as the destructive inventory operations).
func (h *InventoryHandler) CreateSnapshot(c *gin.Context) {
	userDetails, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return
	}
	if !inventoryWriteAllowed(userDetails) {
		c.JSON(http.StatusForbidden, gin.H{"error": "only Admin or Owner may create inventory snapshots"})
		return
	}
	projectID := authorization.ExtractProjectFromRequest(c)
	if projectID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project query parameter is required"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), snapshotCreateTimeout)
	defer cancel()

	if err := authorization.ValidateRequestProject(ctx, h.Context.Client, userDetails, projectID); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	snapshotID, count, err := inventory.WriteSnapshot(ctx, h.Context.Client, projectID)
	if err != nil {
		h.Logger.Errorf("manual snapshot failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create snapshot"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"snapshot_id":     snapshotID,
		"operation_count": count,
		"project_id":      projectID,
	})
}

// ---------------------------------------------------------------------
// diff engine
// ---------------------------------------------------------------------

// diffInventory compares the baseline operation set against the live
// set and emits one or more driftSignal per change. Both inputs are raw
// bson.M (baseline carries status_codes[], live carries status_dist{});
// the loose extractors below normalise the differences.
func diffInventory(baseline, current []bson.M) []driftSignal {
	baseMap := make(map[string]bson.M, len(baseline))
	basePathSet := make(map[string]struct{}, len(baseline))
	for _, b := range baseline {
		baseMap[driftOpKey(b)] = b
		basePathSet[driftPathKey(b)] = struct{}{}
	}

	curMap := make(map[string]bson.M, len(current))
	signals := []driftSignal{}
	now := time.Now().UTC()

	for _, cur := range current {
		key := driftOpKey(cur)
		curMap[key] = cur
		base, existed := baseMap[key]
		if !existed {
			// Brand-new op vs a new verb on a path that already existed.
			if _, pathExisted := basePathSet[driftPathKey(cur)]; pathExisted {
				signals = append(signals, driftSignalBase(signalNewMethod, cur))
			} else {
				signals = append(signals, driftSignalBase(signalNewOperation, cur))
			}
			continue
		}
		signals = append(signals, diffOp(base, cur, now)...)
	}

	for _, base := range baseline {
		if _, ok := curMap[driftOpKey(base)]; !ok {
			signals = append(signals, driftSignalBase(signalRemovedOperation, base))
		}
	}
	return signals
}

// diffOp emits the field-level signals for an operation present in both
// the baseline and the live inventory.
func diffOp(base, cur bson.M, now time.Time) []driftSignal {
	out := []driftSignal{}

	// Auth downgrade: an endpoint that previously served ONLY
	// authenticated traffic is now seen serving unauthenticated
	// requests. auth_observed / noauth_observed / auth_schemes are all
	// cumulative in the catalog (collector accumulates via
	// $addToSet/$max — they only ever grow), so the only detectable
	// downgrade is no-auth access NEWLY appearing on an endpoint that
	// had an authenticated-only history. added_auth_schemes is reported
	// for context (schemes are additive; e.g. a freshly observed "none").
	baseAuth := driftBool(base, "auth_observed")
	baseNoauth := driftBool(base, "noauth_observed")
	curNoauth := driftBool(cur, "noauth_observed")
	if baseAuth && !baseNoauth && curNoauth {
		sig := driftSignalBase(signalAuthDowngrade, cur)
		sig.Detail = gin.H{
			"baseline_auth_observed":   baseAuth,
			"baseline_noauth_observed": baseNoauth,
			"current_noauth_observed":  curNoauth,
			"added_auth_schemes":       setKeys(setDiff(driftStrSet(cur, "auth_schemes"), driftStrSet(base, "auth_schemes"))),
		}
		out = append(out, sig)
	}

	// New PII categories.
	if newPII := setDiff(driftStrSet(cur, "pii_categories"), driftStrSet(base, "pii_categories")); len(newPII) > 0 {
		sig := driftSignalBase(signalNewPIICategory, cur)
		sig.Detail = gin.H{"new_pii_categories": setKeys(newPII)}
		out = append(out, sig)
	}

	// New risk flags.
	if newFlags := setDiff(driftStrSet(cur, "risk_flags"), driftStrSet(base, "risk_flags")); len(newFlags) > 0 {
		sig := driftSignalBase(signalNewRiskFlag, cur)
		sig.Detail = gin.H{"new_risk_flags": setKeys(newFlags)}
		out = append(out, sig)
	}

	// Risk score increase.
	baseRisk := driftFloat(base, "max_risk_score")
	curRisk := driftFloat(cur, "max_risk_score")
	if curRisk > baseRisk {
		sig := driftSignalBase(signalRiskIncrease, cur)
		sig.Detail = gin.H{"baseline_max_risk_score": baseRisk, "current_max_risk_score": curRisk}
		out = append(out, sig)
	}

	// Status regression: new 4xx/5xx codes that the baseline never saw.
	baseStatus := driftStatusSet(base)
	newErrCodes := []string{}
	for code := range driftStatusSet(cur) {
		if _, seen := baseStatus[code]; !seen && isErrorStatus(code) {
			newErrCodes = append(newErrCodes, code)
		}
	}
	if len(newErrCodes) > 0 {
		sort.Strings(newErrCodes)
		sig := driftSignalBase(signalStatusRegress, cur)
		sig.Detail = gin.H{"new_error_status_codes": newErrCodes}
		out = append(out, sig)
	}

	// Zombie resurrection: baseline was stale, live is fresh again.
	baseLast := driftLastSeen(base)
	curLast := driftLastSeen(cur)
	if !baseLast.IsZero() && !curLast.IsZero() &&
		now.Sub(baseLast) >= zombieResurrectStaleAfter &&
		now.Sub(curLast) <= zombieResurrectFreshWithin {
		sig := driftSignalBase(signalZombieResurrection, cur)
		sig.Detail = gin.H{"baseline_last_seen": baseLast, "current_last_seen": curLast}
		out = append(out, sig)
	}

	return out
}

// ---------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------

// driftFindAll runs Find and decodes the full cursor into []bson.M.
// Both diff inputs are loaded fully into memory; per-project scoping
// keeps the working set bounded.
func driftFindAll(ctx context.Context, coll *mongo.Collection, filter bson.M, opts *options.FindOptions) ([]bson.M, error) {
	cur, err := coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := []bson.M{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// driftProjection is the field set both diff sides need. snapshot=true
// pulls the snapshot's status_codes[]; snapshot=false pulls the live
// status_dist{} map. _id is dropped — the 8-field op key identifies rows.
func driftProjection(snapshot bool) bson.D {
	proj := bson.D{
		{Key: "_id", Value: 0},
		{Key: "listener_name", Value: 1},
		{Key: "protocol", Value: 1},
		{Key: "host", Value: 1},
		{Key: "method", Value: 1},
		{Key: "normalized_path", Value: 1},
		{Key: "grpc_service", Value: 1},
		{Key: "grpc_method", Value: 1},
		{Key: "auth_observed", Value: 1},
		{Key: "noauth_observed", Value: 1},
		{Key: "auth_schemes", Value: 1},
		{Key: "pii_categories", Value: 1},
		{Key: "risk_flags", Value: 1},
		{Key: "max_risk_score", Value: 1},
		{Key: "max_posture_score", Value: 1},
		{Key: "last_seen", Value: 1},
		{Key: "seen_count", Value: 1},
	}
	if snapshot {
		proj = append(proj, bson.E{Key: "status_codes", Value: 1})
	} else {
		proj = append(proj, bson.E{Key: "status_dist", Value: 1})
	}
	return proj
}

// driftSignalBase builds a signal pre-filled with the operation's
// identity + last_seen (the sort key). Callers attach Detail.
func driftSignalBase(typ string, m bson.M) driftSignal {
	return driftSignal{
		Type:           typ,
		ListenerName:   asString(m, "listener_name"),
		Protocol:       asString(m, "protocol"),
		Host:           asString(m, "host"),
		Method:         asString(m, "method"),
		NormalizedPath: asString(m, "normalized_path"),
		GRPCService:    asString(m, "grpc_service"),
		GRPCMethod:     asString(m, "grpc_method"),
		LastSeen:       driftLastSeen(m),
	}
}

// driftOpKey is the 8-field operation identity minus project_id (both
// sides are already scoped to one project). \x1f (unit separator) is
// never present in the component values.
func driftOpKey(m bson.M) string {
	return strings.Join([]string{
		asString(m, "listener_name"),
		asString(m, "protocol"),
		asString(m, "host"),
		asString(m, "method"),
		asString(m, "normalized_path"),
		asString(m, "grpc_service"),
		asString(m, "grpc_method"),
	}, "\x1f")
}

// driftPathKey is the (host, normalized_path) identity used to tell a
// brand-new operation from a new method on an existing path.
func driftPathKey(m bson.M) string {
	return asString(m, "host") + "\x1f" + asString(m, "normalized_path")
}

// driftBool tolerates both the BSON bool the collector writes today and
// a future 0/1 uint encoding (mirrors AuthCoverage's $in:[true,1] guard).
func driftBool(m bson.M, key string) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case int32:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0
	}
	return false
}

// driftFloat coerces the mixed numeric types the driver returns.
func driftFloat(m bson.M, key string) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case int32:
		return float64(v)
	case int64:
		return float64(v)
	}
	return 0
}

// driftStrSet reads a BSON string array into a set.
func driftStrSet(m bson.M, key string) map[string]struct{} {
	out := map[string]struct{}{}
	if raw, ok := m[key].(bson.A); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok && s != "" {
				out[s] = struct{}{}
			}
		}
	}
	return out
}

// driftStatusSet returns the observed status-code set, handling both
// the snapshot form (status_codes[]) and the live form (status_dist{}
// map keys).
func driftStatusSet(m bson.M) map[string]struct{} {
	out := map[string]struct{}{}
	if raw, ok := m["status_codes"].(bson.A); ok { // snapshot form
		for _, v := range raw {
			if s, ok := v.(string); ok && s != "" {
				out[s] = struct{}{}
			}
		}
		return out
	}
	if dist, ok := m["status_dist"].(bson.M); ok { // live form
		for code := range dist {
			out[code] = struct{}{}
		}
	}
	return out
}

// driftLastSeen extracts last_seen as a time.Time (driver returns
// primitive.DateTime for bson.M targets).
func driftLastSeen(m bson.M) time.Time {
	switch v := m["last_seen"].(type) {
	case primitive.DateTime:
		return v.Time()
	case time.Time:
		return v
	}
	return time.Time{}
}

// setDiff returns the elements of a not present in b.
func setDiff(a, b map[string]struct{}) map[string]struct{} {
	out := map[string]struct{}{}
	for k := range a {
		if _, ok := b[k]; !ok {
			out[k] = struct{}{}
		}
	}
	return out
}

// setKeys returns the set's members as a sorted slice (deterministic
// JSON output).
func setKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// isErrorStatus reports whether a 3-digit status code is a 4xx/5xx.
func isErrorStatus(code string) bool {
	return len(code) == 3 && (code[0] == '4' || code[0] == '5')
}

// filterSignalsByMode keeps only the signals belonging to the requested
// ?mode bucket. mode=all (or unknown) passes everything through.
func filterSignalsByMode(sigs []driftSignal, mode string) []driftSignal {
	if mode == "all" || mode == "" {
		return sigs
	}
	out := []driftSignal{}
	for _, s := range sigs {
		if signalModeMap[s.Type] == mode {
			out = append(out, s)
		}
	}
	return out
}

// paginateSignals applies limit/offset to the in-memory signal slice.
func paginateSignals(sigs []driftSignal, limit, offset int64) []driftSignal {
	n := int64(len(sigs))
	if offset >= n {
		return []driftSignal{}
	}
	end := min(offset+limit, n)
	return sigs[offset:end]
}

// parseSince resolves the ?since anchor: an RFC3339 timestamp is used
// verbatim; a relative shorthand ("24h"/"7d"/"30d") is subtracted from
// now (capped at driftMaxSinceWindow); empty defaults to 24h ago.
func parseSince(s string) time.Time {
	now := time.Now().UTC()
	s = strings.TrimSpace(s)
	if s == "" {
		return now.Add(-driftDefaultSince)
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return now.Add(-parseWindowDuration(s, driftDefaultSince, driftMaxSinceWindow))
}
