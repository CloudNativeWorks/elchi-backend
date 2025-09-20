package handlers

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/go-cmp/cmp"
	"github.com/r3labs/diff/v3"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/pkg/audit"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
)

type ResourceTemplateHandler struct {
	AppContext *db.AppContext
	Handler    *Handler // Main handler for audit access
}

func NewResourceTemplateHandler(appContext *db.AppContext, handler *Handler) *ResourceTemplateHandler {
	return &ResourceTemplateHandler{
		AppContext: appContext,
		Handler:    handler, // Can be nil initially, set later to avoid circular dependency
	}
}

// CheckTemplateExists checks if a template exists for given gtype, version, and project
// GET /api/v3/templates/check/:gtype?project={project_id}&version={version}
func (h *ResourceTemplateHandler) CheckTemplateExists(c *gin.Context) {
	gtype := c.Param("gtype")
	project := c.Query("project")
	version := c.Query("version")

	if gtype == "" || project == "" || version == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "gtype, project, and version are required",
		})
		return
	}

	collection := h.AppContext.Client.Collection("resource_templates")
	filter := bson.M{
		"gtype":   gtype,
		"project": project,
		"version": version,
	}

	var template models.ResourceTemplate
	err := collection.FindOne(context.Background(), filter).Decode(&template)

	exists := err == nil
	c.JSON(http.StatusOK, gin.H{
		"exists": exists,
	})
}

// GetTemplate retrieves a template for given gtype, version, and project
// GET /api/v3/templates/:gtype?project={project_id}&version={version}
func (h *ResourceTemplateHandler) GetTemplate(c *gin.Context) {
	gtype := c.Param("gtype")
	project := c.Query("project")
	version := c.Query("version")

	if gtype == "" || project == "" || version == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "gtype, project, and version are required",
		})
		return
	}

	// Check user permissions (read access)
	if !h.hasReadPermission(c, project) {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"error":   "Insufficient permissions",
		})
		return
	}

	collection := h.AppContext.Client.Collection("resource_templates")
	filter := bson.M{
		"gtype":   gtype,
		"project": project,
		"version": version,
	}

	var template models.ResourceTemplate
	err := collection.FindOne(context.Background(), filter).Decode(&template)

	if err != nil {
		if err == mongo.ErrNoDocuments {
			c.JSON(http.StatusNotFound, gin.H{
				"success": false,
				"error":   "Template not found",
			})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{
				"success": false,
				"error":   "Failed to retrieve template",
			})
		}
		return
	}

	// Return template without exposing internal _id field for security
	response := models.ResourceTemplateResponse{
		GType:    template.GType,
		Version:  template.Version,
		Project:  template.Project,
		General:  template.General,
		Resource: template.Resource,
	}

	c.JSON(http.StatusOK, response)
}

// CreateOrUpdateTemplate creates a new template or updates existing one (upsert)
// POST /api/v3/templates/:gtype?project={project_id}&version={version}
func (h *ResourceTemplateHandler) CreateOrUpdateTemplate(c *gin.Context) {
	gtype := c.Param("gtype")
	project := c.Query("project")
	version := c.Query("version")

	if gtype == "" || project == "" || version == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "gtype, project, and version are required",
		})
		return
	}

	// Check user permissions (owner or admin)
	if !h.hasWritePermission(c, project) {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"error":   "Insufficient permissions - only owners and admins can create templates",
		})
		return
	}

	var requestBody models.ResourceTemplateRequest
	if err := c.ShouldBindJSON(&requestBody); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "Invalid request body: " + err.Error(),
		})
		return
	}

	// Filter out dynamic fields for bootstrap templates
	if gtype == "envoy.config.bootstrap.v3.Bootstrap" {
		requestBody.Resource = h.filterBootstrapDynamicFields(requestBody.Resource)
	}

	// Create temporary DBResource to decode typed configs (same as XDS)
	tempResource := &models.DBResource{
		General: models.General{
			GType: models.GType(gtype),
		},
		Resource: models.Resource{
			Resource: requestBody.Resource,
		},
	}

	// Decode typed configs from resource (same as XDS update)
	typedConfigs := resources.DecodeSetTypedConfigs(tempResource, h.AppContext.Logger)

	// Override general.typed_config with decoded values
	if requestBody.General == nil {
		requestBody.General = &models.TemplateGeneral{}
	}
	requestBody.General.TypedConfig = typedConfigs

	collection := h.AppContext.Client.Collection("resource_templates")
	filter := bson.M{
		"gtype":   gtype,
		"project": project,
		"version": version,
	}

	// Check if template exists for comparison
	var existingTemplate models.ResourceTemplate
	err := collection.FindOne(context.Background(), filter).Decode(&existingTemplate)
	isCreate := err == mongo.ErrNoDocuments

	var diffString string
	auditAction := "TEMPLATE_CREATE"

	if !isCreate {
		// Template exists, compare for changes
		auditAction = "TEMPLATE_UPDATE"
		diffString = h.compareTemplates(&existingTemplate, &models.ResourceTemplate{
			GType:    gtype,
			Project:  project,
			Version:  version,
			General:  requestBody.General,
			Resource: requestBody.Resource,
		})

		// If no changes, don't update
		if diffString == "" {
			c.JSON(http.StatusOK, gin.H{
				"success": true,
				"message": "No changes detected",
			})
			return
		}
	}

	// Use upsert to create or update
	// SECURITY: Client cannot set _id field - ResourceTemplateRequest struct excludes it
	// MongoDB will auto-generate _id for new documents
	update := bson.M{
		"$set": bson.M{
			"gtype":    gtype,
			"project":  project,
			"version":  version,
			"general":  requestBody.General,
			"resource": requestBody.Resource,
		},
	}

	result, err := collection.UpdateOne(context.Background(), filter, update,
		options.Update().SetUpsert(true))

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "Failed to create or update template: " + err.Error(),
		})
		return
	}

	var message string
	var statusCode int
	if result.UpsertedCount > 0 || isCreate {
		message = "Template created successfully"
		statusCode = http.StatusCreated
	} else {
		message = "Template updated successfully"
		statusCode = http.StatusOK
	}

	// Set audit context using existing pattern
	h.setTemplateAuditContext(c, auditAction, gtype, project, version)
	if diffString != "" {
		h.setTemplateAuditChanges(c, diffString)
	}

	c.JSON(statusCode, gin.H{
		"success": true,
		"message": message,
	})
}

