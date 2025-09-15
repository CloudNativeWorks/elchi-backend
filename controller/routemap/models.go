package routemap

import (
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// RouteMapGraph represents the complete route mapping graph compatible with Cytoscape.js
type RouteMapGraph struct {
	Nodes []RouteNode `json:"nodes"`
	Edges []RouteEdge `json:"edges"`
}

// RouteNode represents a node in the route graph (Cytoscape format)
type RouteNode struct {
	Data RouteNodeData `json:"data"`
}

// RouteNodeData contains the node information
type RouteNodeData struct {
	ID         string         `json:"id"`
	Label      string         `json:"label"`
	Type       string         `json:"type"`                  // listener, filter, route_config, virtual_host, route, cluster, weighted_cluster, direct_response, redirect
	Category   string         `json:"category"`              // for styling/grouping
	Properties map[string]any `json:"properties"`            // domain, path, method, headers, etc.
	Source     string         `json:"source"`                // rds, vhds, inline
	ResourceID string         `json:"resource_id,omitempty"` // MongoDB resource ID
	Parent     string         `json:"parent,omitempty"`      // For compound nodes if needed
}

// RouteEdge represents an edge in the route graph (Cytoscape format)
type RouteEdge struct {
	Data RouteEdgeData `json:"data"`
}

// RouteEdgeData contains the edge information
type RouteEdgeData struct {
	ID         string         `json:"id"`
	Source     string         `json:"source"`
	Target     string         `json:"target"`
	Label      string         `json:"label"`
	Type       string         `json:"type"`                 // routes_to, matches, redirects_to, responds, has_virtual_host, has_route
	Properties map[string]any `json:"properties,omitempty"` // match conditions, weights, etc.
}

// RouteAnalysisRequest contains parameters for route analysis
type RouteAnalysisRequest struct {
	ResourceName string       `json:"resource_name"`
	GType        models.GType `json:"gtype"`
	Collection   string       `json:"collection"`
	Project      string       `json:"project"`
	Version      string       `json:"version"`
}
