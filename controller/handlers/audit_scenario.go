package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/audit"
	"github.com/gin-gonic/gin"
	"github.com/google/go-cmp/cmp"
	"github.com/r3labs/diff/v3"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// ================== SCENARIO AUDIT HANDLERS ==================
//
// This file contains audit functionality for Scenario operations:
// - Scenario creation, update, delete, execute audit
// - Scenario format change filtering
// - Scenario-specific change comparison functions
//

// getScenarioNameFromID fetches scenario name from database by ID for audit logging
func (h *Handler) getScenarioNameFromID(c *gin.Context, scenarioID string) string {
	// Get project from request context
	projectParam := c.Query("project")
	if projectParam == "" {
		return scenarioID // fallback to ID if no project
	}

	db := h.getDatabaseConnection()
	if db == nil {
		return scenarioID // fallback to ID if no database connection
	}

	// Query scenario from database using scenario_id field
	filter := bson.M{
		"scenario_id": scenarioID,
	}

	// Add project filter - handle nil project case (global scenarios)
	if projectParam != "" {
		filter["$or"] = []bson.M{
			{"project": projectParam},
			{"project": bson.M{"$exists": false}},
			{"project": nil},
		}
	}

	var result struct {
		Name string `bson:"name"`
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	collection := db.Collection("scenarios")
	err := collection.FindOne(ctx, filter).Decode(&result)
	if err != nil {
		// If not found or error, return the ID as fallback
		return scenarioID
	}

	return result.Name
}

// ================== SCENARIO AUDIT HANDLERS ==================

// setScenarioAuditChanges handles changes detection for scenario operations
func (h *Handler) setScenarioAuditChanges(c *gin.Context, path string) {
	// Only handle scenario UPDATE operations that have scenario_id in path
	if !strings.Contains(path, "/scenarios/") || c.Request.Method != "PUT" {
		return
	}

	scenarioID := c.Param("scenario_id")
	if scenarioID == "" {
		return
	}

	// Get project from query param
	project := c.Query("project")
	if project == "" {
		return
	}

	// Get new scenario data from request body
	originalBody, exists := c.Get("_original_body")
	if !exists {
		return
	}

	bodyBytes, ok := originalBody.([]byte)
	if !ok {
		return
	}

	var newScenario map[string]any
	if err := json.Unmarshal(bodyBytes, &newScenario); err != nil {
		return
	}

	db := h.getDatabaseConnection()
	if db == nil {
		return
	}

	// Fetch existing scenario from database using scenario_id field (not MongoDB _id)
	filter := bson.M{
		"scenario_id": scenarioID,
	}

	// Add project filter - handle nil project case (global scenarios)
	if project != "" {
		filter["$or"] = []bson.M{
			{"project": project},
			{"project": bson.M{"$exists": false}},
			{"project": nil},
		}
	}

	var existingScenario map[string]any
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	collection := db.Collection("scenarios")
	err := collection.FindOne(ctx, filter).Decode(&existingScenario)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			audit.SetAuditChanges(c, map[string]any{"new_scenario": true})
		}
		return
	}

	// Remove metadata fields for comparison
	cleanExisting := h.cleanScenarioData(existingScenario)
	cleanNew := h.cleanScenarioData(newScenario)

	// Normalize both sides to handle primitive types
	normalizedExisting, err := normalizePrimitiveTypes(cleanExisting)
	if err != nil {
		return
	}
	normalizedNew, err := normalizePrimitiveTypes(cleanNew)
	if err != nil {
		return
	}

	// Use r3labs/diff for better JSON comparison
	changelog, err := diff.Diff(normalizedExisting, normalizedNew)
	if err != nil {
		// Fallback to go-cmp if diff fails
		if !cmp.Equal(normalizedExisting, normalizedNew) {
			audit.SetAuditChanges(c, map[string]any{"has_changes": true})
		}
		return
	}

	if len(changelog) > 0 {
		// Filter out format-only changes (legacy field naming)
		filteredChanges := h.filterScenarioFormatChanges(changelog)

		if len(filteredChanges) > 0 {
			// Use same format as XDS - JSON string with structured changes
			diffJSON := h.formatChangelogAsJSON(filteredChanges)
			audit.SetAuditChanges(c, map[string]any{
				"diff": diffJSON,
			})
		}
	}
}

