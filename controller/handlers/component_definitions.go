package handlers

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// GetComponentDefinitionsHandler handles GET /api/v3/components/definitions
func (h *Handler) GetComponentDefinitionsHandler(c *gin.Context) {
	h.handleRequest(c, func(ctx context.Context, _ models.ResourceClass, reqDetails models.RequestDetails) (any, error) {
		return h.Scenario.GetComponentCatalog()
	})
}

// GetComponentDefinitionHandler handles GET /api/v3/components/definitions/:component_name
func (h *Handler) GetComponentDefinitionHandler(c *gin.Context) {
	componentName := c.Param("component_name")
	
	response, err := h.Scenario.GetComponentDefinitionByType(componentName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, response)
}