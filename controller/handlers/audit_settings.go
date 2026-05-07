package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/controller/waf"
	"github.com/CloudNativeWorks/elchi-backend/pkg/audit"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/gin-gonic/gin"
	"github.com/google/go-cmp/cmp"
	"github.com/r3labs/diff/v3"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// ================== SETTINGS AUDIT HANDLERS ==================
//
// This file contains audit functionality for Settings operations:
// - User, Group, Project, Token, Cloud management audit
// - Settings resource type definitions and mappings
// - Settings-specific change comparison functions
//

// updateSettingsActionIfNeeded updates audit action for settings operations after changes are analyzed
func (h *Handler) updateSettingsActionIfNeeded(c *gin.Context) {
	path := c.Request.URL.Path

	// Only process settings endpoints
	if !strings.Contains(path, "/api/v3/setting/") {
		return
	}

	// Only process PUT requests (potential CREATE operations)
	if c.Request.Method != http.MethodPut {
		return
	}

	// Get resource type
	resourceType := h.getSettingsResourceTypeFromPath(path)
	if resourceType != "user" && resourceType != "group" && resourceType != "project" {
		return
	}

	// Check if changes indicate this is a CREATE operation
	if auditEntry := audit.GetAuditEntry(c); auditEntry != nil && auditEntry.Changes != nil {
		if changes, ok := auditEntry.Changes["diff"].(string); ok {
			shouldCreate := false
			switch resourceType {
			case "user":
				if strings.Contains(changes, "new_user: true") {
					shouldCreate = true
				}
			case "group":
				if strings.Contains(changes, "new_group: true") {
					shouldCreate = true
				}
			case "project":
				if strings.Contains(changes, "new_project: true") {
					shouldCreate = true
				}
			}

			if shouldCreate {
				audit.SetAuditAction(c, "CREATE")
			}
		}
	}
}

// Settings resource type definitions
type settingsResourceType struct {
	collection   string
	paramKey     string
	dbCollection string
	nameField    string
	queryField   string
	useObjectID  bool
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
		collection:   "client_token",
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
	case strings.Contains(path, "/openrouter-token"):
		return "openrouter_token" // Special case for AI tokens
	case strings.Contains(path, "/discovery-token"):
		return "discovery_token" // Special case for discovery tokens
	default:
		return ""
	}
}

