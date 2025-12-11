package routemap

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/controller/routemap/extractors"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// AnalyzeRouteConfiguration analyzes a Route Configuration resource for route mapping
func (ra *RouteAnalyzer) AnalyzeRouteConfiguration(ctx context.Context, req RouteAnalysisRequest) (*RouteMapGraph, error) {
	// Mark as visited
	visitKey := fmt.Sprintf("route_%s_%s", req.ResourceName, req.Project)
	if ra.markVisited(visitKey) {
		return ra.graph, nil
	}

	// Fetch the route configuration resource
	routeResource, err := ra.getResource(ctx, "routes", req.ResourceName, req.Project, req.Version)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch route configuration: %w", err)
	}

	// Extract the route configuration
	routeConfig := routeResource.Resource.Resource
	if routeConfig == nil {
		return nil, fmt.Errorf("route configuration is empty")
	}

	// Create route configuration node
	routeConfigNode := ra.createRouteConfigNode(req.ResourceName, "direct", routeResource.ID.Hex())

	ra.addNode(routeConfigNode)

	// Check for VHDS (Virtual Host Discovery Service)
	if extractors.ExtractVHDS(routeConfig) != "" {
		ra.logger.Info("VHDS configuration detected in route, checking config_discovery for VHDS names")

		// Extract VHDS names from the resource's config_discovery
		configDiscovery := extractors.ExtractArray(routeResource.General, "config_discovery")
		if len(configDiscovery) > 0 {
			vhdsNames := extractors.ExtractVHDSNamesFromConfigDiscovery(configDiscovery)
			if len(vhdsNames) > 0 {
				ra.logger.Infof("Found %d VHDS virtual hosts: %v", len(vhdsNames), vhdsNames)

				// Create VHDS node first (using the first VHDS name as the representative)
				vhdsNodeID := ra.generateNodeID("vhds", vhdsNames[0], req.Project)
				vhdsNode := RouteNode{
					Data: RouteNodeData{
						ID:       vhdsNodeID,
						Label:    vhdsNames[0], // This will be the general.name
						Type:     "vhds",
						Category: "vhds",
						Properties: map[string]any{
							"description": fmt.Sprintf("VHDS resource: %s", vhdsNames[0]),
						},
						Source: "vhds",
					},
				}
				ra.addNode(vhdsNode)
				ra.addEdge(routeConfigNode.Data.ID, vhdsNodeID, "uses VHDS", "uses_vhds", nil)

				// Fetch virtual hosts from VHDS
				vhdsResources, err := ra.fetchVirtualHostResourcesFromVHDS(ctx, vhdsNames, req.Project, req.Version)
				if err != nil {
					ra.logger.Warnf("Failed to fetch VHDS virtual hosts: %v", err)
				} else {
					ra.logger.Infof("Processing %d VHDS virtual host resources", len(vhdsResources))
					for i, vhResource := range vhdsResources {
						ra.logger.Infof("Processing VHDS virtual host %d: name=%s, id=%s", i, vhResource.General.Name, vhResource.ID.Hex())
						// Now connect virtual hosts to VHDS node instead of route config
						ra.processVirtualHostResourceFromRouteConfig(vhResource, vhdsNodeID, "vhds")
					}
				}
			}
		}
	}

	// Check for inline virtual hosts
	if virtualHosts := extractors.ExtractVirtualHosts(routeConfig); len(virtualHosts) > 0 {
		ra.logger.Infof("Found %d inline virtual hosts", len(virtualHosts))
		for vhIndex, vh := range virtualHosts {
			ra.processInlineVirtualHostFromRouteConfig(vh, routeConfigNode.Data.ID, "inline", vhIndex)
		}
	}

	// Also check for internal_only_headers if present
	if internalHeaders := extractors.ExtractStringArray(routeConfig, "internal_only_headers"); len(internalHeaders) > 0 {
		// Add as properties to the route config node
		for i, node := range ra.graph.Nodes {
			if node.Data.ID == routeConfigNode.Data.ID {
				if ra.graph.Nodes[i].Data.Properties == nil {
					ra.graph.Nodes[i].Data.Properties = make(map[string]any)
				}
				ra.graph.Nodes[i].Data.Properties["internal_only_headers"] = internalHeaders
				break
			}
		}
	}

	// Check for most_specific_header_mutations_wins setting
	if mostSpecific := extractors.ExtractBool(routeConfig, "most_specific_header_mutations_wins"); mostSpecific {
		for i, node := range ra.graph.Nodes {
			if node.Data.ID == routeConfigNode.Data.ID {
				if ra.graph.Nodes[i].Data.Properties == nil {
					ra.graph.Nodes[i].Data.Properties = make(map[string]any)
				}
				ra.graph.Nodes[i].Data.Properties["most_specific_header_mutations_wins"] = mostSpecific
				break
			}
		}
	}

	return ra.graph, nil
}

