package routemap

import (
	"context"
	"crypto/md5"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/controller/routemap/extractors"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
)

// RouteAnalyzer is the main analyzer for route mapping
type RouteAnalyzer struct {
	db       *db.AppContext
	logger   *logger.Logger
	graph    *RouteMapGraph
	visited  map[string]bool
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
	
	// Generate new ID
	hash := md5.Sum([]byte(key))
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

// createVirtualHostNode creates a node for virtual host
func (ra *RouteAnalyzer) createVirtualHostNode(name string, domains []string, source string) RouteNode {
	nodeID := ra.generateNodeID("virtual_host", name)
	return RouteNode{
		Data: RouteNodeData{
			ID:       nodeID,
			Label:    name,
			Type:     "virtual_host",
			Category: "virtual_host",
			Properties: map[string]any{
				"domains": domains,
			},
			Source: source,
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

// createRouteNode creates a node for a route
func (ra *RouteAnalyzer) createRouteNode(name string, match extractors.RouteMatch) RouteNode {
	nodeID := ra.generateNodeID("route", name)
	
	// Create a readable label for the route
	label := name
	if label == "" {
		if match.Path != "" {
			label = "Path: " + match.Path
		} else if match.Prefix != "" {
			label = "Prefix: " + match.Prefix
		} else if match.PathSeparatedPrefix != "" {
			label = "PathSepPrefix: " + match.PathSeparatedPrefix
		} else if match.Regex != "" {
			label = "Regex: " + match.Regex
		} else {
			label = "Route"
		}
	}
	
	properties := make(map[string]any)
	if match.Path != "" {
		properties["path"] = match.Path
	}
	if match.Prefix != "" {
		properties["prefix"] = match.Prefix
	}
	if match.PathSeparatedPrefix != "" {
		properties["path_separated_prefix"] = match.PathSeparatedPrefix
	}
	if match.Regex != "" {
		properties["regex"] = match.Regex
	}
	if len(match.Methods) > 0 {
		properties["methods"] = match.Methods
	}
	if len(match.Headers) > 0 {
		properties["headers"] = match.Headers
	}
	if len(match.QueryParams) > 0 {
		properties["query_params"] = match.QueryParams
	}
	
	return RouteNode{
		Data: RouteNodeData{
			ID:         nodeID,
			Label:      label,
			Type:       "route",
			Category:   "route",
			Properties: properties,
		},
	}
}

// createClusterNode creates a node for a cluster destination
func (ra *RouteAnalyzer) createClusterNode(clusterName string) RouteNode {
	nodeID := ra.generateNodeID("cluster", clusterName)
	return RouteNode{
		Data: RouteNodeData{
			ID:       nodeID,
			Label:    clusterName,
			Type:     "cluster",
			Category: "destination",
		},
	}
}

// createRedirectNode creates a node for redirect action
func (ra *RouteAnalyzer) createRedirectNode(redirect *extractors.RedirectAction) RouteNode {
	nodeID := ra.generateNodeID("redirect", redirect.HostRedirect, redirect.PathRedirect)
	
	label := "Redirect"
	if redirect.HostRedirect != "" {
		label = fmt.Sprintf("Redirect to %s", redirect.HostRedirect)
	} else if redirect.PathRedirect != "" {
		label = fmt.Sprintf("Redirect to %s", redirect.PathRedirect)
	}
	
	properties := make(map[string]any)
	if redirect.HostRedirect != "" {
		properties["host"] = redirect.HostRedirect
	}
	if redirect.PathRedirect != "" {
		properties["path"] = redirect.PathRedirect
	}
	if redirect.ResponseCode > 0 {
		properties["code"] = redirect.ResponseCode
	}
	properties["https_redirect"] = redirect.HTTPSRedirect
	
	return RouteNode{
		Data: RouteNodeData{
			ID:         nodeID,
			Label:      label,
			Type:       "redirect",
			Category:   "action",
			Properties: properties,
		},
	}
}

// createDirectResponseNode creates a node for direct response action
func (ra *RouteAnalyzer) createDirectResponseNode(response *extractors.DirectResponse) RouteNode {
	nodeID := ra.generateNodeID("direct_response", fmt.Sprintf("%d", response.Status))
	label := fmt.Sprintf("Direct Response (%d)", response.Status)
	
	properties := map[string]any{
		"status": response.Status,
	}
	if response.Body != "" {
		properties["body"] = response.Body
	}
	
	return RouteNode{
		Data: RouteNodeData{
			ID:         nodeID,
			Label:      label,
			Type:       "direct_response",
			Category:   "action",
			Properties: properties,
		},
	}
}

// Public accessor methods for collectors package

// GetResource fetches a resource from database (public for collectors)
func (ra *RouteAnalyzer) GetResource(ctx context.Context, collection, name, project, version string) (*models.DBResource, error) {
	return ra.getResource(ctx, collection, name, project, version)
}

// GenerateNodeID generates a consistent ID for nodes (public for collectors)
func (ra *RouteAnalyzer) GenerateNodeID(nodeType, name string, properties ...string) string {
	return ra.generateNodeID(nodeType, name, properties...)
}

// AddNode adds a node to the graph (public for collectors)
func (ra *RouteAnalyzer) AddNode(node RouteNode) {
	ra.addNode(node)
}

// AddEdge adds an edge to the graph (public for collectors)
func (ra *RouteAnalyzer) AddEdge(source, target, label, edgeType string, properties map[string]any) {
	ra.addEdge(source, target, label, edgeType, properties)
}

// MarkVisited marks a resource as visited (public for collectors)
func (ra *RouteAnalyzer) MarkVisited(key string) bool {
	return ra.markVisited(key)
}

// CreateFilterNode creates a filter node (public for collectors)
func (ra *RouteAnalyzer) CreateFilterNode(filterName, filterType string, properties map[string]any) RouteNode {
	return ra.createFilterNode(filterName, filterType, properties)
}

// CreateRouteConfigNode creates a route config node (public for collectors)
func (ra *RouteAnalyzer) CreateRouteConfigNode(name string, source string, resourceID string) RouteNode {
	return ra.createRouteConfigNode(name, source, resourceID)
}

// CreateVirtualHostNode creates a virtual host node (public for collectors)
func (ra *RouteAnalyzer) CreateVirtualHostNode(name string, domains []string, source string) RouteNode {
	return ra.createVirtualHostNode(name, domains, source)
}

// CreateVirtualHostNodeWithLabel creates a virtual host node with custom label (public for collectors)
func (ra *RouteAnalyzer) CreateVirtualHostNodeWithLabel(name string, label string, domains []string, source string) RouteNode {
	return ra.createVirtualHostNodeWithLabel(name, label, domains, source)
}

// CreateRouteNode creates a route node (public for collectors)
func (ra *RouteAnalyzer) CreateRouteNode(name string, match extractors.RouteMatch) RouteNode {
	return ra.createRouteNode(name, match)
}

// CreateClusterNode creates a cluster node (public for collectors)
func (ra *RouteAnalyzer) CreateClusterNode(clusterName string) RouteNode {
	return ra.createClusterNode(clusterName)
}

// CreateRedirectNode creates a redirect node (public for collectors)
func (ra *RouteAnalyzer) CreateRedirectNode(redirect *extractors.RedirectAction) RouteNode {
	return ra.createRedirectNode(redirect)
}

// CreateDirectResponseNode creates a direct response node (public for collectors)
func (ra *RouteAnalyzer) CreateDirectResponseNode(response *extractors.DirectResponse) RouteNode {
	return ra.createDirectResponseNode(response)
}

// Graph returns the current graph (public for collectors)
func (ra *RouteAnalyzer) Graph() *RouteMapGraph {
	return ra.graph
}

// Logger returns the logger (public for collectors)
func (ra *RouteAnalyzer) Logger() *logger.Logger {
	return ra.logger
}

// DB returns the database context (public for collectors)
func (ra *RouteAnalyzer) DB() *db.AppContext {
	return ra.db
}