package generators

import (
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// HCMGenerator generates HTTP Connection Manager resources
type HCMGenerator struct {
	*BaseGenerator
	componentTypeMap  map[string]string // Maps component name to type
	metadataProcessor *MetadataProcessor
}

// NewHCMGeneratorWithMapping creates a new HCM generator with component type mapping
func NewHCMGeneratorWithMapping(project, version string, user models.UserDetails, componentTypeMap map[string]string) *HCMGenerator {
	return &HCMGenerator{
		BaseGenerator:     NewBaseGenerator(project, version, user),
		componentTypeMap:  componentTypeMap,
		metadataProcessor: NewMetadataProcessor(),
	}
}

// Generate generates an HTTP Connection Manager resource document
func (hg *HCMGenerator) Generate(instance models.ComponentInstance) (any, error) {
	// Get field values
	hcmName := hg.GetFieldValueWithComponentNameSupport(instance, "name", instance.Name).(string)
	// Get gtype from instance and use it to build general section dynamically
	gtype := models.GType(instance.GType)

	// Build general section using GType methods
	general := hg.BuildGeneralSection(
		instance,
		gtype.Type(),
		gtype.CollectionString(),
		gtype.CanonicalName(),
		gtype.String(),
		gtype.Category(),
	)

	// Build resource section - only include selected fields (no defaults)
	resource := map[string]any{}

	// Add stat_prefix only if selected
	if statPrefix := hg.GetFieldValueIfSelected(instance.SelectedFields, "stat_prefix"); statPrefix != nil {
		resource["stat_prefix"] = statPrefix
	}

	// Add codec_type only if selected
	if codecType := hg.GetFieldValueIfSelected(instance.SelectedFields, "codec_type"); codecType != nil {
		resource["codec_type"] = codecType
	}

	// Add server_name only if selected
	if serverName := hg.GetFieldValueIfSelected(instance.SelectedFields, "server_name"); serverName != nil {
		resource["server_name"] = serverName
	}

	// Add server_header_transformation only if selected
	if serverHeaderTransformation := hg.GetFieldValueIfSelected(instance.SelectedFields, "server_header_transformation"); serverHeaderTransformation != nil {
		resource["server_header_transformation"] = serverHeaderTransformation
	}

	// Handle route configuration using nested fields
	routeConfigSelection := hg.GetNestedFieldSelection(instance.SelectedFields, "route_configuration")
	inlineRouteName := hcmName + "_routes" // Default route name

	if routeConfigSelection != nil && routeConfigSelection.SelectedChoice == "rds" {
		// Use RDS (Route Discovery Service)
		routeConfigName := ""
		for _, subField := range routeConfigSelection.SubFields {
			if subField.FieldName == "route_config_name" && subField.Value != nil {
				routeConfigName = subField.Value.(string)
				break
			}
		}
		if routeConfigName != "" {
			resource["rds"] = map[string]any{
				"route_config_name": routeConfigName,
				"config_source": map[string]any{
					"ads":                  map[string]any{},
					"resource_api_version": "V3",
				},
			}
		}
	} else if routeConfigSelection != nil && routeConfigSelection.SelectedChoice == "inline" {
		// Use inline route configuration
		var vhostSelection *models.NestedFieldSelection

		// Get sub fields from inline choice
		for _, subField := range routeConfigSelection.SubFields {
			if subField.FieldName == "name" && subField.Value != nil {
				inlineRouteName = subField.Value.(string)
			}
			if subField.FieldName == "virtual_host_config" && subField.NestedSelection != nil {
				vhostSelection = subField.NestedSelection
			}
		}

		routeConfig := map[string]any{
			"name": inlineRouteName,
		}

		if vhostSelection != nil && vhostSelection.SelectedChoice == "inline_virtual_hosts" {
			// Use inline virtual hosts
			var virtualHosts any
			for _, vhSubField := range vhostSelection.SubFields {
				if vhSubField.FieldName == "virtual_hosts" && vhSubField.Value != nil {
					virtualHosts = vhSubField.Value
					break
				}
			}

			if virtualHosts != nil {
				// Convert frontend virtual hosts format to Envoy format
				convertedVirtualHosts := hg.convertVirtualHostsToEnvoyFormat(virtualHosts)
				fmt.Printf("Converted virtual hosts from frontend to Envoy format\n")
				routeConfig["virtual_hosts"] = convertedVirtualHosts
			}
			// No default virtual hosts - only add if user provided virtual_hosts
		}

		resource["route_config"] = routeConfig
	}
	// No default route config - only add if user selected route_configuration field

	// Handle direct route_config field (alternative to route_configuration)
	if routeConfig := hg.GetFieldValueIfSelected(instance.SelectedFields, "route_config"); routeConfig != nil {
		// Parse route_config as an object and add directly
		if routeConfigMap, ok := routeConfig.(map[string]any); ok {
			resource["route_config"] = routeConfigMap
		} else if routeConfigStr, ok := routeConfig.(string); ok && routeConfigStr != "" {
			// If it's a string, try to parse it as reference to a route config
			resource["route_config"] = map[string]any{
				"name": routeConfigStr,
			}
		}
	}

	// Initialize config discovery entries
	var configDiscoveryEntries []any

	// VHDS config discovery removed - VHDS doesn't work with inline route config in Envoy

	// Handle HTTP filters with config discovery
	if httpFilters := hg.GetFieldValueIfSelected(instance.SelectedFields, "http_filters"); httpFilters != nil {
		// Build config discovery entries and transformed filters
		configEntries, transformedFilters := hg.buildConfigDiscoveryForHTTPFilters(httpFilters)
		// Append HTTP filter entries to existing config discovery entries
		configDiscoveryEntries = append(configDiscoveryEntries, configEntries...)
		// Only add http_filters if transformedFilters is not empty
		if len(transformedFilters) > 0 {
			resource["http_filters"] = transformedFilters
		}
	}

	// Add other new fields from example JSON
	if addUserAgent := hg.GetFieldValueIfSelected(instance.SelectedFields, "add_user_agent"); addUserAgent != nil {
		resource["add_user_agent"] = addUserAgent
	}
	if serverHeaderTransformation := hg.GetFieldValueIfSelected(instance.SelectedFields, "server_header_transformation"); serverHeaderTransformation != nil {
		resource["server_header_transformation"] = serverHeaderTransformation
	}
	if maxRequestHeadersKb := hg.GetFieldValueIfSelected(instance.SelectedFields, "max_request_headers_kb"); maxRequestHeadersKb != nil {
		resource["max_request_headers_kb"] = maxRequestHeadersKb
	}
	if streamIdleTimeout := hg.GetFieldValueIfSelected(instance.SelectedFields, "stream_idle_timeout"); streamIdleTimeout != nil {
		resource["stream_idle_timeout"] = streamIdleTimeout
	}
	if requestTimeout := hg.GetFieldValueIfSelected(instance.SelectedFields, "request_timeout"); requestTimeout != nil {
		resource["request_timeout"] = requestTimeout
	}
	if requestHeadersTimeout := hg.GetFieldValueIfSelected(instance.SelectedFields, "request_headers_timeout"); requestHeadersTimeout != nil {
		resource["request_headers_timeout"] = requestHeadersTimeout
	}
	if delayedCloseTimeout := hg.GetFieldValueIfSelected(instance.SelectedFields, "delayed_close_timeout"); delayedCloseTimeout != nil {
		resource["delayed_close_timeout"] = delayedCloseTimeout
	}
	if useRemoteAddress := hg.GetFieldValueIfSelected(instance.SelectedFields, "use_remote_address"); useRemoteAddress != nil {
		resource["use_remote_address"] = useRemoteAddress
	}
	if skipXffAppend := hg.GetFieldValueIfSelected(instance.SelectedFields, "skip_xff_append"); skipXffAppend != nil {
		resource["skip_xff_append"] = skipXffAppend
	}
	if via := hg.GetFieldValueIfSelected(instance.SelectedFields, "via"); via != nil {
		resource["via"] = via
	}
	if generateRequestID := hg.GetFieldValueIfSelected(instance.SelectedFields, "generate_request_id"); generateRequestID != nil {
		resource["generate_request_id"] = generateRequestID
	}
	if proxy100Continue := hg.GetFieldValueIfSelected(instance.SelectedFields, "proxy_100_continue"); proxy100Continue != nil {
		resource["proxy_100_continue"] = proxy100Continue
	}

	// Add HTTP/2 protocol options if enabled
	if enableHTTP2 := hg.GetFieldValueIfSelected(instance.SelectedFields, "enable_http2_protocol_options"); enableHTTP2 != nil {
		if enabled, ok := enableHTTP2.(bool); ok && enabled {
			resource["http2_protocol_options"] = map[string]any{}
		}
	}

	// Add access log only if selected - process metadata and create typed_config structure
	if accessLog := hg.GetFieldValueIfSelected(instance.SelectedFields, "access_log"); accessLog != nil {
		if logObject, ok := accessLog.(map[string]any); ok {
			processedAccessLogs, err := hg.metadataProcessor.ProcessAccessLog(logObject)
			if err != nil {
				return nil, fmt.Errorf("failed to process access log: %w", err)
			}
			resource["access_log"] = processedAccessLogs
		}
	}

	// Add config_discovery to general section
	if len(configDiscoveryEntries) > 0 {
		general["config_discovery"] = configDiscoveryEntries
	}

	// For HCM, return the resource directly (not as filter wrapper)
	// This matches the working example structure

	// Debug log final resource
	finalDoc := hg.BuildCompleteDocument(general, resource)

	return finalDoc, nil
}

// buildConfigDiscoveryForHTTPFilters builds config discovery entries for HTTP filters
func (hg *HCMGenerator) buildConfigDiscoveryForHTTPFilters(httpFilters any) ([]any, []any) {
	var configDiscoveryEntries []any
	var transformedFilters []any

	if httpFilters == nil {
		return configDiscoveryEntries, transformedFilters
	}

	// Handle array of filters from frontend
	if filterArray, ok := httpFilters.([]any); ok {
		priority := 0
		var routerFilter map[string]any
		var routerConfigEntry map[string]any

		// Process all filters but keep router filter separate
		for _, filter := range filterArray {
			if filterMap, ok := filter.(map[string]any); ok {
				// Use metadata processor for HTTP filter
				configDiscoveryEntry, transformedFilter := hg.metadataProcessor.ProcessHTTPFilter(filterMap, priority)

				// Check if this is router filter (should be last)
				filterGType := ""
				if gtype, exists := filterMap["gtype"]; exists {
					filterGType = fmt.Sprint(gtype)
				}
				isRouterFilter := filterGType == models.Router.String()

				if isRouterFilter {
					// Store router filter separately to add at the end
					routerFilter = transformedFilter
					routerConfigEntry = configDiscoveryEntry
				} else {
					// Add non-router filters immediately
					configDiscoveryEntries = append(configDiscoveryEntries, configDiscoveryEntry)
					transformedFilters = append(transformedFilters, transformedFilter)
				}

				priority++
			}
		}

		// Add router filter at the end if it exists
		if routerFilter != nil {
			// Update router filter priority to be last
			routerConfigEntry["priority"] = len(transformedFilters)
			configDiscoveryEntries = append(configDiscoveryEntries, routerConfigEntry)
			transformedFilters = append(transformedFilters, routerFilter)
		}
	}

	return configDiscoveryEntries, transformedFilters
}

// GetComponentType returns the component type
func (hg *HCMGenerator) GetComponentType() string {
	return "http_connection_manager"
}

// GetCollection returns the MongoDB collection name
func (hg *HCMGenerator) GetCollection() string {
	return "filters"
}

// convertVirtualHostsToEnvoyFormat converts frontend virtual hosts format to Envoy format
func (hg *HCMGenerator) convertVirtualHostsToEnvoyFormat(virtualHosts any) []map[string]any {
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
						envoyRoute := hg.convertRouteToEnvoyFormat(route)
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
func (hg *HCMGenerator) convertRouteToEnvoyFormat(frontendRoute map[string]any) map[string]any {
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
