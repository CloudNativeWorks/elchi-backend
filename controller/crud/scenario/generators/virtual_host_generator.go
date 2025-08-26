package generators

import (
	"encoding/json"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// VirtualHostGenerator generates virtual host resources
type VirtualHostGenerator struct {
	*BaseGenerator
}

// NewVirtualHostGenerator creates a new virtual host generator
func NewVirtualHostGenerator(project, version string, user models.UserDetails) *VirtualHostGenerator {
	return &VirtualHostGenerator{
		BaseGenerator: NewBaseGenerator(project, version, user),
	}
}

// Generate generates a virtual host resource document
func (vhg *VirtualHostGenerator) Generate(instance models.ComponentInstance) (any, error) {
	// Get field values with UseComponentName support
	vhostName := vhg.GetFieldValueWithComponentNameSupport(instance, "name", instance.Name).(string)
	
	// Get gtype from instance and use it to build general section dynamically
	gtype := models.GType(instance.GType)
	
	// Build general section using GType methods
	general := vhg.BuildGeneralSection(
		instance,
		gtype.Type(),
		gtype.CollectionString(),
		gtype.CanonicalName(),
		gtype.String(),
		gtype.Category(),
	)
	
	// Build resource section - only include selected fields
	resource := map[string]any{
		"name": vhostName,
	}
	
	// Handle domains only if selected
	if domains := vhg.GetFieldValueIfSelected(instance.SelectedFields, "domains"); domains != nil {
		// Parse domains if it's a string (JSON array)
		if domainStr, ok := domains.(string); ok {
			var parsedDomains any
			if err := json.Unmarshal([]byte(domainStr), &parsedDomains); err == nil {
				resource["domains"] = parsedDomains
			} else {
				// If parsing fails, use as single domain
				resource["domains"] = []string{domainStr}
			}
		} else {
			resource["domains"] = domains
		}
	}
	// No default domains - only add if user selected domains field
	
	// Handle routes only if selected
	if routes := vhg.GetFieldValueIfSelected(instance.SelectedFields, "routes"); routes != nil {
		// Parse routes if it's a string (JSON)
		if routeStr, ok := routes.(string); ok {
			var parsedRoutes any
			if err := json.Unmarshal([]byte(routeStr), &parsedRoutes); err == nil {
				// Convert frontend route format to Envoy format
				resource["routes"] = vhg.convertRoutesToEnvoyFormat(parsedRoutes, vhostName)
			}
		} else {
			// Convert frontend route format to Envoy format
			resource["routes"] = vhg.convertRoutesToEnvoyFormat(routes, vhostName)
		}
	}
	// No default routes - only add if user selected routes field
	
	// Add all optional fields from JSON example
	if requestHeadersToAdd := vhg.GetFieldValueIfSelected(instance.SelectedFields, "request_headers_to_add"); requestHeadersToAdd != nil {
		// Transform to Envoy format 
		if headersArray, ok := requestHeadersToAdd.([]any); ok && len(headersArray) > 0 {
			var envoyHeaders []map[string]any
			for _, headerInterface := range headersArray {
				if header, ok := headerInterface.(map[string]any); ok {
					envoyHeader := map[string]any{
						"header": map[string]any{
							"key":   header["key"],
							"value": header["value"],
						},
					}
					if appendAction, exists := header["append_action"]; exists {
						envoyHeader["append_action"] = appendAction
					}
					if keepEmpty, exists := header["keep_empty_value"]; exists {
						envoyHeader["keep_empty_value"] = keepEmpty
					}
					envoyHeaders = append(envoyHeaders, envoyHeader)
				}
			}
			if len(envoyHeaders) > 0 {
				resource["request_headers_to_add"] = envoyHeaders
			}
		}
	}
	
	if requestHeadersToRemove := vhg.GetFieldValueIfSelected(instance.SelectedFields, "request_headers_to_remove"); requestHeadersToRemove != nil {
		if removeArray, ok := requestHeadersToRemove.([]any); ok && len(removeArray) > 0 {
			resource["request_headers_to_remove"] = requestHeadersToRemove
		}
	}
	
	if responseHeadersToAdd := vhg.GetFieldValueIfSelected(instance.SelectedFields, "response_headers_to_add"); responseHeadersToAdd != nil {
		// Transform to Envoy format
		if headersArray, ok := responseHeadersToAdd.([]any); ok && len(headersArray) > 0 {
			var envoyHeaders []map[string]any
			for _, headerInterface := range headersArray {
				if header, ok := headerInterface.(map[string]any); ok {
					envoyHeader := map[string]any{
						"header": map[string]any{
							"key":   header["key"],
							"value": header["value"],
						},
					}
					if appendAction, exists := header["append_action"]; exists {
						envoyHeader["append_action"] = appendAction
					}
					if keepEmpty, exists := header["keep_empty_value"]; exists {
						envoyHeader["keep_empty_value"] = keepEmpty
					}
					envoyHeaders = append(envoyHeaders, envoyHeader)
				}
			}
			if len(envoyHeaders) > 0 {
				resource["response_headers_to_add"] = envoyHeaders
			}
		}
	}
	
	if responseHeadersToRemove := vhg.GetFieldValueIfSelected(instance.SelectedFields, "response_headers_to_remove"); responseHeadersToRemove != nil {
		if removeArray, ok := responseHeadersToRemove.([]any); ok && len(removeArray) > 0 {
			resource["response_headers_to_remove"] = responseHeadersToRemove
		}
	}
	
	if includeRequestAttemptCount := vhg.GetFieldValueIfSelected(instance.SelectedFields, "include_request_attempt_count"); includeRequestAttemptCount != nil {
		resource["include_request_attempt_count"] = includeRequestAttemptCount
	}
	
	if includeAttemptCountInResponse := vhg.GetFieldValueIfSelected(instance.SelectedFields, "include_attempt_count_in_response"); includeAttemptCountInResponse != nil {
		resource["include_attempt_count_in_response"] = includeAttemptCountInResponse
	}
	
	if includeTimeoutRetryHeader := vhg.GetFieldValueIfSelected(instance.SelectedFields, "include_is_timeout_retry_header"); includeTimeoutRetryHeader != nil {
		resource["include_is_timeout_retry_header"] = includeTimeoutRetryHeader
	}
	
	if perRequestBufferLimit := vhg.GetFieldValueIfSelected(instance.SelectedFields, "per_request_buffer_limit_bytes"); perRequestBufferLimit != nil {
		resource["per_request_buffer_limit_bytes"] = perRequestBufferLimit
	}
	
	// Virtual host resource should be an array like vhds.json example
	// Build structure manually to match vhds.json format
	finalDoc := map[string]any{
		"general":  general,
		"resource": map[string]any{
			"resource": []map[string]any{resource},
			"version":  "1",
		},
	}
	
	// Debug log for virtual host generation
	if finalDocJSON, err := json.MarshalIndent(finalDoc, "", "  "); err == nil {
		fmt.Printf("🎯 VirtualHost Generator - Final JSON:\\n%s\\n", string(finalDocJSON))
	}
	
	return finalDoc, nil
}

// convertRoutesToEnvoyFormat converts frontend route format to Envoy format
func (vhg *VirtualHostGenerator) convertRoutesToEnvoyFormat(frontendRoutes any, vhostName string) []map[string]any {
	var envoyRoutes []map[string]any

	if routesSlice, ok := frontendRoutes.([]any); ok {
		for i, routeInterface := range routesSlice {
			if route, ok := routeInterface.(map[string]any); ok {
				envoyRoute := vhg.convertRouteToEnvoyFormat(route)
				// Add route name if not provided
				if _, hasName := envoyRoute["name"]; !hasName {
					envoyRoute["name"] = fmt.Sprintf("%s_route_%d", vhostName, i)
				}
				envoyRoutes = append(envoyRoutes, envoyRoute)
			}
		}
	}

	return envoyRoutes
}

// convertRouteToEnvoyFormat converts single frontend route format to Envoy format
func (vhg *VirtualHostGenerator) convertRouteToEnvoyFormat(frontendRoute map[string]any) map[string]any {
	envoyRoute := map[string]any{}

	// Copy name if provided
	if name, exists := frontendRoute["name"]; exists {
		envoyRoute["name"] = name
	}

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

// GetComponentType returns the component type
func (vhg *VirtualHostGenerator) GetComponentType() string {
	return "virtual_host"
}

// GetCollection returns the MongoDB collection name
func (vhg *VirtualHostGenerator) GetCollection() string {
	return "virtual_hosts"
}