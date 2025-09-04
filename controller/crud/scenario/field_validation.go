package scenario

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// ValidationContext defines when validation is being performed
type ValidationContext string

const (
	ValidationContextCreation  ValidationContext = "creation"  // Scenario creation - lenient
	ValidationContextExecution ValidationContext = "execution" // Scenario execution - strict
)

// FieldValidationEngine handles field-level validation rules
type FieldValidationEngine struct {
	scenarios []models.ComponentInstance // All components in scenario for cross-component validation
	context   ValidationContext          // When validation is being performed
}

// NewFieldValidationEngine creates a new field validation engine for creation context
func NewFieldValidationEngine(scenarios []models.ComponentInstance) *FieldValidationEngine {
	return &FieldValidationEngine{
		scenarios: scenarios,
		context:   ValidationContextCreation, // Default to creation context
	}
}

// NewFieldValidationEngineForExecution creates a new field validation engine for execution context
func NewFieldValidationEngineForExecution(scenarios []models.ComponentInstance) *FieldValidationEngine {
	return &FieldValidationEngine{
		scenarios: scenarios,
		context:   ValidationContextExecution,
	}
}

// ValidateField validates a field according to its validation rules
func (fve *FieldValidationEngine) ValidateField(comp models.ComponentInstance, field models.AvailableField, selectedField models.SelectedField) []string {
	var errors []string
	
	for _, rule := range field.ValidationRules {
		ruleErrors := fve.applyValidationRule(comp, field, selectedField, rule)
		errors = append(errors, ruleErrors...)
	}
	
	// Validate array items if field has ArraySchema with Properties
	if field.Type == models.FieldTypeArray && field.ArraySchema != nil && field.ArraySchema.Properties != nil {
		arrayErrors := fve.validateArrayItems(comp, field, selectedField)
		errors = append(errors, arrayErrors...)
	}
	
	return errors
}

// validateArrayItems validates items in an array field with ArraySchema.Properties
func (fve *FieldValidationEngine) validateArrayItems(comp models.ComponentInstance, field models.AvailableField, selectedField models.SelectedField) []string {
	var errors []string
	
	// Get array value
	if selectedField.Value == nil {
		return errors
	}
	
	// Convert value to array
	var arrayItems []any
	switch v := selectedField.Value.(type) {
	case []any:
		arrayItems = v
	case []map[string]any:
		for _, item := range v {
			arrayItems = append(arrayItems, item)
		}
	default:
		return errors
	}
	
	// Validate each array item
	for i, item := range arrayItems {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		
		// Check required properties in array items
		for propName, propDef := range field.ArraySchema.Properties {
			value, exists := itemMap[propName]
			
			// Check if required field exists and has value
			if propDef.Required {
				if !exists {
					// Check context for execution vs creation
					if fve.context == ValidationContextExecution {
						errors = append(errors, fmt.Sprintf("Component %s: Array item %d in field '%s' is missing required property '%s'",
							comp.Name, i+1, field.Label, propDef.Label))
					}
				} else if fve.context == ValidationContextExecution && (value == nil || value == "") {
					errors = append(errors, fmt.Sprintf("Component %s: Array item %d in field '%s' has empty required property '%s'",
						comp.Name, i+1, field.Label, propDef.Label))
				}
			}
			
			// Apply validation rules if property exists
			if exists && value != nil {
				for _, rule := range propDef.ValidationRules {
					if rule == "duration" && !fve.isValidDuration(value) {
						errors = append(errors, fmt.Sprintf("Component %s: Array item %d property '%s' must be a valid duration (e.g., '10s', '5m')",
							comp.Name, i+1, propDef.Label))
					}
					// Add more validation rules as needed
				}
			}
		}
	}
	
	return errors
}

