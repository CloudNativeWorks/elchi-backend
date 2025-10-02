package handlers

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/common"
	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/security"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type SearchMatch struct {
	FieldPath string         `json:"field_path"`
	Value     string         `json:"value"`
	Context   map[string]any `json:"context,omitempty"`
}

type SearchResult struct {
	Collection   string        `json:"collection"`
	ResourceID   string        `json:"resource_id"`
	ResourceName string        `json:"resource_name"`
	Project      string        `json:"project"`
	Version      string        `json:"version"`
	GType        string        `json:"gtype,omitempty"`
	URL          string        `json:"url,omitempty"`
	Matches      []SearchMatch `json:"matches"`
}

type SearchResponse struct {
	Query        string         `json:"query"`
	TotalResults int            `json:"total_results"`
	Results      []SearchResult `json:"results"`
}

// SearchFieldConfig defines a field path to search for IPs or domains
type SearchFieldConfig struct {
	FieldPath   string   // MongoDB field path (e.g., "resource.resource.endpoints[].lb_endpoints[].endpoint.address.socket_address.address")
	ContextPath string   // Path for extraction context (e.g., "endpoints[%d].lb_endpoints[%d]")
	ContextKeys []string // Additional context keys to extract (e.g., ["locality.region", "port_value"])
}

// CollectionSearchConfig defines search configuration for a MongoDB collection
type CollectionSearchConfig struct {
	Collection      string
	GType           models.GType
	IPFields        []SearchFieldConfig // Fields to search for IP addresses
	DomainFields    []SearchFieldConfig // Fields to search for domains
	UseSecureFilter bool                // Whether to apply user-based security filters
}

// getSearchConfigurations returns all search configurations for IP and domain searches
func getSearchConfigurations() map[string]CollectionSearchConfig {
	return map[string]CollectionSearchConfig{
		// Endpoints collection
		"endpoints": {
			Collection:      "endpoints",
			GType:           models.Endpoint,
			UseSecureFilter: true,
			IPFields: []SearchFieldConfig{
				{FieldPath: "resource.resource.endpoints.lb_endpoints.endpoint.address.socket_address.address"},
			},
		},

		// Discovery collection (K8s nodes)
		"discovery": {
			Collection:      "discovery",
			GType:           "",
			UseSecureFilter: false, // Has project filter built-in
			IPFields: []SearchFieldConfig{
				{FieldPath: "nodes.addresses.ExternalIP"},
				{FieldPath: "nodes.addresses.InternalIP"},
			},
		},

		// Clusters collection
		"clusters": {
			Collection:      "clusters",
			GType:           models.Cluster,
			UseSecureFilter: true,
			IPFields: []SearchFieldConfig{
				{FieldPath: "resource.resource.load_assignment.endpoints.lb_endpoints.endpoint.address.socket_address.address"},
			},
		},

		// Listeners collection
		"listeners": {
			Collection:      "listeners",
			GType:           models.Listener,
			UseSecureFilter: true,
			IPFields: []SearchFieldConfig{
				{FieldPath: "resource.resource.address.socket_address.address"},
			},
		},

		// Bootstrap collection
		"bootstrap": {
			Collection:      "bootstrap",
			GType:           models.BootStrap,
			UseSecureFilter: true,
			IPFields: []SearchFieldConfig{
				{FieldPath: "resource.resource.admin.address.socket_address.address"},
			},
		},

		// Services collection
		"services": {
			Collection:      "services",
			GType:           "",
			UseSecureFilter: false, // Has project filter built-in
			IPFields: []SearchFieldConfig{
				{FieldPath: "clients.downstream_address"},
			},
		},

		// Virtual hosts collection
		"virtual_hosts": {
			Collection:      "virtual_hosts",
			GType:           models.VirtualHost,
			UseSecureFilter: true,
			DomainFields: []SearchFieldConfig{
				{FieldPath: "resource.resource.domains"},
				{FieldPath: "resource.resource.routes.route.host_rewrite"},
				{FieldPath: "resource.resource.routes.redirect.host_redirect"},
			},
		},

		// Routes collection
		"routes": {
			Collection:      "routes",
			GType:           models.Route,
			UseSecureFilter: true,
			DomainFields: []SearchFieldConfig{
				{FieldPath: "resource.resource.virtual_hosts.domains"},
				{FieldPath: "resource.resource.virtual_hosts.routes.route.host_rewrite"},
				{FieldPath: "resource.resource.virtual_hosts.routes.redirect.host_redirect"},
			},
		},

		// Filters collection (HTTP Connection Manager, HTTP RBAC, Network RBAC)
		"filters": {
			Collection:      "filters",
			GType:           models.HTTPConnectionManager,
			UseSecureFilter: true,
			IPFields: []SearchFieldConfig{
				// RBAC rules with IP-based permissions
				{FieldPath: "resource.resource.rules.policies.permissions.any"},
				{FieldPath: "resource.resource.rules.policies.principals.any"},
			},
			DomainFields: []SearchFieldConfig{
				// HTTP Connection Manager inline routes
				{FieldPath: "resource.resource.route_config.virtual_hosts.domains"},
				{FieldPath: "resource.resource.route_config.virtual_hosts.routes.route.host_rewrite"},
				{FieldPath: "resource.resource.route_config.virtual_hosts.routes.redirect.host_redirect"},
			},
		},
	}
}

