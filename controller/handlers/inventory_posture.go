package handlers

import (
	"context"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"

	"github.com/CloudNativeWorks/elchi-backend/pkg/clickhouse"
)

// GetInventoryCurrentPosture handles GET /api/v3/inventory/:id/current-posture.
//
// Returns the operation's CURRENT posture (ClickHouse, time-windowed)
// alongside its "_ever" scores (Mongo inventory, monotonic). The inventory
// catalog only stores "ever observed" — a remediated endpoint stays red
// forever; this endpoint answers "is it STILL bad?".
//
// Graceful degradation: ClickHouse missing or the query failing returns 200
// with current=null + current_available=false, never blocking the "_ever"
// answer the inventory doc already carries. A dormant operation (no traffic
// in the window) also returns current=null with dormant=true, so the UI can
// render "dormant — last seen X, historical max Y" rather than a misleading
// zero.
//
// Limitation surfaced explicitly: posture_current_available is always false
// — the collector ships risk_score (THREAT) to ClickHouse but not
// posture_score (EXPOSURE), so the exposure axis is "_ever" only until the
// collector mirrors posture_score into the raw-event schema.
func (h *InventoryHandler) GetInventoryCurrentPosture(c *gin.Context) {
	ctx := c.Request.Context()

	userDetails, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return
	}

	// fetchInventoryItem resolves the doc by _id and enforces project-scope
	// auth — so a user can't probe other projects' endpoints by ObjectID.
	item, status, errMsg := h.fetchInventoryItem(ctx, c.Param("id"), userDetails)
	if errMsg != "" {
		c.JSON(status, gin.H{"error": errMsg})
		return
	}

	// "_ever" THREAT + EXPOSURE scores from the (monotonic) inventory doc.
	resp := gin.H{
		"ever": gin.H{
			"max_risk_score":    toInt64Loose(item["max_risk_score"]),
			"max_posture_score": toInt64Loose(item["max_posture_score"]),
		},
		// EXPOSURE has no current value (collector does not ship
		// posture_score to ClickHouse) — posture is "_ever" only.
		"posture_current_available": false,
	}

	if h.Context.Clickhouse == nil {
		resp["current_available"] = false
		resp["current"] = nil
		c.JSON(http.StatusOK, resp)
		return
	}

	windowDays := 0 // 0 → query layer applies the default (7d)
	if v, e := strconv.Atoi(c.Query("window_days")); e == nil && v > 0 {
		windowDays = v
	}

	cp, qerr := h.Context.Clickhouse.QueryCurrentPosture(ctx, clickhouse.CurrentPostureFilter{
		ProjectID:      asString(item, "project_id"),
		ListenerName:   asString(item, "listener_name"),
		Host:           asString(item, "host"),
		Protocol:       asString(item, "protocol"),
		Method:         asString(item, "method"),
		NormalizedPath: asString(item, "normalized_path"),
		GRPCService:    asString(item, "grpc_service"),
		GRPCMethod:     asString(item, "grpc_method"),
		WindowDays:     windowDays,
	})
	if qerr != nil {
		// Progressive enhancement: keep the "_ever" answer, flag current
		// as unavailable rather than failing the whole request.
		h.Logger.Errorf("current posture query failed: %v", qerr)
		resp["current_available"] = false
		resp["current"] = nil
		resp["current_error"] = "failed to query current posture"
		c.JSON(http.StatusOK, resp)
		return
	}

	resp["current_available"] = true
	resp["window_days"] = cp.WindowDays
	if cp.Active {
		resp["dormant"] = false
		resp["current"] = cp
	} else {
		// No traffic in the window — dormant. Null current; the UI shows
		// the "_ever" max + the doc's last_seen for context.
		resp["dormant"] = true
		resp["current"] = nil
	}

	c.JSON(http.StatusOK, resp)
}

// currentWindowDaysQuery reads the optional ?current_window_days knob; 0
// (absent / invalid) lets the ClickHouse layer apply its default (7d).
func currentWindowDaysQuery(c *gin.Context) int {
	if v, err := strconv.Atoi(c.Query("current_window_days")); err == nil && v > 0 {
		return v
	}
	return 0
}

