package routemap

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/controller/routemap/extractors"
)

// AnalyzeVirtualHost analyzes a Virtual Host resource for route mapping
func (ra *RouteAnalyzer) AnalyzeVirtualHost(ctx context.Context, req RouteAnalysisRequest) (*RouteMapGraph, error) {
	// Mark as visited
	visitKey := fmt.Sprintf("vh_%s_%s", req.ResourceName, req.Project)
	if ra.markVisited(visitKey) {
		return ra.graph, nil
	}

	// Fetch the virtual host resource
	vhResource, err := ra.getResource(ctx, "virtual_hosts", req.ResourceName, req.Project, req.Version)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch virtual host: %w", err)
	}

	// Extract the virtual host configuration
	vhConfig := vhResource.Resource.Resource
	if vhConfig == nil {
		return nil, fmt.Errorf("virtual host configuration is empty")
	}

	// Check if vhConfig is an array (Envoy virtual host resource can contain multiple VHs)
	var vhArray []any
	
	ra.logger.Debugf("Raw vhConfig type: %T, value: %+v", vhConfig, vhConfig)
	
	// Handle MongoDB primitive.A (array) type
	switch v := vhConfig.(type) {
	case []any:
		vhArray = v
		ra.logger.Debugf("vhConfig is []any with %d elements", len(vhArray))
	default:
		// For MongoDB primitive.A or other array types, use JSON marshaling/unmarshaling
		jsonBytes, err := json.Marshal(vhConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal vhConfig: %w", err)
		}
		
		var tempArray []any
		if err := json.Unmarshal(jsonBytes, &tempArray); err == nil && len(tempArray) > 0 {
			// Successfully unmarshaled as array
			vhArray = tempArray
			ra.logger.Debugf("vhConfig unmarshaled as array with %d elements", len(vhArray))
		} else {
			// Not an array or failed to unmarshal as array, treat as single object
			vhArray = []any{vhConfig}
			ra.logger.Debugf("vhConfig treated as single object, wrapping in array")
		}
	}

	// Process each virtual host in the resource
	for vhIndex, singleVH := range vhArray {
		// Extract virtual host information
		vhName := extractors.ExtractString(singleVH, "name")
		if vhName == "" {
			if len(vhArray) == 1 {
				vhName = req.ResourceName
			} else {
				vhName = fmt.Sprintf("%s_%d", req.ResourceName, vhIndex)
			}
		}

		// Add index number to virtual host label
		vhLabelWithIndex := fmt.Sprintf("(%d) %s", vhIndex+1, vhName)

		domains := extractors.ExtractDomains(singleVH)
		
		// Create virtual host node with indexed label
		vhNode := ra.createVirtualHostNodeWithLabel(vhName, vhLabelWithIndex, domains, "direct")
		vhNode.Data.ResourceID = vhResource.ID.Hex()
		
		ra.addNode(vhNode)

		// Add additional virtual host properties
		ra.addVirtualHostProperties(singleVH, vhNode.Data.ID)

		// Create domain nodes and process routes for each domain
		routes := extractors.ExtractRoutes(singleVH)
		ra.logger.Infof("Virtual host %s has %d routes", vhName, len(routes))
		
		// Debug: print raw virtual host data
		ra.logger.Debugf("Virtual host raw data: %+v", singleVH)
		ra.logger.Debugf("Extracted routes: %+v", routes)

		// Create domain nodes first
		for _, domain := range domains {
			domainNodeID := ra.generateNodeID("domain", domain, vhNode.Data.ID)
			domainNode := RouteNode{
				Data: RouteNodeData{
					ID:       domainNodeID,
					Label:    domain,
					Type:     "domain",
					Category: "domain",
					Properties: map[string]any{
						"description": "Host domain or pattern for virtual host routing",
					},
				},
			}
			ra.addNode(domainNode)
			ra.addEdge(vhNode.Data.ID, domainNodeID, "has domain", "has_domain", nil)

			// Process all routes for this domain
			for idx, route := range routes {
				ra.logger.Debugf("Processing route %d for domain %s: %+v", idx, domain, route)
				ra.processVirtualHostRoute(route, domainNodeID, domain, idx)
			}
		}

		// Process rate limits if present
		if rateLimits := extractors.ExtractArray(singleVH, "rate_limits"); len(rateLimits) > 0 {
			ra.processVHRateLimits(rateLimits, vhNode.Data.ID)
		}

		// Process CORS policy if present
		if corsPolicy := extractors.ExtractMap(singleVH, "cors"); corsPolicy != nil {
			ra.processVHCORSPolicy(corsPolicy, vhNode.Data.ID)
		}

		// Process retry policy if present
		if retryPolicy := extractors.ExtractMap(singleVH, "retry_policy"); retryPolicy != nil {
			ra.processVHRetryPolicy(retryPolicy, vhNode.Data.ID)
		}
	}

	return ra.graph, nil
}