// applyValidationRule applies a single validation rule
func (fve *FieldValidationEngine) applyValidationRule(comp models.ComponentInstance, field models.AvailableField, selectedField models.SelectedField, rule string) []string {
	var errors []string
	
	switch {
	case rule == "required":
		// Required validation depends on context
		switch fve.context {
		case ValidationContextExecution:
			// Execution: field must have value or nested selection
			if field.Type == models.FieldTypeNestedChoice || field.Type == models.FieldTypeConditional {
				// For nested fields, check if a choice is selected
				if selectedField.NestedSelection == nil || selectedField.NestedSelection.SelectedChoice == "" {
					errors = append(errors, fmt.Sprintf("Component %s: Field '%s' is required for execution but no choice selected", comp.Name, field.Name))
				}
			} else {
				// For regular fields, check if value is provided
				if selectedField.Value == nil || selectedField.Value == "" {
					errors = append(errors, fmt.Sprintf("Component %s: Field '%s' is required for execution", comp.Name, field.Name))
				}
			}
		case ValidationContextCreation:
			// Creation: required fields just need to be selected, not filled (handled by DefaultRequired)
		}
		
	case rule == "unique":
		if !fve.isFieldValueUnique(comp, field, selectedField) {
			errors = append(errors, fmt.Sprintf("Component %s: Field '%s' value must be unique across components", comp.Name, field.Name))
		}
		
	case rule == "ipv4":
		if !fve.isValidIPv4(selectedField.Value) {
			errors = append(errors, fmt.Sprintf("Component %s: Field '%s' must be a valid IPv4 address", comp.Name, field.Name))
		}
		
	case rule == "port":
		if !fve.isValidPort(selectedField.Value) {
			errors = append(errors, fmt.Sprintf("Component %s: Field '%s' must be a valid port number (1-65535)", comp.Name, field.Name))
		}
		
	case rule == "duration":
		if !fve.isValidDuration(selectedField.Value) {
			errors = append(errors, fmt.Sprintf("Component %s: Field '%s' must be a valid duration (e.g., '10s', '5m')", comp.Name, field.Name))
		}
		
	case strings.HasPrefix(rule, "min_length:"):
		minLength := fve.extractMinLength(rule)
		if !fve.hasMinLength(selectedField.Value, minLength) {
			errors = append(errors, fmt.Sprintf("Component %s: Field '%s' must have at least %d characters", comp.Name, field.Name, minLength))
		}
		
	case strings.HasPrefix(rule, "min_length_execution:"):
		// Min length validation that only applies during execution
		switch fve.context {
		case ValidationContextExecution:
			minLength := fve.extractMinLength(rule)
			if !fve.hasMinLength(selectedField.Value, minLength) {
				errors = append(errors, fmt.Sprintf("Component %s: Field '%s' must have at least %d items for execution", comp.Name, field.Name, minLength))
			}
		case ValidationContextCreation:
			// Skip validation during scenario creation
		}
		
	case rule == "required_if_eds":
		// Conditional requirement validation
		switch fve.context {
		case ValidationContextExecution:
			if fve.isEDSCluster(comp) && (selectedField.Value == nil || selectedField.Value == "") {
				errors = append(errors, fmt.Sprintf("Component %s: Field '%s' is required when cluster type is 'EDS'", comp.Name, field.Name))
			}
		case ValidationContextCreation:
			// Creation: conditional requirements not enforced
		}
		
	case rule == "cluster_exists":
		// Cross-component reference validation
		switch fve.context {
		case ValidationContextExecution:
			if selectedField.Value != nil && !fve.clusterExists(selectedField.Value) {
				errors = append(errors, fmt.Sprintf("Component %s: Cluster '%v' does not exist in scenario", comp.Name, selectedField.Value))
			}
		case ValidationContextCreation:
			// Creation: cross-references not validated yet
		}
		
	case rule == "route_exists":
		// Cross-component route reference validation
		switch fve.context {
		case ValidationContextExecution:
			if selectedField.Value != nil && !fve.routeExists(selectedField.Value) {
				errors = append(errors, fmt.Sprintf("Component %s: Route '%v' does not exist in scenario", comp.Name, selectedField.Value))
			}
		case ValidationContextCreation:
			// Creation: cross-references not validated yet
		}
		
	case rule == "array_of_endpoints":
		if !fve.isValidEndpointsArray(selectedField.Value) {
			errors = append(errors, fmt.Sprintf("Component %s: Field '%s' must be a valid array of endpoints", comp.Name, field.Name))
		}
		
	case rule == "domains":
		if !fve.isValidDomainsArray(selectedField.Value) {
			errors = append(errors, fmt.Sprintf("Component %s: Field '%s' must be a valid array of domain names", comp.Name, field.Name))
		}
		
	case rule == "routes_array":
		if !fve.isValidRoutesArray(selectedField.Value) {
			errors = append(errors, fmt.Sprintf("Component %s: Field '%s' must be a valid array of routes", comp.Name, field.Name))
		}
		
	case rule == "virtual_hosts_array":
		if !fve.isValidVirtualHostsArray(selectedField.Value) {
			errors = append(errors, fmt.Sprintf("Component %s: Field '%s' must be a valid array of virtual hosts", comp.Name, field.Name))
		}
		
	case rule == "vhds_config":
		if !fve.isValidVHDSConfig(selectedField.Value) {
			errors = append(errors, fmt.Sprintf("Component %s: Field '%s' must be a valid VHDS configuration", comp.Name, field.Name))
		}
		
	case rule == "listener_must_have_network_filter":
		// Structural validation - applies to both contexts but differently
		switch fve.context {
		case ValidationContextCreation:
			// Creation: Check if scenario has network filter components (HCM or TCP Proxy)
			if !fve.scenarioHasNetworkFilterComponents() {
				errors = append(errors, "To create listeners, add at least one network filter component (HTTP Connection Manager or TCP Proxy) to your scenario")
			}
		case ValidationContextExecution:
			// Execution: Check if this listener instance has network filters configured
			if !fve.listenerHasNetworkFilter(comp) {
				errors = append(errors, fmt.Sprintf("Listener '%s' must have at least one network filter selected", comp.Name))
			}
		}
		
	case rule == "hcm_must_have_router_filter":
		// This validation rule is deprecated - users can select filters via API or create new ones
		// HTTP filters are now optional and can be managed flexibly
		// No validation needed

	case rule == "required_router_filter":
		// HTTP filters must include router filter for scenario execution
		switch fve.context {
		case ValidationContextExecution:
			if !fve.httpFiltersIncludeRouter(selectedField.Value) {
				errors = append(errors, fmt.Sprintf("Component %s: HTTP filters must include 'router_filter' for scenario execution", comp.Name))
			}
		case ValidationContextCreation:
			// Creation: router filter requirement not enforced yet
		}
		
	case rule == "cluster_type_endpoint_consistency":
		// Validate consistency between cluster type and endpoint discovery config
		if comp.Type == "cluster" {
			typeConsistencyErrors := fve.validateClusterTypeEndpointConsistency(comp)
			errors = append(errors, typeConsistencyErrors...)
		}
		
	default:
		// Unknown rule - log but don't fail
		fmt.Printf("⚠️  Unknown validation rule '%s' for field '%s' in component '%s'\n", rule, field.Name, comp.Name)
	}
	
	return errors
}

