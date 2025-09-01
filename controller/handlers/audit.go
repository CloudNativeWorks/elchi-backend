package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/pkg/audit"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/gin-gonic/gin"
	"github.com/google/go-cmp/cmp"
	"github.com/r3labs/diff/v3"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// ================== AUDIT HELPER METHODS ==================

// Command type constants for better maintainability
const (
	cmdDeploy          = "DEPLOY"
	cmdUndeploy        = "UNDEPLOY"
	cmdUpdateBootstrap = "UPDATE_BOOTSTRAP"
	cmdService         = "SERVICE"
	cmdProxy           = "PROXY"
	cmdNetwork         = "NETWORK"
	cmdFRR             = "FRR"
	cmdEnvoyVersion    = "ENVOY_VERSION"
	cmdClientLogs      = "CLIENT_LOGS"
	cmdClientStats     = "CLIENT_STATS"
)

// Read-only command subtypes that should not be audited
var readOnlySubTypes = map[string]map[string]bool{
	cmdService: {
		"SUB_STATUS": true,
		"SUB_LOGS":   true,
	},
	cmdNetwork: {
		"SUB_NETPLAN_GET":        true,
		"SUB_ROUTE_LIST":         true,
		"SUB_POLICY_LIST":        true,
		"SUB_TABLE_LIST":         true,
		"SUB_GET_NETWORK_STATE":  true,
	},
}

// mapCommandTypeToAction maps command type/subtype combinations to audit actions
// Returns empty string if command should not be audited
func mapCommandTypeToAction(cmdType, subType string) string {
	// Check if this is a read-only operation
	if readOnlyTypes, exists := readOnlySubTypes[cmdType]; exists {
		if readOnlyTypes[subType] {
			return "" // Don't audit read operations
		}
	}
	
	// Handle monitoring/logging commands
	if cmdType == cmdClientLogs || cmdType == cmdClientStats {
		return "" // Don't audit monitoring/logging commands
	}
	
	// Map commands to actions
	switch cmdType {
	case cmdDeploy, cmdUndeploy, cmdUpdateBootstrap, cmdProxy, cmdFRR, cmdEnvoyVersion:
		return cmdType
	case cmdService:
		if strings.HasPrefix(subType, "SUB_") {
			return "SERVICE_" + strings.TrimPrefix(subType, "SUB_")
		}
		return cmdService
	case cmdNetwork:
		if strings.HasPrefix(subType, "SUB_") {
			return "NETWORK_" + strings.TrimPrefix(subType, "SUB_")
		}
		return cmdNetwork
	default:
		return cmdType
	}
}

// setResourceAuditContext sets audit context for resource operations (XDS, Extension, Settings, Scenario)
func (h *Handler) setResourceAuditContext(c *gin.Context, requestDetails models.RequestDetails) {
	if h.AuditService == nil {
		return
	}
	
	path := c.Request.URL.Path
	
	// Check if this is a resource endpoint that should be audited
	isXDS := strings.Contains(path, "/api/v3/xds/")
	isExtension := strings.Contains(path, "/api/v3/eo/")
	isSettings := strings.Contains(path, "/api/v3/setting/")
	isScenario := strings.Contains(path, "/api/v3/scenario/")
	
	if !isXDS && !isExtension && !isSettings && !isScenario {
		return
	}
	
	// Determine action based on HTTP method
	action := h.getActionFromMethod(c.Request.Method)
	if action == "" {
		return
	}
	
	var collection, resourceID, resourceName string
	
	switch {
	case isXDS:
		collection = c.Param("collection")
		resourceID = c.Param("id")
		resourceName = c.Param("name")
		
	case isExtension:
		collection = c.Param("collection")
		extensionType := c.Param("type")
		resourceName = c.Param("name")
		
		// For extensions, combine type info for better context
		if extensionType != "" {
			collection = fmt.Sprintf("%s/%s", collection, extensionType)
		}
		// Use user-provided name for audit (not canonical_name which is technical like envoy.filters.http.basic_auth)
		
	case isSettings:
		// Settings operations: users, groups, projects, tokens, clouds
		// Get actual resource name from database for better audit tracking
		collection, resourceID, resourceName = h.getSettingsResourceInfo(c, path)
		
	case isScenario:
		collection = "scenarios"
		if strings.Contains(path, "/scenarios/") {
			resourceID = c.Param("scenario_id")
		} else if strings.Contains(path, "/execute") {
			action = "EXECUTE_SCENARIO"
		} else if strings.Contains(path, "/validate") {
			action = "VALIDATE_SCENARIO"
		} else if strings.Contains(path, "/export") {
			action = "EXPORT_SCENARIOS"
		} else if strings.Contains(path, "/import") {
			action = "IMPORT_SCENARIOS"
		}
	}
	
	// For POST requests (CREATE), try to extract resource name from body if not in URL
	if c.Request.Method == "POST" && resourceName == "" {
		resourceName = h.extractResourceNameFromBody(c)
	}
	
	audit.SetAuditAction(c, action)
	audit.SetAuditResource(c, collection, resourceID, resourceName, requestDetails.Project)
}

