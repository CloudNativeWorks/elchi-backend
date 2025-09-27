package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/version"
)

// CreateScenario creates a new scenario in the database
func (t *AppHandler) CreateScenario(request models.CreateScenarioRequest, reqDetails models.RequestDetails) (*models.Scenario, error) {
	// Enrich component instances with GType from catalog
	enrichedComponents := make([]models.ComponentInstance, len(request.Components))
	for i, component := range request.Components {
		enrichedComponents[i] = component

		// If component doesn't have gtype, get it from catalog
		if enrichedComponents[i].GType == "" {
			if componentDef, err := t.GetComponentDefinitionByType(component.Type); err == nil && componentDef != nil {
				enrichedComponents[i].GType = componentDef.GType
			}
		}
	}

	// Validate component instances
	for _, component := range enrichedComponents {
		if err := ValidateComponentInstance(component); err != nil {
			return nil, fmt.Errorf("invalid component %s: %w", component.Name, err)
		}
	}

	// Validate component rules (including field conflicts)
	if errors := t.ValidateComponentRules(enrichedComponents); len(errors) > 0 {
		// Format errors in a more readable way
		formattedError := "Validation failed with " + fmt.Sprintf("%d", len(errors)) + " errors:\n"
		for i, err := range errors {
			formattedError += fmt.Sprintf("  %d. %s\n", i+1, err)
		}
		return nil, fmt.Errorf("%s", strings.TrimSpace(formattedError))
	}

	var project *string
	if !request.AllProjects {
		// Scenario is project-specific, use project from request body
		if request.Project != "" {
			project = &request.Project
		} else if reqDetails.Project != "" {
			// Fallback to query param if not in body
			project = &reqDetails.Project
		}
	}
	// If AllProjects is true, project remains nil (available to all projects)

	scenario := models.Scenario{
		ID:          primitive.NewObjectID(),
		Name:        request.Name,
		Description: request.Description,
		ScenarioID:  request.ScenarioID,
		Components:  enrichedComponents, // Use enriched components with GType
		CreatedBy:   reqDetails.User.UserID,
		Project:     project,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	collection := t.Context.Client.Collection("scenarios")
	_, err := collection.InsertOne(context.Background(), scenario)
	if err != nil {
		return nil, fmt.Errorf("failed to create scenario: %w", err)
	}

	return &scenario, nil
}

// GetScenarios retrieves scenarios for a project (includes global scenarios)
func (t *AppHandler) GetScenarios(project string, reqDetails models.RequestDetails) (*models.ScenarioListResponse, error) {
	collection := t.Context.Client.Collection("scenarios")

	// Filter: scenarios that belong to this specific project OR are available to all projects (project == nil)
	filter := bson.M{
		"$or": []bson.M{
			{"project": project},            // Project-specific scenarios
			{"project": ""},                 // Empty string project (should be treated as project-specific)
			{"project": bson.M{"$eq": nil}}, // Global scenarios (all projects)
		},
	}

	// Debug: log the filter being used
	t.Logger.Infof("GetScenarios filter for project '%s': %+v", project, filter)

	cursor, err := collection.Find(context.Background(), filter)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch scenarios: %w", err)
	}
	defer cursor.Close(context.Background())

	var scenarios []models.Scenario
	if err := cursor.All(context.Background(), &scenarios); err != nil {
		return nil, fmt.Errorf("failed to decode scenarios: %w", err)
	}

	// Debug: log results
	t.Logger.Infof("Found %d scenarios for project '%s'", len(scenarios), project)
	for i, scenario := range scenarios {
		if scenario.Project == nil {
			t.Logger.Infof("  [%d] %s (global)", i, scenario.Name)
		} else {
			t.Logger.Infof("  [%d] %s (project: %s)", i, scenario.Name, *scenario.Project)
		}
	}

	total, err := collection.CountDocuments(context.Background(), filter)
	if err != nil {
		return nil, fmt.Errorf("failed to count scenarios: %w", err)
	}

	return &models.ScenarioListResponse{
		Scenarios: scenarios,
		Total:     total,
	}, nil
}

