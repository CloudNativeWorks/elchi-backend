package scenario

import (
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// validateNestedFieldsWithContext validates nested field selections with context awareness
func validateNestedFieldsWithContext(comp models.ComponentInstance, compDef models.ComponentDefinition, context ValidationContext) []string {
	var errors []string

	for _, selectedField := range comp.SelectedFields {
		// Find the field definition
		var fieldDef *models.AvailableField
		for _, availableField := range compDef.AvailableFields {
			if availableField.Name == selectedField.FieldName {
				fieldDef = &availableField
				break
			}
		}

		if fieldDef == nil {
			continue
		}

		// Validate nested selections
		if fieldDef.Type == models.FieldTypeNestedChoice && selectedField.NestedSelection != nil {
			nestedErrors := validateNestedChoiceWithContext(comp, *fieldDef, *selectedField.NestedSelection, selectedField.FieldName, context)
			errors = append(errors, nestedErrors...)
		}

		// Validate conditional fields
		if fieldDef.Type == models.FieldTypeConditional && selectedField.NestedSelection != nil {
			conditionalErrors := validateConditionalFieldWithContext(comp, *fieldDef, *selectedField.NestedSelection, selectedField.FieldName, context)
			errors = append(errors, conditionalErrors...)
		}
	}

	return errors
}

// validateNestedChoiceWithContext validates nested choice field selection with context
func validateNestedChoiceWithContext(comp models.ComponentInstance, fieldDef models.AvailableField, selection models.NestedFieldSelection, fieldPath string, context ValidationContext) []string {
	var errors []string

	if fieldDef.NestedConfig == nil {
		return errors
	}

	// Check if selected choice exists
	var selectedChoice *models.ConditionalChoice
	for _, choice := range fieldDef.NestedConfig.Choices {
		if choice.Value == selection.SelectedChoice {
			selectedChoice = &choice
			break
		}
	}

	if selectedChoice == nil {
		errors = append(errors, fmt.Sprintf("Component %s: Invalid choice '%s' for field '%s'",
			comp.Name, selection.SelectedChoice, fieldPath))
		return errors
	}

	// Validate sub-fields with context
	subFieldErrors := validateSubFieldsWithContext(comp, selectedChoice.SubFields, selection.SubFields, fieldPath+"."+selection.SelectedChoice, context)
	errors = append(errors, subFieldErrors...)

	return errors
}

// validateConditionalFieldWithContext validates conditional field selection with context
func validateConditionalFieldWithContext(comp models.ComponentInstance, fieldDef models.AvailableField, selection models.NestedFieldSelection, fieldPath string, context ValidationContext) []string {
	var errors []string

	if fieldDef.NestedConfig == nil {
		return errors
	}

	// Similar to nested choice but may allow multiple selections if not mutually exclusive
	if fieldDef.NestedConfig.MutuallyExclusive {
		// Only one choice allowed
		var selectedChoice *models.ConditionalChoice
		for _, choice := range fieldDef.NestedConfig.Choices {
			if choice.Value == selection.SelectedChoice {
				selectedChoice = &choice
				break
			}
		}

		if selectedChoice == nil {
			errors = append(errors, fmt.Sprintf("Component %s: Invalid choice '%s' for field '%s'",
				comp.Name, selection.SelectedChoice, fieldPath))
			return errors
		}

		subFieldErrors := validateSubFieldsWithContext(comp, selectedChoice.SubFields, selection.SubFields, fieldPath+"."+selection.SelectedChoice, context)
		errors = append(errors, subFieldErrors...)
	}

	return errors
}

// validateSubFieldsWithContext validates sub-field selections with context awareness
func validateSubFieldsWithContext(comp models.ComponentInstance, availableSubFields []models.AvailableField, selectedSubFields []models.SelectedField, fieldPath string, context ValidationContext) []string {
	var errors []string

	// Create maps for quick lookup
	availableMap := make(map[string]models.AvailableField)
	for _, field := range availableSubFields {
		availableMap[field.Name] = field
	}

	selectedMap := make(map[string]models.SelectedField)
	for _, field := range selectedSubFields {
		selectedMap[field.FieldName] = field
	}

	// Check creation requirements - only for selected fields
	if context == ValidationContextCreation {
		for _, availableField := range availableSubFields {
			if availableField.RequiredForCreation {
				if _, exists := selectedMap[availableField.Name]; !exists {
					errors = append(errors, fmt.Sprintf("Component %s: Required sub-field '%s' is missing in '%s'",
						comp.Name, availableField.Name, fieldPath))
				}
			}
		}
	}

	// Validate selected sub-fields
	for _, selectedSubField := range selectedSubFields {
		availableField, exists := availableMap[selectedSubField.FieldName]
		if !exists {
			errors = append(errors, fmt.Sprintf("Component %s: Invalid sub-field '%s' in '%s'",
				comp.Name, selectedSubField.FieldName, fieldPath))
			continue
		}

		// Execution context validation for RequiredForExecution fields
		if context == ValidationContextExecution && availableField.RequiredForExecution {
			// Special handling for nested choice fields
			if availableField.Type == models.FieldTypeNestedChoice {
				// For nested choice, check if a choice is selected
				if selectedSubField.NestedSelection == nil || selectedSubField.NestedSelection.SelectedChoice == "" {
					errors = append(errors, fmt.Sprintf("Component %s: Sub-field '%s' is required for execution but no choice selected in '%s'",
						comp.Name, availableField.Label, fieldPath))
				}
			} else {
				// For regular fields, check if value is provided
				if selectedSubField.Value == nil || selectedSubField.Value == "" {
					errors = append(errors, fmt.Sprintf("Component %s: Sub-field '%s' is required for execution but has no value in '%s'",
						comp.Name, availableField.Label, fieldPath))
				}
			}
		}

		// Recursive validation for nested sub-fields
		if availableField.Type == models.FieldTypeNestedChoice && selectedSubField.NestedSelection != nil {
			nestedErrors := validateNestedChoiceWithContext(comp, availableField, *selectedSubField.NestedSelection, fieldPath+"."+selectedSubField.FieldName, context)
			errors = append(errors, nestedErrors...)
		}
	}

	return errors
}

// GetFlattenedSelectedFields flattens nested field selections for easier processing
func GetFlattenedSelectedFields(comp models.ComponentInstance) map[string]interface{} {
	flattened := make(map[string]interface{})

	for _, selectedField := range comp.SelectedFields {
		// Add top-level field
		flattened[selectedField.FieldName] = selectedField.Value

		// Add nested fields
		if selectedField.NestedSelection != nil {
			nestedPrefix := selectedField.FieldName + "." + selectedField.NestedSelection.SelectedChoice
			flattened[selectedField.FieldName+"._choice"] = selectedField.NestedSelection.SelectedChoice

			for _, subField := range selectedField.NestedSelection.SubFields {
				subFieldKey := nestedPrefix + "." + subField.FieldName
				flattened[subFieldKey] = subField.Value

				// Handle recursive nesting
				if subField.NestedSelection != nil {
					subNestedPrefix := subFieldKey + "." + subField.NestedSelection.SelectedChoice
					flattened[subFieldKey+"._choice"] = subField.NestedSelection.SelectedChoice

					for _, subSubField := range subField.NestedSelection.SubFields {
						flattened[subNestedPrefix+"."+subSubField.FieldName] = subSubField.Value
					}
				}
			}
		}
	}

	return flattened
}