// setClientCommandAuditContext sets audit context for client command operations
func (h *Handler) setClientCommandAuditContext(c *gin.Context) {
	if h.AuditService == nil {
		return
	}
	
	// Only process client command endpoints
	if !strings.Contains(c.Request.URL.Path, "/api/op/clients") {
		return
	}
	
	// Parse operation to get command details
	op, err := decoderOp(c)
	if err != nil {
		return
	}
	
	action := mapCommandTypeToAction(op.GetType(), op.GetSubType())
	if action == "" {
		return // Skip auditing for read operations
	}
	audit.SetAuditAction(c, action)
	audit.SetAuditResource(c, "client_command", op.GetCommandName(), op.GetCommandName(), op.GetCommandProject())
}

// setAuditResult sets audit result (success/error) at the end of request processing
func (h *Handler) setAuditResult(c *gin.Context, err error) {
	if h.AuditService == nil {
		return
	}
	
	if err != nil {
		audit.SetAuditError(c, formatErrorMessage(err))
	} else {
		audit.SetAuditSuccess(c, true)
	}
}

// setAuditChanges captures changes by comparing new vs existing resource from database
func (h *Handler) setAuditChanges(c *gin.Context) {
	// Get request details for database query
	requestDetails, _ := h.getRequestDetails(c)
	path := c.Request.URL.Path
	
	// Handle Settings resources differently from XDS resources
	if strings.Contains(path, "/api/v3/setting/") {
		h.setSettingsAuditChanges(c, path)
		return
	}
	
	// Handle XDS/Extension resources
	if originalBody, exists := c.Get("_original_body"); exists {
		if bodyBytes, ok := originalBody.([]byte); ok {
			var newResource models.DBResource
			if err := json.Unmarshal(bodyBytes, &newResource); err != nil {
				return // Skip if unable to parse new resource
			}
			
			// Compare with existing resource from database
			diff := h.compareWithExistingResource(c.Request.Context(), &newResource, requestDetails)
			if diff != "" {
				audit.SetAuditChanges(c, map[string]interface{}{
					"diff": diff,
				})
			}
		}
	}
}

