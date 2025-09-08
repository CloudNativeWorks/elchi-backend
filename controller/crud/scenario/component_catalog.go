package scenario

import (
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/scenario/catalog"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// GetComponentCatalog returns all available component definitions sorted by priority
func (t *AppHandler) GetComponentCatalog() (*models.ComponentCatalogResponse, error) {
	return &models.ComponentCatalogResponse{
		Components: catalog.GetComponentCatalogSorted(), // Use priority-sorted catalog
	}, nil
}

// GetComponentDefinitionByType returns specific component definition
func (t *AppHandler) GetComponentDefinitionByType(componentType string) (*models.ComponentDefinition, error) {
	def, err := catalog.GetComponentDefinitionByType(componentType)
	if err != nil {
		return nil, err
	}
	if def == nil {
		return nil, fmt.Errorf("component definition not found for type: %s", componentType)
	}
	return def, nil
}

// GetComponents returns all available component definitions
func (t *AppHandler) GetComponents() []models.ComponentDefinition {
	return catalog.GetComponentCatalog()
}

// ValidateComponentRules validates if component combination follows rules (creation context)
func (t *AppHandler) ValidateComponentRules(components []models.ComponentInstance) []string {
	return t.ValidateComponentRulesWithContext(components, ValidationContextCreation)
}

// ValidateComponentRulesWithContext validates component rules with context awareness
func (t *AppHandler) ValidateComponentRulesWithContext(components []models.ComponentInstance, context ValidationContext) []string {
	var errors []string
	componentCounts := make(map[string]int)
	componentNames := make(map[string][]string)

	// Count components and collect names
	for _, comp := range components {
		componentCounts[comp.Type]++
		componentNames[comp.Type] = append(componentNames[comp.Type], comp.Name)
	}

	// Note: Special validations are now handled by component-level validation rules

	// Apply component-level validation rules based on context
	for _, compDef := range catalog.ComponentCatalog {
		count := componentCounts[compDef.Name]
		if count > 0 {
			// Apply context-specific validation rules
			var rulesToApply []string
			switch context {
			case ValidationContextCreation:
				rulesToApply = compDef.Rules.ValidationRulesForCreation
			case ValidationContextExecution:
				rulesToApply = append(compDef.Rules.ValidationRulesForCreation, compDef.Rules.ValidationRulesForExecution...)
			}

			for _, rule := range rulesToApply {
				ruleErrors := t.applyComponentValidationRule(rule, components, componentCounts)
				errors = append(errors, ruleErrors...)
			}
		}
	}

	// Check each component definition's rules
	for _, compDef := range catalog.ComponentCatalog {
		count := componentCounts[compDef.Name]

		// Check min/max counts
		if count < compDef.Rules.MinCount {
			errors = append(errors, fmt.Sprintf("Component %s requires at least %d instances, got %d",
				compDef.Label, compDef.Rules.MinCount, count))
		}
		if compDef.Rules.MaxCount > 0 && count > compDef.Rules.MaxCount {
			errors = append(errors, fmt.Sprintf("Component %s allows maximum %d instances, got %d",
				compDef.Label, compDef.Rules.MaxCount, count))
		}

		if count > 0 {
			// Check required with (skip this for listener since we handle it specially above)
			if compDef.Name != "listener" {
				for _, requiredType := range compDef.Rules.RequiredWith {
					if componentCounts[requiredType] == 0 {
						requiredLabel := getComponentLabel(requiredType)
						errors = append(errors, fmt.Sprintf("Component %s requires %s to be present",
							compDef.Label, requiredLabel))
					}
				}
			}

			// Check conflicts
			for _, conflictType := range compDef.Rules.ConflictWith {
				if componentCounts[conflictType] > 0 {
					conflictLabel := getComponentLabel(conflictType)
					errors = append(errors, fmt.Sprintf("Component %s conflicts with %s - cannot be used together",
						compDef.Label, conflictLabel))
				}
			}

			// Check field-level rules for each component instance
			for _, comp := range components {
				if comp.Type == compDef.Name {
					fieldErrors := t.validateComponentFieldRulesWithValidationContext(comp, compDef, components, context)
					errors = append(errors, fieldErrors...)

					// Validate nested fields with context
					nestedErrors := validateNestedFieldsWithContext(comp, compDef, context)
					errors = append(errors, nestedErrors...)
				}
			}
		}
	}

	return errors
}

// validateComponentFieldRulesWithValidationContext validates field-level rules with context awareness
func (t *AppHandler) validateComponentFieldRulesWithValidationContext(comp models.ComponentInstance, compDef models.ComponentDefinition, allComponents []models.ComponentInstance, context ValidationContext) []string {
	var errors []string

	// Create field validation engine with context-aware validation
	var fieldValidator *FieldValidationEngine
	if context == ValidationContextExecution {
		fieldValidator = NewFieldValidationEngineForExecution(allComponents)
	} else {
		fieldValidator = NewFieldValidationEngine(allComponents)
	}

	// Create map of selected fields for legacy validation
	selectedFields := make(map[string]models.SelectedField)
	for _, field := range comp.SelectedFields {
		selectedFields[field.FieldName] = field
	}

	// Apply field-level validation rules from catalog
	// Only validate selected fields - never validate unselected fields
	for _, availableField := range compDef.AvailableFields {
		for _, selectedField := range comp.SelectedFields {
			if selectedField.FieldName == availableField.Name {
				// Apply ValidationRules from catalog
				fieldErrors := fieldValidator.ValidateField(comp, availableField, selectedField)
				errors = append(errors, fieldErrors...)
				
				// Additional validation for execution context and RequiredForExecution fields
				if context == ValidationContextExecution && availableField.RequiredForExecution {
					// Special handling for nested choice fields
					if availableField.Type == models.FieldTypeNestedChoice {
						// For nested choice, check if a choice is selected
						if selectedField.NestedSelection == nil || selectedField.NestedSelection.SelectedChoice == "" {
							errors = append(errors, fmt.Sprintf("Component %s: Field '%s' is required for execution but no choice selected", 
								comp.Name, availableField.Label))
						}
						// Individual sub-field validation is handled in nested validation
					} else {
						// For regular fields, check if value is provided
						if selectedField.Value == nil || selectedField.Value == "" {
							errors = append(errors, fmt.Sprintf("Component %s: Field '%s' is required for execution but has no value", 
								comp.Name, availableField.Label))
						}
					}
				}
				break
			}
		}
	}

	// Check field conflicts
	for _, conflict := range compDef.Rules.FieldConflicts {
		selectedConflictFields := []string{}
		for _, fieldName := range conflict.Fields {
			if _, exists := selectedFields[fieldName]; exists {
				selectedConflictFields = append(selectedConflictFields, fieldName)
			}
		}

		if len(selectedConflictFields) > 1 {
			if conflict.Message != "" {
				errors = append(errors, fmt.Sprintf("Component %s: %s (selected: %v)",
					comp.Name, conflict.Message, selectedConflictFields))
			} else {
				errors = append(errors, fmt.Sprintf("Component %s: Fields %v cannot be used together",
					comp.Name, selectedConflictFields))
			}
		}
	}

	// Check field requirements
	for _, requirement := range compDef.Rules.FieldRequires {
		if _, exists := selectedFields[requirement.Field]; exists {
			// Field is selected, check if required fields are present
			for _, requiredField := range requirement.RequiredFields {
				if _, reqExists := selectedFields[requiredField]; !reqExists {
					if requirement.Message != "" {
						errors = append(errors, fmt.Sprintf("Component %s: %s", comp.Name, requirement.Message))
					} else {
						errors = append(errors, fmt.Sprintf("Component %s: Field %s requires field %s to be selected",
							comp.Name, requirement.Field, requiredField))
					}
					break
				}
			}
		}
	}

	// Special validation for cluster type-specific fields
	// Only validate if this is a cluster component and type field has a value
	if comp.Type == "cluster" {
		_, hasEDS := selectedFields["eds_service_name"]
		_, hasEndpoints := selectedFields["endpoints"]

		typeField, typeExists := selectedFields["type"]
		if typeExists && typeField.Value != nil {
			typeValue := typeField.Value.(string)

			// Check if eds_service_name is selected with wrong type
			if hasEDS && typeValue != "EDS" {
				errors = append(errors, fmt.Sprintf("Component %s: EDS service name can only be used when cluster type is 'EDS' (current type: %s)", comp.Name, typeValue))
			}

			// Check if endpoints is selected with wrong type (endpoints work with STATIC, STRICT_DNS, LOGICAL_DNS - NOT with EDS)
			if hasEndpoints && typeValue == "EDS" {
				errors = append(errors, fmt.Sprintf("Component %s: Static endpoints cannot be used when cluster type is 'EDS' (current type: %s)", comp.Name, typeValue))
			}
		}
	}

	return errors
}

// validateClusterEndpointRelationship validates cluster internal configuration and endpoint component references
func (t *AppHandler) validateClusterEndpointRelationship(components []models.ComponentInstance) []string {
	var errors []string

	// Collect cluster info
	clusterNames := make(map[string]bool)        // cluster_name -> exists
	edsServiceNames := make(map[string]bool)     // EDS service names that clusters reference
	endpointServiceNames := make(map[string]bool) // Service names that endpoint components provide

	for _, comp := range components {
		switch comp.Type {
		case "cluster":
			clusterNames[comp.Name] = true

			// Validate that cluster has endpoint discovery configuration
			hasEndpointConfig := false
			for _, field := range comp.SelectedFields {
				if field.FieldName == "endpoint_discovery_config" && field.NestedSelection != nil {
					hasEndpointConfig = true
					
					// If using EDS, collect the eds_service_name
					if field.NestedSelection.SelectedChoice == "eds" {
						for _, subField := range field.NestedSelection.SubFields {
							if subField.FieldName == "eds_service_name" && subField.Value != nil {
								edsServiceName := subField.Value.(string)
								edsServiceNames[edsServiceName] = true
								break
							}
						}
					}
					break
				}
			}

			if !hasEndpointConfig {
				errors = append(errors, fmt.Sprintf("Cluster '%s' must have endpoint discovery configuration (EDS or static endpoints)", comp.Name))
			}

		case "endpoint":
			// Get cluster_name from endpoint component (this is the service name for EDS)
			for _, field := range comp.SelectedFields {
				if field.FieldName == "cluster_name" && field.Value != nil {
					serviceName := field.Value.(string)
					endpointServiceNames[serviceName] = true
					break
				}
			}
		}
	}

	// Note: We don't check for orphaned EDS references because endpoint components
	// can exist in the database even if they're not in the current scenario

	return errors
}

// getComponentLabel returns the label for a component type
func getComponentLabel(componentType string) string {
	return catalog.GetComponentLabel(componentType)
}

// applyComponentValidationRule applies a component-level validation rule
func (t *AppHandler) applyComponentValidationRule(rule string, components []models.ComponentInstance, componentCounts map[string]int) []string {
	var errors []string

	switch rule {
	case "cluster_endpoint_relationship":
		// Validate cluster-endpoint compatibility
		if componentCounts["cluster"] > 0 {
			clusterEndpointErrors := t.validateClusterEndpointRelationship(components)
			errors = append(errors, clusterEndpointErrors...)
		}

	case "listener_filter_requirement":
		// Validate that listener has either HCM or TCP proxy
		listenerCount := componentCounts["listener"]
		hcmCount := componentCounts["http_connection_manager"]
		tcpProxyCount := componentCounts["tcp_proxy"]

		if listenerCount > 0 {
			if hcmCount == 0 && tcpProxyCount == 0 {
				errors = append(errors, "Scenario must contain at least one network filter component (HTTP Connection Manager or TCP Proxy) when using listeners")
			}
			if hcmCount > 0 && tcpProxyCount > 0 {
				errors = append(errors, "Scenario cannot have both HTTP Connection Manager and TCP Proxy components - please choose one filter type")
			}
		}

	default:
		// Unknown component validation rule
		fmt.Printf("⚠️  Unknown component validation rule: %s\n", rule)
	}

	return errors
}
