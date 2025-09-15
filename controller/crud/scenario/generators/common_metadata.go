package generators

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// MetadataProcessor handles common metadata extraction and encoding
type MetadataProcessor struct{}

// NewMetadataProcessor creates a new metadata processor
func NewMetadataProcessor() *MetadataProcessor {
	return &MetadataProcessor{}
}

// ExtractMetadataFields extracts common metadata fields from a resource object
func (mp *MetadataProcessor) ExtractMetadataFields(resourceObject map[string]any) (name string, gtypeStr string) {
	if nameValue, exists := resourceObject["name"]; exists {
		name = fmt.Sprint(nameValue)
	}
	if gtypeValue, exists := resourceObject["gtype"]; exists {
		gtypeStr = fmt.Sprint(gtypeValue)
	}
	return name, gtypeStr
}

// BuildMetadataFromGType builds metadata using GType methods
func (mp *MetadataProcessor) BuildMetadataFromGType(name, gtypeStr string) map[string]any {
	gtype := models.GType(gtypeStr)

	return map[string]any{
		"name":           name,
		"canonical_name": gtype.CanonicalName(),
		"gtype":          gtypeStr,
		"type":           gtype.Type(),
		"category":       gtype.Category(),
		"collection":     gtype.CollectionString(),
	}
}

// BuildMetadataWithOverrides builds metadata with custom overrides
func (mp *MetadataProcessor) BuildMetadataWithOverrides(name, gtypeStr string, overrides map[string]any) map[string]any {
	metadata := mp.BuildMetadataFromGType(name, gtypeStr)

	// Apply overrides
	maps.Copy(metadata, overrides)

	return metadata
}

// EncodeMetadata encodes metadata to base64
func (mp *MetadataProcessor) EncodeMetadata(metadata map[string]any) (string, error) {
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("failed to marshal metadata: %w", err)
	}

	return base64.StdEncoding.EncodeToString(metadataJSON), nil
}

// ProcessResourceWithMetadata processes a resource object with HasMetadata pattern
// Returns the typed_config structure for resources that need metadata encoding
func (mp *MetadataProcessor) ProcessResourceWithMetadata(resourceObject map[string]any, additionalMetadata map[string]any) (map[string]any, error) {
	// Extract basic fields
	name, gtypeStr := mp.ExtractMetadataFields(resourceObject)

	// Build metadata from GType
	metadata := mp.BuildMetadataFromGType(name, gtypeStr)

	// Add any additional metadata
	maps.Copy(metadata, additionalMetadata)

	// Encode metadata
	encodedMetadata, err := mp.EncodeMetadata(metadata)
	if err != nil {
		return nil, err
	}

	// Get canonical name for the outer name field
	gtype := models.GType(gtypeStr)
	canonicalName := gtype.CanonicalName()

	// Build typed_config structure
	return map[string]any{
		"name": canonicalName,
		"typed_config": map[string]any{
			"type_url": gtypeStr,
			"value":    encodedMetadata,
		},
	}, nil
}

// ProcessAccessLog processes access log configuration with metadata
func (mp *MetadataProcessor) ProcessAccessLog(accessLogObject map[string]any) ([]map[string]any, error) {
	processedLog, err := mp.ProcessResourceWithMetadata(accessLogObject, nil)
	if err != nil {
		return nil, err
	}

	// Access log is always an array
	return []map[string]any{processedLog}, nil
}

// ProcessTransportSocket processes transport socket configuration with metadata
func (mp *MetadataProcessor) ProcessTransportSocket(tlsObject map[string]any) (map[string]any, error) {
	processedTLS, err := mp.ProcessResourceWithMetadata(tlsObject, nil)
	if err != nil {
		return nil, err
	}

	// Transport socket uses static name
	processedTLS["name"] = "envoy.transport_sockets.tls"

	return processedTLS, nil
}

// ProcessNetworkFilter processes network filter with metadata and additional fields
func (mp *MetadataProcessor) ProcessNetworkFilter(filterObject map[string]any, filterName string, priority int) (map[string]any, string, error) {
	// Extract basic fields
	name, gtypeStr := mp.ExtractMetadataFields(filterObject)

	// Build metadata with additional fields
	metadata := mp.BuildMetadataFromGType(name, gtypeStr)
	metadata["priority"] = priority
	metadata["type"] = "network_filter"
	metadata["parent_name"] = name

	// Encode metadata
	encodedMetadata, err := mp.EncodeMetadata(metadata)
	if err != nil {
		return nil, "", err
	}

	// Build filter structure
	filter := map[string]any{
		"name": filterName,
		"typed_config": map[string]any{
			"type_url": gtypeStr,
			"value":    encodedMetadata,
		},
	}

	return filter, encodedMetadata, nil
}