// cleanScenarioData removes metadata fields and normalizes field naming for comparison
func (h *Handler) cleanScenarioData(scenario map[string]any) map[string]any {
	cleaned := make(map[string]any)

	// Fields to skip during comparison (metadata and system fields)
	skipFields := map[string]bool{
		"_id":         true, // MongoDB ObjectID
		"created_at":  true, // Creation timestamp
		"updated_at":  true, // Update timestamp
		"created_by":  true, // Creator user ID
		"project":     true, // Project should not change in UPDATE
		"scenario_id": true, // Scenario ID should not change in UPDATE
		"is_default":  true, // Usually false and causes noise when deleted
	}

	for key, value := range scenario {
		if !skipFields[key] {
			// Handle components field specially to normalize field naming
			if key == "components" {
				cleaned[key] = h.normalizeComponentsFieldNaming(value)
			} else {
				cleaned[key] = value
			}
		}
	}

	return cleaned
}

// normalizeComponentsFieldNaming converts legacy field names to new format for consistent comparison
func (h *Handler) normalizeComponentsFieldNaming(components any) any {
	componentsList, ok := components.([]any)
	if !ok {
		return components // Return as-is if not an array
	}

	normalizedComponents := make([]any, len(componentsList))

	for i, component := range componentsList {
		componentMap, ok := component.(map[string]any)
		if !ok {
			normalizedComponents[i] = component
			continue
		}

		normalizedComponent := make(map[string]any)

		// Copy all fields from original component
		for key, value := range componentMap {
			normalizedComponent[key] = value
		}

		// Handle both legacy and new field naming for consistent comparison
		if selectedFields, exists := componentMap["selectedfields"]; exists {
			// Remove legacy field
			delete(normalizedComponent, "selectedfields")

			// Convert to new format
			normalizedComponent["selected_fields"] = h.normalizeSelectedFields(selectedFields)
		} else if selectedFields, exists := componentMap["selected_fields"]; exists {
			// Normalize new format as well (in case it has inconsistencies)
			normalizedComponent["selected_fields"] = h.normalizeSelectedFields(selectedFields)
		}

		normalizedComponents[i] = normalizedComponent
	}

	return normalizedComponents
}

// normalizeSelectedFields converts legacy selected field format to new format
func (h *Handler) normalizeSelectedFields(selectedFields any) any {
	fieldsList, ok := selectedFields.([]any)
	if !ok {
		return selectedFields // Return as-is if not an array
	}

	normalizedFields := make([]any, len(fieldsList))

	for i, field := range fieldsList {
		fieldMap, ok := field.(map[string]any)
		if !ok {
			normalizedFields[i] = field
			continue
		}

		normalizedField := make(map[string]any)

		// Convert legacy field names to new format
		for key, value := range fieldMap {
			switch key {
			case "fieldname":
				normalizedField["field_name"] = value
			case "nestedselection":
				normalizedField["nested_selection"] = value
			default:
				normalizedField[key] = value
			}
		}

		normalizedFields[i] = normalizedField
	}

	return normalizedFields
}

// filterScenarioFormatChanges removes format-only changes from changelog
func (h *Handler) filterScenarioFormatChanges(changelog diff.Changelog) diff.Changelog {
	// First, identify paired format changes (delete selectedfields + create selected_fields)
	formatChangePairs := h.identifyFormatChangePairs(changelog)

	var filteredChanges diff.Changelog
	skippedIndices := make(map[int]bool)

	// Process format pairs - convert them to actual content changes
	for _, pair := range formatChangePairs {
		// Mark these indices as processed
		skippedIndices[pair.deleteIndex] = true
		skippedIndices[pair.createIndex] = true

		// Compare the actual content and add real changes
		realChanges := h.extractRealChangesFromFormatPair(pair, changelog)
		filteredChanges = append(filteredChanges, realChanges...)
	}

	// Add non-format changes
	for i, change := range changelog {
		if !skippedIndices[i] {
			filteredChanges = append(filteredChanges, change)
		}
	}

	return filteredChanges
}

