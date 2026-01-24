// Package generators provides resource generators for the scenario system,
// creating Envoy XDS configurations from scenario component definitions.
package generators

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/scenario/catalog"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// BaseGenerator provides common functionality for all component generators
type BaseGenerator struct {
	Project string
	Version string
	User    models.UserDetails
	Managed bool // Whether resources should be saved to database
}

// ComponentGenerator interface that all generators must implement
type ComponentGenerator interface {
	Generate(instance models.ComponentInstance) (any, error) // Can return single map or array of maps
	GetComponentType() string
	GetCollection() string
}

// NewBaseGenerator creates a new base generator
func NewBaseGenerator(project, version string, user models.UserDetails) *BaseGenerator {
	return &BaseGenerator{
		Project: project,
		Version: version,
		User:    user,
		Managed: true,
	}
}

// NewBaseGeneratorWithManaged creates a new base generator with managed setting
func NewBaseGeneratorWithManaged(project, version string, user models.UserDetails, managed bool) *BaseGenerator {
	return &BaseGenerator{
		Project: project,
		Version: version,
		User:    user,
		Managed: managed,
	}
}

// GenerateRandomString generates a random string for naming
func (bg *BaseGenerator) GenerateRandomString(length int) string {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		// Fallback to timestamp-based random on error
		return hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))[:length]
	}
	return hex.EncodeToString(bytes)[:length]
}

// BuildGeneralSection creates the general section for any component
func (bg *BaseGenerator) BuildGeneralSection(instance models.ComponentInstance, componentType, collection, canonicalName, gtype, category string) map[string]interface{} {
	general := map[string]interface{}{
		"name":           instance.Name,
		"version":        bg.Version, // This comes from execution request, not default
		"type":           componentType,
		"gtype":          gtype,
		"project":        bg.Project,
		"collection":     collection,
		"canonical_name": canonicalName,
		"category":       category,
		"metadata": map[string]interface{}{
			"from_template": true,
		},
		"permissions": map[string]interface{}{
			"users":  []string{},
			"groups": []string{},
		},
		"created_at": time.Now(),
		"updated_at": time.Now(),
	}

	return general
}

// BuildGeneralSectionWithManaged creates the general section for listener (with managed field)
func (bg *BaseGenerator) BuildGeneralSectionWithManaged(instance models.ComponentInstance, componentType, collection, canonicalName, gtype, category string, managed bool) map[string]interface{} {
	general := bg.BuildGeneralSection(instance, componentType, collection, canonicalName, gtype, category)
	general["managed"] = managed // Only add managed for listener
	return general
}

// GetFieldValue retrieves field value from selected fields
func (bg *BaseGenerator) GetFieldValue(selectedFields []models.SelectedField, fieldName string, defaultValue interface{}) interface{} {
	for _, field := range selectedFields {
		if field.FieldName == fieldName && field.Value != nil {
			return field.Value
		}
	}
	return defaultValue
}

// GetNestedFieldSelection retrieves nested field selection
func (bg *BaseGenerator) GetNestedFieldSelection(selectedFields []models.SelectedField, fieldName string) *models.NestedFieldSelection {
	for _, field := range selectedFields {
		if field.FieldName == fieldName && field.NestedSelection != nil {
			return field.NestedSelection
		}
	}
	return nil
}

// GetNestedFieldValue retrieves value from nested field path (e.g., "route_configuration.inline.name")
func (bg *BaseGenerator) GetNestedFieldValue(selectedFields []models.SelectedField, fieldPath string, defaultValue interface{}) interface{} {
	// Split path by dots: route_configuration.inline.name
	parts := strings.Split(fieldPath, ".")
	if len(parts) < 2 {
		return bg.GetFieldValue(selectedFields, fieldPath, defaultValue)
	}

	// Get root field nested selection
	rootFieldName := parts[0]
	nestedSelection := bg.GetNestedFieldSelection(selectedFields, rootFieldName)
	if nestedSelection == nil {
		return defaultValue
	}

	// Navigate through nested path
	return bg.getValueFromNestedPath(nestedSelection, parts[1:], defaultValue)
}

// getValueFromNestedPath navigates through nested selection path
func (bg *BaseGenerator) getValueFromNestedPath(selection *models.NestedFieldSelection, pathParts []string, defaultValue interface{}) interface{} {
	if len(pathParts) == 0 {
		return defaultValue
	}

	// If first part is choice value, continue with sub fields
	if pathParts[0] == selection.SelectedChoice {
		if len(pathParts) == 1 {
			return selection.SelectedChoice
		}
		// Look in sub fields
		fieldName := pathParts[1]
		for _, subField := range selection.SubFields {
			if subField.FieldName == fieldName {
				if len(pathParts) == 2 {
					return subField.Value
				}
				// Recursive nested selection
				if subField.NestedSelection != nil {
					return bg.getValueFromNestedPath(subField.NestedSelection, pathParts[2:], defaultValue)
				}
			}
		}
	}

	return defaultValue
}