// GetScenarioByID retrieves a specific scenario by scenario_id
func (t *AppHandler) GetScenarioByID(scenarioID string, reqDetails models.RequestDetails) (*models.Scenario, error) {
	collection := t.Context.Client.Collection("scenarios")
	var scenario models.Scenario

	err := collection.FindOne(context.Background(), bson.M{"scenario_id": scenarioID}).Decode(&scenario)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, fmt.Errorf("scenario not found")
		}
		return nil, fmt.Errorf("failed to fetch scenario: %w", err)
	}

	return &scenario, nil
}

// UpdateScenario updates an existing scenario
func (t *AppHandler) UpdateScenario(scenarioID string, request models.UpdateScenarioRequest, reqDetails models.RequestDetails) (*models.Scenario, error) {
	// First find the scenario to get its ObjectID
	existingScenario, err := t.GetScenarioByID(scenarioID, reqDetails)
	if err != nil {
		return nil, err
	}

	// Validate component instances if provided
	var enrichedComponents []models.ComponentInstance
	if len(request.Components) > 0 {
		// Enrich component instances with GType from catalog
		enrichedComponents = make([]models.ComponentInstance, len(request.Components))
		for i, component := range request.Components {
			enrichedComponents[i] = component

			// If component doesn't have gtype, get it from catalog
			if enrichedComponents[i].GType == "" {
				if componentDef, err := t.GetComponentDefinitionByType(component.Type); err == nil && componentDef != nil {
					enrichedComponents[i].GType = componentDef.GType
				}
			}
		}

		for _, component := range enrichedComponents {
			if err := ValidateComponentInstance(component); err != nil {
				return nil, fmt.Errorf("invalid component %s: %w", component.Name, err)
			}
		}

		// Validate component rules (including field conflicts)
		if errors := t.ValidateComponentRules(enrichedComponents); len(errors) > 0 {
			// Format errors in a more readable way
			formattedError := "Validation failed with " + fmt.Sprintf("%d", len(errors)) + " errors:\n"
			for i, err := range errors {
				formattedError += fmt.Sprintf("  %d. %s\n", i+1, err)
			}
			return nil, fmt.Errorf("%s", strings.TrimSpace(formattedError))
		}
	}

	update := bson.M{
		"$set": bson.M{
			"updated_at": time.Now(),
		},
	}

	if request.Name != "" {
		update["$set"].(bson.M)["name"] = request.Name
	}
	if request.Description != "" {
		update["$set"].(bson.M)["description"] = request.Description
	}
	if len(request.Components) > 0 {
		update["$set"].(bson.M)["components"] = enrichedComponents // Use enriched components with GType
	}
	if request.AllProjects != nil {
		if *request.AllProjects {
			// Set to all projects (nil)
			update["$unset"] = bson.M{"project": ""}
		} else {
			// Set to current project
			update["$set"].(bson.M)["project"] = reqDetails.Project
		}
	}

	collection := t.Context.Client.Collection("scenarios")
	_, err = collection.UpdateOne(context.Background(), bson.M{"_id": existingScenario.ID}, update)
	if err != nil {
		return nil, fmt.Errorf("failed to update scenario: %w", err)
	}

	// Return updated scenario
	return t.GetScenarioByID(scenarioID, reqDetails)
}

// DeleteScenario deletes a scenario
func (t *AppHandler) DeleteScenario(scenarioID string, reqDetails models.RequestDetails) error {
	// First find the scenario to get its ObjectID
	existingScenario, err := t.GetScenarioByID(scenarioID, reqDetails)
	if err != nil {
		return err
	}

	collection := t.Context.Client.Collection("scenarios")
	result, err := collection.DeleteOne(context.Background(), bson.M{"_id": existingScenario.ID})
	if err != nil {
		return fmt.Errorf("failed to delete scenario: %w", err)
	}

	if result.DeletedCount == 0 {
		return fmt.Errorf("scenario not found")
	}

	return nil
}