// GlobalSearch performs a global search across multiple collections for domains and IPs
func (h *Handler) GlobalSearch(c *gin.Context) {
	ctx := c.Request.Context()
	requestDetails, userDetails := h.getRequestDetails(c)

	// Check role permissions
	if err := checkRole(c, userDetails); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	// Project is required
	if requestDetails.Project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	// Validate project access
	if db := h.getDatabaseConnection(); db != nil {
		if err := authorization.ValidateRequestProject(ctx, db, userDetails, requestDetails.Project); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"message": err.Error()})
			return
		}
	}

	// Get search query from request
	query := c.Query("query")
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "query parameter is required"})
		return
	}

	db := h.getDatabaseConnection()
	if db == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "database connection not available"})
		return
	}

	// Perform search
	results, err := performGlobalSearch(ctx, db, requestDetails, query)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": fmt.Sprintf("search failed: %v", err)})
		return
	}

	response := SearchResponse{
		Query:        query,
		TotalResults: len(results),
		Results:      results,
	}

	c.JSON(http.StatusOK, gin.H{"message": "Success", "data": response})
}

// performGlobalSearch executes the search across all relevant collections using configurations
func performGlobalSearch(ctx context.Context, db *mongo.Database, requestDetails models.RequestDetails, query string) ([]SearchResult, error) {
	var allResults []SearchResult

	// Sanitize input first
	sanitizedQuery, valid := security.SanitizeSearchInput(query)
	if !valid || sanitizedQuery == "" {
		return []SearchResult{}, nil
	}

	// Determine search type (IP or domain)
	isIP := isIPAddress(query)
	configs := getSearchConfigurations()

	// Search in all configured collections
	for _, config := range configs {
		var fieldConfigs []SearchFieldConfig
		var searchQuery string

		if isIP && len(config.IPFields) > 0 {
			fieldConfigs = config.IPFields
			// For IPs, escape dots for literal matching
			searchQuery = "^" + strings.ReplaceAll(sanitizedQuery, ".", "\\.")
		} else if !isIP && len(config.DomainFields) > 0 {
			fieldConfigs = config.DomainFields
			// For domains, use sanitized query as-is
			searchQuery = sanitizedQuery
		} else {
			continue // Skip this collection
		}

		results, err := searchCollection(ctx, db, requestDetails, config, fieldConfigs, searchQuery, query)
		if err != nil {
			// Log error but continue with other collections
			fmt.Printf("[WARN] Failed to search %s: %v\n", config.Collection, err)
			continue
		}
		allResults = append(allResults, results...)
	}

	return allResults, nil
}