// GetFieldValueWithComponentNameSupport retrieves field value with UseComponentName support
func (bg *BaseGenerator) GetFieldValueWithComponentNameSupport(instance models.ComponentInstance, fieldName string, defaultValue interface{}) interface{} {
	// Check if this field should use component name
	if fieldDef := bg.getFieldDefinition(instance.Type, fieldName); fieldDef != nil && fieldDef.UseComponentName {
		return instance.Name
	}

	// Standard field value lookup
	return bg.GetFieldValue(instance.SelectedFields, fieldName, defaultValue)
}

// BuildCompleteDocument builds the complete document with general and resource sections
func (bg *BaseGenerator) BuildCompleteDocument(general map[string]interface{}, resource interface{}) map[string]interface{} {
	return map[string]interface{}{
		"_id":     primitive.NewObjectID(),
		"general": general,
		"resource": map[string]interface{}{
			"version":  "1",
			"resource": resource, // Direct resource, let each generator decide array vs object
		},
	}
}

// BuildCompleteDocumentWithArray builds the complete document with array resource format (for listener, virtualhost)
func (bg *BaseGenerator) BuildCompleteDocumentWithArray(general map[string]interface{}, resource interface{}) map[string]interface{} {
	return map[string]interface{}{
		"_id":     primitive.NewObjectID(),
		"general": general,
		"resource": map[string]interface{}{
			"version":  "1",
			"resource": []interface{}{resource}, // Array format
		},
	}
}

// BuildNameWithSuffix builds name with random suffix for uniqueness
func (bg *BaseGenerator) BuildNameWithSuffix(baseName string, suffixLength int) string {
	suffix := bg.GenerateRandomString(suffixLength)
	return fmt.Sprintf("%s%s", baseName, suffix)
}

// BuildFilterChainName builds filter chain name from listener name
func (bg *BaseGenerator) BuildFilterChainName(listenerName string, suffixLength int) string {
	suffix := bg.GenerateRandomString(suffixLength)
	return fmt.Sprintf("%s-fc%s", listenerName, suffix)
}

// BuildFilterName builds filter name with proper three-part naming convention
// filterChainName already contains: listenerName6chars-fc6chars
// We append: -filter6chars
// Final format: listenername6chars-fc6chars-filter6chars
func (bg *BaseGenerator) BuildFilterName(listenerName, filterChainName, filterSuffix string) string {
	// filterChainName already has the format: listenerName6chars-fc6chars
	// Just append the filter suffix to complete the three-part name
	return fmt.Sprintf("%s-filter%s", filterChainName, filterSuffix)
}

// getFieldDefinition finds field definition from catalog for a component type and field name
func (bg *BaseGenerator) getFieldDefinition(componentType, fieldName string) *models.AvailableField {
	for _, compDef := range catalog.ComponentCatalog {
		if compDef.Name == componentType {
			for _, field := range compDef.AvailableFields {
				if field.Name == fieldName {
					return &field
				}
			}
		}
	}
	return nil
}

// AddRouteNames adds name field to routes array if missing
func (bg *BaseGenerator) AddRouteNames(routes interface{}, baseName string) interface{} {
	if routesArray, ok := routes.([]interface{}); ok {
		for i, route := range routesArray {
			if routeMap, ok := route.(map[string]interface{}); ok {
				// Add name if not present
				if _, hasName := routeMap["name"]; !hasName {
					routeMap["name"] = fmt.Sprintf("%s_route_%d", baseName, i)
				}
			}
		}
		return routesArray
	} else if routesMapArray, ok := routes.([]map[string]interface{}); ok {
		for i, routeMap := range routesMapArray {
			// Add name if not present
			if _, hasName := routeMap["name"]; !hasName {
				routeMap["name"] = fmt.Sprintf("%s_route_%d", baseName, i)
			}
		}
		return routesMapArray
	}
	return routes
}

// IsFieldSelected checks if a field is actually selected by the user (not just has a default)
func (bg *BaseGenerator) IsFieldSelected(selectedFields []models.SelectedField, fieldName string) bool {
	for _, field := range selectedFields {
		if field.FieldName == fieldName {
			return true // Field was explicitly selected by user
		}
	}
	return false // Field was not selected by user
}

// GetFieldValueIfSelected gets field value only if user selected it, otherwise returns nil
func (bg *BaseGenerator) GetFieldValueIfSelected(selectedFields []models.SelectedField, fieldName string) interface{} {
	for _, field := range selectedFields {
		if field.FieldName == fieldName && field.Value != nil {
			return field.Value
		}
	}
	return nil // Field not selected or has no value
}
