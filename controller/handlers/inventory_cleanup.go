package handlers

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/CloudNativeWorks/elchi-backend/pkg/audit"
	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// api_inventory write/cleanup endpoints.
//
// `api_inventory` is deliberately TTL-less — it is the endpoint catalog
// and accumulates permanently. Pruning stale / wrongly-normalized /
// deprecated rows is the controller's job, not the collector's: the
// collector only ever upserts. These handlers provide that capability.
//
// IMPORTANT — resurrection: the collector upserts on the 8-field key
// (project_id, listener_name, protocol, host, method, normalized_path,
// grpc_service, grpc_method). Deleting a document is only durable for
// endpoints whose traffic has stopped — a still-served endpoint is
// recreated by the collector on its next request. The API responses
// surface this so the operator is not surprised.
//
// All three handlers are destructive and restricted to Admin / Owner
// (the inventory read endpoints are open to every project role). Single
// document operations resolve + project-scope-check via fetchInventoryItem;
// the bulk cleanup validates an explicit project. Every operation is
// audited via the audit middleware (action set below).

// staleCleanup day bounds. The floor stops an operator from passing a
// tiny value that would delete recently-active endpoints en masse; the
// ceiling is a sanity bound. Default mirrors the recommended periodic
// cleanup window.
const (
	staleCleanupMinDays     = 7
	staleCleanupMaxDays     = 3650
	staleCleanupDefaultDays = 90
)

// inventoryWriteAllowed reports whether the user may run a destructive
// inventory operation. Owner or Admin only — uses the IsOwner boolean
// per the project's auth rules, never a Role==Owner comparison.
func inventoryWriteAllowed(ud models.UserDetails) bool {
	return ud.IsOwner || ud.Role == models.RoleAdmin
}

// DeleteInventoryItem handles DELETE /api/v3/inventory/:id.
//
// Removes a single endpoint catalog row. fetchInventoryItem resolves the
// document by _id and enforces project-scope auth; the delete then keys
// on that resolved _id so a concurrent collector upsert cannot shift the
// target. Durable only if the endpoint's traffic has stopped.
func (h *InventoryHandler) DeleteInventoryItem(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), mongoQueryTimeout)
	defer cancel()

	userDetails, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return
	}
	if !inventoryWriteAllowed(userDetails) {
		c.JSON(http.StatusForbidden, gin.H{"error": "only Admin or Owner can delete inventory endpoints"})
		return
	}

	item, status, errMsg := h.fetchInventoryItem(ctx, c.Param("id"), userDetails)
	if errMsg != "" {
		c.JSON(status, gin.H{"error": errMsg})
		return
	}
	objID, ok := item["_id"].(primitive.ObjectID)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "inventory item missing _id"})
		return
	}
	project := asString(item, "project_id")
	path := asString(item, "normalized_path")

	audit.SetAuditAction(c, "DELETE")
	audit.SetAuditResource(c, inventoryCollection, objID.Hex(), path, project)

	res, err := h.Context.Client.Collection(inventoryCollection).DeleteOne(ctx, bson.M{"_id": objID})
	if err != nil {
		h.Logger.Errorf("inventory delete failed for %s: %v", objID.Hex(), err)
		audit.SetAuditError(c, err.Error())
		audit.SetAuditSuccess(c, false)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete inventory item"})
		return
	}

	audit.SetAuditSuccess(c, true)
	c.JSON(http.StatusOK, gin.H{
		"message":       "inventory endpoint deleted",
		"deleted_count": res.DeletedCount,
		"warning":       "if this endpoint still receives traffic, the collector will recreate it on the next request",
	})
}

