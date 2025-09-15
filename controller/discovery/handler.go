package discovery

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/gin-gonic/gin"
)

type DiscoveryHandler struct {
	service *DiscoveryService
	logger  *logger.Logger
}

func NewDiscoveryHandler(dbContext *db.AppContext, pokeService *bridge.PokeServiceClient) *DiscoveryHandler {
	return &DiscoveryHandler{
		service: NewDiscoveryService(dbContext, pokeService),
		logger:  logger.NewLogger("controller/discovery"),
	}
}

// HandleK8sDiscovery handles incoming K8s discovery requests
func (dh *DiscoveryHandler) HandleK8sDiscovery(c *gin.Context) {
	// Get and validate discovery token
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error":   "Missing Authorization header",
			"message": "Discovery token is required",
		})
		return
	}

	// Extract token from Bearer format
	tokenParts := strings.Split(authHeader, " ")
	if len(tokenParts) != 2 || tokenParts[0] != "Bearer" {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error":   "Invalid Authorization format",
			"message": "Use Bearer <token> format",
		})
		return
	}

	token := tokenParts[1]

	// Parse discovery request
	var discoveryRequest K8sDiscoveryRequest
	if err := c.ShouldBindJSON(&discoveryRequest); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "Invalid request format",
			"details": err.Error(),
		})
		return
	}

	// Validate project field
	if discoveryRequest.Project == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "Missing project field",
			"message": "project is required",
		})
		return
	}

	// Validate token for this project
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	valid, err := dh.service.ValidateDiscoveryToken(ctx, token, discoveryRequest.Project)
	if err != nil {
		dh.logger.Errorf("Token validation error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Token validation failed",
			"message": "Internal server error",
		})
		return
	}

	if !valid {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error":   "Invalid discovery token",
			"message": "Token not found or invalid for project",
		})
		return
	}

	// Validate request data
	if discoveryRequest.Data.ClusterInfo.ClusterName == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "Missing cluster name",
			"message": "data.cluster_info.cluster_name is required",
		})
		return
	}

	if len(discoveryRequest.Data.Nodes) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "No nodes provided",
			"message": "At least one node is required in data.nodes",
		})
		return
	}

	// Read initial flag from header ("true"/"false")
	initialHeader := c.GetHeader("initial")
	isInitial := false
	if initialHeader != "" {
		if parsed, err := strconv.ParseBool(initialHeader); err == nil {
			isInitial = parsed
		}
	}

	// Process discovery
	dh.logger.Infof("Processing K8s discovery for cluster: %s, project: %s, nodes: %d, initial: %t",
		discoveryRequest.Data.ClusterInfo.ClusterName, discoveryRequest.Project, len(discoveryRequest.Data.Nodes), isInitial)

	// Log node roles summary
	masterCount := 0
	workerCount := 0
	nodeRoleCounts := make(map[string]int)

	for _, node := range discoveryRequest.Data.Nodes {
		for _, role := range node.Roles {
			nodeRoleCounts[role]++
			switch role {
			case "master", "control-plane":
				masterCount++
			case "worker":
				workerCount++
			}
		}
	}

	if len(nodeRoleCounts) > 0 {
		dh.logger.Infof("Node roles summary - Masters: %d, Workers: %d, All roles: %v",
			masterCount, workerCount, nodeRoleCounts)
	}

	result, err := dh.service.ProcessK8sDiscovery(ctx, discoveryRequest, discoveryRequest.Project, isInitial)
	if err != nil {
		dh.logger.Errorf("Discovery processing failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Discovery processing failed",
			"details": err.Error(),
		})
		return
	}

	// Return result
	if result.Success {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"result":  result,
			"message": "Discovery processed successfully",
		})
	} else {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"result":  result,
			"error":   result.Message,
		})
	}
}

// GetClusters returns registered clusters for a project
func (dh *DiscoveryHandler) GetClusters(c *gin.Context) {
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "Missing project parameter",
			"message": "Project is required",
		})
		return
	}

	// This endpoint is for internal data collection, not for regular users
	// Authentication middleware ensures the user is authenticated, that's sufficient
	dh.logger.Infof("Discovery clusters requested for project: %s", project)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	clusters, err := dh.service.GetClusters(ctx, project)
	if err != nil {
		dh.logger.Infof("Failed to get clusters: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to get clusters",
			"details": err.Error(),
		})
		return
	}

	// Filter to return only required fields
	filteredClusters := make([]gin.H, len(clusters))
	for i, cluster := range clusters {
		filteredClusters[i] = gin.H{
			"cluster_name":    cluster.ClusterName,
			"project":         cluster.Project,
			"nodes":           cluster.Nodes,
			"last_seen":       cluster.LastSeen,
			"cluster_version": cluster.ClusterVersion,
		}
	}

	c.JSON(http.StatusOK, filteredClusters)
}