// formatChangePair represents a delete+create pair for format migration
type formatChangePair struct {
	deleteIndex   int
	createIndex   int
	componentPath string // e.g., "components.0"
}

// identifyFormatChangePairs finds delete selectedfields + create selected_fields pairs
func (h *Handler) identifyFormatChangePairs(changelog diff.Changelog) []formatChangePair {
	var pairs []formatChangePair

	// Find all selectedfields deletes
	deleteMap := make(map[string]int) // component path -> changelog index
	createMap := make(map[string]int) // component path -> changelog index

	for i, change := range changelog {
		if len(change.Path) >= 3 && change.Path[0] == "components" {
			componentPath := change.Path[0] + "." + change.Path[1]

			if change.Type == "delete" && change.Path[2] == "selectedfields" {
				deleteMap[componentPath] = i
			} else if change.Type == "create" && change.Path[2] == "selected_fields" {
				createMap[componentPath] = i
			}
		}
	}

	// Match deletes with creates for same component
	for componentPath, deleteIndex := range deleteMap {
		if createIndex, exists := createMap[componentPath]; exists {
			pairs = append(pairs, formatChangePair{
				deleteIndex:   deleteIndex,
				createIndex:   createIndex,
				componentPath: componentPath,
			})
		}
	}

	return pairs
}

// extractRealChangesFromFormatPair compares old and new field values to find real changes
func (h *Handler) extractRealChangesFromFormatPair(pair formatChangePair, changelog diff.Changelog) []diff.Change {
	deleteChange := changelog[pair.deleteIndex]
	createChange := changelog[pair.createIndex]

	// Extract old and new field arrays
	oldFields, ok1 := deleteChange.From.([]any)
	newFields, ok2 := createChange.To.([]any)

	if !ok1 || !ok2 {
		return nil // Can't compare, skip
	}

	// Normalize both arrays for comparison
	normalizedOld := h.normalizeFieldArray(oldFields)
	normalizedNew := h.normalizeFieldArray(newFields)

	// Find real differences
	var realChanges []diff.Change

	// Simple comparison - if normalized arrays are different, report as field array change
	if !h.areFieldArraysEqual(normalizedOld, normalizedNew) {
		// Create a single meaningful change instead of format noise
		realChanges = append(realChanges, diff.Change{
			Type: "update",
			Path: []string{deleteChange.Path[0], deleteChange.Path[1], "selected_fields"},
			From: normalizedOld,
			To:   normalizedNew,
		})
	}

	return realChanges
}

// normalizeFieldArray normalizes field array for comparison (convert legacy format to standard)
func (h *Handler) normalizeFieldArray(fields []any) []map[string]any {
	normalized := make([]map[string]any, len(fields))

	for i, field := range fields {
		fieldMap, ok := field.(map[string]any)
		if !ok {
			continue
		}

		normalizedField := make(map[string]any)

		// Normalize field names and skip noise-causing fields
		for key, value := range fieldMap {
			switch key {
			case "fieldname":
				normalizedField["field_name"] = value
			case "nestedselection", "nested_selection":
				// Skip nested_selection as it's usually null and causes noise
				continue
			case "value":
				// Skip value as it's often null/empty in creation phase and causes noise
				continue
			default:
				normalizedField[key] = value
			}
		}

		normalized[i] = normalizedField
	}

	return normalized
}

// areFieldArraysEqual checks if two normalized field arrays are equal
func (h *Handler) areFieldArraysEqual(a, b []map[string]any) bool {
	if len(a) != len(b) {
		return false
	}

	// Create maps for easier comparison
	aMap := make(map[string]map[string]any)
	bMap := make(map[string]map[string]any)

	for _, field := range a {
		if name, ok := field["field_name"].(string); ok {
			aMap[name] = field
		}
	}

	for _, field := range b {
		if name, ok := field["field_name"].(string); ok {
			bMap[name] = field
		}
	}

	// Compare field by field
	if len(aMap) != len(bMap) {
		return false
	}

	for name, fieldA := range aMap {
		fieldB, exists := bMap[name]
		if !exists {
			return false
		}

		// Compare only essential properties, skip nested_selection and value
		for key, valueA := range fieldA {
			// Skip fields that are often null/empty and cause noise
			if key == "nested_selection" || key == "value" {
				continue
			}
			if valueB, exists := fieldB[key]; !exists || valueA != valueB {
				return false
			}
		}
	}

	return true
}