// compareWithExistingResource fetches existing resource from DB and compares with new one
func (h *Handler) compareWithExistingResource(ctx context.Context, newResource *models.DBResource, requestDetails models.RequestDetails) string {
	// Determine which database to use based on the handler that has context
	var db *mongo.Database
	var collection string
	
	// Get database connection from any handler that has Context
	if h.XDS != nil && h.XDS.Context != nil {
		db = h.XDS.Context.Client
	} else if h.Extension != nil && h.Extension.Context != nil {
		db = h.Extension.Context.Client
	} else if h.Settings != nil && h.Settings.Context != nil {
		db = h.Settings.Context.Client
	}
	
	if db == nil {
		return "" // No database connection available
	}
	
	// Determine collection name from request
	general := newResource.GetGeneral()
	if general.Collection != "" {
		collection = general.Collection
	} else {
		// Fallback: try to determine from URL
		collection = requestDetails.Collection
	}
	
	if collection == "" {
		return "" // Cannot determine collection
	}
	
	// Build query filter: name + version + project must match for uniqueness
	filter := bson.M{
		"general.name":    general.Name,
		"general.version": general.Version, 
		"general.project": general.Project,
	}
	
	// Fetch existing resource
	var existingResource models.DBResource
	err := db.Collection(collection).FindOne(ctx, filter).Decode(&existingResource)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			// Resource doesn't exist yet (should be POST not PUT, but handle gracefully)
			return "new_resource: true"
		}
		return "" // Database error, skip comparison
	}
	
	// Compare resource.resource fields (the actual Envoy config)
	existingResourceData := existingResource.GetResource()
	newResourceData := newResource.GetResource()
	
	// Normalize both sides by JSON serialize/deserialize to handle primitive.M/primitive.A vs map[string]any/[]any differences
	normalizedExisting, err := normalizePrimitiveTypes(existingResourceData)
	if err != nil {
		return "" // Skip comparison if normalization fails
	}
	
	normalizedNew, err := normalizePrimitiveTypes(newResourceData)
	if err != nil {
		return "" // Skip comparison if normalization fails  
	}
	
	// Use r3labs/diff for better JSON comparison and output
	changelog, err := diff.Diff(normalizedExisting, normalizedNew)
	if err != nil {
		// Fallback to go-cmp if diff fails
		if !cmp.Equal(normalizedExisting, normalizedNew) {
			return cmp.Diff(normalizedExisting, normalizedNew)
		}
		return ""
	}
	
	// If there are changes, format them as JSON
	if len(changelog) > 0 {
		return h.formatChangelogAsJSON(changelog)
	}
	
	// No changes detected
	return ""
}

// normalizePrimitiveTypes converts primitive.M/primitive.A to standard Go types via JSON marshaling
// This ensures MongoDB types and API types can be properly compared
func normalizePrimitiveTypes(data any) (any, error) {
	// Marshal to JSON to convert primitive types to standard types
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal data: %w", err)
	}
	
	// Unmarshal back to standard Go types (map[string]any, []any)
	var normalized any
	if err := json.Unmarshal(jsonBytes, &normalized); err != nil {
		return nil, fmt.Errorf("failed to unmarshal data: %w", err)
	}
	
	return normalized, nil
}

// formatChangelogAsJSON converts r3labs/diff changelog to clean JSON format
func (h *Handler) formatChangelogAsJSON(changelog diff.Changelog) string {
	changes := make([]map[string]any, 0, len(changelog))
	
	for _, change := range changelog {
		changeMap := map[string]any{
			"type": change.Type, // "create", "update", or "delete"
			"path": change.Path,
		}
		
		// Add values based on change type
		switch change.Type {
		case "create":
			changeMap["value"] = change.To
		case "update":
			changeMap["from"] = change.From
			changeMap["to"] = change.To
		case "delete":
			changeMap["value"] = change.From
		}
		
		changes = append(changes, changeMap)
	}
	
	// Create a clean JSON structure
	result := map[string]any{
		"changes": changes,
		"total":   len(changes),
	}
	
	// Marshal to JSON string
	jsonBytes, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		// Fallback to simple representation
		return fmt.Sprintf("%+v", changelog)
	}
	
	return string(jsonBytes)
}