// addVirtualHostProperties adds additional properties to virtual host node
func (ra *RouteAnalyzer) addVirtualHostProperties(vhConfig any, nodeID string) {
	properties := make(map[string]any)

	// Add require_tls setting
	if requireTLS := extractors.ExtractString(vhConfig, "require_tls"); requireTLS != "" {
		properties["require_tls"] = requireTLS
	}

	// Add include_request_attempt_count
	if includeAttemptCount := extractors.ExtractBool(vhConfig, "include_request_attempt_count"); includeAttemptCount {
		properties["include_request_attempt_count"] = includeAttemptCount
	}

	// Add include_attempt_count_header
	if attemptCountHeader := extractors.ExtractString(vhConfig, "include_attempt_count_in_response"); attemptCountHeader != "" {
		properties["include_attempt_count_in_response"] = attemptCountHeader
	}

	// Update node properties
	if len(properties) > 0 {
		for i, node := range ra.graph.Nodes {
			if node.Data.ID == nodeID {
				if ra.graph.Nodes[i].Data.Properties == nil {
					ra.graph.Nodes[i].Data.Properties = make(map[string]any)
				}
				for k, v := range properties {
					ra.graph.Nodes[i].Data.Properties[k] = v
				}
				break
			}
		}
	}
}

// processVHRateLimits processes rate limit configurations
func (ra *RouteAnalyzer) processVHRateLimits(rateLimits []any, parentNodeID string) {
	for _, rl := range rateLimits {
		// Create rate limit node
		nodeID := ra.generateNodeID("rate_limit", parentNodeID)
		rlNode := RouteNode{
			Data: RouteNodeData{
				ID:       nodeID,
				Label:    "Rate Limit",
				Type:     "rate_limit",
				Category: "policy",
				Properties: map[string]any{
					"stage":         extractors.ExtractInt(rl, "stage"),
					"disable_key":   extractors.ExtractString(rl, "disable_key"),
					"actions":       extractors.ExtractArray(rl, "actions"),
				},
			},
		}
		ra.addNode(rlNode)
		ra.addEdge(parentNodeID, nodeID, "has_rate_limit", "policy", nil)
	}
}

// processVHCORSPolicy processes CORS policy configuration
func (ra *RouteAnalyzer) processVHCORSPolicy(corsPolicy map[string]any, parentNodeID string) {
	nodeID := ra.generateNodeID("cors", parentNodeID)
	corsNode := RouteNode{
		Data: RouteNodeData{
			ID:       nodeID,
			Label:    "CORS Policy",
			Type:     "cors",
			Category: "policy",
			Properties: map[string]any{
				"allow_origin":      extractors.ExtractStringArray(corsPolicy, "allow_origin_string_match"),
				"allow_methods":     extractors.ExtractString(corsPolicy, "allow_methods"),
				"allow_headers":     extractors.ExtractString(corsPolicy, "allow_headers"),
				"expose_headers":    extractors.ExtractString(corsPolicy, "expose_headers"),
				"allow_credentials": extractors.ExtractBool(corsPolicy, "allow_credentials"),
				"max_age":           extractors.ExtractString(corsPolicy, "max_age"),
			},
		},
	}
	ra.addNode(corsNode)
	ra.addEdge(parentNodeID, nodeID, "has_cors", "policy", nil)
}

// processVHRetryPolicy processes retry policy configuration
func (ra *RouteAnalyzer) processVHRetryPolicy(retryPolicy map[string]any, parentNodeID string) {
	nodeID := ra.generateNodeID("retry", parentNodeID)
	retryNode := RouteNode{
		Data: RouteNodeData{
			ID:       nodeID,
			Label:    "Retry Policy",
			Type:     "retry",
			Category: "policy",
			Properties: map[string]any{
				"retry_on":              extractors.ExtractString(retryPolicy, "retry_on"),
				"num_retries":           extractors.ExtractInt(retryPolicy, "num_retries.value"),
				"per_try_timeout":       extractors.ExtractString(retryPolicy, "per_try_timeout"),
				"retry_priority":        extractors.ExtractString(retryPolicy, "retry_priority.name"),
				"host_selection_policy": extractors.ExtractStringArray(retryPolicy, "retry_host_predicate"),
				"retriable_status_codes": extractors.ExtractArray(retryPolicy, "retriable_status_codes"),
			},
		},
	}
	ra.addNode(retryNode)
	ra.addEdge(parentNodeID, nodeID, "has_retry", "policy", nil)
}