// ProcessHTTPFilter processes HTTP filter with metadata
func (mp *MetadataProcessor) ProcessHTTPFilter(filterObject map[string]any, priority int) (configEntry map[string]any, transformedFilter map[string]any) {
	// Extract basic fields
	name, gtypeStr := mp.ExtractMetadataFields(filterObject)

	// Get canonical name and category from GType
	gtype := models.GType(gtypeStr)
	canonicalName := gtype.CanonicalName()
	category := gtype.Category()

	// Build config discovery entry
	configEntry = map[string]any{
		"name":           name,
		"gtype":          gtypeStr,
		"priority":       priority,
		"category":       category,
		"canonical_name": canonicalName,
		"collection":     "filters",
		"type":           "http_filter",
		"parent_name":    name,
	}

	// Build transformed filter
	transformedFilter = map[string]any{
		"name": name,
		"config_discovery": map[string]any{
			"config_source": map[string]any{
				"ads":                   map[string]any{},
				"initial_fetch_timeout": "5.0s",
				"resource_api_version":  "V3",
			},
			"type_urls": []string{gtypeStr},
		},
		"is_optional": false,
		"disabled":    false,
	}

	return configEntry, transformedFilter
}

// TransformHeadersToEnvoyFormat transforms header array to Envoy format
func (mp *MetadataProcessor) TransformHeadersToEnvoyFormat(headersArray []any) []map[string]any {
	var envoyHeaders []map[string]any

	for _, headerInterface := range headersArray {
		if header, ok := headerInterface.(map[string]any); ok {
			envoyHeader := map[string]any{
				"header": map[string]any{
					"key":   header["key"],
					"value": header["value"],
				},
			}

			// Add optional fields if they exist
			if appendAction, exists := header["append_action"]; exists {
				envoyHeader["append_action"] = appendAction
			}
			if keepEmpty, exists := header["keep_empty_value"]; exists {
				envoyHeader["keep_empty_value"] = keepEmpty
			}

			envoyHeaders = append(envoyHeaders, envoyHeader)
		}
	}

	return envoyHeaders
}

// ProcessLbEndpoints processes lb_endpoints array and creates proper Envoy endpoint structure
func (mp *MetadataProcessor) ProcessLbEndpoints(lbEndpoints any) ([]map[string]any, error) {
	if lbEndpoints == nil {
		return nil, nil
	}

	lbEndpointsArray, ok := lbEndpoints.([]any)
	if !ok {
		return nil, fmt.Errorf("lb_endpoints must be an array")
	}

	if len(lbEndpointsArray) == 0 {
		return nil, nil
	}

	var envoyLbEndpoints []map[string]any

	for _, lbEndpointInterface := range lbEndpointsArray {
		if lbEndpoint, ok := lbEndpointInterface.(map[string]any); ok {
			// Process each lb_endpoint and transform to Envoy format
			envoyLbEndpoint := mp.transformLbEndpointToEnvoyFormat(lbEndpoint)
			if envoyLbEndpoint != nil {
				envoyLbEndpoints = append(envoyLbEndpoints, envoyLbEndpoint)
			}
		}
	}

	return envoyLbEndpoints, nil
}

// transformLbEndpointToEnvoyFormat transforms a single lb_endpoint to Envoy format
func (mp *MetadataProcessor) transformLbEndpointToEnvoyFormat(lbEndpoint map[string]any) map[string]any {
	envoyLbEndpoint := make(map[string]any)

	// Handle address and port
	if address, exists := lbEndpoint["address"]; exists {
		if port, portExists := lbEndpoint["port"]; portExists {
			socketAddress := map[string]any{
				"address":    address,
				"port_value": port,
			}

			// Add protocol if provided, don't default to TCP
			if protocol, protocolExists := lbEndpoint["protocol"]; protocolExists {
				socketAddress["protocol"] = protocol
			}

			envoyLbEndpoint["endpoint"] = map[string]any{
				"address": map[string]any{
					"socket_address": socketAddress,
				},
			}
		}
	}

	// Handle optional fields like load_balancing_weight, health_status, etc.
	if weight, exists := lbEndpoint["load_balancing_weight"]; exists {
		envoyLbEndpoint["load_balancing_weight"] = weight
	}

	if healthStatus, exists := lbEndpoint["health_status"]; exists {
		envoyLbEndpoint["health_status"] = healthStatus
	}

	if metadata, exists := lbEndpoint["metadata"]; exists {
		envoyLbEndpoint["metadata"] = metadata
	}

	return envoyLbEndpoint
}

// BuildClusterLoadAssignment builds load_assignment structure for cluster
func (mp *MetadataProcessor) BuildClusterLoadAssignment(clusterName string, lbEndpoints []map[string]any) map[string]any {
	if len(lbEndpoints) == 0 {
		return nil
	}

	return map[string]any{
		"cluster_name": clusterName,
		"endpoints": []map[string]any{
			{
				"lb_endpoints": lbEndpoints,
			},
		},
	}
}

// BuildEndpointResource builds endpoints structure for endpoint resource
func (mp *MetadataProcessor) BuildEndpointResource(clusterName string, lbEndpoints []map[string]any) map[string]any {
	resource := map[string]any{
		"cluster_name": clusterName,
	}

	if len(lbEndpoints) > 0 {
		resource["endpoints"] = []map[string]any{
			{
				"lb_endpoints": lbEndpoints,
			},
		}
	}

	return resource
}