// searchCollection performs a generic search on a MongoDB collection
func searchCollection(ctx context.Context, db *mongo.Database, requestDetails models.RequestDetails, config CollectionSearchConfig, fieldConfigs []SearchFieldConfig, searchQuery, originalQuery string) ([]SearchResult, error) {
	collection := db.Collection(config.Collection)

	// Build OR filter for all field paths
	orFilters := make([]bson.M, 0, len(fieldConfigs))
	for _, fc := range fieldConfigs {
		orFilters = append(orFilters, bson.M{
			fc.FieldPath: bson.M{
				"$regex":   searchQuery,
				"$options": "i",
			},
		})
	}

	var baseFilter bson.M

	// Add special filters for certain collections
	switch config.Collection {
	case "discovery", "services":
		baseFilter = bson.M{
			"$or":     orFilters,
			"project": requestDetails.Project,
		}
	case "filters":
		// HTTP Connection Manager OR HTTP RBAC OR Network RBAC
		baseFilter = bson.M{
			"$and": []bson.M{
				{"$or": orFilters},
				{
					"$or": []bson.M{
						{"general.canonical_name": "envoy.filters.network.http_connection_manager"},
						{"general.canonical_name": "envoy.extensions.filters.http.rbac.v3.RBAC"},
						{"general.canonical_name": "envoy.extensions.filters.network.rbac.v3.RBAC"},
					},
				},
			},
		}
	default:
		baseFilter = bson.M{"$or": orFilters}
	}

	// Apply security filters if needed
	var finalFilter bson.M
	if config.UseSecureFilter {
		filterWithRestriction, err := common.AddSecureUserFilter(ctx, db, requestDetails, baseFilter)
		if err != nil {
			return nil, err
		}
		finalFilter = filterWithRestriction
	} else {
		finalFilter = baseFilter
	}

	cursor, err := collection.Find(ctx, finalFilter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var results []SearchResult
	for cursor.Next(ctx) {
		var doc bson.M
		if err := cursor.Decode(&doc); err != nil {
			continue
		}

		result := extractMatches(doc, config, originalQuery)
		if result != nil {
			results = append(results, *result)
		}
	}

	return results, nil
}

// extractMatches extracts search matches from a MongoDB document based on configuration
func extractMatches(doc bson.M, config CollectionSearchConfig, query string) *SearchResult {
	result := &SearchResult{
		Collection: config.Collection,
		Matches:    []SearchMatch{},
	}

	// Handle different collection structures
	switch config.Collection {
	case "discovery":
		// Discovery: cluster_name instead of general.name
		result.ResourceID = getObjectIDString(doc, "_id")
		result.ResourceName = getStringField(doc, "cluster_name")
		result.Project = getStringField(doc, "project")
		result.URL = "/discovery"

		// Search in nodes array for IPs
		if nodes := getArrayField(doc, "nodes"); len(nodes) > 0 {
			for nodeIdx, nodeRaw := range nodes {
				if node, ok := nodeRaw.(bson.M); ok {
					nodeName := getStringField(node, "name")
					if addresses := getMapField(node, "addresses"); addresses != nil {
						checkIPField(addresses, "ExternalIP", query, &result.Matches, fmt.Sprintf("nodes[%d].addresses.ExternalIP", nodeIdx), map[string]any{"node_name": nodeName, "address_type": "ExternalIP"})
						checkIPField(addresses, "InternalIP", query, &result.Matches, fmt.Sprintf("nodes[%d].addresses.InternalIP", nodeIdx), map[string]any{"node_name": nodeName, "address_type": "InternalIP"})
					}
				}
			}
		}

	case "services":
		// Services: name instead of general.name
		result.ResourceID = getObjectIDString(doc, "_id")
		result.ResourceName = getStringField(doc, "name")
		result.Project = getStringField(doc, "project")
		result.URL = "/services"

		// Search in clients array for IPs
		if clients := getArrayField(doc, "clients"); len(clients) > 0 {
			for clientIdx, clientRaw := range clients {
				if client, ok := clientRaw.(bson.M); ok {
					context := map[string]any{
						"client_id": getStringField(client, "client_id"),
					}
					// Add interface_id if exists
					if interfaceID := getStringField(client, "interface_id"); interfaceID != "" {
						context["interface_id"] = interfaceID
					}
					// Add ip_mode if exists
					if ipMode := getStringField(client, "ip_mode"); ipMode != "" {
						context["ip_mode"] = ipMode
					}
					checkIPField(client, "downstream_address", query, &result.Matches, fmt.Sprintf("clients[%d].downstream_address", clientIdx), context)
				}
			}
		}

	default:
		// Standard XDS resource structure
		general := getMapField(doc, "general")
		if general == nil {
			return nil
		}

		gtypeStr := getStringField(general, "gtype")
		gtype := models.GType(gtypeStr)

		result.ResourceID = getObjectIDString(doc, "_id")
		result.ResourceName = getStringField(general, "name")
		result.Project = getStringField(general, "project")
		result.Version = getStringField(general, "version")
		result.GType = gtypeStr
		result.URL = gtype.URL()

		// For standard resources, use the old extraction methods (they work well)
		switch config.Collection {
		case "virtual_hosts":
			if oldResult := extractVirtualHostMatches(doc, query); oldResult != nil {
				result.Matches = oldResult.Matches
			}
		case "routes":
			if oldResult := extractRouteMatches(doc, query); oldResult != nil {
				result.Matches = oldResult.Matches
			}
		case "filters":
			if oldResult := extractFilterMatches(doc, query); oldResult != nil {
				result.Matches = oldResult.Matches
			}
		case "endpoints":
			if oldResult := extractEndpointMatches(doc, query); oldResult != nil {
				result.Matches = oldResult.Matches
			}
		case "clusters", "listeners", "bootstrap":
			// Simple extraction for IP fields in these collections
			resource := getMapField(doc, "resource")
			if resource != nil {
				extractSimpleMatches(resource, config, query, &result.Matches)
			}
		}
	}

	if len(result.Matches) == 0 {
		return nil
	}

	return result
}

// checkIPField checks if a field contains the query and adds it to matches
func checkIPField(doc bson.M, fieldName, query string, matches *[]SearchMatch, fieldPath string, context map[string]any) {
	if value := getStringField(doc, fieldName); value != "" && strings.Contains(strings.ToLower(value), strings.ToLower(query)) {
		*matches = append(*matches, SearchMatch{
			FieldPath: fieldPath,
			Value:     value,
			Context:   context,
		})
	}
}

// extractSimpleMatches extracts matches from simple IP fields (clusters, listeners, bootstrap)
func extractSimpleMatches(resource bson.M, config CollectionSearchConfig, query string, matches *[]SearchMatch) {
	resourceContent := getMapField(resource, "resource")
	if resourceContent == nil {
		return
	}

	switch config.Collection {
	case "clusters":
		// Check load_assignment.endpoints[].lb_endpoints[].endpoint.address.socket_address.address
		if loadAssignment := getMapField(resourceContent, "load_assignment"); loadAssignment != nil {
			if endpoints := getArrayField(loadAssignment, "endpoints"); len(endpoints) > 0 {
				for epIdx, epRaw := range endpoints {
					if ep, ok := epRaw.(bson.M); ok {
						extractLbEndpointIPs(ep, epIdx, query, matches, "load_assignment.endpoints")
					}
				}
			}
		}
	case "listeners":
		// Check address.socket_address.address
		if address := getMapField(resourceContent, "address"); address != nil {
			if socketAddress := getMapField(address, "socket_address"); socketAddress != nil {
				checkIPField(socketAddress, "address", query, matches, "resource.resource.address.socket_address.address", map[string]any{"port": getIntField(socketAddress, "port_value")})
			}
		}
	case "bootstrap":
		// Check admin.address.socket_address.address
		if admin := getMapField(resourceContent, "admin"); admin != nil {
			if address := getMapField(admin, "address"); address != nil {
				if socketAddress := getMapField(address, "socket_address"); socketAddress != nil {
					checkIPField(socketAddress, "address", query, matches, "resource.resource.admin.address.socket_address.address", map[string]any{"port": getIntField(socketAddress, "port_value")})
				}
			}
		}
	}
}

// extractLbEndpointIPs extracts IPs from lb_endpoints array
func extractLbEndpointIPs(ep bson.M, epIdx int, query string, matches *[]SearchMatch, basePath string) {
	if lbEndpoints := getArrayField(ep, "lb_endpoints"); len(lbEndpoints) > 0 {
		for lbIdx, lbRaw := range lbEndpoints {
			if lb, ok := lbRaw.(bson.M); ok {
				if endpoint := getMapField(lb, "endpoint"); endpoint != nil {
					if address := getMapField(endpoint, "address"); address != nil {
						if socketAddress := getMapField(address, "socket_address"); socketAddress != nil {
							ipAddress := getStringField(socketAddress, "address")
							if ipAddress != "" && strings.Contains(strings.ToLower(ipAddress), strings.ToLower(query)) {
								*matches = append(*matches, SearchMatch{
									FieldPath: fmt.Sprintf("%s[%d].lb_endpoints[%d].endpoint.address.socket_address.address", basePath, epIdx, lbIdx),
									Value:     ipAddress,
									Context: map[string]any{
										"port": getIntField(socketAddress, "port_value"),
									},
								})
							}
						}
					}
				}
			}
		}
	}
}

// isIPAddress checks if the query looks like an IP address (full or partial)
func isIPAddress(query string) bool {
	// Check for full IP: 10.218.16.1
	fullIPPattern := `^(\d{1,3}\.){3}\d{1,3}$`
	if matched, _ := regexp.MatchString(fullIPPattern, query); matched {
		return true
	}

	// Check for partial IP: Must contain at least one dot to be considered IP
	// Examples: 10.218.16, 10.254, 10.1
	// This prevents single numbers like "1" or "10" from being treated as IPs
	if strings.Contains(query, ".") {
		partialIPPattern := `^(\d{1,3}\.)+\d{1,3}$`
		matched, _ := regexp.MatchString(partialIPPattern, query)
		return matched
	}

	return false
}

// Helper functions to extract matches from documents

func extractVirtualHostMatches(doc bson.M, query string) *SearchResult {
	general := getMapField(doc, "general")
	resource := getMapField(doc, "resource")

	if general == nil || resource == nil {
		return nil
	}

	gtypeStr := getStringField(general, "gtype")
	gtype := models.GType(gtypeStr)

	result := &SearchResult{
		Collection:   "virtual_hosts",
		ResourceID:   getObjectIDString(doc, "_id"),
		ResourceName: getStringField(general, "name"),
		Project:      getStringField(general, "project"),
		Version:      getStringField(general, "version"),
		GType:        gtypeStr,
		URL:          gtype.URL(),
		Matches:      []SearchMatch{},
	}

	// Search in resource.resource array (virtual hosts)
	resourceField := resource["resource"]

	// Try different array types from MongoDB
	var vhosts []any
	switch v := resourceField.(type) {
	case []any:
		vhosts = v
	case primitive.A:
		vhosts = []any(v)
	default:
		return nil
	}

	if len(vhosts) > 0 {
		for vhIdx, vhRaw := range vhosts {
			vh, ok := vhRaw.(bson.M)
			if !ok {
				continue
			}

			vhName := getStringField(vh, "name")

			// Check domains
			if domains := getArrayField(vh, "domains"); len(domains) > 0 {
				for domainIdx, domainRaw := range domains {
					if domain, ok := domainRaw.(string); ok && strings.Contains(strings.ToLower(domain), strings.ToLower(query)) {
						result.Matches = append(result.Matches, SearchMatch{
							FieldPath: fmt.Sprintf("resource.resource[%d].domains[%d]", vhIdx, domainIdx),
							Value:     domain,
							Context: map[string]any{
								"virtual_host_name": vhName,
							},
						})
					}
				}
			}

			// Check routes for host_rewrite and host_redirect
			if routes := getArrayField(vh, "routes"); len(routes) > 0 {
				for routeIdx, routeRaw := range routes {
					route, ok := routeRaw.(bson.M)
					if !ok {
						continue
					}

					routeName := getStringField(route, "name")

					// Check host_rewrite in route.route
					if routeAction, ok := route["route"].(bson.M); ok {
						if hostRewrite := getStringField(routeAction, "host_rewrite"); hostRewrite != "" && strings.Contains(strings.ToLower(hostRewrite), strings.ToLower(query)) {
							result.Matches = append(result.Matches, SearchMatch{
								FieldPath: fmt.Sprintf("resource.resource[%d].routes[%d].route.host_rewrite", vhIdx, routeIdx),
								Value:     hostRewrite,
								Context: map[string]any{
									"virtual_host_name": vhName,
									"route_name":        routeName,
								},
							})
						}
					}

					// Check host_redirect in redirect
					if redirect, ok := route["redirect"].(bson.M); ok {
						if hostRedirect := getStringField(redirect, "host_redirect"); hostRedirect != "" && strings.Contains(strings.ToLower(hostRedirect), strings.ToLower(query)) {
							result.Matches = append(result.Matches, SearchMatch{
								FieldPath: fmt.Sprintf("resource.resource[%d].routes[%d].redirect.host_redirect", vhIdx, routeIdx),
								Value:     hostRedirect,
								Context: map[string]any{
									"virtual_host_name": vhName,
									"route_name":        routeName,
								},
							})
						}
					}
				}
			}
		}
	}

	if len(result.Matches) == 0 {
		return nil
	}

	return result
}

func extractRouteMatches(doc bson.M, query string) *SearchResult {
	general := getMapField(doc, "general")
	resource := getMapField(doc, "resource")

	if general == nil || resource == nil {
		return nil
	}

	resourceContent := getMapField(resource, "resource")
	if resourceContent == nil {
		return nil
	}

	gtypeStr := getStringField(general, "gtype")
	gtype := models.GType(gtypeStr)

	result := &SearchResult{
		Collection:   "routes",
		ResourceID:   getObjectIDString(doc, "_id"),
		ResourceName: getStringField(general, "name"),
		Project:      getStringField(general, "project"),
		Version:      getStringField(general, "version"),
		GType:        gtypeStr,
		URL:          gtype.URL(),
		Matches:      []SearchMatch{},
	}

	// Check if inline virtual_hosts exist
	if vhosts := getArrayField(resourceContent, "virtual_hosts"); len(vhosts) > 0 {
		for vhIdx, vhRaw := range vhosts {
			vh, ok := vhRaw.(bson.M)
			if !ok {
				continue
			}

			vhName := getStringField(vh, "name")

			// Check domains
			if domains := getArrayField(vh, "domains"); len(domains) > 0 {
				for domainIdx, domainRaw := range domains {
					if domain, ok := domainRaw.(string); ok && strings.Contains(strings.ToLower(domain), strings.ToLower(query)) {
						result.Matches = append(result.Matches, SearchMatch{
							FieldPath: fmt.Sprintf("resource.resource.virtual_hosts[%d].domains[%d]", vhIdx, domainIdx),
							Value:     domain,
							Context: map[string]any{
								"inline_route":      true,
								"virtual_host_name": vhName,
							},
						})
					}
				}
			}

			// Check routes
			if routes := getArrayField(vh, "routes"); len(routes) > 0 {
				for routeIdx, routeRaw := range routes {
					route, ok := routeRaw.(bson.M)
					if !ok {
						continue
					}

					routeName := getStringField(route, "name")

					// Check host_rewrite
					if routeAction, ok := route["route"].(bson.M); ok {
						if hostRewrite := getStringField(routeAction, "host_rewrite"); hostRewrite != "" && strings.Contains(strings.ToLower(hostRewrite), strings.ToLower(query)) {
							result.Matches = append(result.Matches, SearchMatch{
								FieldPath: fmt.Sprintf("resource.resource.virtual_hosts[%d].routes[%d].route.host_rewrite", vhIdx, routeIdx),
								Value:     hostRewrite,
								Context: map[string]any{
									"inline_route":      true,
									"virtual_host_name": vhName,
									"route_name":        routeName,
								},
							})
						}
					}

					// Check host_redirect
					if redirect, ok := route["redirect"].(bson.M); ok {
						if hostRedirect := getStringField(redirect, "host_redirect"); hostRedirect != "" && strings.Contains(strings.ToLower(hostRedirect), strings.ToLower(query)) {
							result.Matches = append(result.Matches, SearchMatch{
								FieldPath: fmt.Sprintf("resource.resource.virtual_hosts[%d].routes[%d].redirect.host_redirect", vhIdx, routeIdx),
								Value:     hostRedirect,
								Context: map[string]any{
									"inline_route":      true,
									"virtual_host_name": vhName,
									"route_name":        routeName,
								},
							})
						}
					}
				}
			}
		}
	}

	if len(result.Matches) == 0 {
		return nil
	}

	return result
}

func extractFilterMatches(doc bson.M, query string) *SearchResult {
	general := getMapField(doc, "general")
	resource := getMapField(doc, "resource")

	if general == nil || resource == nil {
		return nil
	}

	resourceContent := getMapField(resource, "resource")
	if resourceContent == nil {
		return nil
	}

	gtypeStr := getStringField(general, "gtype")
	gtype := models.GType(gtypeStr)

	result := &SearchResult{
		Collection:   "filters",
		ResourceID:   getObjectIDString(doc, "_id"),
		ResourceName: getStringField(general, "name"),
		Project:      getStringField(general, "project"),
		Version:      getStringField(general, "version"),
		GType:        gtypeStr,
		URL:          gtype.URL(),
		Matches:      []SearchMatch{},
	}

	// Only check inline route_config
	if routeConfig, ok := resourceContent["route_config"].(bson.M); ok {
		if vhosts := getArrayField(routeConfig, "virtual_hosts"); len(vhosts) > 0 {
			for vhIdx, vhRaw := range vhosts {
				vh, ok := vhRaw.(bson.M)
				if !ok {
					continue
				}

				vhName := getStringField(vh, "name")

				// Check domains
				if domains := getArrayField(vh, "domains"); len(domains) > 0 {
					for domainIdx, domainRaw := range domains {
						if domain, ok := domainRaw.(string); ok && strings.Contains(strings.ToLower(domain), strings.ToLower(query)) {
							result.Matches = append(result.Matches, SearchMatch{
								FieldPath: fmt.Sprintf("resource.resource.route_config.virtual_hosts[%d].domains[%d]", vhIdx, domainIdx),
								Value:     domain,
								Context: map[string]any{
									"filter_type":       "http_connection_manager",
									"inline_route":      true,
									"virtual_host_name": vhName,
								},
							})
						}
					}
				}

				// Check routes
				if routes := getArrayField(vh, "routes"); len(routes) > 0 {
					for routeIdx, routeRaw := range routes {
						route, ok := routeRaw.(bson.M)
						if !ok {
							continue
						}

						routeName := getStringField(route, "name")

						// Check host_rewrite
						if routeAction, ok := route["route"].(bson.M); ok {
							if hostRewrite := getStringField(routeAction, "host_rewrite"); hostRewrite != "" && strings.Contains(strings.ToLower(hostRewrite), strings.ToLower(query)) {
								result.Matches = append(result.Matches, SearchMatch{
									FieldPath: fmt.Sprintf("resource.resource.route_config.virtual_hosts[%d].routes[%d].route.host_rewrite", vhIdx, routeIdx),
									Value:     hostRewrite,
									Context: map[string]any{
										"filter_type":       "http_connection_manager",
										"inline_route":      true,
										"virtual_host_name": vhName,
										"route_name":        routeName,
									},
								})
							}
						}

						// Check host_redirect
						if redirect, ok := route["redirect"].(bson.M); ok {
							if hostRedirect := getStringField(redirect, "host_redirect"); hostRedirect != "" && strings.Contains(strings.ToLower(hostRedirect), strings.ToLower(query)) {
								result.Matches = append(result.Matches, SearchMatch{
									FieldPath: fmt.Sprintf("resource.resource.route_config.virtual_hosts[%d].routes[%d].redirect.host_redirect", vhIdx, routeIdx),
									Value:     hostRedirect,
									Context: map[string]any{
										"filter_type":       "http_connection_manager",
										"inline_route":      true,
										"virtual_host_name": vhName,
										"route_name":        routeName,
									},
								})
							}
						}
					}
				}
			}
		}
	}

	if len(result.Matches) == 0 {
		return nil
	}

	return result
}

func extractEndpointMatches(doc bson.M, query string) *SearchResult {
	general := getMapField(doc, "general")
	resource := getMapField(doc, "resource")

	if general == nil || resource == nil {
		return nil
	}

	resourceContent := getMapField(resource, "resource")
	if resourceContent == nil {
		return nil
	}

	gtypeStr := getStringField(general, "gtype")
	gtype := models.GType(gtypeStr)

	result := &SearchResult{
		Collection:   "endpoints",
		ResourceID:   getObjectIDString(doc, "_id"),
		ResourceName: getStringField(general, "name"),
		Project:      getStringField(general, "project"),
		Version:      getStringField(general, "version"),
		GType:        gtypeStr,
		URL:          gtype.URL(),
		Matches:      []SearchMatch{},
	}

	// Search in endpoints array
	if endpoints := getArrayField(resourceContent, "endpoints"); len(endpoints) > 0 {
		for epIdx, epRaw := range endpoints {
			ep, ok := epRaw.(bson.M)
			if !ok {
				continue
			}

			locality := getStringField(getMapField(ep, "locality"), "region")

			// Check lb_endpoints
			if lbEndpoints := getArrayField(ep, "lb_endpoints"); len(lbEndpoints) > 0 {
				for lbIdx, lbRaw := range lbEndpoints {
					lb, ok := lbRaw.(bson.M)
					if !ok {
						continue
					}

					// Navigate: lb_endpoint.endpoint.address.socket_address.address
					endpoint := getMapField(lb, "endpoint")
					if endpoint == nil {
						continue
					}

					address := getMapField(endpoint, "address")
					if address == nil {
						continue
					}

					socketAddress := getMapField(address, "socket_address")
					if socketAddress == nil {
						continue
					}

					ipAddress := getStringField(socketAddress, "address")
					if ipAddress != "" && strings.Contains(strings.ToLower(ipAddress), strings.ToLower(query)) {
						result.Matches = append(result.Matches, SearchMatch{
							FieldPath: fmt.Sprintf("resource.resource.endpoints[%d].lb_endpoints[%d].endpoint.address.socket_address.address", epIdx, lbIdx),
							Value:     ipAddress,
							Context: map[string]any{
								"locality": locality,
								"port":     getIntField(socketAddress, "port_value"),
							},
						})
					}
				}
			}
		}
	}

	if len(result.Matches) == 0 {
		return nil
	}

	return result
}

// Utility helper functions

func getArrayField(m bson.M, key string) []any {
	if m == nil {
		return nil
	}
	// Handle both []any and primitive.A from MongoDB
	switch v := m[key].(type) {
	case []any:
		return v
	case primitive.A:
		return []any(v)
	default:
		return nil
	}
}

func getMapField(m bson.M, key string) bson.M {
	if m == nil {
		return nil
	}
	if val, ok := m[key].(bson.M); ok {
		return val
	}
	return nil
}

func getStringField(m bson.M, key string) string {
	if m == nil {
		return ""
	}
	if val, ok := m[key].(string); ok {
		return val
	}
	return ""
}

func getIntField(m bson.M, key string) int {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case uint32:
		return int(v)
	case uint64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func getObjectIDString(m bson.M, key string) string {
	if m == nil {
		return ""
	}
	if val, ok := m[key]; ok {
		// Try to convert primitive.ObjectID to hex string
		if objID, ok := val.(primitive.ObjectID); ok {
			return objID.Hex()
		}
		// Fallback to string conversion
		return fmt.Sprintf("%v", val)
	}
	return ""
}