// enrichFlatCurrentRisk overlays current (windowed) max risk onto a flat
// inventory page (one row per operation). Each row gains current_max_risk
// (null when dormant) + current_dormant. Best-effort: returns false when
// ClickHouse is unavailable or the batch fails — the list still renders with
// its "_ever" max_risk_score, so current is a pure progressive enhancement.
func (h *InventoryHandler) enrichFlatCurrentRisk(ctx context.Context, projectID string, items []bson.M, windowDays int) bool {
	if h.Context.Clickhouse == nil || len(items) == 0 {
		return false
	}
	keys := make([]clickhouse.CurrentRiskKey, 0, len(items))
	for _, it := range items {
		keys = append(keys, flatRiskKey(it))
	}
	m, err := h.Context.Clickhouse.QueryCurrentRiskBatch(ctx, projectID, keys, windowDays)
	if err != nil {
		h.Logger.Errorf("flat current-risk enrichment failed: %v", err)
		return false
	}
	for _, it := range items {
		applyCurrentRisk(it, m[flatRiskKey(it)], hasKey(m, flatRiskKey(it)))
	}
	return true
}

// enrichOperationsCurrentRisk overlays current max risk onto a path-grouped
// operations page: each entry in operations[] gets current_max_risk /
// current_dormant, and the row gets the max across its active operations
// (null when every operation is dormant). Best-effort, same contract as the
// flat variant.
func (h *InventoryHandler) enrichOperationsCurrentRisk(ctx context.Context, projectID string, items []bson.M, windowDays int) bool {
	if h.Context.Clickhouse == nil || len(items) == 0 {
		return false
	}
	keys := []clickhouse.CurrentRiskKey{}
	for _, it := range items {
		ops, _ := it["operations"].(bson.A)
		for _, o := range ops {
			if om, ok := o.(bson.M); ok {
				keys = append(keys, operationRiskKey(it, om))
			}
		}
	}
	m, err := h.Context.Clickhouse.QueryCurrentRiskBatch(ctx, projectID, keys, windowDays)
	if err != nil {
		h.Logger.Errorf("operations current-risk enrichment failed: %v", err)
		return false
	}
	for _, it := range items {
		ops, _ := it["operations"].(bson.A)
		rowMax := int64(-1) // -1 → no active operation in window
		for _, o := range ops {
			om, ok := o.(bson.M)
			if !ok {
				continue
			}
			k := operationRiskKey(it, om)
			cur, active := m[k], hasKey(m, k)
			applyCurrentRisk(om, cur, active)
			if active && cur > rowMax {
				rowMax = cur
			}
		}
		applyCurrentRisk(it, rowMaxNonNeg(rowMax), rowMax >= 0)
	}
	return true
}

// operationRiskKey builds the FULL operation key for one operations[] entry.
// host + normalized_path come from the path ROW (the operations view groups
// by (host, normalized_path)); listener / protocol / method / grpc_* are
// per-operation fields carried inside the entry.
func operationRiskKey(row, op bson.M) clickhouse.CurrentRiskKey {
	return clickhouse.CurrentRiskKey{
		ListenerName:   asString(op, "listener_name"),
		Host:           asString(row, "host"),
		Protocol:       asString(op, "protocol"),
		Method:         asString(op, "method"),
		NormalizedPath: asString(row, "normalized_path"),
		GRPCService:    asString(op, "grpc_service"),
		GRPCMethod:     asString(op, "grpc_method"),
	}
}

// flatRiskKey builds the FULL operation key for a flat inventory row.
func flatRiskKey(it bson.M) clickhouse.CurrentRiskKey {
	return clickhouse.CurrentRiskKey{
		ListenerName:   asString(it, "listener_name"),
		Host:           asString(it, "host"),
		Protocol:       asString(it, "protocol"),
		Method:         asString(it, "method"),
		NormalizedPath: asString(it, "normalized_path"),
		GRPCService:    asString(it, "grpc_service"),
		GRPCMethod:     asString(it, "grpc_method"),
	}
}

// applyCurrentRisk writes the current_max_risk / current_dormant pair onto a
// row (or operation entry). active=false → null score + dormant flag.
func applyCurrentRisk(doc bson.M, score int64, active bool) {
	if active {
		doc["current_max_risk"] = score
		doc["current_dormant"] = false
	} else {
		doc["current_max_risk"] = nil
		doc["current_dormant"] = true
	}
}

func hasKey(m map[clickhouse.CurrentRiskKey]int64, k clickhouse.CurrentRiskKey) bool {
	_, ok := m[k]
	return ok
}

func rowMaxNonNeg(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}
