package routemap

import (
	"fmt"
	"net/http"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/gin-gonic/gin"
)

// RouteMapHandler handles route map API requests
type RouteMapHandler struct {
	analyzer *RouteAnalyzer
}

// NewRouteMapHandler creates a new route map handler
func NewRouteMapHandler(db *db.AppContext) *RouteMapHandler {
	return &RouteMapHandler{
		analyzer: NewRouteAnalyzer(db),
	}
}

// GetRouteMap handles GET /api/v3/routemap/:name
func (h *RouteMapHandler) GetRouteMap(c *gin.Context) {
	ctx := c.Request.Context()

	// Extract request parameters
	resourceName := c.Param("name")
	gTypeStr := c.Query("gtype")
	collection := c.Query("collection")
	project := c.Query("project")
	version := c.Query("version")

	// Validate required parameters
	if resourceName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "resource name is required"})
		return
	}
	if gTypeStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "gtype is required"})
		return
	}
	if collection == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "collection is required"})
		return
	}
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project is required"})
		return
	}
	if version == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "version is required"})
		return
	}

	// Parse gType
	gType := models.GType(gTypeStr)

	// Check if resource type supports route mapping
	supportedTypes := []models.GType{
		models.HTTPConnectionManager,
		models.Route,
		models.VirtualHost,
	}

	supported := false
	for _, supportedType := range supportedTypes {
		if gType == supportedType {
			supported = true
			break
		}
	}

	if !supported {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("route mapping not supported for %s", gType),
			"supported_types": []string{
				string(models.HTTPConnectionManager),
				string(models.Route),
				string(models.VirtualHost),
			},
		})
		return
	}

	// Create analysis request
	req := RouteAnalysisRequest{
		ResourceName: resourceName,
		GType:        gType,
		Collection:   collection,
		Project:      project,
		Version:      version,
	}

	// Perform route analysis
	graph, err := h.analyzer.Analyze(ctx, req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to analyze route map: %v", err),
		})
		return
	}

	// Add metadata to response
	response := gin.H{
		"resource": gin.H{
			"name":       resourceName,
			"type":       gTypeStr,
			"collection": collection,
			"project":    project,
			"version":    version,
		},
		"graph": graph,
		"stats": gin.H{
			"nodes": len(graph.Nodes),
			"edges": len(graph.Edges),
		},
	}

	c.JSON(http.StatusOK, response)
}

// GetSupportedTypes handles GET /api/v3/routemap/supported-types
func (h *RouteMapHandler) GetSupportedTypes(c *gin.Context) {
	supportedTypes := []gin.H{
		{
			"gtype":       string(models.HTTPConnectionManager),
			"collection":  "filters",
			"category":    "http_connection_manager",
			"description": "HTTP Connection Manager filter with routing configuration",
		},
		{
			"gtype":       string(models.Route),
			"collection":  "routes",
			"category":    "route_configuration",
			"description": "Route Configuration with virtual hosts and routes",
		},
		{
			"gtype":       string(models.VirtualHost),
			"collection":  "virtual_hosts",
			"category":    "virtual_host",
			"description": "Virtual Host with domain matching and routes",
		},
	}

	c.JSON(http.StatusOK, gin.H{
		"supported_types": supportedTypes,
	})
}