// processVirtualHostResourceFromRouteConfig processes a virtual host resource found in route configuration (for VHDS)
func (ra *RouteAnalyzer) processVirtualHostResourceFromRouteConfig(vhResource *models.DBResource, parentNodeID, source string) {
	// Get the actual virtual host data - this can be an array of virtual hosts
	virtualHostData := vhResource.Resource.Resource
	ra.logger.Debugf("Processing VHDS virtual host resource: %+v", virtualHostData)

	// Handle array of virtual hosts (like in vh_analyzer.go)
	var vhArray []any

	// Try to cast to []any directly first
	if arr, ok := virtualHostData.([]any); ok {
		vhArray = arr
		ra.logger.Debugf("virtualHostData is array with %d elements", len(vhArray))
	} else {
		// For MongoDB primitive.A or other array types, use JSON marshaling/unmarshaling
		jsonBytes, err := json.Marshal(virtualHostData)
		if err != nil {
			ra.logger.Errorf("Failed to marshal virtualHostData: %v", err)
			return
		}

		var tempArray []any
		if err := json.Unmarshal(jsonBytes, &tempArray); err == nil && len(tempArray) > 0 {
			// Successfully unmarshaled as array
			vhArray = tempArray
			ra.logger.Debugf("virtualHostData unmarshaled as array with %d elements", len(vhArray))
		} else {
			// Not an array or failed to unmarshal as array, treat as single object
			vhArray = []any{virtualHostData}
			ra.logger.Debugf("virtualHostData treated as single object, wrapping in array")
		}
	}

	// Process each virtual host in the array
	for vhIndex, singleVH := range vhArray {
		ra.logger.Debugf("Processing virtual host %d: %+v", vhIndex, singleVH)

		// Extract virtual host name from the individual VH object
		vhName := extractors.ExtractString(singleVH, "name")
		ra.logger.Debugf("Extracted VH name: '%s' from virtual host %d", vhName, vhIndex)

		if vhName == "" {
			// Generate name using general.name and index
			vhName = fmt.Sprintf("%s_vh%d", vhResource.General.Name, vhIndex)
			ra.logger.Warnf("Virtual host name field is empty, using: %s", vhName)
		}

		// Add index number to virtual host label
		vhLabelWithIndex := fmt.Sprintf("(%d) %s", vhIndex+1, vhName)

		domains := extractors.ExtractDomains(singleVH)

		// Create virtual host node with unique ID and indexed label
		vhNode := ra.createVirtualHostNodeWithLabel(vhName, vhLabelWithIndex, domains, source)
		vhNode.Data.ResourceID = vhResource.ID.Hex()

		ra.addNode(vhNode)

		// Connect to parent (VHDS node)
		ra.addEdge(parentNodeID, vhNode.Data.ID, "has virtual host", "has_virtual_host", map[string]any{
			"domains": domains,
			"source":  source,
		})

		// Now process this individual virtual host
		ra.processVirtualHostDetails(singleVH, vhNode.Data.ID, vhName, domains)
	}
}