// DeleteTemplate deletes a template
// DELETE /api/v3/templates/:gtype?project={project_id}&version={version}
func (h *ResourceTemplateHandler) DeleteTemplate(c *gin.Context) {
	gtype := c.Param("gtype")
	project := c.Query("project")
	version := c.Query("version")

	if gtype == "" || project == "" || version == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "gtype, project, and version are required",
		})
		return
	}

	// Check user permissions (owner or admin)
	if !h.hasWritePermission(c, project) {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"error":   "Insufficient permissions - only owners and admins can delete templates",
		})
		return
	}

	collection := h.AppContext.Client.Collection("resource_templates")
	filter := bson.M{
		"gtype":   gtype,
		"project": project,
		"version": version,
	}

	result, err := collection.DeleteOne(context.Background(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "Failed to delete template: " + err.Error(),
		})
		return
	}

	if result.DeletedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"error":   "Template not found",
		})
		return
	}

	// Set audit context for deletion
	h.setTemplateAuditContext(c, "TEMPLATE_DELETE", gtype, project, version)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Template deleted successfully",
	})
}

// hasReadPermission checks if user has read permission for the project
func (h *ResourceTemplateHandler) hasReadPermission(c *gin.Context, _ string) bool {
	userDetails, err := GetUserDetails(c)
	if err != nil {
		h.AppContext.Logger.Errorf("Template permission check: failed to get user details: %v", err)
		return false
	}

	// Owner can access all projects
	if userDetails.IsOwner {
		return true
	}

	// Admin can access all projects
	if userDetails.Role == models.RoleAdmin {
		return true
	}

	// Editors and viewers can read templates
	if userDetails.Role == models.RoleEditor || userDetails.Role == models.RoleViewer {
		return true
	}

	return false
}

// hasWritePermission checks if user has write permission for the project
func (h *ResourceTemplateHandler) hasWritePermission(c *gin.Context, _ string) bool {
	userDetails, err := GetUserDetails(c)
	if err != nil {
		h.AppContext.Logger.Errorf("Template permission check: failed to get user details: %v", err)
		return false
	}

	// Debug logging
	h.AppContext.Logger.Infof("Template permission check: user=%s, role=%s, isOwner=%t",
		userDetails.UserName, string(userDetails.Role), userDetails.IsOwner)

	// Owner can write to all projects
	if userDetails.IsOwner {
		h.AppContext.Logger.Infof("Template permission granted: user is owner")
		return true
	}

	// Admin can write to all projects
	if userDetails.Role == models.RoleAdmin {
		h.AppContext.Logger.Infof("Template permission granted: user is admin")
		return true
	}

	// Editors and viewers cannot create/update/delete templates
	h.AppContext.Logger.Infof("Template permission denied: user role not sufficient")
	return false
}

