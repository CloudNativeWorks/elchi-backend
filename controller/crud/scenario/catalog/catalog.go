package catalog

import (
	"sort"
	
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// ComponentCatalog contains all available component definitions
var ComponentCatalog = []models.ComponentDefinition{
	ClusterDefinition,
	ListenerDefinition,
	HttpConnectionManagerDefinition,
	TcpProxyDefinition,
	RouteDefinition,
	VirtualHostDefinition,
	EndpointDefinition,
	RouterFilterDefinition,
}

// GetComponentCatalog returns all available component definitions
func GetComponentCatalog() []models.ComponentDefinition {
	return ComponentCatalog
}

// GetComponentCatalogSorted returns all available component definitions sorted by priority
func GetComponentCatalogSorted() []models.ComponentDefinition {
	// Create a copy to avoid modifying the original slice
	sortedComponents := make([]models.ComponentDefinition, len(ComponentCatalog))
	copy(sortedComponents, ComponentCatalog)
	
	// Sort by priority (lower priority numbers first)
	sort.Slice(sortedComponents, func(i, j int) bool {
		return sortedComponents[i].Priority < sortedComponents[j].Priority
	})
	
	return sortedComponents
}

// GetComponentDefinitionByType returns specific component definition
func GetComponentDefinitionByType(componentType string) (*models.ComponentDefinition, error) {
	for _, component := range ComponentCatalog {
		if component.Name == componentType {
			return &component, nil
		}
	}
	return nil, nil
}

// GetComponentLabel returns the label for a component type
func GetComponentLabel(componentType string) string {
	for _, comp := range ComponentCatalog {
		if comp.Name == componentType {
			return comp.Label
		}
	}
	return componentType
}