// processInlineVirtualHostFromRouteConfig processes inline virtual hosts with index numbering
func (ra *RouteAnalyzer) processInlineVirtualHostFromRouteConfig(virtualHost any, parentNodeID, source string, vhIndex int) {
	// Extract virtual host name and domains
	vhName := extractors.ExtractString(virtualHost, "name")
	if vhName == "" {
		vhName = "unnamed_vhost"
	}

	// Add index number to virtual host label for inline VHs
	vhLabelWithIndex := fmt.Sprintf("(%d) %s", vhIndex+1, vhName)

	domains := extractors.ExtractDomains(virtualHost)

	// Create virtual host node with indexed label
	vhNode := ra.createVirtualHostNodeWithLabel(vhName, vhLabelWithIndex, domains, source)

	ra.addNode(vhNode)

	// Connect to parent (route config)
	ra.addEdge(parentNodeID, vhNode.Data.ID, "has virtual host", "has_virtual_host", map[string]any{
		"domains": domains,
		"source":  source,
	})

	// Now process the virtual host in detail like in AnalyzeVirtualHost
	ra.processVirtualHostDetails(virtualHost, vhNode.Data.ID, vhName, domains)
}

// processVirtualHostDetails processes virtual host details (domains, routes, matches, actions)
func (ra *RouteAnalyzer) processVirtualHostDetails(virtualHost any, vhNodeID, vhName string, domains []string) {
	// Debug: print raw virtual host data
	ra.logger.Debugf("Virtual host raw data: %+v", virtualHost)

	// Handle MongoDB primitive.A (array) type like in vh_analyzer
	var vhArray []any

	// Try to cast to []any directly first
	if arr, ok := virtualHost.([]any); ok {
		vhArray = arr
		ra.logger.Debugf("virtualHost is array with %d elements", len(vhArray))
	} else {
		// For MongoDB primitive.A or other array types, use JSON marshaling/unmarshaling
		jsonBytes, err := json.Marshal(virtualHost)
		if err != nil {
			ra.logger.Errorf("Failed to marshal virtualHost: %v", err)
			return
		}

		var tempArray []any
		if err := json.Unmarshal(jsonBytes, &tempArray); err == nil && len(tempArray) > 0 {
			// Successfully unmarshaled as array
			vhArray = tempArray
			ra.logger.Debugf("virtualHost unmarshaled as array with %d elements", len(vhArray))
		} else {
			// Not an array or failed to unmarshal as array, treat as single object
			vhArray = []any{virtualHost}
			ra.logger.Debugf("virtualHost treated as single object, wrapping in array")
		}
	}

	// Process each virtual host in the array (usually just one)
	for vhIndex, singleVH := range vhArray {
		ra.logger.Debugf("Processing virtual host %d: %+v", vhIndex, singleVH)

		// Create domain nodes and process routes for each domain
		routes := extractors.ExtractRoutes(singleVH)
		ra.logger.Infof("Virtual host %s has %d routes", vhName, len(routes))
		ra.logger.Debugf("Extracted routes: %+v", routes)

		// Create domain nodes first
		for _, domain := range domains {
			// Include virtual host name in domain ID to make it unique across different VHs
			domainNodeID := ra.generateNodeID("domain", domain, vhName, fmt.Sprintf("vh%d", vhIndex))
			domainNode := RouteNode{
				Data: RouteNodeData{
					ID:       domainNodeID,
					Label:    domain, // Just show the domain name
					Type:     "domain",
					Category: "domain",
					Properties: map[string]any{
						"description": "Host domain or pattern for virtual host routing",
					},
				},
			}
			ra.addNode(domainNode)
			ra.addEdge(vhNodeID, domainNodeID, "has domain", "has_domain", nil)

			// Process all routes for this domain
			for idx, route := range routes {
				ra.logger.Debugf("Processing route %d for domain %s: %+v", idx, domain, route)
				// Use vhIndex to make routes unique across virtual hosts
				uniqueRouteIndex := fmt.Sprintf("vh%d_r%d", vhIndex, idx)
				ra.processRouteFromRouteConfigWithVH(route, domainNodeID, domain, idx, uniqueRouteIndex)
			}
		}

		// Process virtual host-level policies (call methods from vh_analyzer)
		if rateLimits := extractors.ExtractArray(singleVH, "rate_limits"); len(rateLimits) > 0 {
			ra.processVHRateLimits(rateLimits, vhNodeID)
		}

		if corsPolicy := extractors.ExtractMap(singleVH, "cors"); corsPolicy != nil {
			ra.processVHCORSPolicy(corsPolicy, vhNodeID)
		}

		if retryPolicy := extractors.ExtractMap(singleVH, "retry_policy"); retryPolicy != nil {
			ra.processVHRetryPolicy(retryPolicy, vhNodeID)
		}
	}
}