// extractScenarioInfoFromExecuteRequest extracts scenario ID and name from execute request body
func (h *Handler) extractScenarioInfoFromExecuteRequest(c *gin.Context) (resourceID, resourceName string) {
	// Get cached body from middleware
	originalBody, exists := c.Get("_original_body")
	if !exists {
		return "", ""
	}

	bodyBytes, ok := originalBody.([]byte)
	if !ok {
		return "", ""
	}

	// Parse execute request to get scenario_id
	var executeRequest struct {
		ScenarioID string `json:"scenario_id"`
		Project    string `json:"project"`
	}

	if err := json.Unmarshal(bodyBytes, &executeRequest); err != nil {
		return "", ""
	}

	if executeRequest.ScenarioID == "" {
		return "", ""
	}

	// Use scenario ID as resource ID
	resourceID = executeRequest.ScenarioID

	// Try to get scenario name from database
	if executeRequest.Project != "" {
		resourceName = h.getScenarioNameFromIDAndProject(executeRequest.ScenarioID, executeRequest.Project)
	}

	return resourceID, resourceName
}

// getScenarioNameFromIDAndProject fetches scenario name using both ID and project
func (h *Handler) getScenarioNameFromIDAndProject(scenarioID, project string) string {
	db := h.getDatabaseConnection()
	if db == nil {
		return scenarioID // fallback to ID if no database connection
	}

	// Query scenario from database using scenario_id field
	filter := bson.M{
		"scenario_id": scenarioID,
	}

	// Add project filter - handle nil project case (global scenarios)
	if project != "" {
		filter["$or"] = []bson.M{
			{"project": project},
			{"project": bson.M{"$exists": false}},
			{"project": nil},
		}
	}

	var result struct {
		Name string `bson:"name"`
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	collection := db.Collection("scenarios")
	err := collection.FindOne(ctx, filter).Decode(&result)
	if err != nil {
		// If not found or error, return the ID as fallback
		return scenarioID
	}

	return result.Name
}

// setScenarioExecuteAuditChanges captures execute request details for audit
func (h *Handler) setScenarioExecuteAuditChanges(c *gin.Context) {
	// Get execute request from body
	originalBody, exists := c.Get("_original_body")
	if !exists {
		return
	}

	bodyBytes, ok := originalBody.([]byte)
	if !ok {
		return
	}

	var executeRequest map[string]any
	if err := json.Unmarshal(bodyBytes, &executeRequest); err != nil {
		return
	}

	// Extract key information for audit
	auditData := make(map[string]any)

	if scenarioID, ok := executeRequest["scenario_id"].(string); ok && scenarioID != "" {
		auditData["scenario_id"] = scenarioID
	}

	if project, ok := executeRequest["project"].(string); ok && project != "" {
		auditData["project"] = project
	}

	if version, ok := executeRequest["version"].(string); ok && version != "" {
		auditData["version"] = version
	}

	// Count components if present
	if components, ok := executeRequest["components"].([]any); ok {
		auditData["component_count"] = len(components)

		// Extract component types for audit
		componentTypes := make([]string, 0, len(components))
		for _, comp := range components {
			if compMap, ok := comp.(map[string]any); ok {
				if compType, ok := compMap["type"].(string); ok {
					componentTypes = append(componentTypes, compType)
				}
			}
		}
		if len(componentTypes) > 0 {
			auditData["component_types"] = componentTypes
		}
	}

	// Set audit changes
	if len(auditData) > 0 {
		audit.SetAuditChanges(c, map[string]any{
			"execute_details": auditData,
		})
	}
}