// CreatedResource tracks created resources for rollback
type CreatedResource struct {
	ID         interface{} // ObjectID or resource identifier
	Collection string      // MongoDB collection name
	Name       string      // Resource name for logging
}

// ExecuteScenario executes a scenario and generates resources
func (t *AppHandler) ExecuteScenario(request models.ExecuteScenarioRequest, reqDetails models.RequestDetails) ([]map[string]interface{}, error) {
	scenario, err := t.GetScenarioByID(request.ScenarioID, reqDetails)
	if err != nil {
		return nil, fmt.Errorf("failed to get scenario: %w", err)
	}

	// Use components from request if provided, otherwise use scenario components
	componentsToProcess := scenario.Components
	if len(request.Components) > 0 {
		componentsToProcess = request.Components
	}

	var generatedResources []map[string]interface{}
	var createdResources []CreatedResource // Track created resources for rollback
	version := request.Version             // Use version from request

	// Validate component rules with execution context before processing
	if errors := t.ValidateComponentRulesWithContext(componentsToProcess, ValidationContextExecution); len(errors) > 0 {
		// Rollback any previously created resources
		if len(createdResources) > 0 {
			t.rollbackCreatedResources(createdResources, reqDetails)
		}
		// Format errors in a more readable way
		formattedError := ""
		for i, err := range errors {
			formattedError += fmt.Sprintf("  %d. %s\n", i+1, err)
		}
		return nil, fmt.Errorf("%s", strings.TrimSpace(formattedError))
	}

	// Build component type mapping for listener network_filters
	componentTypeMap := make(map[string]string)
	for _, comp := range componentsToProcess {
		componentTypeMap[comp.Name] = comp.Type
	}

	for i, componentInstance := range componentsToProcess {
		// Validate component instance for execution (stricter validation) with full scenario context
		if err := ValidateComponentInstanceForExecutionWithContext(componentInstance, componentsToProcess); err != nil {
			// Rollback any previously created resources
			if len(createdResources) > 0 {
				t.rollbackCreatedResources(createdResources, reqDetails)
			}
			return nil, fmt.Errorf("invalid component %s: %w", componentInstance.Name, err)
		}

		// Generate Envoy resource document with correct format
		result, err := GenerateEnvoyResourceDocumentWithMapping(componentInstance, reqDetails, version, componentTypeMap, request.Managed)
		if err != nil {
			// Rollback any previously created resources
			if len(createdResources) > 0 {
				t.rollbackCreatedResources(createdResources, reqDetails)
			}
			return nil, fmt.Errorf("failed to generate document for component %s: %w", componentInstance.Name, err)
		}

		// All generators now return single document (even listeners with multiple resources)
		document, ok := result.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("unexpected result type from generator for component %s: %T", componentInstance.Name, result)
		}

		// Always save to database regardless of managed flag
		t.Logger.Logger.Infof("[%d/%d] Saving %s to database: %s\n", i+1, len(componentsToProcess), componentInstance.Type, componentInstance.Name)
		resourceID, err := t.saveResourceToDatabaseWithID(document, reqDetails)
		if err != nil {
			// Check if XDS already performed internal rollback
			errStr := err.Error()
			if strings.Contains(errStr, "Bootstrap creation failed") || strings.Contains(errStr, "Service creation failed") {
				t.Logger.Logger.Infof("🔄 XDS internal rollback detected for %s, current resource already cleaned up", componentInstance.Name)
				// XDS already rolled back the current resource, but we still need to rollback previous resources
				if len(createdResources) > 0 {
					t.Logger.Logger.Infof("🔄 Rolling back %d previously created resources", len(createdResources))
					t.rollbackCreatedResources(createdResources, reqDetails)
				}
			} else {
				// Normal rollback: rollback all previously created resources
				if len(createdResources) > 0 {
					t.rollbackCreatedResources(createdResources, reqDetails)
				}
			}
			return nil, fmt.Errorf("failed to save component %s to database: %w", componentInstance.Name, err)
		}

		// Track created resource for potential rollback
		if collection := t.getCollectionName(componentInstance.Type); collection != "" {
			createdResources = append(createdResources, CreatedResource{
				ID:         resourceID,
				Collection: collection,
				Name:       componentInstance.Name,
			})
		}

		generatedResources = append(generatedResources, document)
	}

	t.Logger.Logger.Infof("✅ Successfully created %d resources\n", len(createdResources))
	return generatedResources, nil
}