// extractResourceNameFromBody attempts to extract resource name from request body for CREATE operations
func (h *Handler) extractResourceNameFromBody(c *gin.Context) string {
	// Get cached body from middleware
	originalBody, exists := c.Get("_original_body")
	if !exists {
		return ""
	}
	
	bodyBytes, ok := originalBody.([]byte)
	if !ok {
		return ""
	}
	
	// Try different JSON structures based on endpoint type
	path := c.Request.URL.Path
	
	if strings.Contains(path, "/api/v3/xds/") {
		// XDS resources have structure: { "general": { "name": "..." }, ... }
		var resource struct {
			General struct {
				Name string `json:"name"`
			} `json:"general"`
		}
		if err := json.Unmarshal(bodyBytes, &resource); err == nil && resource.General.Name != "" {
			return resource.General.Name
		}
		
	} else if strings.Contains(path, "/api/v3/eo/") {
		// Extension resources might have "name" at root level or "general.name"
		var resource map[string]any
		if err := json.Unmarshal(bodyBytes, &resource); err == nil {
			// Try "name" field first
			if name, ok := resource["name"].(string); ok && name != "" {
				return name
			}
			
			// Try "general.name" field
			if general, ok := resource["general"].(map[string]any); ok {
				if name, ok := general["name"].(string); ok && name != "" {
					return name
				}
			}
		}
		
	} else if strings.Contains(path, "/api/v3/setting/") {
		// Settings resources have different name fields
		var resource map[string]any
		if err := json.Unmarshal(bodyBytes, &resource); err == nil {
			// Try common name fields for settings
			nameFields := []string{"username", "groupname", "projectname", "name", "cloud_name"}
			for _, field := range nameFields {
				if name, ok := resource[field].(string); ok && name != "" {
					return name
				}
			}
		}
	}
	
	return ""
}

// getActionFromMethod maps HTTP method to audit action
func (h *Handler) getActionFromMethod(method string) string {
	switch method {
	case "POST":
		return "CREATE"
	case "PUT":
		return "UPDATE"
	case "DELETE":
		return "DELETE"
	default:
		return ""
	}
}

// Settings resource type definitions
type settingsResourceType struct {
	collection     string
	paramKey       string
	dbCollection   string
	nameField      string
	queryField     string
	useObjectID    bool
}

var settingsResourceTypes = map[string]settingsResourceType{
	"user": {
		collection:   "users",
		paramKey:     "user_id",
		dbCollection: "users",
		nameField:    "username",
		queryField:   "user_id",
		useObjectID:  false,
	},
	"group": {
		collection:   "groups", 
		paramKey:     "group_id",
		dbCollection: "groups",
		nameField:    "groupname",
		queryField:   "_id",
		useObjectID:  true,
	},
	"project": {
		collection:   "projects",
		paramKey:     "project_id", 
		dbCollection: "projects",
		nameField:    "projectname",
		queryField:   "_id",
		useObjectID:  true,
	},
	"token": {
		collection:   "api_tokens",
		paramKey:     "token_id",
		dbCollection: "settings",
		nameField:    "name",
		queryField:   "_id",
		useObjectID:  true,
	},
	"cloud": {
		collection:   "cloud_configs",
		paramKey:     "cloud_name",
		dbCollection: "clouds",
		nameField:    "name",
		queryField:   "name",
		useObjectID:  false,
	},
}

// getSettingsResourceInfo fetches actual resource names from database for Settings operations
func (h *Handler) getSettingsResourceInfo(c *gin.Context, path string) (collection, resourceID, resourceName string) {
	// Determine resource type from path
	resourceType := h.getSettingsResourceTypeFromPath(path)
	if resourceType == "" {
		return "settings", "", ""
	}
	
	// Get resource definition
	resType, exists := settingsResourceTypes[resourceType]
	if !exists {
		return h.handleSpecialSettingsTypes(c, path)
	}
	
	// Extract basic info
	collection = resType.collection
	resourceID = c.Param(resType.paramKey)
	
	// For cloud resources, name is directly in URL
	if resourceType == "cloud" {
		resourceName = resourceID
		return collection, resourceID, resourceName
	}
	
	// Fetch resource name from database
	if resourceID != "" {
		resourceName = h.fetchResourceNameFromDB(c.Request.Context(), resType, resourceID)
	}
	
	return collection, resourceID, resourceName
}

