package generators

import (
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// RouterFilterGenerator generates router HTTP filter resources
type RouterFilterGenerator struct {
	*BaseGenerator
}

// NewRouterFilterGenerator creates a new router filter generator
func NewRouterFilterGenerator(project, version string, user models.UserDetails) *RouterFilterGenerator {
	return &RouterFilterGenerator{
		BaseGenerator: NewBaseGenerator(project, version, user),
	}
}

// Generate generates a router HTTP filter resource document
func (rfg *RouterFilterGenerator) Generate(instance models.ComponentInstance) (any, error) {
	// Get gtype from instance and use it to build general section dynamically
	gtype := models.GType(instance.GType)
	
	// Build general section using GType methods
	general := rfg.BuildGeneralSection(
		instance,
		gtype.Type(),
		gtype.CollectionString(),
		gtype.CanonicalName(),
		gtype.String(),
		gtype.Category(),
	)
	
	// Add http_filter metadata as per JSON example
	if general["metadata"] == nil {
		general["metadata"] = make(map[string]any)
	}
	if metadata, ok := general["metadata"].(map[string]any); ok {
		metadata["http_filter"] = "main"
	}
	
	// Router filter has minimal resource configuration matching JSON example
	// Resource section is empty as per JSON example
	resource := map[string]any{
		// Router filter resource is empty - no configuration needed
	}
	
	return rfg.BuildCompleteDocument(general, resource), nil
}

// GetComponentType returns the component type
func (rfg *RouterFilterGenerator) GetComponentType() string {
	return "router_filter"
}

// GetCollection returns the MongoDB collection name
func (rfg *RouterFilterGenerator) GetCollection() string {
	return "filters"
}