// validateClusterTypeEndpointConsistency validates type and endpoint_discovery_config consistency
func (fve *FieldValidationEngine) validateClusterTypeEndpointConsistency(comp models.ComponentInstance) []string {
	var errors []string
	
	// Find type and endpoint_discovery_config fields
	var typeValue string
	var endpointConfigSelection *models.NestedFieldSelection
	
	for _, selectedField := range comp.SelectedFields {
		if selectedField.FieldName == "type" && selectedField.Value != nil {
			typeValue = fmt.Sprint(selectedField.Value)
		}
		if selectedField.FieldName == "endpoint_discovery_config" && selectedField.NestedSelection != nil {
			endpointConfigSelection = selectedField.NestedSelection
		}
	}
	
	// If both fields are selected, validate consistency
	if typeValue != "" && endpointConfigSelection != nil {
		selectedChoice := endpointConfigSelection.SelectedChoice
		
		if typeValue == "EDS" && selectedChoice != "eds" {
			errors = append(errors, fmt.Sprintf("Component %s: Cluster type 'EDS' requires endpoint discovery config choice 'eds', but '%s' is selected", 
				comp.Name, selectedChoice))
		}
		
		if typeValue != "EDS" && selectedChoice == "eds" {
			errors = append(errors, fmt.Sprintf("Component %s: Endpoint discovery config choice 'eds' can only be used with cluster type 'EDS', but type is '%s'", 
				comp.Name, typeValue))
		}
		
		if (typeValue == "STATIC" || typeValue == "STRICT_DNS" || typeValue == "LOGICAL_DNS") && selectedChoice != "static_endpoints" {
			errors = append(errors, fmt.Sprintf("Component %s: Cluster type '%s' requires endpoint discovery config choice 'static_endpoints', but '%s' is selected", 
				comp.Name, typeValue, selectedChoice))
		}
	}
	
	return errors
}