// getSettingsResourceTypeFromPath extracts resource type from URL path
func (h *Handler) getSettingsResourceTypeFromPath(path string) string {
	switch {
	case strings.Contains(path, "/user/") || strings.Contains(path, "/users/"):
		return "user"
	case strings.Contains(path, "/group/"):
		return "group"
	case strings.Contains(path, "/project/"):
		return "project"
	case strings.Contains(path, "/tokens"):
		return "token"
	case strings.Contains(path, "/clouds/"):
		return "cloud"
	default:
		return ""
	}
}

// handleSpecialSettingsTypes handles token types that don't follow standard pattern
func (h *Handler) handleSpecialSettingsTypes(c *gin.Context, path string) (collection, resourceID, resourceName string) {
	_ = c // Context not used but kept for interface consistency
	
	switch {
	case strings.Contains(path, "/openrouter-token"):
		return "openrouter_tokens", "", "openrouter_api_key"
	case strings.Contains(path, "/discovery-token"):
		return "discovery_tokens", "", "discovery_token"
	default:
		return "settings", "", ""
	}
}

// fetchResourceNameFromDB retrieves the actual resource name from database
func (h *Handler) fetchResourceNameFromDB(ctx context.Context, resType settingsResourceType, resourceID string) string {
	// Get database connection
	var db *mongo.Database
	if h.Settings != nil && h.Settings.Context != nil {
		db = h.Settings.Context.Client
	}
	if db == nil {
		return "" // No database connection available
	}
	
	// Build query
	var query bson.M
	if resType.useObjectID {
		query = bson.M{resType.queryField: parseObjectID(resourceID)}
	} else {
		query = bson.M{resType.queryField: resourceID}
	}
	
	// Execute query
	var result bson.M
	err := db.Collection(resType.dbCollection).FindOne(ctx, query).Decode(&result)
	if err != nil {
		// Log error but don't fail audit - graceful degradation
		if err != mongo.ErrNoDocuments {
			// Could add logging here if logger is available
		}
		return ""
	}
	
	// Extract name field
	if name, ok := result[resType.nameField].(string); ok {
		return name
	}
	
	// Handle pointer fields for User model
	if namePtr, ok := result[resType.nameField].(*string); ok && namePtr != nil {
		return *namePtr
	}
	
	return ""
}

// setSettingsAuditChanges handles change tracking for Settings operations
func (h *Handler) setSettingsAuditChanges(c *gin.Context, path string) {
	ctx := c.Request.Context()
	
	// Get database connection
	var db *mongo.Database
	if h.Settings != nil && h.Settings.Context != nil {
		db = h.Settings.Context.Client
	}
	if db == nil {
		return // No database available
	}
	
	// Parse new data from request body
	originalBody, exists := c.Get("_original_body")
	if !exists {
		return
	}
	
	bodyBytes, ok := originalBody.([]byte)
	if !ok {
		return
	}
	
	// Determine resource type and get comparison
	resourceType := h.getSettingsResourceTypeFromPath(path)
	if resourceType == "" {
		return
	}
	
	diff := h.compareSettingsResourceChanges(ctx, db, resourceType, bodyBytes, c)
	if diff != "" {
		audit.SetAuditChanges(c, map[string]any{
			"diff": diff,
		})
	}
}

// compareSettingsResourceChanges handles comparison for all Settings resource types
func (h *Handler) compareSettingsResourceChanges(ctx context.Context, db *mongo.Database, resourceType string, bodyBytes []byte, c *gin.Context) string {
	switch resourceType {
	case "user":
		return h.compareUserChanges(ctx, db, bodyBytes, c.Param("user_id"))
	case "group":
		return h.compareGroupChanges(ctx, db, bodyBytes, c.Param("group_id"))
	case "project":
		return h.compareProjectChanges(ctx, db, bodyBytes, c.Param("project_id"))
	case "token":
		return h.compareTokenChanges(ctx, db, bodyBytes, c.Param("token_id"))
	case "cloud":
		return h.compareCloudChanges(ctx, db, bodyBytes, c.Param("cloud_name"))
	default:
		return ""
	}
}

// parseObjectID safely converts string to ObjectID  
func parseObjectID(id string) any {
	if objID, err := primitive.ObjectIDFromHex(id); err == nil {
		return objID
	}
	return id // Return as string if conversion fails
}

