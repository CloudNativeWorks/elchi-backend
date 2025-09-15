package generators

import (
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// ClusterGenerator generates cluster resources
type ClusterGenerator struct {
	*BaseGenerator
	metadataProcessor *MetadataProcessor
}

// NewClusterGenerator creates a new cluster generator
func NewClusterGenerator(project, version string, user models.UserDetails) *ClusterGenerator {
	return &ClusterGenerator{
		BaseGenerator:     NewBaseGenerator(project, version, user),
		metadataProcessor: NewMetadataProcessor(),
	}
}

// Generate generates a cluster resource document
func (cg *ClusterGenerator) Generate(instance models.ComponentInstance) (any, error) {
	// Get field values with UseComponentName support
	clusterName := cg.GetFieldValueWithComponentNameSupport(instance, "name", instance.Name).(string)

	// Get gtype from instance and use it to build general section dynamically
	gtype := models.GType(instance.GType)

	// Build general section using GType methods
	general := cg.BuildGeneralSection(
		instance,
		gtype.Type(),
		gtype.CollectionString(),
		gtype.CanonicalName(),
		gtype.String(),
		gtype.Category(),
	)

	// Build resource section - start with required fields only
	resource := map[string]any{
		"name": clusterName,
	}

	// Add optional fields only if user selected them
	if clusterType := cg.GetFieldValueIfSelected(instance.SelectedFields, "type"); clusterType != nil {
		resource["type"] = clusterType
	} else {
		// type is required, use default if not selected
		resource["type"] = "STRICT_DNS"
	}

	if connectTimeout := cg.GetFieldValueIfSelected(instance.SelectedFields, "connect_timeout"); connectTimeout != nil {
		resource["connect_timeout"] = connectTimeout
	}

	if lbPolicy := cg.GetFieldValueIfSelected(instance.SelectedFields, "lb_policy"); lbPolicy != nil {
		resource["lb_policy"] = lbPolicy
	}

	// Add optional fields
	if altStatName := cg.GetFieldValueIfSelected(instance.SelectedFields, "alt_stat_name"); altStatName != nil {
		resource["alt_stat_name"] = altStatName
	}

	if perConnBufferLimit := cg.GetFieldValueIfSelected(instance.SelectedFields, "per_connection_buffer_limit_bytes"); perConnBufferLimit != nil {
		resource["per_connection_buffer_limit_bytes"] = perConnBufferLimit
	}

	if waitForWarm := cg.GetFieldValueIfSelected(instance.SelectedFields, "wait_for_warm_on_init"); waitForWarm != nil {
		resource["wait_for_warm_on_init"] = waitForWarm
	}

	if cleanupInterval := cg.GetFieldValueIfSelected(instance.SelectedFields, "cleanup_interval"); cleanupInterval != nil {
		resource["cleanup_interval"] = cleanupInterval
	}

	if closeConnOnHealthFailure := cg.GetFieldValueIfSelected(instance.SelectedFields, "close_connections_on_host_health_failure"); closeConnOnHealthFailure != nil {
		resource["close_connections_on_host_health_failure"] = closeConnOnHealthFailure
	}

	if ignoreHealthOnRemoval := cg.GetFieldValueIfSelected(instance.SelectedFields, "ignore_health_on_host_removal"); ignoreHealthOnRemoval != nil {
		resource["ignore_health_on_host_removal"] = ignoreHealthOnRemoval
	}

	if connPoolPerDownstream := cg.GetFieldValueIfSelected(instance.SelectedFields, "connection_pool_per_downstream_connection"); connPoolPerDownstream != nil {
		resource["connection_pool_per_downstream_connection"] = connPoolPerDownstream
	}

	// Add health check if provided - convert single health_check to health_checks array
	if healthCheck := cg.GetFieldValueIfSelected(instance.SelectedFields, "health_check"); healthCheck != nil {
		if healthCheckMap, ok := healthCheck.(map[string]any); ok {
			transformedHealthCheck := make(map[string]any)

			// Copy common fields
			if timeout, exists := healthCheckMap["timeout"]; exists {
				transformedHealthCheck["timeout"] = timeout
			}
			if interval, exists := healthCheckMap["interval"]; exists {
				transformedHealthCheck["interval"] = interval
			}
			if unhealthyThreshold, exists := healthCheckMap["unhealthy_threshold"]; exists {
				transformedHealthCheck["unhealthy_threshold"] = unhealthyThreshold
			}
			if healthyThreshold, exists := healthCheckMap["healthy_threshold"]; exists {
				transformedHealthCheck["healthy_threshold"] = healthyThreshold
			}

			// Get health check type - handle conditional field structure
			healthCheckType := "tcp"
			var httpConfig map[string]any

			if typeField, exists := healthCheckMap["type"]; exists {
				if typeMap, ok := typeField.(map[string]any); ok {
					// Conditional field structure
					if selectedChoice, exists := typeMap["selected_choice"]; exists {
						healthCheckType = fmt.Sprint(selectedChoice)
					}
					// Get HTTP config if available
					if httpData, exists := typeMap["http"]; exists {
						if httpMap, ok := httpData.(map[string]any); ok {
							httpConfig = httpMap
						}
					}
				} else {
					// Simple string value
					healthCheckType = fmt.Sprint(typeField)
				}
			}

			// Build health check based on type
			switch healthCheckType {
			case "tcp":
				// TCP health check - empty object
				transformedHealthCheck["tcp_health_check"] = map[string]any{}
			case "http":
				// HTTP health check with path and optional host
				httpHealthCheck := make(map[string]any)

				// Default path
				httpHealthCheck["path"] = "/health"

				// Override with user values from conditional field
				if httpConfig != nil {
					if path, exists := httpConfig["path"]; exists && fmt.Sprint(path) != "" {
						httpHealthCheck["path"] = path
					}
					if host, exists := httpConfig["host"]; exists && fmt.Sprint(host) != "" {
						httpHealthCheck["host"] = host
					}
				}

				transformedHealthCheck["http_health_check"] = httpHealthCheck
			}

			// Convert single health check to array
			resource["health_checks"] = []map[string]any{transformedHealthCheck}
		}
	}

	// Get current cluster type for conditional logic
	currentClusterType := resource["type"].(string)

	// Add EDS cluster config if type is EDS or STATIC (both can have EDS config)
	if currentClusterType == "EDS" {
		// Get nested field selection for endpoint_discovery_config
		if nestedSelection := cg.GetNestedFieldSelection(instance.SelectedFields, "endpoint_discovery_config"); nestedSelection != nil {
			if nestedSelection.SelectedChoice == "eds" {
				// Get EDS service name from sub_fields
				edsServiceName := cg.GetFieldValue(nestedSelection.SubFields, "eds_service_name", "").(string)

				// Create EDS config if service name found
				if edsServiceName != "" {
					edsConfig := map[string]any{
						"eds_config": map[string]any{
							"ads":                   map[string]any{},
							"resource_api_version":  "V3",
							"initial_fetch_timeout": "5.0s",
						},
						"service_name": edsServiceName,
					}

					resource["eds_cluster_config"] = edsConfig
				}
			}
		}
	}

	// Build load_assignment from static endpoints if provided via nested choice
	// Try to get nested selection first, if not found, parse from Value field directly
	var staticEndpoints []any

	// Method 1: Get from proper NestedSelection struct
	if nestedSelection := cg.GetNestedFieldSelection(instance.SelectedFields, "endpoint_discovery_config"); nestedSelection != nil {
		if nestedSelection.SelectedChoice == "static_endpoints" {
			if endpointsValue := cg.GetFieldValue(nestedSelection.SubFields, "endpoints", nil); endpointsValue != nil {
				if endpoints, ok := endpointsValue.([]any); ok {
					staticEndpoints = endpoints
				}
			}
		}
	}

	// Method 2: Parse from Value field (for cases where JSON unmarshal didn't create proper NestedSelection)
	if len(staticEndpoints) == 0 {
		if endpointDiscoveryConfig := cg.GetFieldValueIfSelected(instance.SelectedFields, "endpoint_discovery_config"); endpointDiscoveryConfig != nil {
			if discoveryMap, ok := endpointDiscoveryConfig.(map[string]any); ok {
				// Handle nested_selection structure in Value field
				if nestedSelectionData, exists := discoveryMap["nested_selection"]; exists {
					if nestedMap, ok := nestedSelectionData.(map[string]any); ok {
						if choice, exists := nestedMap["selected_choice"]; exists && fmt.Sprint(choice) == "static_endpoints" {
							if subFields, exists := nestedMap["sub_fields"]; exists {
								if subFieldsArray, ok := subFields.([]any); ok {
									for _, subFieldInterface := range subFieldsArray {
										if subField, ok := subFieldInterface.(map[string]any); ok {
											if fmt.Sprint(subField["field_name"]) == "endpoints" {
												if value, exists := subField["value"]; exists {
													if endpoints, ok := value.([]any); ok {
														staticEndpoints = endpoints
														break
													}
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	// Generate load_assignment if we found static endpoints and they are not empty
	if len(staticEndpoints) > 0 {
		processedEndpoints, err := cg.metadataProcessor.ProcessLbEndpoints(staticEndpoints)
		if err != nil {
			return nil, fmt.Errorf("failed to process lb_endpoints: %w", err)
		}

		// Only add load_assignment if processedEndpoints is not empty
		if loadAssignment := cg.metadataProcessor.BuildClusterLoadAssignment(clusterName, processedEndpoints); loadAssignment != nil {
			resource["load_assignment"] = loadAssignment
		}
	}

	// Add HTTP protocol options if provided
	if httpProtocolOptions := cg.GetFieldValueIfSelected(instance.SelectedFields, "http_protocol_options"); httpProtocolOptions != nil {
		resource["typed_extension_protocol_options"] = map[string]any{
			"envoy.extensions.upstreams.http.v3.HttpProtocolOptions": httpProtocolOptions,
		}
	}

	return cg.BuildCompleteDocument(general, resource), nil
}

// GetComponentType returns the component type
func (cg *ClusterGenerator) GetComponentType() string {
	return "cluster"
}

// GetCollection returns the MongoDB collection name
func (cg *ClusterGenerator) GetCollection() string {
	return "clusters"
}
