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
	cmdFRRLogs         = "FRR_LOGS"
)

// Read-only command subtypes that should not be audited
var readOnlySubTypes = map[string]map[string]bool{
	cmdService: {
		"SUB_STATUS": true,
		"SUB_LOGS":   true,
	},
	cmdNetwork: {
		"SUB_NETPLAN_GET":       true,
		"SUB_ROUTE_LIST":        true,
		"SUB_POLICY_LIST":       true,
		"SUB_TABLE_LIST":        true,
		"SUB_GET_NETWORK_STATE": true,
	},
}

// Read-only BGP operations that should not be audited (based on frr.proto)
var readOnlyBGPOperations = map[string]bool{
	"BGP_GET_CONFIG":        true,
	"BGP_LIST_NEIGHBORS":    true,
	"BGP_GET_NEIGHBOR":      true,
	"BGP_GET_POLICY_CONFIG": true,
	"BGP_SHOW_ROUTES":       true,
	"BGP_GET_SUMMARY":       true,
}

// Read-only Envoy Version operations that should not be audited (based on request.proto)
var readOnlyEnvoyVersionOperations = map[string]bool{
	"GET_VERSIONS": true, // List locally downloaded versions
}

// mapCommandTypeToAction maps command type/subtype combinations to audit actions
// Returns empty string if command should not be audited
// KEEP IN: audit.go (shared utility)
func mapCommandTypeToAction(cmdType, subType string) string {
	// Check if this is a read-only operation
	if readOnlyTypes, exists := readOnlySubTypes[cmdType]; exists {
		if readOnlyTypes[subType] {
			return "" // Don't audit read operations
		}
	}

	// Handle monitoring/logging commands
	if cmdType == cmdClientLogs || cmdType == cmdClientStats || cmdType == cmdFRRLogs {
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
		// Action will be updated later in setAuditResult if this is a CREATE operation

	case isScenario:
		collection = "scenarios"
		if strings.Contains(path, "/scenarios/") {
			resourceID = c.Param("scenario_id")
			// For DELETE and PUT operations, try to get scenario name from database
			if (c.Request.Method == "DELETE" || c.Request.Method == "PUT") && resourceID != "" {
				resourceName = h.getScenarioNameFromID(c, resourceID)
			}
		} else if strings.Contains(path, "/execute") {
			action = "EXECUTE_SCENARIO"
			// For execute, try to get scenario ID and name from request body
			if c.Request.Method == "POST" {
				resourceID, resourceName = h.extractScenarioInfoFromExecuteRequest(c)
			}
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

	// Handle DELETE method specially (client deletion)
	if c.Request.Method == "DELETE" {
		h.setClientDeleteAuditContext(c)
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

	// Additional check for FRR BGP read-only operations
	if op.GetType() == cmdFRR {
		if bgpOp := h.extractBGPOperationFromRequest(c); bgpOp != "" {
			if readOnlyBGPOperations[bgpOp] {
				return // Skip auditing for read-only BGP operations
			}
		}
	}

	// Additional check for ENVOY_VERSION read-only operations
	if op.GetType() == cmdEnvoyVersion {
		if envoyOp := h.extractEnvoyVersionOperationFromRequest(c); envoyOp != "" {
			if readOnlyEnvoyVersionOperations[envoyOp] {
				return // Skip auditing for read-only ENVOY_VERSION operations
			}
		}
	}

	// Additional check for PROXY operations - only audit if queries field exists (config changes)
	if op.GetType() == cmdProxy {
		if !h.hasProxyQueriesInRequest(c) {
			return // Skip auditing for PROXY operations without queries (read-only admin calls)
		}
		// If queries exist, it's a config change (like log level), so continue with audit
	}

	// Get project from command or fallback to query parameter
	project := op.GetCommandProject()
	if project == "" {
		project = c.Query("project")
	}

	audit.SetAuditAction(c, action)
	audit.SetAuditResource(c, "client_command", op.GetCommandName(), op.GetCommandName(), project)

	// Set original request body as command details for audit
	if originalBody, exists := c.Get("_original_body"); exists {
		if bodyBytes, ok := originalBody.([]byte); ok {
			var requestBody map[string]any
			if err := json.Unmarshal(bodyBytes, &requestBody); err == nil {
				audit.SetAuditCommand(c, requestBody)
			}
		}
	}
}

// setAuditResult sets audit result (success/error) at the end of request processing
func (h *Handler) setAuditResult(c *gin.Context, err error) {
	if h.AuditService == nil {
		return
	}

	// Update action for settings operations if changes indicate CREATE
	h.updateSettingsActionIfNeeded(c)

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

	// Handle Scenario resources
	if strings.Contains(path, "/api/v3/scenario/") {
		if strings.Contains(path, "/execute") {
			h.setScenarioExecuteAuditChanges(c)
		} else {
			h.setScenarioAuditChanges(c, path)
		}
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
	db := h.getDatabaseConnection()
	if db == nil {
		return "" // No database connection available
	}
	
	var collection string

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
// KEEP IN: audit.go (shared utility)
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

	} else if strings.Contains(path, "/api/v3/scenario/") {
		// Scenario resources have structure: { "name": "...", ... }
		var resource struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(bodyBytes, &resource); err == nil && resource.Name != "" {
			return resource.Name
		}

		// Also try generic map approach for flexibility
		var resourceMap map[string]any
		if err := json.Unmarshal(bodyBytes, &resourceMap); err == nil {
			if name, ok := resourceMap["name"].(string); ok && name != "" {
				return name
			}
		}
	}

	return ""
}

// setBridgeAuditContext sets audit context for bridge operations (snapshot management)
func (h *Handler) setBridgeAuditContext(c *gin.Context) {
	if h.AuditService == nil {
		return
	}

	path := c.Request.URL.Path

	// Check if this is a bridge endpoint that should be audited
	if !strings.Contains(path, "/api/v3/bridge/") {
		return
	}

	// Determine action based on HTTP method and endpoint
	var action, resourceType, resourceName string

	if strings.Contains(path, "/snapshot") {
		resourceType = "snapshot"
		resourceName = c.Param("nodeID") // Extract node ID as resource identifier
		
		switch c.Request.Method {
		case "GET":
			action = "GET_SNAPSHOT"
		case "DELETE":
			action = "DELETE_SNAPSHOT"
		}
	}

	if action == "" {
		return
	}

	// Extract project from query parameter if available
	project := c.Query("project")

	audit.SetAuditAction(c, action)
	audit.SetAuditResource(c, resourceType, resourceName, resourceName, project)
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

// setLDAPAuditContext sets audit context specifically for LDAP operations
func (h *Handler) setLDAPAuditContext(c *gin.Context, requestDetails models.RequestDetails) {
	if h.AuditService == nil {
		return
	}

	path := c.Request.URL.Path

	// Only process LDAP-related endpoints
	if !strings.Contains(path, "/ldap-config") {
		return
	}

	// Skip audit for test endpoints and GET operations
	if strings.Contains(path, "/test") || c.Request.Method == "GET" {
		return
	}

	// Determine action based on HTTP method
	action := ""
	switch c.Request.Method {
	case "POST":
		action = "CREATE_LDAP_CONFIG"
	case "PUT":
		action = "UPDATE_LDAP_CONFIG"
	case "DELETE":
		action = "DELETE_LDAP_CONFIG"
	}

	if action == "" {
		return
	}

	// Set LDAP-specific audit context
	audit.SetAuditResource(c, "ldap", "", "ldap-config", requestDetails.Project)
	audit.SetAuditAction(c, action)
}