// compareUserChanges compares new user data with existing user in database
func (h *Handler) compareUserChanges(ctx context.Context, db *mongo.Database, bodyBytes []byte, userID string) string {
	var newUser models.User
	if err := json.Unmarshal(bodyBytes, &newUser); err != nil {
		return ""
	}
	
	// Fetch existing user
	var existingUser models.User
	err := db.Collection("users").FindOne(ctx, bson.M{"user_id": userID}).Decode(&existingUser)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return "new_user: true"
		}
		return ""
	}
	
	// Compare relevant fields (excluding passwords and tokens for security)
	existingData := h.extractUserData(existingUser)
	newData := h.extractUserData(newUser)
	
	if !cmp.Equal(existingData, newData) {
		return cmp.Diff(existingData, newData)
	}
	return ""
}

// extractUserData extracts comparable user data (helper to eliminate duplication)
func (h *Handler) extractUserData(user models.User) map[string]any {
	return map[string]any{
		"username":     getStringOrEmpty(user.Username),
		"email":        getStringOrEmpty(user.Email),
		"role":         getRoleOrDefault(user.Role),
		"active":       getBoolOrDefault(user.Active),
		"base_group":   getStringOrEmpty(user.BaseGroup),
		"base_project": getStringOrEmpty(user.BaseProject),
	}
}

// compareGroupChanges compares new group data with existing group in database
func (h *Handler) compareGroupChanges(ctx context.Context, db *mongo.Database, bodyBytes []byte, groupID string) string {
	var newGroup bson.M
	if err := json.Unmarshal(bodyBytes, &newGroup); err != nil {
		return ""
	}
	
	// Fetch existing group
	var existingGroup bson.M
	err := db.Collection("groups").FindOne(ctx, bson.M{"_id": parseObjectID(groupID)}).Decode(&existingGroup)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return "new_group: true"
		}
		return ""
	}
	
	// Remove sensitive fields and metadata
	cleanExisting := cleanGroupData(existingGroup)
	cleanNew := cleanGroupData(newGroup)
	
	// Normalize both sides to handle primitive types
	normalizedExisting, err := normalizePrimitiveTypes(cleanExisting)
	if err != nil {
		return ""
	}
	normalizedNew, err := normalizePrimitiveTypes(cleanNew)
	if err != nil {
		return ""
	}
	
	// Use r3labs/diff for better JSON comparison
	changelog, err := diff.Diff(normalizedExisting, normalizedNew)
	if err != nil {
		// Fallback to go-cmp if diff fails
		if !cmp.Equal(normalizedExisting, normalizedNew) {
			return cmp.Diff(normalizedExisting, normalizedNew)
		}
	}
	
	if len(changelog) > 0 {
		return h.formatChangelogAsJSON(changelog)
	}
	return ""
}

// compareProjectChanges compares new project data with existing project in database
func (h *Handler) compareProjectChanges(ctx context.Context, db *mongo.Database, bodyBytes []byte, projectID string) string {
	var newProject bson.M
	if err := json.Unmarshal(bodyBytes, &newProject); err != nil {
		return ""
	}
	
	// Fetch existing project
	var existingProject bson.M
	err := db.Collection("projects").FindOne(ctx, bson.M{"_id": parseObjectID(projectID)}).Decode(&existingProject)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return "new_project: true"
		}
		return ""
	}
	
	// Remove sensitive fields and metadata
	cleanExisting := cleanProjectData(existingProject)
	cleanNew := cleanProjectData(newProject)
	
	// Normalize both sides to handle primitive types
	normalizedExisting, err := normalizePrimitiveTypes(cleanExisting)
	if err != nil {
		return ""
	}
	normalizedNew, err := normalizePrimitiveTypes(cleanNew)
	if err != nil {
		return ""
	}
	
	// Use r3labs/diff for better JSON comparison
	changelog, err := diff.Diff(normalizedExisting, normalizedNew)
	if err != nil {
		// Fallback to go-cmp if diff fails
		if !cmp.Equal(normalizedExisting, normalizedNew) {
			return cmp.Diff(normalizedExisting, normalizedNew)
		}
	}
	
	if len(changelog) > 0 {
		return h.formatChangelogAsJSON(changelog)
	}
	return ""
}