// inventoryResetFields are the per-endpoint counters / observed-traffic
// fields zeroed by ResetInventoryItem. The set is the contract handed
// down by the collector team. first_seen is deliberately absent — it is
// the true historical first-seen timestamp and is preserved; the 8 key
// fields and discovery metadata (methods/clusters/routes/...) are also
// left intact. The collector resumes $inc/$max/$addToSet accumulation
// from these zeros on the next event — a reset, not a freeze.
var inventoryResetFields = bson.M{
	"seen_count":           int64(0),
	"response_bytes_total": int64(0),
	"response_bytes_max":   int64(0),
	"latency_max_ms":       int64(0),
	// Both monotonic ($max) score axes must be cleared. max_posture_score
	// (EXPOSURE) was previously omitted here, so a reset cleared the THREAT
	// axis but left the posture/exposure score stale-high — the collector
	// re-accumulates the correct value on the next event.
	"max_risk_score":      int32(0),
	"max_posture_score":   int32(0),
	"latency_buckets":     bson.M{},
	"status_dist":         bson.M{},
	"risk_flags":          bson.A{},
	"pii_categories":      bson.A{},
	"endpoint_categories": bson.A{},
	"consumers":           bson.A{},
	"sample_event_ids":    bson.A{},
}

// ResetInventoryItem handles POST /api/v3/inventory/:id/reset.
//
// Zeros the row's counters and observed-traffic arrays in place, keeping
// the document (and first_seen). The primary use: clear the monotonic
// max_risk_score / risk_flags — they only ever rise, so a flag that
// fired once stays even after the cause is fixed; this reset is the only
// way to clear them. The collector keeps accumulating afterwards.
func (h *InventoryHandler) ResetInventoryItem(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), mongoQueryTimeout)
	defer cancel()

	userDetails, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return
	}
	if !inventoryWriteAllowed(userDetails) {
		c.JSON(http.StatusForbidden, gin.H{"error": "only Admin or Owner can reset inventory endpoints"})
		return
	}

	item, status, errMsg := h.fetchInventoryItem(ctx, c.Param("id"), userDetails)
	if errMsg != "" {
		c.JSON(status, gin.H{"error": errMsg})
		return
	}
	objID, ok := item["_id"].(primitive.ObjectID)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "inventory item missing _id"})
		return
	}
	project := asString(item, "project_id")
	path := asString(item, "normalized_path")

	audit.SetAuditAction(c, "UPDATE")
	audit.SetAuditResource(c, inventoryCollection, objID.Hex(), path, project)
	audit.SetAuditChanges(c, map[string]any{"operation": "reset_counters"})

	_, err = h.Context.Client.Collection(inventoryCollection).
		UpdateOne(ctx, bson.M{"_id": objID}, bson.M{"$set": inventoryResetFields})
	if err != nil {
		h.Logger.Errorf("inventory reset failed for %s: %v", objID.Hex(), err)
		audit.SetAuditError(c, err.Error())
		audit.SetAuditSuccess(c, false)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reset inventory item"})
		return
	}

	audit.SetAuditSuccess(c, true)
	c.JSON(http.StatusOK, gin.H{
		"message": "inventory endpoint counters reset",
		"note":    "first_seen preserved; the collector resumes accumulating counters and risk flags from the next event",
	})
}

