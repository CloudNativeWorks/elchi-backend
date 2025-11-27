package maintenance

import (
	"context"
	"fmt"
	"net/http"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
)

type CleanupHandler struct {
	Context *db.AppContext
	Logger  *logger.Logger
}

// DeleteVersionResourcesRequest represents the request body for version cleanup
type DeleteVersionResourcesRequest struct {
	Mode string `form:"mode" binding:"omitempty,oneof=all default-only non-default-only"`
}

// DeleteVersionResourcesResponse represents the cleanup result
type DeleteVersionResourcesResponse struct {
	Success       bool              `json:"success"`
	Message       string            `json:"message"`
	Version       string            `json:"version"`
	Mode          string            `json:"mode"`
	DeletedCounts map[string]int64  `json:"deleted_counts"`
	TotalDeleted  int64             `json:"total_deleted"`
	SkippedReason string            `json:"skipped_reason,omitempty"`
}

// DeleteVersionResources handles version cleanup
// DELETE /api/v3/settings/maintenance/cleanup/versions/:version?mode=all&project=xxx
func (h *CleanupHandler) DeleteVersionResources(c *gin.Context) {
	version := c.Param("version")
	if version == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "version parameter is required"})
		return
	}

	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project query parameter is required"})
		return
	}

	// Note: Authorization (Owner/Admin check) is handled by InitSettingMiddleware
	// No need to duplicate authorization logic here

	var req DeleteVersionResourcesRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Default mode is "all"
	if req.Mode == "" {
		req.Mode = "all"
	}

	// Get username for logging (optional, no error if not present)
	username := "unknown"
	if uname, exists := c.Get("user_name"); exists {
		if unamePtr, ok := uname.(*string); ok && unamePtr != nil {
			username = *unamePtr
		}
	}

	h.Logger.Infof("🧹 Cleanup: User %s requesting version cleanup - version=%s, project=%s, mode=%s",
		username, version, project, req.Mode)

	ctx := context.Background()

	// Step 1: CRITICAL CHECK - Check if any listeners exist with this version
	listenersCollection := h.Context.Client.Collection("listeners")
	listenerFilter := bson.M{
		"general.version": version,
		"general.project": project,
	}

	listenerCount, err := listenersCollection.CountDocuments(ctx, listenerFilter)
	if err != nil {
		h.Logger.Errorf("Failed to check listeners for version %s: %v", version, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to verify version safety: %v", err)})
		return
	}

	if listenerCount > 0 {
		h.Logger.Warnf("❌ ABORT: Version %s has %d active listener(s) - cannot delete resources", version, listenerCount)
		c.JSON(http.StatusConflict, DeleteVersionResourcesResponse{
			Success:       false,
			Message:       fmt.Sprintf("Cannot delete resources: version %s has %d active listener(s). Delete listeners first.", version, listenerCount),
			Version:       version,
			Mode:          req.Mode,
			SkippedReason: fmt.Sprintf("Active listeners found: %d", listenerCount),
			DeletedCounts: make(map[string]int64),
			TotalDeleted:  0,
		})
		return
	}

	h.Logger.Infof("✅ Version %s has no active listeners - proceeding with cleanup", version)

	// Step 2: Build deletion filter based on mode
	baseFilter := bson.M{
		"general.version": version,
		"general.project": project,
	}

	var deletionFilter bson.M
	switch req.Mode {
	case "default-only":
		// Only delete default resources
		deletionFilter = bson.M{
			"$and": []bson.M{
				baseFilter,
				{"metadata.is_default": true},
			},
		}
		h.Logger.Infof("Mode: default-only - will only delete default resources")

	case "non-default-only":
		// Only delete non-default resources (where is_default is not true)
		deletionFilter = bson.M{
			"$and": []bson.M{
				baseFilter,
				{"metadata.is_default": bson.M{"$ne": true}},
			},
		}
		h.Logger.Infof("Mode: non-default-only - will skip default resources")

	case "all":
		// Delete all resources regardless of default status
		deletionFilter = baseFilter
		h.Logger.Infof("Mode: all - will delete all resources including defaults")

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid mode: %s", req.Mode)})
		return
	}

	// Step 3: Delete resources from all collections
	collections := []string{
		"clusters",
		"routes",
		"endpoints",
		"filters",
		"extensions",
		"secrets",
		"bootstrap",
		"virtual_hosts",
		"tls",
	}

	deletedCounts := make(map[string]int64)
	var totalDeleted int64

	for _, collectionName := range collections {
		collection := h.Context.Client.Collection(collectionName)
		result, err := collection.DeleteMany(ctx, deletionFilter)
		if err != nil {
			h.Logger.Errorf("Failed to delete from %s: %v", collectionName, err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   fmt.Sprintf("Failed to delete from %s: %v", collectionName, err),
				"partial": deletedCounts,
			})
			return
		}

		deletedCounts[collectionName] = result.DeletedCount
		totalDeleted += result.DeletedCount

		if result.DeletedCount > 0 {
			h.Logger.Infof("  ✅ Deleted %d resource(s) from %s", result.DeletedCount, collectionName)
		}
	}

	h.Logger.Infof("🎉 Cleanup completed successfully: version=%s, mode=%s, total_deleted=%d",
		version, req.Mode, totalDeleted)

	response := DeleteVersionResourcesResponse{
		Success:       true,
		Message:       fmt.Sprintf("Successfully deleted %d resource(s) from version %s (%s mode)", totalDeleted, version, req.Mode),
		Version:       version,
		Mode:          req.Mode,
		DeletedCounts: deletedCounts,
		TotalDeleted:  totalDeleted,
	}

	c.JSON(http.StatusOK, response)
}