// compareTokenChanges handles token changes - tokens are typically regenerated rather than modified
func (h *Handler) compareTokenChanges(ctx context.Context, db *mongo.Database, bodyBytes []byte, tokenID string) string {
	// For security reasons, we don't track token content changes
	// Only track that a token operation occurred
	_ = ctx      // Context not used but kept for interface consistency
	_ = db       // Database not used but kept for interface consistency  
	_ = bodyBytes // Body not used but kept for interface consistency
	_ = tokenID  // ID not used but kept for interface consistency
	
	return "token_operation: true"
}

// compareCloudChanges compares new cloud config data with existing cloud config
func (h *Handler) compareCloudChanges(ctx context.Context, db *mongo.Database, bodyBytes []byte, cloudName string) string {
	var newCloud bson.M
	if err := json.Unmarshal(bodyBytes, &newCloud); err != nil {
		return ""
	}
	
	// Fetch existing cloud config
	var existingCloud bson.M
	err := db.Collection("clouds").FindOne(ctx, bson.M{"name": cloudName}).Decode(&existingCloud)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return "new_cloud: true"
		}
		return ""
	}
	
	// Remove sensitive fields (passwords, keys) and metadata
	cleanExisting := cleanCloudData(existingCloud)
	cleanNew := cleanCloudData(newCloud)
	
	// Normalize both sides to handle primitive types
	normalizedExisting, err := normalizePrimitiveTypes(cleanExisting)
	if err != nil {
		return ""
	}
	normalizedNew, err := normalizePrimitiveTypes(cleanNew)
	if err != nil {
		return ""
	}
	
	// Use r3labs/diff for better JSON comparison
	changelog, err := diff.Diff(normalizedExisting, normalizedNew)
	if err != nil {
		// Fallback to go-cmp if diff fails
		if !cmp.Equal(normalizedExisting, normalizedNew) {
			return cmp.Diff(normalizedExisting, normalizedNew)
		}
	}
	
	if len(changelog) > 0 {
		return h.formatChangelogAsJSON(changelog)
	}
	return ""
}

// ================== HELPER FUNCTIONS ==================

// Safe pointer dereference helpers
func getStringOrEmpty(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return *ptr
}

func getRoleOrDefault(ptr *models.Role) string {
	if ptr == nil {
		return "viewer"
	}
	return string(*ptr)
}

func getBoolOrDefault(ptr *bool) bool {
	if ptr == nil {
		return false
	}
	return *ptr
}

// Data cleaning functions for different resource types
func cleanGroupData(group bson.M) map[string]any {
	return map[string]any{
		"groupname":   group["groupname"],
		"description": group["description"],
		"project":     group["project"],
		"members":     group["members"],
	}
}

func cleanProjectData(project bson.M) map[string]any {
	return map[string]any{
		"projectname": project["projectname"],
		"description": project["description"],
		"members":     project["members"],
		"is_default":  project["is_default"],
	}
}

// Sensitive field patterns for cloud data cleaning
var sensitiveFieldPatterns = []string{
	"password", "key", "secret", "token", 
	"_id", "created_at", "updated_at",
}

func cleanCloudData(cloud bson.M) map[string]any {
	cleaned := make(map[string]any)
	for key, value := range cloud {
		// Skip sensitive fields
		if isSensitiveField(key) {
			continue
		}
		cleaned[key] = value
	}
	return cleaned
}

// isSensitiveField checks if a field contains sensitive data
func isSensitiveField(fieldName string) bool {
	lowerFieldName := strings.ToLower(fieldName)
	for _, pattern := range sensitiveFieldPatterns {
		if strings.Contains(lowerFieldName, pattern) {
			return true
		}
	}
	return false
}