// CleanupStaleInventory handles POST /api/v3/inventory/cleanup-stale.
//
// Bulk-deletes a project's endpoints not seen within `days` (default 90,
// clamped to [7, 3650]). This is the safe periodic operation that fills
// the TTL gap: no traffic in the window means no resurrection. Uses the
// project_last_seen index. Project is taken from the request and
// project-scope-checked; restricted to Admin / Owner.
func (h *InventoryHandler) CleanupStaleInventory(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), mongoQueryTimeout)
	defer cancel()

	userDetails, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return
	}
	if !inventoryWriteAllowed(userDetails) {
		c.JSON(http.StatusForbidden, gin.H{"error": "only Admin or Owner can run inventory cleanup"})
		return
	}

	projectID := authorization.ExtractProjectFromRequest(c)
	if projectID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project is required"})
		return
	}
	if err := authorization.ValidateRequestProject(ctx, h.Context.Client, userDetails, projectID); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	// days: invalid / missing falls back to the default; the clamp keeps
	// a hostile or careless value from deleting recently-active rows.
	days := staleCleanupDefaultDays
	if v, convErr := strconv.Atoi(c.Query("days")); convErr == nil && v > 0 {
		days = v
	}
	if days < staleCleanupMinDays {
		days = staleCleanupMinDays
	}
	if days > staleCleanupMaxDays {
		days = staleCleanupMaxDays
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)

	filter := bson.M{"project_id": projectID, "last_seen": bson.M{"$lt": cutoff}}

	audit.SetAuditAction(c, "DELETE")
	audit.SetAuditResource(c, inventoryCollection, "", "stale-cleanup", projectID)
	audit.SetAuditChanges(c, map[string]any{
		"operation": "cleanup_stale",
		"days":      days,
		"cutoff":    cutoff,
	})

	res, err := h.Context.Client.Collection(inventoryCollection).DeleteMany(ctx, filter)
	if err != nil {
		h.Logger.Errorf("inventory stale cleanup failed for project %s: %v", projectID, err)
		audit.SetAuditError(c, err.Error())
		audit.SetAuditSuccess(c, false)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to clean up stale inventory"})
		return
	}

	audit.SetAuditSuccess(c, true)
	c.JSON(http.StatusOK, gin.H{
		"message":       "stale inventory endpoints deleted",
		"deleted_count": res.DeletedCount,
		"days":          days,
		"cutoff":        cutoff,
		"warning":       "endpoints still receiving traffic are recreated by the collector on the next request",
	})
}

// RebaselineInventoryScores handles POST /api/v3/inventory/rebaseline-scores.
//
// Bulk-resets the two monotonic ($max) score axes — max_risk_score AND
// max_posture_score — to 0 across a project. The collector re-accumulates
// the correct values from the next event onward.
//
// Why this exists: the collector's scoring changed (THREAT/POSTURE axis
// split + max-anchored risk_score), but inventory scores are sticky `$max`
// and never self-lower — existing endpoints keep showing inflated/stale
// danger. The collector cannot fix this; this read-side bulk reset is the
// fleet-wide consistency fix (per-endpoint ResetInventoryItem only does one
// row at a time). Project-scoped, Admin/Owner only — mirrors
// CleanupStaleInventory but UpdateMany + $set instead of DeleteMany.
func (h *InventoryHandler) RebaselineInventoryScores(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), mongoQueryTimeout)
	defer cancel()

	userDetails, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return
	}
	if !inventoryWriteAllowed(userDetails) {
		c.JSON(http.StatusForbidden, gin.H{"error": "only Admin or Owner can rebaseline inventory scores"})
		return
	}

	projectID := authorization.ExtractProjectFromRequest(c)
	if projectID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project is required"})
		return
	}
	if err := authorization.ValidateRequestProject(ctx, h.Context.Client, userDetails, projectID); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	filter := bson.M{"project_id": projectID}
	update := bson.M{"$set": bson.M{
		"max_risk_score":    int32(0),
		"max_posture_score": int32(0),
	}}

	audit.SetAuditAction(c, "UPDATE")
	audit.SetAuditResource(c, inventoryCollection, "", "rebaseline-scores", projectID)
	audit.SetAuditChanges(c, map[string]any{"operation": "rebaseline_scores"})

	res, err := h.Context.Client.Collection(inventoryCollection).UpdateMany(ctx, filter, update)
	if err != nil {
		h.Logger.Errorf("inventory score rebaseline failed for project %s: %v", projectID, err)
		audit.SetAuditError(c, err.Error())
		audit.SetAuditSuccess(c, false)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to rebaseline inventory scores"})
		return
	}

	audit.SetAuditSuccess(c, true)
	c.JSON(http.StatusOK, gin.H{
		"message":        "inventory risk/posture scores reset; collector re-accumulates from the next event",
		"matched_count":  res.MatchedCount,
		"modified_count": res.ModifiedCount,
	})
}