// convertDocumentToResourceClass converts map[string]interface{} document to models.ResourceClass
func convertDocumentToResourceClass(document map[string]interface{}) (models.ResourceClass, error) {
	// Convert map to JSON bytes
	jsonData, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal document to JSON: %w", err)
	}

	// Convert JSON bytes to DBResource
	var resource models.DBResource
	if err := json.Unmarshal(jsonData, &resource); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON to DBResource: %w", err)
	}

	return &resource, nil
}

// saveResourceToDatabaseWithID saves resource and returns the created ID
func (t *AppHandler) saveResourceToDatabaseWithID(document map[string]interface{}, reqDetails models.RequestDetails) (interface{}, error) {
	// Use normal DBResource conversion for all resource types
	resource, err := convertDocumentToResourceClass(document)
	if err != nil {
		return nil, fmt.Errorf("failed to convert document to ResourceClass: %w", err)
	}

	// Call XDS SetResource with full validation, bootstrap creation, etc.
	result, err := t.XDS.SetResource(context.Background(), resource, reqDetails)
	if err != nil {
		t.Logger.Logger.Infof("❌ XDS SetResource failed for %s: %v\n", resource.GetGeneral().Name, err)

		// Check for duplicate key error and provide cleaner message
		errStr := err.Error()
		if strings.Contains(errStr, "E11000 duplicate key error") {
			resourceName := resource.GetGeneral().Name
			return nil, fmt.Errorf("resource \"%s\" already exists", resourceName)
		}

		return nil, fmt.Errorf("failed to save resource via XDS SetResource: %w", err)
	}

	// Extract ID from result
	if resultMap, ok := result.(map[string]any); ok {
		// Check for resource_id in nested data
		if data, exists := resultMap["data"].(map[string]interface{}); exists {
			if resourceID, exists := data["resource_id"]; exists {
				return resourceID, nil
			}
		}

		// Check for direct _id
		if id, exists := resultMap["_id"]; exists {
			return id, nil
		}

		// Try to get from general section
		if general, ok := resultMap["general"].(map[string]interface{}); ok {
			if id, exists := general["_id"]; exists {
				return id, nil
			}
		}
	}

	return result, nil
}

// rollbackCreatedResources removes all created resources in reverse order
func (t *AppHandler) rollbackCreatedResources(createdResources []CreatedResource, _ models.RequestDetails) {
	// Rollback in reverse order (LIFO)
	for i := len(createdResources) - 1; i >= 0; i-- {
		resource := createdResources[i]
		t.Logger.Logger.Infof("  - Deleting %s from %s (ID: %v)\n", resource.Name, resource.Collection, resource.ID)

		// Use MongoDB collection to delete by ID
		collection := t.Context.Client.Collection(resource.Collection)
		if collection != nil {
			// Convert string ID to ObjectID if needed
			var deleteFilter bson.M
			if idStr, ok := resource.ID.(string); ok {
				if objectID, err := primitive.ObjectIDFromHex(idStr); err == nil {
					deleteFilter = bson.M{"_id": objectID}
				} else {
					deleteFilter = bson.M{"_id": idStr}
				}
			} else {
				deleteFilter = bson.M{"_id": resource.ID}
			}

			result, err := collection.DeleteOne(context.Background(), deleteFilter)
			if err != nil {
				t.Logger.Logger.Infof("    ⚠️  Failed to delete %s: %v\n", resource.Name, err)
			} else if result.DeletedCount == 0 {
				t.Logger.Logger.Infof("    ⚠️  No document deleted for %s (ID: %v)\n", resource.Name, resource.ID)
			} else {
				t.Logger.Logger.Infof("    ✅ Deleted %s successfully\n", resource.Name)
			}
		}
	}

	t.Logger.Logger.Info("🔄 Rollback completed\n")
}