// handleSpecialSettingsTypes handles token types that don't follow standard pattern
func (h *Handler) handleSpecialSettingsTypes(c *gin.Context, path string) (collection, resourceID, resourceName string) {
	_ = c // Context not used but kept for interface consistency

	switch {
	case strings.Contains(path, "/openrouter-token"):
		return "ai_token", "", "openrouter_api_key"
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
		// Graceful degradation - return empty on any error (including ErrNoDocuments)
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
		if errors.Is(err, mongo.ErrNoDocuments) {
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
		if errors.Is(err, mongo.ErrNoDocuments) {
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
		if errors.Is(err, mongo.ErrNoDocuments) {
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
	_ = ctx       // Context not used but kept for interface consistency
	_ = db        // Database not used but kept for interface consistency
	_ = bodyBytes // Body not used but kept for interface consistency
	_ = tokenID   // ID not used but kept for interface consistency

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
		if errors.Is(err, mongo.ErrNoDocuments) {
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

// extractUserData extracts comparable user data (helper to eliminate duplication)
func (h *Handler) extractUserData(user models.User) map[string]any {
	return map[string]any{
		"username":     getStringOrEmpty(user.Username),
		"email":        maskEmail(getStringOrEmpty(user.Email)), // Mask PII
		"role":         getRoleOrDefault(user.Role),
		"active":       getBoolOrDefault(user.Active),
		"base_group":   getStringOrEmpty(user.BaseGroup),
		"base_project": getStringOrEmpty(user.BaseProject),
	}
}

// maskEmail masks email addresses for audit security (PII protection)
func maskEmail(email string) string {
	if email == "" {
		return ""
	}

	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		// Invalid email format, mask the whole string
		if len(email) <= 3 {
			return strings.Repeat("*", len(email))
		}
		return email[:1] + strings.Repeat("*", len(email)-2) + email[len(email)-1:]
	}

	username := parts[0]
	domain := parts[1]

	// Mask username part
	var maskedUsername string
	switch {
	case len(username) <= 1:
		maskedUsername = "*"
	case len(username) <= 3:
		maskedUsername = username[:1] + strings.Repeat("*", len(username)-1)
	default:
		maskedUsername = username[:1] + strings.Repeat("*", len(username)-2) + username[len(username)-1:]
	}

	// Mask domain part
	maskedDomain := ""
	domainParts := strings.Split(domain, ".")
	for i, part := range domainParts {
		if i > 0 {
			maskedDomain += "."
		}
		switch {
		case len(part) <= 1:
			maskedDomain += "*"
		case len(part) <= 3:
			maskedDomain += part[:1] + strings.Repeat("*", len(part)-1)
		default:
			maskedDomain += part[:1] + strings.Repeat("*", len(part)-2) + part[len(part)-1:]
		}
	}

	return maskedUsername + "@" + maskedDomain
}

// Sensitive field patterns for cloud data cleaning
var sensitiveFieldPatterns = []string{
	"password", "key", "secret", "token",
	"_id", "created_at", "updated_at",
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

// ================== WAF AUDIT HANDLERS ==================

// getWAFConfigNameFromID fetches WAF config name from database by ID
func (h *Handler) getWAFConfigNameFromID(c *gin.Context, configID string) string {
	db := h.getDatabaseConnection()
	if db == nil {
		return ""
	}

	objID, err := primitive.ObjectIDFromHex(configID)
	if err != nil {
		return ""
	}

	var config struct {
		Name string `bson:"name"`
	}

	ctx := c.Request.Context()
	err = db.Collection("waf").FindOne(ctx, bson.M{"_id": objID}).Decode(&config)
	if err != nil {
		return ""
	}

	return config.Name
}

// setWAFAuditChanges handles change comparison for WAF config updates.
//
// Both sides of the diff are routed through the canonical waf.WAFConfigData
// (Un)Marshal hooks: the request body via UnmarshalJSON (which accepts either
// legacy or modern wire shape during the transition), and the existing DB doc
// via UnmarshalBSON. Both are then re-marshalled through MarshalJSON so the
// diff library compares apples-to-apples in the modern shape — without this
// canonicalisation, a legacy-on-disk doc compared against a modern-wire body
// would report the entire data subtree as changed.
func (h *Handler) setWAFAuditChanges(ctx context.Context, db *mongo.Database, bodyBytes []byte, configID string) string {
	var newReq waf.WAFConfigRequest
	if err := json.Unmarshal(bodyBytes, &newReq); err != nil {
		return ""
	}

	objID, err := primitive.ObjectIDFromHex(configID)
	if err != nil {
		return ""
	}

	var existing waf.WAFConfig
	err = db.Collection("waf").FindOne(ctx, bson.M{"_id": objID}).Decode(&existing)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return "new_waf_config: true"
		}
		return ""
	}

	newSide := map[string]any{
		"name":    newReq.Name,
		"project": newReq.Project,
		"data":    newReq.Data,
	}
	existingSide := map[string]any{
		"name":    existing.Name,
		"project": existing.Project,
		"data":    existing.Data,
	}

	// Round-trip through MarshalJSON → map[string]any so both sides share the
	// modern shape (sorted Sets[]) and primitive types diff cleanly.
	normalizedNew, err := normaliseViaJSON(newSide)
	if err != nil {
		return ""
	}
	normalizedExisting, err := normaliseViaJSON(existingSide)
	if err != nil {
		return ""
	}

	changelog, err := diff.Diff(normalizedExisting, normalizedNew)
	if err != nil {
		if !cmp.Equal(normalizedExisting, normalizedNew) {
			return cmp.Diff(normalizedExisting, normalizedNew)
		}
	}

	if len(changelog) > 0 {
		return h.formatChangelogAsJSON(changelog)
	}
	return ""
}

// normaliseViaJSON marshals via the canonical MarshalJSON hooks then
// unmarshals into a plain map. This guarantees both sides of an audit diff
// share the same shape and primitive types.
func normaliseViaJSON(side map[string]any) (map[string]any, error) {
	b, err := json.Marshal(side)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}
