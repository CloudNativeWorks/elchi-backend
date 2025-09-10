package generators

import (
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// EndpointGenerator generates endpoint resources
type EndpointGenerator struct {
	*BaseGenerator
	metadataProcessor *MetadataProcessor
}

// NewEndpointGenerator creates a new endpoint generator
func NewEndpointGenerator(project, version string, user models.UserDetails) *EndpointGenerator {
	return &EndpointGenerator{
		BaseGenerator:     NewBaseGenerator(project, version, user),
		metadataProcessor: NewMetadataProcessor(),
	}
}

// Generate generates an endpoint resource document
func (eg *EndpointGenerator) Generate(instance models.ComponentInstance) (any, error) {
	// Get field values with UseComponentName support
	clusterName := eg.GetFieldValueWithComponentNameSupport(instance, "cluster_name", instance.Name).(string)

	// Get gtype from instance and use it to build general section dynamically
	gtype := models.GType(instance.GType)

	// Build general section using GType methods
	general := eg.BuildGeneralSection(
		instance,
		gtype.Type(),
		gtype.CollectionString(),
		gtype.CanonicalName(),
		gtype.String(),
		gtype.Category(),
	)

	// Check endpoint configuration type from nested selection
	endpointConfig := eg.GetNestedFieldSelection(instance.SelectedFields, "endpoint_configuration")
	if endpointConfig == nil {
		return nil, fmt.Errorf("endpoint_configuration is required")
	}

	switch endpointConfig.SelectedChoice {
	case "static":
		// Handle static endpoints - get lb_endpoints from nested selection
		lbEndpoints := eg.getValueFromNestedSelection(endpointConfig, "lb_endpoints")
		if lbEndpoints != nil {
			// Check if lb_endpoints array is not empty
			if endpointsArray, ok := lbEndpoints.([]any); ok && len(endpointsArray) > 0 {
				processedEndpoints, err := eg.metadataProcessor.ProcessLbEndpoints(lbEndpoints)
				if err != nil {
					return nil, fmt.Errorf("failed to process lb_endpoints: %w", err)
				}

				resource := eg.metadataProcessor.BuildEndpointResource(clusterName, processedEndpoints)
				return eg.BuildCompleteDocument(general, resource), nil
			}
		}

		// If no lb_endpoints, return basic static resource
		resource := map[string]any{
			"cluster_name": clusterName,
		}
		return eg.BuildCompleteDocument(general, resource), nil

	case "discovery":
		// Handle discovery endpoints - create elchi_discovery configuration
		discoveryClusterName := eg.getValueFromNestedSelection(endpointConfig, "cluster_name")
		port := eg.getValueFromNestedSelection(endpointConfig, "port")
		protocol := eg.getValueFromNestedSelection(endpointConfig, "protocol")
		addressType := eg.getValueFromNestedSelection(endpointConfig, "address_type")

		if discoveryClusterName == nil || port == nil || protocol == nil {
			return nil, fmt.Errorf("discovery configuration requires cluster_name, port, and protocol")
		}

		// Extract cluster name from metadata if it's an object with metadata
		var clusterNameStr string
		if clusterObj, ok := discoveryClusterName.(map[string]any); ok {
			if name, exists := clusterObj["name"]; exists {
				clusterNameStr = fmt.Sprint(name)
			} else {
				return nil, fmt.Errorf("discovery cluster object missing name field")
			}
		} else {
			clusterNameStr = fmt.Sprint(discoveryClusterName)
		}

		// Convert port to int32
		var portInt32 int32
		switch p := port.(type) {
		case int:
			portInt32 = int32(p)
		case int32:
			portInt32 = p
		case int64:
			portInt32 = int32(p)
		case float64:
			portInt32 = int32(p)
		default:
			return nil, fmt.Errorf("invalid port type: %T", port)
		}

		protocolStr := fmt.Sprint(protocol)
		addressTypeStr := "ExternalIP" // Default
		if addressType != nil {
			addressTypeStr = fmt.Sprint(addressType)
		}

		// Create elchi_discovery configuration
		elchiDiscovery := []*models.ElchiDiscovery{
			{
				ClusterName: clusterNameStr,
				Port:        portInt32,
				Protocol:    protocolStr,
				AddressType: addressTypeStr,
			},
		}

		// Add discovery configuration to general section (general is already map[string]interface{})
		general["elchi_discovery"] = elchiDiscovery

		// Return basic resource with discovery configuration in general section
		resource := map[string]any{
			"cluster_name": clusterName,
		}

		return eg.BuildCompleteDocument(general, resource), nil

	default:
		return nil, fmt.Errorf("unknown endpoint configuration type: %s", endpointConfig.SelectedChoice)
	}
}

// getValueFromNestedSelection gets value from nested selection field
func (eg *EndpointGenerator) getValueFromNestedSelection(selection *models.NestedFieldSelection, fieldName string) any {
	if selection == nil || selection.SubFields == nil {
		return nil
	}

	for _, subField := range selection.SubFields {
		if subField.FieldName == fieldName {
			return subField.Value
		}
	}
	return nil
}

// GetComponentType returns the component type
func (eg *EndpointGenerator) GetComponentType() string {
	return "endpoint"
}

// GetCollection returns the MongoDB collection name
func (eg *EndpointGenerator) GetCollection() string {
	return "endpoints"
}