// getCollectionName returns MongoDB collection name for component type
func (t *AppHandler) getCollectionName(componentType string) string {
	switch componentType {
	case "cluster":
		return "clusters"
	case "listener":
		return "listeners"
	case "http_connection_manager", "tcp_proxy", "router_filter":
		return "filters"
	case "route":
		return "routes"
	case "virtual_host":
		return "virtual_hosts"
	case "endpoint":
		return "endpoints"
	case "access_log_file", "access_log_stdout", "access_log_fluentd":
		return "extensions"
	default:
		return ""
	}
}

// getMapKeys returns map keys for debugging
func getMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

// ExportScenarios exports multiple scenarios by IDs
func (t *AppHandler) ExportScenarios(request models.ExportScenarioRequest, reqDetails models.RequestDetails) (*models.ExportScenarioResponse, error) {
	// Build filter for scenarios
	objectIDs := make([]primitive.ObjectID, len(request.ScenarioIDs))
	for i, id := range request.ScenarioIDs {
		objectID, err := primitive.ObjectIDFromHex(id)
		if err != nil {
			return nil, fmt.Errorf("invalid scenario ID: %s", id)
		}
		objectIDs[i] = objectID
	}

	// Filter by project access
	filter := bson.M{
		"_id": bson.M{"$in": objectIDs},
	}

	// Add project filter if not admin
	if reqDetails.Project != "" {
		filter["$or"] = bson.A{
			bson.M{"project": nil},                // All projects scenarios
			bson.M{"project": reqDetails.Project}, // Project specific scenarios
		}
	}

	// Find scenarios
	collection := t.Context.Client.Collection("scenarios")
	cursor, err := collection.Find(context.Background(), filter)
	if err != nil {
		return nil, fmt.Errorf("failed to query scenarios: %w", err)
	}
	defer cursor.Close(context.Background())

	var scenarios []models.Scenario
	if err := cursor.All(context.Background(), &scenarios); err != nil {
		return nil, fmt.Errorf("failed to decode scenarios: %w", err)
	}

	// Check if all requested scenarios were found
	if len(scenarios) != len(request.ScenarioIDs) {
		foundIDs := make([]string, len(scenarios))
		for i, scenario := range scenarios {
			foundIDs[i] = scenario.ID.Hex()
		}
		return nil, fmt.Errorf("some scenarios not found or not accessible. Found: %v", foundIDs)
	}

	response := &models.ExportScenarioResponse{
		Scenarios:  scenarios,
		ExportedBy: reqDetails.User.UserName,
		ExportedAt: time.Now(),
		Version:    version.Version,
		Count:      len(scenarios),
	}

	return response, nil
}