// processVirtualHostRoute processes a single route within a virtual host
func (ra *RouteAnalyzer) processVirtualHostRoute(route any, domainNodeID, domain string, routeIndex int) {
	ra.logger.Debugf("processVirtualHostRoute called with route: %+v", route)
	
	// Extract route name and match conditions
	routeName := extractors.ExtractRouteName(route)
	match := extractors.ExtractRouteMatch(route)
	
	ra.logger.Debugf("Extracted route name: %s, match: %+v", routeName, match)
	
	// Add index number to route label
	routeLabelWithIndex := fmt.Sprintf("(%d) %s", routeIndex+1, routeName)
	
	// Generate unique route ID
	routeNodeID := ra.generateNodeID("route", routeName, domain, fmt.Sprintf("%d", routeIndex))
	
	// Create route node with indexed label
	routeNode := RouteNode{
		Data: RouteNodeData{
			ID:       routeNodeID,
			Label:    routeLabelWithIndex,
			Type:     "route",
			Category: "route",
			Properties: map[string]any{
				"description": "HTTP route configuration with matching conditions and actions",
			},
		},
	}
	ra.addNode(routeNode)
	
	ra.logger.Debugf("Created route node: %+v", routeNode)
	
	// Connect domain to route
	ra.addEdge(domainNodeID, routeNodeID, "has route", "has_route", map[string]any{
		"index": routeIndex,
	})
	
	// Process route action first
	action := extractors.ExtractRouteAction(route)
	ra.logger.Debugf("Extracted route action: %+v", action)
	
	// Create match nodes for each match condition and connect them to actions
	ra.processVirtualHostRouteMatches(route, match, routeNodeID, routeName, domain, routeIndex, action)
	
	// Process route-level policies if present
	ra.processVirtualHostRoutePolicies(route, routeNodeID)
}

// processVirtualHostRouteMatches creates a single comprehensive match node for route matching conditions
func (ra *RouteAnalyzer) processVirtualHostRouteMatches(route any, match extractors.RouteMatch, routeNodeID, routeName, domain string, routeIndex int, action extractors.RouteAction) {
	// Check if there are any match conditions
	hasAnyMatch := match.Path != "" || match.Prefix != "" || match.PathSeparatedPrefix != "" || match.Regex != "" || 
		len(match.Methods) > 0 || len(match.Headers) > 0 || len(match.QueryParams) > 0
	
	if hasAnyMatch {
		// Create comprehensive match description
		matchDescription := extractors.CreateMatchDescription(match)
		
		// Generate match node ID
		matchNodeID := ra.generateNodeID("match", "comprehensive", matchDescription, domain, fmt.Sprintf("%d", routeIndex))
		
		matchNode := RouteNode{
			Data: RouteNodeData{
				ID:       matchNodeID,
				Label:    matchDescription,
				Type:     "match",
				Category: "match",
				Properties: map[string]any{
					"description": "HTTP request matching conditions for route selection",
				},
			},
		}
		ra.addNode(matchNode)
		ra.addEdge(routeNodeID, matchNodeID, "matches", "has_match", nil)
		
		// Connect match to action
		ra.processVirtualHostRouteActionFromMatch(route, matchNodeID, action, routeName, domain, routeIndex)
	} else {
		// No match conditions, connect route directly to action
		ra.processVirtualHostRouteActionFromMatch(route, routeNodeID, action, routeName, domain, routeIndex)
	}
}

