package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/registry"
	"github.com/gin-gonic/gin"
)

type RegistryHandler struct {
	registryClient *registry.RegistryClient
	logger         *logger.Logger
}

// NewRegistryHandler creates a new registry handler
func NewRegistryHandler(registryClient *registry.RegistryClient, logger *logger.Logger) *RegistryHandler {
	return &RegistryHandler{
		registryClient: registryClient,
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

// GetRegistryData returns all registry data (control planes and controllers) as JSON
func (h *RegistryHandler) GetRegistryData(c *gin.Context) {
	ctx := c.Request.Context()

	if h.registryClient == nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "Registry client not available",
			"data":    nil,
		})
		return
	}

	// Registry'den veri al
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