// ImportScenarios imports scenarios with conflict resolution
func (t *AppHandler) ImportScenarios(request models.ImportScenarioRequest, reqDetails models.RequestDetails) (*models.ImportScenarioResponse, error) {
	collection := t.Context.Client.Collection("scenarios")

	// Validate version compatibility if version is provided
	if request.Version != "" && request.Version != version.Version {
		return nil, fmt.Errorf("version mismatch: import version '%s' does not match system version '%s'. Please export from a compatible version", request.Version, version.Version)
	}

	var conflicts []models.ScenarioConflict
	var imported int
	var skipped int

	for _, scenario := range request.Scenarios {
		// Check if scenario_id already exists in target project
		existingFilter := bson.M{
			"scenario_id": scenario.ScenarioID,
		}

		// Add project filter
		if request.Project != "" {
			existingFilter["$or"] = bson.A{
				bson.M{"project": nil},             // All projects scenarios
				bson.M{"project": request.Project}, // Project specific scenarios
			}
		}

		var existingScenario models.Scenario
		err := collection.FindOne(context.Background(), existingFilter).Decode(&existingScenario)

		conflict := models.ScenarioConflict{
			ScenarioID: scenario.ScenarioID,
			ImportName: scenario.Name,
		}

		if err == nil {
			// Scenario exists - handle conflict
			conflict.ExistingName = existingScenario.Name

			switch request.ConflictAction {
			case "skip":
				conflict.Action = "skipped"
				conflicts = append(conflicts, conflict)
				skipped++
				continue

			case "overwrite":
				conflict.Action = "overwritten"
				// Update existing scenario
				scenario.ID = existingScenario.ID               // Keep existing ID
				scenario.CreatedAt = existingScenario.CreatedAt // Keep original created time
				scenario.UpdatedAt = time.Now()
				scenario.CreatedBy = reqDetails.User.UserID // Set to current importing user

				// Set project
				if request.Project != "" {
					scenario.Project = &request.Project
				} else {
					scenario.Project = nil
				}

				// Replace existing document
				_, err := collection.ReplaceOne(context.Background(), bson.M{"_id": existingScenario.ID}, scenario)
				if err != nil {
					return nil, fmt.Errorf("failed to overwrite scenario %s: %w", scenario.ScenarioID, err)
				}
				conflicts = append(conflicts, conflict)
				imported++

			case "rename":
				// Generate new unique scenario_id
				newScenarioID := t.generateUniqueScenarioID(scenario.ScenarioID, collection, request.Project)
				conflict.Action = "renamed"
				conflict.NewName = scenario.Name + " (Imported)"

				// Create new scenario with new ID
				scenario.ID = primitive.NewObjectID()
				scenario.ScenarioID = newScenarioID
				scenario.Name = conflict.NewName
				scenario.CreatedAt = time.Now()
				scenario.UpdatedAt = time.Now()
				scenario.CreatedBy = reqDetails.User.UserID // Set to current importing user
				scenario.IsDefault = false                  // Imported scenarios are never default

				// Set project
				if request.Project != "" {
					scenario.Project = &request.Project
				} else {
					scenario.Project = nil
				}

				_, err := collection.InsertOne(context.Background(), scenario)
				if err != nil {
					return nil, fmt.Errorf("failed to import renamed scenario %s: %w", scenario.ScenarioID, err)
				}
				conflicts = append(conflicts, conflict)
				imported++
			}
		} else if err == mongo.ErrNoDocuments {
			// Scenario doesn't exist - create new
			scenario.ID = primitive.NewObjectID()
			scenario.CreatedAt = time.Now()
			scenario.UpdatedAt = time.Now()
			scenario.CreatedBy = reqDetails.User.UserID // Set to current importing user
			scenario.IsDefault = false                  // Imported scenarios are never default

			// Set project
			if request.Project != "" {
				scenario.Project = &request.Project
			} else {
				scenario.Project = nil
			}

			_, err := collection.InsertOne(context.Background(), scenario)
			if err != nil {
				return nil, fmt.Errorf("failed to import scenario %s: %w", scenario.ScenarioID, err)
			}
			imported++
		} else {
			return nil, fmt.Errorf("failed to check existing scenario %s: %w", scenario.ScenarioID, err)
		}
	}

	response := &models.ImportScenarioResponse{
		Success:    true,
		Message:    fmt.Sprintf("Import completed: %d imported, %d skipped", imported, skipped),
		Imported:   imported,
		Skipped:    skipped,
		Conflicts:  conflicts,
		ImportedBy: reqDetails.User.UserName,
		ImportedAt: time.Now(),
	}

	return response, nil
}

// generateUniqueScenarioID generates a unique scenario ID by appending a counter
func (t *AppHandler) generateUniqueScenarioID(originalID string, collection *mongo.Collection, project string) string {
	counter := 1
	for {
		newID := originalID + "_" + strconv.Itoa(counter)

		// Check if this ID exists
		filter := bson.M{"scenario_id": newID}
		if project != "" {
			filter["$or"] = bson.A{
				bson.M{"project": nil},
				bson.M{"project": project},
			}
		}

		count, err := collection.CountDocuments(context.Background(), filter)
		if err == nil && count == 0 {
			return newID
		}
		counter++

		// Safety check to prevent infinite loop
		if counter > 1000 {
			return originalID + "_" + strconv.FormatInt(time.Now().Unix(), 10)
		}
	}
}