// Validation helper functions

func (fve *FieldValidationEngine) isFieldValueUnique(comp models.ComponentInstance, field models.AvailableField, selectedField models.SelectedField) bool {
	if selectedField.Value == nil {
		return true
	}
	
	valueStr := fmt.Sprint(selectedField.Value)
	
	for _, otherComp := range fve.scenarios {
		if otherComp.Name == comp.Name {
			continue // Skip same component
		}
		
		// Check if other component has same field with same value
		for _, otherField := range otherComp.SelectedFields {
			if otherField.FieldName == field.Name && otherField.Value != nil {
				otherValueStr := fmt.Sprint(otherField.Value)
				if valueStr == otherValueStr {
					return false
				}
			}
		}
	}
	
	return true
}

func (fve *FieldValidationEngine) isValidIPv4(value interface{}) bool {
	if value == nil {
		return true // nil is valid (optional field)
	}
	
	ip := fmt.Sprint(value)
	return net.ParseIP(ip) != nil && strings.Contains(ip, ".")
}

func (fve *FieldValidationEngine) isValidPort(value interface{}) bool {
	if value == nil {
		return true
	}
	
	var port int
	switch v := value.(type) {
	case int:
		port = v
	case float64:
		port = int(v)
	case string:
		var err error
		port, err = strconv.Atoi(v)
		if err != nil {
			return false
		}
	default:
		return false
	}
	
	return port >= 1 && port <= 65535
}

func (fve *FieldValidationEngine) isValidDuration(value interface{}) bool {
	if value == nil {
		return true
	}
	
	durationStr := fmt.Sprint(value)
	// Only accept seconds (s) with optional decimal values (e.g., 1s, 1.5s, 0.1s)
	durationRegex := regexp.MustCompile(`^\d+(\.\d+)?s$`)
	return durationRegex.MatchString(durationStr)
}

func (fve *FieldValidationEngine) extractMinLength(rule string) int {
	parts := strings.Split(rule, ":")
	if len(parts) != 2 {
		return 0
	}
	
	minLength, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0
	}
	
	return minLength
}

func (fve *FieldValidationEngine) hasMinLength(value interface{}, minLength int) bool {
	if value == nil {
		return false
	}
	
	valueStr := fmt.Sprint(value)
	return len(valueStr) >= minLength
}

func (fve *FieldValidationEngine) isEDSCluster(comp models.ComponentInstance) bool {
	if comp.Type != "cluster" {
		return false
	}
	
	for _, field := range comp.SelectedFields {
		if field.FieldName == "type" && field.Value != nil {
			return fmt.Sprint(field.Value) == "EDS"
		}
	}
	
	return false
}

func (fve *FieldValidationEngine) clusterExists(clusterName interface{}) bool {
	if clusterName == nil {
		return true
	}
	
	clusterNameStr := fmt.Sprint(clusterName)
	
	for _, comp := range fve.scenarios {
		if comp.Type == "cluster" {
			for _, field := range comp.SelectedFields {
				if field.FieldName == "name" && field.Value != nil {
					if fmt.Sprint(field.Value) == clusterNameStr {
						return true
					}
				}
			}
		}
	}
	
	return false
}

