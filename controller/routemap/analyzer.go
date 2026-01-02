package routemap

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
)

// RouteAnalyzer is the main analyzer for route mapping
type RouteAnalyzer struct {
	db        *db.AppContext
	logger    *logger.Logger
	graph     *RouteMapGraph
	visited   map[string]bool
	nodeIDMap map[string]string // For consistent ID generation
}

// NewRouteAnalyzer creates a new route analyzer instance
func NewRouteAnalyzer(db *db.AppContext) *RouteAnalyzer {
	return &RouteAnalyzer{
		db:     db,
		logger: logger.NewLogger("controller/routemap"),
	}
}

// Analyze performs route analysis based on resource type
func (ra *RouteAnalyzer) Analyze(ctx context.Context, req RouteAnalysisRequest) (*RouteMapGraph, error) {
	ra.graph = &RouteMapGraph{
		Nodes: []RouteNode{},
		Edges: []RouteEdge{},
	}
	ra.visited = make(map[string]bool)
	ra.nodeIDMap = make(map[string]string)

	// Check if resource type supports route mapping
	switch req.GType {
	case models.HTTPConnectionManager:
		return ra.AnalyzeHTTPConnectionManager(ctx, req)
	case models.Route:
		return ra.AnalyzeRouteConfiguration(ctx, req)
	case models.VirtualHost:
		return ra.AnalyzeVirtualHost(ctx, req)
	default:
		return nil, fmt.Errorf("route mapping not supported for %s", req.GType)
	}
}

// getResource fetches a resource from database
func (ra *RouteAnalyzer) getResource(ctx context.Context, collection, name, project, version string) (*models.DBResource, error) {
	filter := bson.M{
		"general.name":    name,
		"general.project": project,
		"general.version": version,
	}

	var resource models.DBResource
	err := ra.db.Client.Collection(collection).FindOne(ctx, filter).Decode(&resource)
	if err != nil {
		ra.logger.Errorf("Failed to fetch resource %s from %s: %v", name, collection, err)
		return nil, err
	}

	return &resource, nil
}

// generateNodeID generates a consistent ID for nodes
func (ra *RouteAnalyzer) generateNodeID(nodeType, name string, properties ...string) string {
	key := fmt.Sprintf("%s_%s", nodeType, name)
	for _, prop := range properties {
		key += "_" + prop
	}

	// Check if we already have an ID for this key
	if id, exists := ra.nodeIDMap[key]; exists {
		return id
	}

	// Generate new ID using SHA256 for cryptographic strength
	hash := sha256.Sum256([]byte(key))
	id := fmt.Sprintf("%s_%x", nodeType, hash[:6])
	ra.nodeIDMap[key] = id
	return id
}

// generateEdgeID generates a consistent ID for edges
func (ra *RouteAnalyzer) generateEdgeID(source, target, edgeType string) string {
	return fmt.Sprintf("%s__%s__%s", source, target, edgeType)
}

// addNode adds a node to the graph if not already present
func (ra *RouteAnalyzer) addNode(node RouteNode) {
	// Check if node already exists
	for _, existing := range ra.graph.Nodes {
		if existing.Data.ID == node.Data.ID {
			return
		}
	}
	ra.graph.Nodes = append(ra.graph.Nodes, node)
	ra.logger.Debugf("Added node: %s (%s)", node.Data.Label, node.Data.Type)
}

// addEdge adds an edge to the graph if not already present
func (ra *RouteAnalyzer) addEdge(source, target, label, edgeType string, properties map[string]any) {
	edgeID := ra.generateEdgeID(source, target, edgeType)

	// Check if edge already exists
	for _, existing := range ra.graph.Edges {
		if existing.Data.ID == edgeID {
			return
		}
	}

	edge := RouteEdge{
		Data: RouteEdgeData{
			ID:         edgeID,
			Source:     source,
			Target:     target,
			Label:      label,
			Type:       edgeType,
			Properties: properties,
		},
	}
	ra.graph.Edges = append(ra.graph.Edges, edge)
	ra.logger.Debugf("Added edge: %s -> %s (%s)", source, target, edgeType)
}

// markVisited marks a resource as visited to prevent cycles
func (ra *RouteAnalyzer) markVisited(key string) bool {
	if ra.visited[key] {
		return true
	}
	ra.visited[key] = true
	return false
}

// createFilterNode creates a node for a filter (like HTTP Connection Manager)
func (ra *RouteAnalyzer) createFilterNode(filterName, filterType string, properties map[string]any) RouteNode {
	nodeID := ra.generateNodeID("filter", filterName)

	// Set type based on filter type
	nodeType := "filter"
	if filterType == "http_connection_manager" {
		nodeType = "Http Connection Manager"
	}

	return RouteNode{
		Data: RouteNodeData{
			ID:         nodeID,
			Label:      filterName,
			Type:       nodeType,
			Category:   filterType,
			Properties: properties,
		},
	}
}

// createRouteConfigNode creates a node for route configuration
func (ra *RouteAnalyzer) createRouteConfigNode(name string, source string, resourceID string) RouteNode {
	nodeID := ra.generateNodeID("route_config", name)

	// Set type based on source
	nodeType := "route_config"
	if source == "rds" {
		nodeType = "rds"
	}

	return RouteNode{
		Data: RouteNodeData{
			ID:         nodeID,
			Label:      name,
			Type:       nodeType,
			Category:   "routes",
			Source:     source,
			ResourceID: resourceID,
		},
	}
}

// createVirtualHostNodeWithLabel creates a node for virtual host with custom label
func (ra *RouteAnalyzer) createVirtualHostNodeWithLabel(name string, label string, domains []string, source string) RouteNode {
	nodeID := ra.generateNodeID("virtual_host", name)
	return RouteNode{
		Data: RouteNodeData{
			ID:       nodeID,
			Label:    label,
			Type:     "virtual_host",
			Category: "virtual_host",
			Properties: map[string]any{
				"domains": domains,
			},
			Source: source,
		},
	}
}