// processRouteFromRouteConfigWithVH processes a single route with virtual host context
func (ra *RouteAnalyzer) processRouteFromRouteConfigWithVH(route any, domainNodeID, domain string, routeIndex int, uniqueRouteIndex string) {
	ra.logger.Debugf("processRouteFromRouteConfigWithVH called with route: %+v, uniqueIndex: %s", route, uniqueRouteIndex)

	// Extract route name and match conditions
	routeName := extractors.ExtractRouteName(route)
	match := extractors.ExtractRouteMatch(route)

	ra.logger.Debugf("Extracted route name: %s, match: %+v", routeName, match)

	// Add index number to route label
	routeLabelWithIndex := fmt.Sprintf("(%d) %s", routeIndex+1, routeName)

	// Generate unique route ID using uniqueRouteIndex
	routeNodeID := ra.generateNodeID("route", routeName, domain, uniqueRouteIndex)

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
	ra.processRouteMatchesFromRouteConfigWithVH(route, match, routeNodeID, routeName, domain, uniqueRouteIndex, action)

	// Process route-level policies if present
	ra.processRoutePolicies(route, routeNodeID)
}

// processRouteMatchesFromRouteConfigWithVH creates match nodes for route matching conditions with virtual host context
func (ra *RouteAnalyzer) processRouteMatchesFromRouteConfigWithVH(route any, match extractors.RouteMatch, routeNodeID, routeName, domain string, uniqueRouteIndex string, action extractors.RouteAction) {
	var matchNodeID string
	var hasMatch bool

	// Check if there are any match conditions and create a single comprehensive match node
	hasAnyMatch := match.Path != "" || match.Prefix != "" || match.PathSeparatedPrefix != "" || match.Regex != "" ||
		len(match.Methods) > 0 || len(match.Headers) > 0 || len(match.QueryParams) > 0

	if hasAnyMatch {
		// Create comprehensive match description
		matchDescription := extractors.CreateMatchDescription(match)

		// Generate match node ID with unique index
		matchNodeID = ra.generateNodeID("match", "comprehensive", matchDescription, domain, uniqueRouteIndex)

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
		hasMatch = true
	}

	// If we have match conditions, connect to actions
	if hasMatch {
		// Connect the last match node to action
		ra.processRouteActionFromRouteConfigWithVH(route, matchNodeID, action, routeName, domain, uniqueRouteIndex)
	} else {
		// No match conditions, connect route directly to action
		ra.processRouteActionFromRouteConfigWithVH(route, routeNodeID, action, routeName, domain, uniqueRouteIndex)
	}
}

// processRouteActionFromRouteConfigWithVH processes route actions connected to match nodes with virtual host context
func (ra *RouteAnalyzer) processRouteActionFromRouteConfigWithVH(route any, sourceNodeID string, action extractors.RouteAction, routeName, domain string, uniqueRouteIndex string) {
	ra.logger.Debugf("processRouteActionFromRouteConfigWithVH called with action: %+v, uniqueIndex: %s", action, uniqueRouteIndex)

	switch action.Type {
	case "route":
		if action.Cluster != "" {
			// Single cluster routing
			clusterNodeID := ra.generateNodeID("route_action", action.Cluster, domain, uniqueRouteIndex)
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
			clusterNodeID := ra.generateNodeID("route_action", wc.Name, domain, fmt.Sprintf("%s_%d", uniqueRouteIndex, i))
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
			redirectNodeID := ra.generateNodeID("redirect", routeName, domain, uniqueRouteIndex)

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
			directResponseNodeID := ra.generateNodeID("direct_response", fmt.Sprintf("%d", action.DirectResponse.Status), domain, uniqueRouteIndex)
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

// processRoutePolicies processes route-level policies
func (ra *RouteAnalyzer) processRoutePolicies(route any, routeNodeID string) {
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
