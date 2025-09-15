package scenario

import (
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/scenario/catalog"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/scenario/generators"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// GenerateResourceFromComponent generates Envoy resource JSON from component instance using new generator system
func GenerateResourceFromComponent(componentInstance models.ComponentInstance, project, version string, user models.UserDetails) (any, error) {
	// Create generator factory
	factory := generators.NewGeneratorFactory(project, version, user)

	// Generate component using appropriate generator
	return factory.GenerateComponent(componentInstance)
}

// GenerateEnvoyResourceDocument generates Envoy resource in database format using new generator system
func GenerateEnvoyResourceDocument(componentInstance models.ComponentInstance, reqDetails models.RequestDetails, version string) (any, error) {
	return GenerateEnvoyResourceDocumentWithMapping(componentInstance, reqDetails, version, nil, true) // Default managed to true
}

// GenerateEnvoyResourceDocumentWithMapping generates Envoy resource with component type mapping for listener network filters
// Returns single document for components that generate one resource, multiple documents for listeners with TLS+HTTP
func GenerateEnvoyResourceDocumentWithMapping(componentInstance models.ComponentInstance, reqDetails models.RequestDetails, version string, componentTypeMap map[string]string, managed bool) (any, error) {
	// Create generator factory with component mapping and managed setting
	factory := generators.NewGeneratorFactoryWithMapping(reqDetails.Project, version, reqDetails.User, componentTypeMap, managed)

	// Generate component using appropriate generator
	result, err := factory.GenerateComponent(componentInstance)
	if err != nil {
		return nil, err
	}

	// All generators now return single document
	document, ok := result.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected result type from generator: %T", result)
	}

	// Update general section
	if general, ok := document["general"].(map[string]interface{}); ok {
		updateGeneralSection(general)
	}

	return document, nil
}

// updateGeneralSection updates the general section with standard metadata
func updateGeneralSection(general map[string]interface{}) {
	// Update permissions to use only empty arrays since permissions were removed
	general["permissions"] = map[string]interface{}{
		"users":  []string{},
		"groups": []string{},
	}

	// Update metadata
	general["metadata"] = map[string]interface{}{
		"from_template":         true,
		"created_from_scenario": true,
	}
}

// GenerateScenarioResources generates all resources from a scenario using new generator system
func GenerateScenarioResources(scenario models.Scenario, executionRequest models.ExecuteScenarioRequest, project, version string, user models.UserDetails) ([]map[string]interface{}, error) {
	var resources []map[string]interface{}

	// If executionRequest provides component instances with values, use those
	// Otherwise use the scenario's component definitions
	componentsToProcess := scenario.Components
	if len(executionRequest.Components) > 0 {
		componentsToProcess = executionRequest.Components
	}

	// Create generator factory
	factory := generators.NewGeneratorFactory(project, version, user)

	for _, componentInstance := range componentsToProcess {
		result, err := factory.GenerateComponent(componentInstance)
		if err != nil {
			return nil, fmt.Errorf("failed to generate resource for component %s: %w", componentInstance.Name, err)
		}

		// All generators now return single document
		document, ok := result.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("unexpected result type from generator for component %s: %T", componentInstance.Name, result)
		}

		resources = append(resources, document)
	}

	return resources, nil
}

// ValidateComponentInstance validates component instance for scenario creation (lenient)
func ValidateComponentInstance(componentInstance models.ComponentInstance) error {
	// Find component definition
	var componentDef *models.ComponentDefinition
	for _, component := range catalog.ComponentCatalog {
		if component.Name == componentInstance.Type {
			componentDef = &component
			break
		}
	}

	if componentDef == nil {
		return fmt.Errorf("component definition not found for type: %s", componentInstance.Type)
	}

	// Create a map of selected fields for quick lookup
	selectedFieldMap := make(map[string]models.SelectedField)
	for _, selectedField := range componentInstance.SelectedFields {
		selectedFieldMap[selectedField.FieldName] = selectedField
	}

	// For scenario creation, check if RequiredForCreation fields are selected
	for _, availableField := range componentDef.AvailableFields {
		if availableField.RequiredForCreation {
			_, exists := selectedFieldMap[availableField.Name]
			if !exists {
				return fmt.Errorf("field %s is required for scenario creation but not selected", availableField.Name)
			}
		}
	}

	// Note: Cluster endpoint/EDS choice validation now handled by nested choice structure

	return nil
}

// ValidateComponentInstanceForExecution validates component instance for execution (stricter validation)
func ValidateComponentInstanceForExecution(componentInstance models.ComponentInstance) error {
	// For single component validation, use a slice with just this component
	return ValidateComponentInstanceForExecutionWithContext(componentInstance, []models.ComponentInstance{componentInstance})
}

// ValidateComponentInstanceForExecutionWithContext validates component instance for execution with full scenario context
func ValidateComponentInstanceForExecutionWithContext(componentInstance models.ComponentInstance, allComponents []models.ComponentInstance) error {
	// Find component definition
	var componentDef *models.ComponentDefinition
	for _, component := range catalog.ComponentCatalog {
		if component.Name == componentInstance.Type {
			componentDef = &component
			break
		}
	}

	if componentDef == nil {
		return fmt.Errorf("component definition not found for type: %s", componentInstance.Type)
	}

	// Create a map of selected fields for quick lookup
	selectedFieldMap := make(map[string]models.SelectedField)
	for _, selectedField := range componentInstance.SelectedFields {
		selectedFieldMap[selectedField.FieldName] = selectedField
	}

	// For execution, check that all SELECTED fields have values
	// We only validate fields that were selected in the scenario
	for _, availableField := range componentDef.AvailableFields {
		selectedField, exists := selectedFieldMap[availableField.Name]

		// Only validate fields that are selected in the scenario
		if exists {
			// If field is selected and RequiredForExecution, it must have value
			if availableField.RequiredForExecution {
				// For nested/conditional fields, check nested_selection instead of value
				if availableField.Type == models.FieldTypeNestedChoice || availableField.Type == models.FieldTypeConditional {
					if selectedField.NestedSelection == nil {
						return fmt.Errorf("required nested field %s must have a nested selection for execution", availableField.Name)
					}
				} else {
					// Regular fields must have value (either from user input or default)
					if selectedField.Value == nil && availableField.DefaultValue == nil {
						return fmt.Errorf("required field %s must have a value for execution", availableField.Name)
					}
				}
			}

			// Check user-marked required fields (only for selected fields)
			if selectedField.Required {
				// For nested/conditional fields, check nested_selection instead of value
				if availableField.Type == models.FieldTypeNestedChoice || availableField.Type == models.FieldTypeConditional {
					if selectedField.NestedSelection == nil {
						return fmt.Errorf("required nested field %s must have a nested selection for execution", availableField.Name)
					}
				} else {
					// Regular fields must have value
					if selectedField.Value == nil && availableField.DefaultValue == nil {
						return fmt.Errorf("required field %s must have a value for execution", availableField.Name)
					}
				}
			}

			// Apply field-level validation rules from catalog using execution context
			fieldValidator := NewFieldValidationEngineForExecution(allComponents)
			fieldErrors := fieldValidator.ValidateField(componentInstance, availableField, selectedField)
			if len(fieldErrors) > 0 {
				// Return first field validation error
				return fmt.Errorf("%s", fieldErrors[0])
			}
		}
	}

	return nil
}