// processVirtualHostRouteActionFromMatch processes route actions connected to match nodes
func (ra *RouteAnalyzer) processVirtualHostRouteActionFromMatch(route any, sourceNodeID string, action extractors.RouteAction, routeName, domain string, routeIndex int) {
	ra.logger.Debugf("processVirtualHostRouteActionFromMatch called with action: %+v", action)
	
	switch action.Type {
	case "route":
		if action.Cluster != "" {
			// Single cluster routing
			clusterNodeID := ra.generateNodeID("route_action", action.Cluster, domain, fmt.Sprintf("%d", routeIndex))
			clusterNode := RouteNode{
				Data: RouteNodeData{
					ID:       clusterNodeID,
					Label:    action.Cluster,
					Type:     "cluster",
					Category: "action",
					Properties: map[string]any{
						"description": "Routes traffic to a backend cluster",
					},
				},
			}
			ra.addNode(clusterNode)
			ra.addEdge(sourceNodeID, clusterNodeID, "routes to", "routes_to", nil)
		}
		
		// Weighted cluster routing
		for i, wc := range action.WeightedCluster {
			clusterNodeID := ra.generateNodeID("route_action", wc.Name, domain, fmt.Sprintf("%d_%d", routeIndex, i))
			clusterNode := RouteNode{
				Data: RouteNodeData{
					ID:       clusterNodeID,
					Label:    fmt.Sprintf("%s (weight: %d)", wc.Name, wc.Weight),
					Type:     "weighted_cluster",
					Category: "action",
					Properties: map[string]any{
						"description": "Routes traffic to a weighted backend cluster",
					},
				},
			}
			ra.addNode(clusterNode)
			ra.addEdge(sourceNodeID, clusterNodeID, "routes to", "routes_to", nil)
		}
		
	case "redirect":
		if action.Redirect != nil {
			redirectNodeID := ra.generateNodeID("redirect", routeName, domain, fmt.Sprintf("%d", routeIndex))
			
			// Combine all redirect types that are configured
			var redirectParts []string
			if action.Redirect.HostRedirect != "" {
				redirectParts = append(redirectParts, fmt.Sprintf("host: %s", action.Redirect.HostRedirect))
			}
			if action.Redirect.PathRedirect != "" {
				redirectParts = append(redirectParts, fmt.Sprintf("path: %s", action.Redirect.PathRedirect))
			}
			if action.Redirect.HTTPSRedirect {
				redirectParts = append(redirectParts, "https: true")
			}
			
			var redirectValue string
			if len(redirectParts) > 0 {
				redirectValue = strings.Join(redirectParts, ", ")
			} else {
				redirectValue = "enabled"
			}
			
			redirectNode := RouteNode{
				Data: RouteNodeData{
					ID:       redirectNodeID,
					Label:    fmt.Sprintf("redirect: %s", redirectValue),
					Type:     "redirect",
					Category: "action",
					Properties: map[string]any{
						"description": "Redirects incoming requests to a different location",
					},
				},
			}
			ra.addNode(redirectNode)
			ra.addEdge(sourceNodeID, redirectNodeID, "redirects to", "redirects_to", nil)
		}
		
	case "direct_response":
		if action.DirectResponse != nil {
			directResponseNodeID := ra.generateNodeID("direct_response", fmt.Sprintf("%d", action.DirectResponse.Status), domain, fmt.Sprintf("%d", routeIndex))
			directResponseNode := RouteNode{
				Data: RouteNodeData{
					ID:       directResponseNodeID,
					Label:    fmt.Sprintf("direct response: %d", action.DirectResponse.Status),
					Type:     "direct_response",
					Category: "action",
					Properties: map[string]any{
						"description": "Returns a direct HTTP response without proxying",
					},
				},
			}
			ra.addNode(directResponseNode)
			ra.addEdge(sourceNodeID, directResponseNodeID, "responds with", "responds_with", nil)
		}
	}
}


// processVirtualHostRoutePolicies processes route-level policies
func (ra *RouteAnalyzer) processVirtualHostRoutePolicies(route any, routeNodeID string) {
	// Process route-level timeout
	if timeout := extractors.ExtractString(route, "timeout"); timeout != "" {
		timeoutNodeID := ra.generateNodeID("timeout", routeNodeID)
		timeoutNode := RouteNode{
			Data: RouteNodeData{
				ID:       timeoutNodeID,
				Label:    fmt.Sprintf("Timeout: %s", timeout),
				Type:     "timeout",
				Category: "policy",
				Properties: map[string]any{
					"timeout": timeout,
				},
			},
		}
		ra.addNode(timeoutNode)
		ra.addEdge(routeNodeID, timeoutNodeID, "has_timeout", "policy", nil)
	}
	
	// Process route-level retry policy
	if retryPolicy := extractors.ExtractMap(route, "retry_policy"); retryPolicy != nil {
		ra.processVHRetryPolicy(retryPolicy, routeNodeID)
	}
	
	// Process route-level rate limits
	if rateLimits := extractors.ExtractArray(route, "rate_limits"); len(rateLimits) > 0 {
		ra.processVHRateLimits(rateLimits, routeNodeID)
	}
}