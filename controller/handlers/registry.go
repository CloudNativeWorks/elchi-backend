package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/registry"
	"github.com/gin-gonic/gin"
)

type RegistryHandler struct {
	registryClient *registry.RegistryClient
	// dbContext lets GetRegistryData read the registry's singleton
	// snapshot doc from MongoDB instead of making two parallel gRPC
	// calls into registry-cluster (which Envoy load-balances
	// independently, so the two halves of the response can come from
	// different registries with different in-memory views).
	dbContext *db.AppContext
	logger    *logger.Logger
}

// NewRegistryHandler creates a new registry handler. dbContext is
// optional — when nil, GetRegistryData falls back to the live gRPC
// aggregation path (the only path before snapshot-direct reads landed).
func NewRegistryHandler(registryClient *registry.RegistryClient, dbContext *db.AppContext, logger *logger.Logger) *RegistryHandler {
	return &RegistryHandler{
		registryClient: registryClient,
		dbContext:      dbContext,
		logger:         logger,
	}
}

// DeleteController handles DELETE /api/v3/registry/controller/:id
func (h *RegistryHandler) DeleteController(c *gin.Context) {
	controllerID := c.Param("id")
	if controllerID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "controller ID cannot be empty",
		})
		return
	}

	// Clean the ID (remove any extra characters)
	controllerID = strings.TrimSpace(controllerID)

	h.logger.Infof("Deleting controller from registry: %s", controllerID)

	if err := h.registryClient.DeleteController(controllerID); err != nil {
		h.logger.Errorf("Failed to delete controller %s: %v", controllerID, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "failed to delete controller from registry",
			"details": err.Error(),
		})
		return
	}

	h.logger.Infof("Controller %s deleted from registry successfully", controllerID)
	c.JSON(http.StatusOK, gin.H{
		"success":       true,
		"message":       "controller deleted from registry successfully",
		"controller_id": controllerID,
	})
}

// DeleteControlPlane handles DELETE /api/v3/registry/control-plane/:id
func (h *RegistryHandler) DeleteControlPlane(c *gin.Context) {
	controlPlaneID := c.Param("id")
	if controlPlaneID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "control plane ID cannot be empty",
		})
		return
	}

	// Clean the ID (remove any extra characters)
	controlPlaneID = strings.TrimSpace(controlPlaneID)

	h.logger.Infof("Deleting control plane from registry: %s", controlPlaneID)

	if err := h.registryClient.DeleteControlPlane(controlPlaneID); err != nil {
		h.logger.Errorf("Failed to delete control plane %s: %v", controlPlaneID, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "failed to delete control plane from registry",
			"details": err.Error(),
		})
		return
	}

	h.logger.Infof("Control plane %s deleted from registry successfully", controlPlaneID)
	c.JSON(http.StatusOK, gin.H{
		"success":          true,
		"message":          "control plane deleted from registry successfully",
		"control_plane_id": controlPlaneID,
	})
}

// snapshotStalenessCeiling caps how old a snapshot may be before we fall
// back to the live gRPC path. Snapshots are written every 5 min by the
// leader, so anything past 10 min means the writer is wedged — in that
// case the live (potentially split) view is still better than a stale
// snapshot one.
const snapshotStalenessCeiling = 10 * time.Minute

// GetRegistryData returns all registry data (control planes and
// controllers) as JSON.
//
// Primary path: read the singleton snapshot doc from MongoDB
// (registry_snapshot._id="current"). Single atomic FindOne → all four
// arrays (controllers, client mappings, control planes, node mappings)
// come from the SAME snapshot, so the response is internally consistent.
//
// Fallback: the original two-gRPC aggregation path (one call per
// service). Used only when the snapshot doc is missing (no leader has
// written one yet) or stale beyond snapshotStalenessCeiling (writer
// wedged). Without the fallback admin diagnostics would dark during a
// cold start; with the fallback the UI keeps working and we keep the
// best-available view.
//
// The snapshot path adds source/age metadata to the response so the UI
// (and operators) can tell whether they are looking at the live state
// or a snapshot and how fresh it is.
func (h *RegistryHandler) GetRegistryData(c *gin.Context) {
	ctx := c.Request.Context()

	if h.registryClient == nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "Registry client not available",
			"data":    nil,
		})
		return
	}

	// Try snapshot first.
	if h.dbContext != nil && h.dbContext.Client != nil {
		registryData, age, err := h.registryClient.GetRegistryDataFromSnapshot(ctx, h.dbContext.Client)
		switch {
		case err == nil && age <= snapshotStalenessCeiling:
			c.JSON(http.StatusOK, gin.H{"message": "OK", "data": registryData})
			return
		case err == nil:
			// Snapshot exists but is too old — fall through to live path.
			h.logger.Warnf("Registry snapshot too stale (age=%s > %s), falling back to live gRPC path",
				age.Round(time.Second), snapshotStalenessCeiling)
		case errors.Is(err, registry.ErrSnapshotNotFound):
			h.logger.Debugf("Registry snapshot doc not yet written, falling back to live gRPC path")
		default:
			h.logger.Warnf("Registry snapshot read failed (%v), falling back to live gRPC path", err)
		}
	}

	// Fallback: original live aggregation via two parallel gRPC calls.
	registryData, err := h.registryClient.GetRegistryData(ctx)
	if err != nil {
		h.logger.Errorf("Failed to get registry data: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": fmt.Sprintf("Failed to get registry data: %v", err),
			"data":    nil,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "OK",
		"data":    registryData,
	})
}
