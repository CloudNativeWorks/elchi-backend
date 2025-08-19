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
		BaseGenerator: NewBaseGenerator(project, version, user),
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
	
	// Build resource section using common utilities - check if lb_endpoints is not empty
	if lbEndpoints := eg.GetFieldValueIfSelected(instance.SelectedFields, "lb_endpoints"); lbEndpoints != nil {
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
	
	// If no lb_endpoints selected, return basic resource with just cluster_name
	resource := map[string]any{
		"cluster_name": clusterName,
	}
	
	return eg.BuildCompleteDocument(general, resource), nil
}

// GetComponentType returns the component type
func (eg *EndpointGenerator) GetComponentType() string {
	return "endpoint"
}

// GetCollection returns the MongoDB collection name
func (eg *EndpointGenerator) GetCollection() string {
	return "endpoints"
}