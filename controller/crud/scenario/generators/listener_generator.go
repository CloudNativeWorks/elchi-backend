package generators

import (
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// ListenerGenerator generates listener resources
type ListenerGenerator struct {
	*BaseGenerator
	componentTypeMap  map[string]string // Maps component name to type
	metadataProcessor *MetadataProcessor
}

// NewListenerGenerator creates a new listener generator
func NewListenerGenerator(project, version string, user models.UserDetails) *ListenerGenerator {
	return &ListenerGenerator{
		BaseGenerator:     NewBaseGenerator(project, version, user),
		metadataProcessor: NewMetadataProcessor(),
	}
}

// NewListenerGeneratorWithMapping creates a new listener generator with component type mapping
func NewListenerGeneratorWithMapping(project, version string, user models.UserDetails, componentTypeMap map[string]string) *ListenerGenerator {
	return &ListenerGenerator{
		BaseGenerator:     NewBaseGenerator(project, version, user),
		componentTypeMap:  componentTypeMap,
		metadataProcessor: NewMetadataProcessor(),
	}
}

// NewListenerGeneratorWithManagedMapping creates a new listener generator with component type mapping and managed setting
func NewListenerGeneratorWithManagedMapping(project, version string, user models.UserDetails, componentTypeMap map[string]string, managed bool) *ListenerGenerator {
	return &ListenerGenerator{
		BaseGenerator:     NewBaseGeneratorWithManaged(project, version, user, managed),
		componentTypeMap:  componentTypeMap,
		metadataProcessor: NewMetadataProcessor(),
	}
}

// Generate generates listener resource documents (can be multiple with TLS config)
func (lg *ListenerGenerator) Generate(instance models.ComponentInstance) (any, error) {
	// Check for TLS/SSL configuration
	tlsConfig := lg.getTLSConfig(instance)
	if tlsConfig != nil {
		// TLS is enabled, check for additional HTTP listener options
		httpConfig := lg.getHTTPListenerConfig(tlsConfig)
		if httpConfig != nil {
			// Create multiple listeners: HTTPS + HTTP
			return lg.generateMultipleListeners(instance, tlsConfig, httpConfig)
		} else {
			// Create single HTTPS listener
			return lg.generateSingleListenerWithTLS(instance, tlsConfig)
		}
	}

	// No TLS config, generate single listener with legacy support
	return lg.buildMainListener(instance)
}

// buildMainListener builds the primary listener with current instance configuration
func (lg *ListenerGenerator) buildMainListener(instance models.ComponentInstance) (map[string]any, error) {
	// Get field values with UseComponentName support
	listenerName := lg.GetFieldValueWithComponentNameSupport(instance, "name", instance.Name).(string)

	// Generate unique names - listener name should have suffix for uniqueness
	finalListenerName := lg.BuildNameWithSuffix(listenerName, 6)
	finalFilterChainName := lg.BuildFilterChainName(finalListenerName, 6)

	// Get gtype from instance and use it to build general section dynamically
	gtype := models.GType(instance.GType)

	// Build general section with managed field using GType methods
	general := lg.BuildGeneralSectionWithManaged(
		instance,
		gtype.Type(),
		gtype.CollectionString(),
		gtype.CanonicalName(),
		gtype.String(),
		gtype.Category(),
		lg.Managed, // Use managed value from execution request
	)

	// Build typed_config for network filters only if network_filters selected
	var typedConfig []map[string]any
	if networkFilters := lg.GetFieldValueIfSelected(instance.SelectedFields, "network_filters"); networkFilters != nil {
		if networkFiltersArray, ok := networkFilters.([]any); ok {
			for i, filterInterface := range networkFiltersArray {
				if filterObject, ok := filterInterface.(map[string]any); ok {
					// Extract filter information from the object
					filterName := ""
					filterGType := ""

					if name, exists := filterObject["name"]; exists {
						filterName = fmt.Sprint(name)
					}
					if gtype, exists := filterObject["gtype"]; exists {
						filterGType = fmt.Sprint(gtype)
					}

					// Get canonical name and category from models.GTypes
					gtype := models.GType(filterGType)

					filterConfig := map[string]any{
						"name":           filterName,
						"canonical_name": gtype.CanonicalName(),
						"gtype":          gtype.String(),
						"type":           gtype.Type(),
						"category":       gtype.Category(),
						"collection":     gtype.CollectionString(),
						"disabled":       false,
						"priority":       i,
						"parent_name":    "",
					}
					typedConfig = append(typedConfig, filterConfig)
				}
			}
		}
	}

	// Only add typed_config if it has entries
	if len(typedConfig) > 0 {
		general["typed_config"] = typedConfig
	}
	// Don't add empty config_discovery array

	// Build filter chains with dynamic network filters only if network_filters selected
	filterChains, err := lg.buildFilterChains(instance, finalListenerName, finalFilterChainName, nil)
	if err != nil {
		return nil, err
	}

	// Build resource section
	resource := lg.buildListenerResource(instance, finalListenerName, filterChains)

	return lg.BuildCompleteDocumentWithArray(general, resource), nil
}

// buildFilterChains builds filter chains for a listener
func (lg *ListenerGenerator) buildFilterChains(instance models.ComponentInstance, finalListenerName, finalFilterChainName string, tlsSocket map[string]any) ([]map[string]any, error) {
	var filterChains []map[string]any

	if networkFilters := lg.GetFieldValueIfSelected(instance.SelectedFields, "network_filters"); networkFilters != nil {
		if networkFiltersArray, ok := networkFilters.([]any); ok && len(networkFiltersArray) > 0 {
			var filters []map[string]any

			for i, filterInterface := range networkFiltersArray {
				if filterObject, ok := filterInterface.(map[string]any); ok {
					// Generate filter name using the naming convention: listener-fc-filter
					filterSuffix := lg.GenerateRandomString(6)
					finalFilterName := lg.BuildFilterName(finalListenerName, finalFilterChainName, filterSuffix)

					// Use metadata processor for network filter
					filter, _, err := lg.metadataProcessor.ProcessNetworkFilter(filterObject, finalFilterName, i)
					if err != nil {
						return nil, fmt.Errorf("failed to process network filter: %w", err)
					}

					filters = append(filters, filter)
				}
			}

			if len(filters) > 0 {
				filterChain := map[string]any{
					"name":    finalFilterChainName,
					"filters": filters,
				}

				// Add TLS socket if provided (only through new TLS/SSL config)
				if tlsSocket != nil {
					filterChain["transport_socket"] = tlsSocket
				}

				filterChains = append(filterChains, filterChain)
			}
		}
	}

	return filterChains, nil
}

// buildListenerResource builds the listener resource section
func (lg *ListenerGenerator) buildListenerResource(instance models.ComponentInstance, finalListenerName string, filterChains []map[string]any) map[string]any {
	// Build resource section - only include selected fields
	resource := map[string]any{
		"name": finalListenerName,
	}

	// Add address only if address or port selected
	if address := lg.GetFieldValueIfSelected(instance.SelectedFields, "address"); address != nil {
		if port := lg.GetFieldValueIfSelected(instance.SelectedFields, "port"); port != nil {
			// Default protocol if not specified
			protocol := "TCP"
			if protocolValue := lg.GetFieldValueIfSelected(instance.SelectedFields, "protocol"); protocolValue != nil {
				protocol = protocolValue.(string)
			}

			resource["address"] = map[string]any{
				"socket_address": map[string]any{
					"protocol":   protocol,
					"address":    address,
					"port_value": port,
				},
			}
		}
	}

	// Add filter_chains only if network_filters were selected
	if len(filterChains) > 0 {
		resource["filter_chains"] = filterChains
	}

	// Add other fields only if selected
	if statPrefix := lg.GetFieldValueIfSelected(instance.SelectedFields, "stat_prefix"); statPrefix != nil {
		resource["stat_prefix"] = statPrefix
	}
	if useOriginalDst := lg.GetFieldValueIfSelected(instance.SelectedFields, "use_original_dst"); useOriginalDst != nil {
		resource["use_original_dst"] = useOriginalDst
	}
	if perConnBufferLimit := lg.GetFieldValueIfSelected(instance.SelectedFields, "per_connection_buffer_limit_bytes"); perConnBufferLimit != nil {
		resource["per_connection_buffer_limit_bytes"] = perConnBufferLimit
	}
	if continueOnTimeout := lg.GetFieldValueIfSelected(instance.SelectedFields, "continue_on_listener_filters_timeout"); continueOnTimeout != nil {
		resource["continue_on_listener_filters_timeout"] = continueOnTimeout
	}
	if transparent := lg.GetFieldValueIfSelected(instance.SelectedFields, "transparent"); transparent != nil {
		resource["transparent"] = transparent
	}
	if freebind := lg.GetFieldValueIfSelected(instance.SelectedFields, "freebind"); freebind != nil {
		resource["freebind"] = freebind
	}
	if tcpFastOpenQueue := lg.GetFieldValueIfSelected(instance.SelectedFields, "tcp_fast_open_queue_length"); tcpFastOpenQueue != nil {
		resource["tcp_fast_open_queue_length"] = tcpFastOpenQueue
	}
	if trafficDirection := lg.GetFieldValueIfSelected(instance.SelectedFields, "traffic_direction"); trafficDirection != nil {
		resource["traffic_direction"] = trafficDirection
	}
	if enableReusePort := lg.GetFieldValueIfSelected(instance.SelectedFields, "enable_reuse_port"); enableReusePort != nil {
		resource["enable_reuse_port"] = enableReusePort
	}
	if tcpBacklogSize := lg.GetFieldValueIfSelected(instance.SelectedFields, "tcp_backlog_size"); tcpBacklogSize != nil {
		resource["tcp_backlog_size"] = tcpBacklogSize
	}
	if maxConnections := lg.GetFieldValueIfSelected(instance.SelectedFields, "max_connections_to_accept_per_socket_event"); maxConnections != nil {
		resource["max_connections_to_accept_per_socket_event"] = maxConnections
	}
	if bindToPort := lg.GetFieldValueIfSelected(instance.SelectedFields, "bind_to_port"); bindToPort != nil {
		resource["bind_to_port"] = bindToPort
	}
	if ignoreGlobalLimit := lg.GetFieldValueIfSelected(instance.SelectedFields, "ignore_global_conn_limit"); ignoreGlobalLimit != nil {
		resource["ignore_global_conn_limit"] = ignoreGlobalLimit
	}
	if bypassOverload := lg.GetFieldValueIfSelected(instance.SelectedFields, "bypass_overload_manager"); bypassOverload != nil {
		resource["bypass_overload_manager"] = bypassOverload
	}

	return resource
}

// getTLSConfig extracts TLS/SSL configuration from instance
func (lg *ListenerGenerator) getTLSConfig(instance models.ComponentInstance) map[string]any {
	if tlsSslSelection := lg.GetNestedFieldSelection(instance.SelectedFields, "tls_ssl_config"); tlsSslSelection != nil {
		if tlsSslSelection.SelectedChoice == "enable_tls" {
			return lg.getSubFieldsMap(tlsSslSelection.SubFields)
		}
	}
	return nil
}

// getHTTPListenerConfig extracts HTTP listener configuration from TLS config
func (lg *ListenerGenerator) getHTTPListenerConfig(tlsConfig map[string]any) map[string]any {
	// Look for nested http_listener_config in TLS config sub fields
	for _, field := range tlsConfig["sub_fields"].([]models.SelectedField) {
		if field.FieldName == "http_listener_config" && field.NestedSelection != nil {
			result := lg.getSubFieldsMap(field.NestedSelection.SubFields)
			// Also store the selected choice
			result["selected_choice"] = field.NestedSelection.SelectedChoice
			return result
		}
	}
	return nil
}

// getSubFieldsMap converts sub fields array to map for easy access
func (lg *ListenerGenerator) getSubFieldsMap(subFields []models.SelectedField) map[string]any {
	result := make(map[string]any)
	result["sub_fields"] = subFields

	// Also create direct field access
	for _, field := range subFields {
		result[field.FieldName] = field.Value
	}

	return result
}

// generateSingleListenerWithTLS creates single HTTPS listener with TLS
func (lg *ListenerGenerator) generateSingleListenerWithTLS(instance models.ComponentInstance, tlsConfig map[string]any) (map[string]any, error) {
	// Build HTTPS listener resource
	httpsListenerResource, err := lg.buildListenerResourceWithTLS(instance, tlsConfig)
	if err != nil {
		return nil, err
	}

	// Build general section
	gtype := models.GType(instance.GType)
	general := lg.BuildGeneralSectionWithManaged(
		instance,
		gtype.Type(),
		gtype.CollectionString(),
		gtype.CanonicalName(),
		gtype.String(),
		gtype.Category(),
		lg.Managed,
	)

	// Build typed_config
	typedConfig := lg.buildCombinedTypedConfig(instance)
	general["typed_config"] = typedConfig
	general["config_discovery"] = []any{}

	// Return single document with single listener resource
	return lg.BuildCompleteDocumentWithArray(general, httpsListenerResource), nil
}

// generateMultipleListeners creates both HTTPS and HTTP listeners in single document
func (lg *ListenerGenerator) generateMultipleListeners(instance models.ComponentInstance, tlsConfig, httpConfig map[string]any) (map[string]any, error) {
	// Build HTTPS listener resource
	httpsListenerResource, err := lg.buildListenerResourceWithTLS(instance, tlsConfig)
	if err != nil {
		return nil, err
	}

	// Build HTTP listener resource
	httpListenerResource, err := lg.buildHTTPListenerResource(instance, httpConfig)
	if err != nil {
		return nil, err
	}

	// Build general section (shared for both listeners)
	gtype := models.GType(instance.GType)
	general := lg.BuildGeneralSectionWithManaged(
		instance,
		gtype.Type(),
		gtype.CollectionString(),
		gtype.CanonicalName(),
		gtype.String(),
		gtype.Category(),
		lg.Managed,
	)

	// Build combined typed_config for both listeners
	typedConfig := lg.buildCombinedTypedConfig(instance)

	// If HTTP listener is redirect type, add default-httptohttps to typed_config
	if lg.isRedirectType(httpConfig) {
		gtypeHCM := models.HTTPConnectionManager
		// Add redirect HCM to typed_config
		redirectHCMConfig := map[string]any{
			"name":           "default-httptohttps",
			"canonical_name": gtypeHCM.CanonicalName(),
			"gtype":          gtypeHCM.String(),
			"type":           gtypeHCM.Type(),
			"category":       gtypeHCM.Category(),
			"collection":     gtypeHCM.CollectionString(),
			"disabled":       false,
			"priority":       len(typedConfig), // Add at the end
			"parent_name":    "",
		}
		typedConfig = append(typedConfig, redirectHCMConfig)
	}

	general["typed_config"] = typedConfig
	general["config_discovery"] = []any{}

	// Return single document with array of listener resources
	// Use BuildCompleteDocument and manually set the resource array
	document := lg.BuildCompleteDocument(general, httpsListenerResource)
	document["resource"].(map[string]any)["resource"] = []map[string]any{httpsListenerResource, httpListenerResource}
	return document, nil
}

// copyInstanceForHTTPListener creates a copy of instance with modified port for HTTP listener
func (lg *ListenerGenerator) copyInstanceForHTTPListener(instance models.ComponentInstance, httpPort any) models.ComponentInstance {
	httpInstance := instance

	// Modify the name to include _http suffix (not -http)
	httpInstance.Name = instance.Name + "_http"

	// Update port in selected fields
	for i, field := range httpInstance.SelectedFields {
		if field.FieldName == "port" {
			httpInstance.SelectedFields[i].Value = httpPort
		}
		// Also update the name field to include _http
		if field.FieldName == "name" {
			httpInstance.SelectedFields[i].Value = httpInstance.SelectedFields[i].Value.(string) + "_http"
		}
	}

	return httpInstance
}

// isRedirectType checks if HTTP listener should be a redirect listener
func (lg *ListenerGenerator) isRedirectType(httpConfig map[string]any) bool {
	// Check the selected_choice that we stored in getHTTPListenerConfig
	if selectedChoice, ok := httpConfig["selected_choice"].(string); ok {
		return selectedChoice == "redirect"
	}
	return false
}

// createRedirectHCM creates the default-httptohttps HCM reference with metadata only
func (lg *ListenerGenerator) createRedirectHCM(filterName string) (map[string]any, error) {
	gtypeHCM := models.HTTPConnectionManager
	// Create the redirect HCM configuration object
	hcmObject := map[string]any{
		"name":           "default-httptohttps",
		"canonical_name": gtypeHCM.CanonicalName(),
		"gtype":          gtypeHCM.String(),
		"type":           gtypeHCM.Type(),
		"category":       gtypeHCM.Category(),
		"collection":     gtypeHCM.CollectionString(),
	}

	// Process with metadata processor to add base64 encoded metadata
	// This will return a filter with just name and metadata fields
	filter, _, err := lg.metadataProcessor.ProcessNetworkFilter(hcmObject, filterName, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to process redirect HCM filter: %w", err)
	}

	// Don't add any typed_config - just return the filter with name and metadata
	// The actual redirect configuration should be defined elsewhere (in the actual HCM resource)

	return filter, nil
}

// buildListenerResourceWithTLS builds HTTPS listener resource with TLS configuration
func (lg *ListenerGenerator) buildListenerResourceWithTLS(instance models.ComponentInstance, tlsConfig map[string]any) (map[string]any, error) {
	// Get listener name and generate unique names
	listenerName := lg.GetFieldValueWithComponentNameSupport(instance, "name", instance.Name).(string)
	finalListenerName := lg.BuildNameWithSuffix(listenerName, 6)
	finalFilterChainName := lg.BuildFilterChainName(finalListenerName, 6)

	// Get downstream TLS from config
	downstreamTLS := tlsConfig["downstream_tls"]
	if downstreamTLS == nil {
		return nil, fmt.Errorf("downstream_tls is required when TLS is enabled")
	}

	// Process TLS socket using metadata processor
	var processedTLS map[string]any
	if tlsObject, ok := downstreamTLS.(map[string]any); ok {
		var err error
		processedTLS, err = lg.metadataProcessor.ProcessTransportSocket(tlsObject)
		if err != nil {
			return nil, fmt.Errorf("failed to process TLS socket: %w", err)
		}
	}

	// Build filter chains with TLS
	filterChains, err := lg.buildFilterChains(instance, finalListenerName, finalFilterChainName, processedTLS)
	if err != nil {
		return nil, err
	}

	// Build listener resource
	return lg.buildListenerResource(instance, finalListenerName, filterChains), nil
}

// buildHTTPListenerResource builds HTTP listener resource (redirect or normal)
func (lg *ListenerGenerator) buildHTTPListenerResource(instance models.ComponentInstance, httpConfig map[string]any) (map[string]any, error) {
	// Get HTTP port from config
	httpPort := httpConfig["http_port"]
	if httpPort == nil {
		return nil, fmt.Errorf("http_port is required for HTTP listener")
	}

	// Create HTTP instance with modified port and name
	httpInstance := lg.copyInstanceForHTTPListener(instance, httpPort)

	// Generate unique names for HTTP listener
	listenerName := lg.GetFieldValueWithComponentNameSupport(httpInstance, "name", httpInstance.Name).(string)
	finalListenerName := lg.BuildNameWithSuffix(listenerName, 6)
	finalFilterChainName := lg.BuildFilterChainName(finalListenerName, 6)

	// Check if this should be a redirect listener
	isRedirect := lg.isRedirectType(httpConfig)

	var filterChains []map[string]any
	if isRedirect {
		// Create redirect HCM filter chain with proper filter name
		filterSuffix := lg.GenerateRandomString(6)
		finalFilterName := lg.BuildFilterName(finalListenerName, finalFilterChainName, filterSuffix)

		redirectHCM, err := lg.createRedirectHCM(finalFilterName)
		if err != nil {
			return nil, err
		}

		filterChain := map[string]any{
			"name":    finalFilterChainName,
			"filters": []map[string]any{redirectHCM},
		}
		filterChains = []map[string]any{filterChain}
	} else {
		// Build normal filter chains (same as HTTPS but without TLS)
		var err error
		filterChains, err = lg.buildFilterChains(httpInstance, finalListenerName, finalFilterChainName, nil)
		if err != nil {
			return nil, err
		}
	}

	// Build HTTP listener resource
	return lg.buildListenerResource(httpInstance, finalListenerName, filterChains), nil
}

// buildCombinedTypedConfig builds typed_config for both listeners
func (lg *ListenerGenerator) buildCombinedTypedConfig(instance models.ComponentInstance) []map[string]any {
	var typedConfig []map[string]any

	if networkFilters := lg.GetFieldValueIfSelected(instance.SelectedFields, "network_filters"); networkFilters != nil {
		if networkFiltersArray, ok := networkFilters.([]any); ok {
			for i, filterInterface := range networkFiltersArray {
				if filterObject, ok := filterInterface.(map[string]any); ok {
					// Extract filter information from the object
					filterName := ""
					filterGType := ""

					if name, exists := filterObject["name"]; exists {
						filterName = fmt.Sprint(name)
					}
					if gtype, exists := filterObject["gtype"]; exists {
						filterGType = fmt.Sprint(gtype)
					}

					// Get canonical name and category from models.GTypes
					gtype := models.GType(filterGType)

					filterConfig := map[string]any{
						"name":           filterName,
						"canonical_name": gtype.CanonicalName(),
						"gtype":          gtype.String(),
						"type":           gtype.Type(),
						"category":       gtype.Category(),
						"collection":     gtype.CollectionString(),
						"disabled":       false,
						"priority":       i,
						"parent_name":    "",
					}
					typedConfig = append(typedConfig, filterConfig)
				}
			}
		}
	}

	return typedConfig
}

// GetComponentType returns the component type
func (lg *ListenerGenerator) GetComponentType() string {
	return "listener"
}

// GetCollection returns the MongoDB collection name
func (lg *ListenerGenerator) GetCollection() string {
	return "listeners"
}
