package generators

import (
	"encoding/json"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// RouteGenerator generates route configuration resources
type RouteGenerator struct {
	*BaseGenerator
	metadataProcessor *MetadataProcessor
}

// NewRouteGenerator creates a new route generator
func NewRouteGenerator(project, version string, user models.UserDetails) *RouteGenerator {
	return &RouteGenerator{
		BaseGenerator:     NewBaseGenerator(project, version, user),
		metadataProcessor: NewMetadataProcessor(),
	}
}

// Generate generates a route configuration resource document
func (rg *RouteGenerator) Generate(instance models.ComponentInstance) (any, error) {
	// Get field values with UseComponentName support
	routeName := rg.GetFieldValueWithComponentNameSupport(instance, "name", instance.Name).(string)

	// Get gtype from instance and use it to build general section dynamically
	gtype := models.GType(instance.GType)

	// Build general section using GType methods
	general := rg.BuildGeneralSection(
		instance,
		gtype.Type(),
		gtype.CollectionString(),
		gtype.CanonicalName(),
		gtype.String(),
		gtype.Category(),
	)

	// Build resource section - only include selected fields
	resource := map[string]any{
		"name": routeName,
	}

	// Handle nested virtual_host_config choice field
	vhConfigSelection := rg.GetNestedFieldSelection(instance.SelectedFields, "virtual_host_config")

	if vhConfigSelection != nil {
		switch vhConfigSelection.SelectedChoice {
		case "vhds":
			// VHDS (Virtual Host Discovery Service)
			vhdsConfigValue := rg.GetFieldValue(vhConfigSelection.SubFields, "vhds_config", nil)

			var vhdsName string

			// Handle both string and object formats
			if vhdsConfigStr, ok := vhdsConfigValue.(string); ok && vhdsConfigStr != "" {
				vhdsName = vhdsConfigStr
			} else if vhdsConfigObj, ok := vhdsConfigValue.(map[string]any); ok {
				if name, exists := vhdsConfigObj["name"]; exists {
					vhdsName = fmt.Sprint(name)
				}
			}

			if vhdsName != "" {
				gtypeVHDS := models.VirtualHost
				// Create config_discovery entry for VHDS
				configDiscovery := []map[string]any{
					{
						"name":           vhdsName,
						"gtype":          gtypeVHDS.String(),
						"priority":       0,
						"category":       gtypeVHDS.Category(),
						"canonical_name": gtypeVHDS.CanonicalName(),
						"parent_name":    routeName,
					},
				}
				general["config_discovery"] = configDiscovery

				// Add VHDS config to resource
				resource["vhds"] = map[string]any{
					"config_source": map[string]any{
						"api_config_source": map[string]any{
							"api_type":              "DELTA_GRPC",
							"transport_api_version": "V3",
							"grpc_services": []map[string]any{
								{
									"envoy_grpc": map[string]any{
										"cluster_name": "elchi-control-plane",
									},
									"timeout": "5.0s",
									"initial_metadata": []map[string]any{
										{
											"key":   "nodeid",
											"value": "__NODEID__",
										},
									},
								},
							},
						},
						"initial_fetch_timeout": "5.0s",
						"resource_api_version":  "V3",
					},
				}
			}

		case "inline_virtual_hosts":
			// Inline Virtual Hosts
			virtualHostsValue := rg.GetFieldValue(vhConfigSelection.SubFields, "virtual_hosts", nil)
			if virtualHosts, ok := virtualHostsValue.([]any); ok && len(virtualHosts) > 0 {
				// Convert frontend virtual hosts format to Envoy format
				convertedVirtualHosts := rg.convertVirtualHostsToEnvoyFormat(virtualHosts)
				resource["virtual_hosts"] = convertedVirtualHosts
			}
		}
	}

	// Add all optional fields from JSON example
	if internalOnlyHeaders := rg.GetFieldValueIfSelected(instance.SelectedFields, "internal_only_headers"); internalOnlyHeaders != nil {
		resource["internal_only_headers"] = internalOnlyHeaders
	}

	if responseHeadersToAdd := rg.GetFieldValueIfSelected(instance.SelectedFields, "response_headers_to_add"); responseHeadersToAdd != nil {
		if headersArray, ok := responseHeadersToAdd.([]any); ok {
			resource["response_headers_to_add"] = rg.metadataProcessor.TransformHeadersToEnvoyFormat(headersArray)
		}
	}

	if responseHeadersToRemove := rg.GetFieldValueIfSelected(instance.SelectedFields, "response_headers_to_remove"); responseHeadersToRemove != nil {
		resource["response_headers_to_remove"] = responseHeadersToRemove
	}

	if requestHeadersToAdd := rg.GetFieldValueIfSelected(instance.SelectedFields, "request_headers_to_add"); requestHeadersToAdd != nil {
		if headersArray, ok := requestHeadersToAdd.([]any); ok {
			resource["request_headers_to_add"] = rg.metadataProcessor.TransformHeadersToEnvoyFormat(headersArray)
		}
	}

	if requestHeadersToRemove := rg.GetFieldValueIfSelected(instance.SelectedFields, "request_headers_to_remove"); requestHeadersToRemove != nil {
		resource["request_headers_to_remove"] = requestHeadersToRemove
	}

	if mostSpecificWins := rg.GetFieldValueIfSelected(instance.SelectedFields, "most_specific_header_mutations_wins"); mostSpecificWins != nil {
		resource["most_specific_header_mutations_wins"] = mostSpecificWins
	}

	if validateClusters := rg.GetFieldValueIfSelected(instance.SelectedFields, "validate_clusters"); validateClusters != nil {
		resource["validate_clusters"] = validateClusters
	}

	if maxDirectResponseBodySize := rg.GetFieldValueIfSelected(instance.SelectedFields, "max_direct_response_body_size_bytes"); maxDirectResponseBodySize != nil {
		resource["max_direct_response_body_size_bytes"] = maxDirectResponseBodySize
	}

	if ignorePortInHostMatching := rg.GetFieldValueIfSelected(instance.SelectedFields, "ignore_port_in_host_matching"); ignorePortInHostMatching != nil {
		resource["ignore_port_in_host_matching"] = ignorePortInHostMatching
	}

	if ignorePathParams := rg.GetFieldValueIfSelected(instance.SelectedFields, "ignore_path_parameters_in_path_matching"); ignorePathParams != nil {
		resource["ignore_path_parameters_in_path_matching"] = ignorePathParams
	}

	finalDoc := rg.BuildCompleteDocument(general, resource)

	// Debug log for route generation
	if finalDocJSON, err := json.MarshalIndent(finalDoc, "", "  "); err == nil {
		fmt.Printf("🎯 Route Generator - Final JSON:\\n%s\\n", string(finalDocJSON))
	}

	return finalDoc, nil
}

// GetComponentType returns the component type
func (rg *RouteGenerator) GetComponentType() string {
	return "route"
}

// GetCollection returns the MongoDB collection name
func (rg *RouteGenerator) GetCollection() string {
	return "routes"
}

// convertVirtualHostsToEnvoyFormat converts frontend virtual hosts format to Envoy format
func (rg *RouteGenerator) convertVirtualHostsToEnvoyFormat(virtualHosts any) []map[string]any {
	vhostSlice, ok := virtualHosts.([]any)
	if !ok {
		return []map[string]any{}
	}

	var convertedVHosts []map[string]any

	for _, vhostInterface := range vhostSlice {
		vhost, ok := vhostInterface.(map[string]any)
		if !ok {
			continue
		}

		envoyVHost := map[string]any{
			"name":    vhost["name"],
			"domains": vhost["domains"],
		}

		// Convert routes
		if routesInterface, exists := vhost["routes"]; exists {
			if routesSlice, ok := routesInterface.([]any); ok {
				var envoyRoutes []map[string]any

				vhostName := ""
				if name, ok := vhost["name"].(string); ok {
					vhostName = name
				}

				for i, routeInterface := range routesSlice {
					if route, ok := routeInterface.(map[string]any); ok {
						envoyRoute := rg.convertRouteToEnvoyFormat(route)
						// Add name if not present
						if _, hasName := envoyRoute["name"]; !hasName {
							envoyRoute["name"] = fmt.Sprintf("%s_route_%d", vhostName, i)
						}
						envoyRoutes = append(envoyRoutes, envoyRoute)
					}
				}

				envoyVHost["routes"] = envoyRoutes
			}
		}

		convertedVHosts = append(convertedVHosts, envoyVHost)
	}

	return convertedVHosts
}

// convertRouteToEnvoyFormat converts frontend route format to Envoy format
func (rg *RouteGenerator) convertRouteToEnvoyFormat(frontendRoute map[string]any) map[string]any {
	envoyRoute := map[string]any{}

	// Convert match
	match := map[string]any{}

	matchType, _ := frontendRoute["match_type"].(string)
	matchValue, _ := frontendRoute["match_value"].(string)

	switch matchType {
	case "prefix":
		match["prefix"] = matchValue
	case "path":
		match["path"] = matchValue
	case "safe_regex":
		match["safe_regex"] = map[string]any{
			"google_re2": map[string]any{},
			"regex":      matchValue,
		}
	default:
		match["prefix"] = "/"
	}

	envoyRoute["match"] = match

	// Convert route action
	if clusterName, exists := frontendRoute["route_cluster"]; exists {
		envoyRoute["route"] = map[string]any{
			"cluster": clusterName,
		}

		// Add timeout if provided
		if timeout, exists := frontendRoute["timeout"]; exists {
			envoyRoute["route"].(map[string]any)["timeout"] = timeout
		}
	}

	return envoyRoute
}
