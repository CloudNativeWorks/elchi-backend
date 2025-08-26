package generators

import (
	"fmt"
	
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// GeneratorFactory creates appropriate generators for component types
type GeneratorFactory struct {
	project          string
	version          string
	user             models.UserDetails
	componentTypeMap map[string]string // Maps component name to type
	managed          bool              // Whether resources should be saved to database
}

// NewGeneratorFactory creates a new generator factory
func NewGeneratorFactory(project, version string, user models.UserDetails) *GeneratorFactory {
	return &GeneratorFactory{
		project: project,
		version: version,
		user:    user,
	}
}

// NewGeneratorFactoryWithMapping creates a new generator factory with component type mapping
func NewGeneratorFactoryWithMapping(project, version string, user models.UserDetails, componentTypeMap map[string]string, managed bool) *GeneratorFactory {
	return &GeneratorFactory{
		project:          project,
		version:          version,
		user:             user,
		componentTypeMap: componentTypeMap,
		managed:          managed,
	}
}


// CreateGenerator creates a generator for the specified component type
func (gf *GeneratorFactory) CreateGenerator(componentType string) (ComponentGenerator, error) {
	switch componentType {
	case "listener":
		return NewListenerGeneratorWithManagedMapping(gf.project, gf.version, gf.user, gf.componentTypeMap, gf.managed), nil
	case "cluster":
		return NewClusterGenerator(gf.project, gf.version, gf.user), nil
	case "http_connection_manager":
		return NewHCMGeneratorWithMapping(gf.project, gf.version, gf.user, gf.componentTypeMap), nil
	case "route":
		return NewRouteGenerator(gf.project, gf.version, gf.user), nil
	case "virtual_host":
		return NewVirtualHostGenerator(gf.project, gf.version, gf.user), nil
	case "endpoint":
		return NewEndpointGenerator(gf.project, gf.version, gf.user), nil
	case "tcp_proxy":
		return NewTCPProxyGenerator(gf.project, gf.version, gf.user), nil
	case "router_filter":
		return NewRouterFilterGenerator(gf.project, gf.version, gf.user), nil
	default:
		return nil, fmt.Errorf("unsupported component type: %s", componentType)
	}
}

// GetSupportedTypes returns all supported component types
func (gf *GeneratorFactory) GetSupportedTypes() []string {
	return []string{
		"listener",
		"cluster", 
		"http_connection_manager",
		"route",
		"virtual_host",
		"endpoint",
		"tcp_proxy",
		"router_filter",
	}
}

// GenerateComponent generates a resource document(s) for a component instance
// Can return single document (map) or multiple documents (array) depending on the component type
func (gf *GeneratorFactory) GenerateComponent(instance models.ComponentInstance) (any, error) {
	generator, err := gf.CreateGenerator(instance.Type)
	if err != nil {
		return nil, fmt.Errorf("failed to create generator for type %s: %w", instance.Type, err)
	}
	
	return generator.Generate(instance)
}