// compareTemplates compares two templates and returns detailed diff string using existing audit pattern
func (h *ResourceTemplateHandler) compareTemplates(oldTemplate, newTemplate *models.ResourceTemplate) string {
	if h.Handler == nil {
		return ""
	}

	// Create comparison objects without ID field to avoid false positive changes
	oldComparison := struct {
		GType    string                  `json:"gtype"`
		Version  string                  `json:"version"`
		Project  string                  `json:"project"`
		General  *models.TemplateGeneral `json:"general,omitempty"`
		Resource any                     `json:"resource,omitempty"`
	}{
		GType:    oldTemplate.GType,
		Version:  oldTemplate.Version,
		Project:  oldTemplate.Project,
		General:  oldTemplate.General,
		Resource: oldTemplate.Resource,
	}

	newComparison := struct {
		GType    string                  `json:"gtype"`
		Version  string                  `json:"version"`
		Project  string                  `json:"project"`
		General  *models.TemplateGeneral `json:"general,omitempty"`
		Resource any                     `json:"resource,omitempty"`
	}{
		GType:    newTemplate.GType,
		Version:  newTemplate.Version,
		Project:  newTemplate.Project,
		General:  newTemplate.General,
		Resource: newTemplate.Resource,
	}

	// First normalize the templates using existing normalizePrimitiveTypes (excluding ID)
	normalizedOld, err := normalizePrimitiveTypes(oldComparison)
	if err != nil {
		return ""
	}

	normalizedNew, err := normalizePrimitiveTypes(newComparison)
	if err != nil {
		return ""
	}

	// Use r3labs/diff for better JSON comparison and output (same as existing audit system)
	changelog, err := diff.Diff(normalizedOld, normalizedNew)
	if err != nil {
		// Fallback to go-cmp if diff fails (same as existing audit system)
		if !cmp.Equal(normalizedOld, normalizedNew) {
			return cmp.Diff(normalizedOld, normalizedNew)
		}
		return ""
	}

	// If there are changes, format them as JSON using existing Handler method
	if len(changelog) > 0 {
		return h.Handler.formatChangelogAsJSON(changelog)
	}

	return ""
}

// setTemplateAuditContext sets audit context for template operations using existing audit pattern
func (h *ResourceTemplateHandler) setTemplateAuditContext(c *gin.Context, action, gtype, project, version string) {
	if h.Handler == nil || h.Handler.AuditService == nil {
		return
	}

	// Use existing audit service methods from context
	audit.SetAuditAction(c, action)
	resourceName := fmt.Sprintf("%s (%s)", gtype, version)
	audit.SetAuditResource(c, "resource_templates", "", resourceName, project)
}

// setTemplateAuditChanges sets audit changes for template operations using existing pattern
func (h *ResourceTemplateHandler) setTemplateAuditChanges(c *gin.Context, diffString string) {
	if h.Handler == nil || h.Handler.AuditService == nil {
		return
	}

	// Use existing audit service method from context - same format as other handlers
	audit.SetAuditChanges(c, map[string]any{
		"diff": diffString,
	})
}

// filterBootstrapDynamicFields removes dynamic fields from bootstrap resource before saving as template
func (h *ResourceTemplateHandler) filterBootstrapDynamicFields(resource any) any {
	if resource == nil {
		return resource
	}

	// Convert to map for manipulation
	resourceMap, ok := resource.(map[string]any)
	if !ok {
		return resource
	}

	// Create a deep copy to avoid modifying the original
	filtered := make(map[string]any)
	for k, v := range resourceMap {
		filtered[k] = v
	}

	// 1. Remove admin.address completely (keep admin.access_log)
	if adminInterface, exists := filtered["admin"]; exists {
		if adminMap, ok := adminInterface.(map[string]any); ok {
			// Create new admin map without address
			newAdmin := make(map[string]any)
			for k, v := range adminMap {
				if k != "address" {
					newAdmin[k] = v
				}
			}
			// Only keep admin if it has other fields, otherwise remove completely
			if len(newAdmin) > 0 {
				filtered["admin"] = newAdmin
			} else {
				delete(filtered, "admin")
			}
		}
	}

	// 2. Remove dynamic_resources completely
	delete(filtered, "dynamic_resources")

	// 3. Remove node.cluster and node.id (keep other node fields)
	if nodeInterface, exists := filtered["node"]; exists {
		if nodeMap, ok := nodeInterface.(map[string]any); ok {
			// Create new node map without cluster and id
			newNode := make(map[string]any)
			for k, v := range nodeMap {
				if k != "cluster" && k != "id" {
					newNode[k] = v
				}
			}
			// Only keep node if it has other fields, otherwise remove completely
			if len(newNode) > 0 {
				filtered["node"] = newNode
			} else {
				delete(filtered, "node")
			}
		}
	}

	return filtered
}