func (fve *FieldValidationEngine) isValidEndpointsArray(value interface{}) bool {
	if value == nil {
		return true
	}
	
	// Check if it's an array
	switch v := value.(type) {
	case []interface{}:
		// Valid array format
		return len(v) > 0
	case string:
		// Could be JSON string - basic check
		return strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]")
	default:
		return false
	}
}

func (fve *FieldValidationEngine) isValidDomainsArray(value interface{}) bool {
	if value == nil {
		return true
	}
	
	switch v := value.(type) {
	case []interface{}:
		return len(v) > 0
	case []string:
		return len(v) > 0
	case string:
		return strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") || v != ""
	default:
		return false
	}
}

func (fve *FieldValidationEngine) isValidRoutesArray(value interface{}) bool {
	if value == nil {
		return true
	}
	
	switch v := value.(type) {
	case []interface{}:
		return len(v) > 0
	case string:
		return strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]")
	default:
		return false
	}
}

func (fve *FieldValidationEngine) isValidVirtualHostsArray(value interface{}) bool {
	return fve.isValidRoutesArray(value) // Same logic
}

func (fve *FieldValidationEngine) isValidVHDSConfig(value interface{}) bool {
	if value == nil {
		return true
	}
	
	// Basic VHDS config validation
	valueStr := fmt.Sprint(value)
	return valueStr != "" && valueStr != "null"
}

func (fve *FieldValidationEngine) listenerHasNetworkFilter(comp models.ComponentInstance) bool {
	if comp.Type != "listener" {
		return true
	}
	
	for _, field := range comp.SelectedFields {
		if field.FieldName == "network_filters" && field.Value != nil {
			switch v := field.Value.(type) {
			case []interface{}:
				return len(v) > 0
			case string:
				return v != "" && v != "[]"
			default:
				return false
			}
		}
	}
	
	return false
}

// scenarioHasNetworkFilterComponents checks if scenario has network filter components
func (fve *FieldValidationEngine) scenarioHasNetworkFilterComponents() bool {
	hasHCM := false
	hasTCPProxy := false
	
	for _, comp := range fve.scenarios {
		switch comp.Type {
		case "http_connection_manager":
			hasHCM = true
		case "tcp_proxy":
			hasTCPProxy = true
		}
	}
	
	return hasHCM || hasTCPProxy
}

// hcmHasRouterFilter is deprecated - HTTP filters are now optional and flexible
// Users can select filters from API or create new components
// func (fve *FieldValidationEngine) hcmHasRouterFilter(comp models.ComponentInstance) bool {
// 	// This validation is no longer needed - HTTP filters are optional
// 	return true
// }

// httpFiltersIncludeRouter checks if http_filters array contains a router_filter
func (fve *FieldValidationEngine) httpFiltersIncludeRouter(value interface{}) bool {
	if value == nil {
		return false
	}
	
	switch v := value.(type) {
	case []interface{}:
		// Check each filter in the array
		for _, filter := range v {
			if filterMap, ok := filter.(map[string]interface{}); ok {
				// Check if it's a router filter - must be http_filter type with router gtype
				if filterType, exists := filterMap["type"]; exists && filterType == "http_filter" {
					// Check gtype field for router filter identification
					if gtype, exists := filterMap["gtype"]; exists {
						gtypeStr := fmt.Sprint(gtype)
						if gtypeStr == "envoy.extensions.filters.http.router.v3.Router" {
							return true
						}
					}
				}
			}
		}
		return false
	case string:
		// JSON string format - check if it contains router_filter
		return strings.Contains(v, "router_filter") || strings.Contains(v, "router")
	default:
		return false
	}
}

// routeExists checks if a route component exists in the scenario
func (fve *FieldValidationEngine) routeExists(routeName interface{}) bool {
	if routeName == nil {
		return false
	}
	
	routeNameStr := fmt.Sprint(routeName)
	for _, component := range fve.scenarios {
		if component.Type == "route" && component.Name == routeNameStr {
			return true
		}
	}
	return false
}