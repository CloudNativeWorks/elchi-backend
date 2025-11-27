package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/controller/api/settings"
	"github.com/CloudNativeWorks/elchi-backend/controller/bridge"
	"github.com/CloudNativeWorks/elchi-backend/controller/client"
	"github.com/CloudNativeWorks/elchi-backend/controller/cloud/openstack"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/custom"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/extension"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/scenario"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/controller/dependency"
	"github.com/CloudNativeWorks/elchi-backend/controller/discovery"
	"github.com/CloudNativeWorks/elchi-backend/controller/routemap"
	"github.com/CloudNativeWorks/elchi-backend/controller/service"
	"github.com/CloudNativeWorks/elchi-backend/pkg/audit"
	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/errstr"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/mongo"
)

const (
	MethodGet    = "GET"
	MethodDelete = "DELETE"
)

type (
	ResFunc      func(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error)
	DepFunc      func(ctx context.Context, requestDetails models.RequestDetails) (*dependency.Graph, error)
	ScenarioFunc func(ctx context.Context, scenario models.ScenarioBody, reqDetails models.RequestDetails) (any, error)
	OpFunc       func(ctx context.Context, operation models.OperationClass, requestDetails models.RequestDetails) (any, error)
)

type Handler struct {
	XDS          *xds.AppHandler
	Extension    *extension.AppHandler
	Custom       *custom.AppHandler
	Settings     *settings.AppHandler
	dependency   *dependency.AppHandler
	Bridge       *bridge.AppHandler
	Scenario     *scenario.AppHandler
	Client       *client.AppHandler
	Service      *service.AppHandler
	Discovery    *discovery.DiscoveryHandler
	Jobs         *JobHandler
	Registry     *RegistryHandler
	OpenStack    *openstack.Handler
	AuditService *audit.Service
	Template     *ResourceTemplateHandler
	RouteMap     *routemap.RouteMapHandler
	Snippet      *SnippetHandler
	WAF          *WAFHandler
	Upgrade      *UpgradeHandler
	Maintenance  *MaintenanceHandler
}

// getDatabaseConnection returns the first available database connection from handlers
// This is a shared utility to prevent code duplication across audit handlers
func (h *Handler) getDatabaseConnection() *mongo.Database {
	if h.XDS != nil && h.XDS.Context != nil {
		return h.XDS.Context.Client
	}
	if h.Extension != nil && h.Extension.Context != nil {
		return h.Extension.Context.Client
	}
	if h.Settings != nil && h.Settings.Context != nil {
		return h.Settings.Context.Client
	}
	if h.Custom != nil && h.Custom.Context != nil {
		return h.Custom.Context.Client
	}
	if h.Scenario != nil && h.Scenario.Context != nil {
		return h.Scenario.Context.Client
	}
	return nil
}

func NewHandler(xds *xds.AppHandler, extension *extension.AppHandler, custom *custom.AppHandler, settings *settings.AppHandler, dependency *dependency.AppHandler, stats *bridge.AppHandler, scenario *scenario.AppHandler, client *client.AppHandler, service *service.AppHandler, discovery *discovery.DiscoveryHandler, jobs *JobHandler, registry *RegistryHandler, openstack *openstack.Handler, auditService *audit.Service, template *ResourceTemplateHandler, routeMap *routemap.RouteMapHandler, maintenance *MaintenanceHandler, upgrade *UpgradeHandler) *Handler {
	handler := &Handler{
		XDS:          xds,
		Extension:    extension,
		Custom:       custom,
		Settings:     settings,
		dependency:   dependency,
		Bridge:       stats,
		Scenario:     scenario,
		Client:       client,
		Service:      service,
		Discovery:    discovery,
		Jobs:         jobs,
		Registry:     registry,
		OpenStack:    openstack,
		AuditService: auditService,
		Template:     template,
		RouteMap:     routeMap,
		Maintenance:  maintenance,
		Upgrade:      upgrade,
	}

	// Initialize snippet handler
	handler.Snippet = NewSnippetHandler(handler)

	// Initialize WAF handler with required dependencies
	handler.WAF = NewWAFHandler(xds.Context, xds.PokeService, jobs.asyncSystem, xds.Logger)
	handler.WAF.SetParentHandler(handler) // Set parent reference for audit functions

	return handler
}

// formatErrorMessage extracts a more user-friendly error message from raw MongoDB errors
func formatErrorMessage(err error) string {
	if err == nil {
		return ""
	}

	errStr := err.Error()

	// Handle MongoDB duplicate key errors
	if strings.Contains(errStr, "E11000 duplicate key error") {
		// Extract collection name from error message
		collection := "resource"
		if strings.Contains(errStr, "collection: elchi.") {
			parts := strings.Split(errStr, "collection: elchi.")
			if len(parts) > 1 {
				collectionPart := strings.Split(parts[1], " ")[0]
				collection = collectionPart
			}
		}

		// For MongoDB collation errors, we can't reliably extract the resource name
		// because it's encoded in CollationKey format. Just use a generic message.
		return fmt.Sprintf("A %s with this name already exists. Please choose a different name.", collection)
	}

	// For other errors, return the original message
	return errStr
}

// This function handles a request in the Handler struct.
// It retrieves the necessary data from the context, validates project access,
// and processes the request with proper authorization checks.
func (h *Handler) handleRequest(c *gin.Context, resFunc ResFunc) {
	ctx := c.Request.Context()
	requestDetails, userDetails := h.getRequestDetails(c)

	// Check basic role permissions
	if err := checkRole(c, userDetails); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	// Validate project access if project is specified
	if requestDetails.Project != "" {
		if db := h.getDatabaseConnection(); db != nil {
			if err := authorization.ValidateRequestProject(ctx, db, userDetails, requestDetails.Project); err != nil {
				c.JSON(http.StatusForbidden, gin.H{"message": err.Error()})
				return
			}
		}
	}

	// Set audit context for resource operations (XDS, Extension, Settings, etc.)
	h.setResourceAuditContext(c, requestDetails)

	// For PUT requests, capture changes BEFORE the database update
	if c.Request.Method == "PUT" {
		h.setAuditChanges(c)
	}

	response, err := h.dynamicFuncs(c, ctx, resFunc, requestDetails)
	h.setAuditResult(c, err)

	// For POST requests, update audit context with response data (resource ID, etc.)
	if c.Request.Method == "POST" && err == nil {
		h.updateAuditContextFromResponse(c, response)
	}

	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": formatErrorMessage(err), "data": response})
		return
	}

	c.JSON(http.StatusOK, response)
}

// handlePaginatedRequest handles requests with pagination support
func (h *Handler) handlePaginatedRequest(c *gin.Context, paginatedFunc func(*gin.Context, models.RequestDetails) (any, error)) {
	ctx := c.Request.Context()
	requestDetails, userDetails := h.getRequestDetails(c)

	// Check basic role permissions
	if err := checkRole(c, userDetails); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	// Validate project access if project is specified
	if requestDetails.Project != "" {
		if db := h.getDatabaseConnection(); db != nil {
			if err := authorization.ValidateRequestProject(ctx, db, userDetails, requestDetails.Project); err != nil {
				c.JSON(http.StatusForbidden, gin.H{"message": err.Error()})
				return
			}
		}
	}

	response, err := paginatedFunc(c, requestDetails)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": formatErrorMessage(err), "data": response})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Success", "data": response})
}

func (h *Handler) handleOpRequest(c *gin.Context, opFunc OpFunc) {
	ctx := c.Request.Context()
	requestDetails, userDetails := h.getRequestDetails(c)
	requestDetails.ClientID = getParamOrQuery(c, "client_id")
	requestDetails.ServiceID = getParamOrQuery(c, "service_id")
	requestDetails.FromClient = c.Query("from_client")

	// Extract forwarding-related headers and tokens
	// Extract token from Authorization header
	authHeader := c.GetHeader("Authorization")
	if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
		requestDetails.Token = strings.TrimPrefix(authHeader, "Bearer ")
	}
	// For refresh token, it would also come from Authorization header in refresh endpoint
	requestDetails.IsForwarded = c.GetHeader("X-Forwarded-Request") == "true"

	// Capture original request body for forwarding (from middleware)
	if originalBody, exists := c.Get("_original_body"); exists {
		if bodyBytes, ok := originalBody.([]byte); ok {
			requestDetails.OriginalBody = bodyBytes
		}
	}

	if err := checkRole(c, userDetails); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	// Set audit context for client commands
	h.setClientCommandAuditContext(c)

	response, err := h.dynamicOpFuncs(c, ctx, opFunc, requestDetails)
	h.setAuditResult(c, err)

	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": formatErrorMessage(err), "data": response})
		return
	}

	c.JSON(http.StatusOK, response)
}

// HandleK8sDiscovery delegates to discovery handler
func (h *Handler) HandleK8sDiscovery(c *gin.Context) {
	h.Discovery.HandleK8sDiscovery(c)
}

// GetClusters delegates to discovery handler
func (h *Handler) GetClusters(c *gin.Context) {
	h.Discovery.GetClusters(c)
}

// DeleteCluster delegates to discovery handler
func (h *Handler) DeleteCluster(c *gin.Context) {
	h.Discovery.DeleteCluster(c)
}

// GetClusterUsage delegates to discovery handler
func (h *Handler) GetClusterUsage(c *gin.Context) {
	h.Discovery.GetClusterUsage(c)
}

func (h *Handler) getRequestDetails(c *gin.Context) (models.RequestDetails, models.UserDetails) {
	userDetails, _ := GetUserDetails(c)

	// Safely extract project ID using authorization helper
	projectID := authorization.ExtractProjectFromRequest(c)

	requestDetails := models.RequestDetails{
		CanonicalName:     getParamOrQuery(c, "canonical_name"),
		Collection:        getParamOrQuery(c, "collection"),
		Version:           getParamOrQuery(c, "version"),
		Category:          c.Query("category"),
		ResourceID:        c.Query("resource_id"),
		Name:              c.Param("name"),
		SaveOrPublish:     c.Query("save_or_publish"),
		Project:           projectID,
		Metadata:          extractMetadata(c),
		Type:              models.KnownTYPES(getOptionalParam(c, "type")),
		GType:             models.GType(c.Query("gtype")),
		User:              userDetails,
		WithServiceIPs:    c.Query("with_service_ips"),
		ForMetrics:        c.Query("for_metrics"),
		Search:            c.Query("search"),
		Validate:          c.Query("validate"),
		DownstreamAddress: c.Query("downstream_ip"),
	}

	return requestDetails, userDetails
}

func (h *Handler) dynamicFuncs(c *gin.Context, ctx context.Context, resFunc ResFunc, requestDetails models.RequestDetails) (any, error) {
	resource, err := decodeR(c)
	if err != nil {
		return nil, err
	}

	response, err := resFunc(ctx, resource, requestDetails)
	if err != nil {
		return response, err
	}

	return response, nil
}

func (h *Handler) dynamicOpFuncs(c *gin.Context, ctx context.Context, opFunc OpFunc, requestDetails models.RequestDetails) (any, error) {
	resource, err := decoderOp(c)
	if err != nil {
		return nil, err
	}

	response, err := opFunc(ctx, resource, requestDetails)
	if err != nil {
		return response, err
	}

	return response, nil
}

func GetUserDetails(c *gin.Context) (models.UserDetails, error) {
	groups, _ := c.Get("groups")
	isOwner, _ := c.Get("isOwner")
	userRole, _ := c.Get("role")
	UserID, _ := c.Get("user_id")
	projects, _ := c.Get("projects")
	userName, _ := c.Get("user_name")
	BaseGroup, _ := c.Get("base_group")

	userGroup, ok := groups.(*[]string)
	if !ok {
		userGroup = &[]string{}
	}

	// Handle projects - support both []string and *[]CombinedProjects from JWT
	userProjects, ok := projects.([]string)
	if !ok {
		// Try CombinedProjects (from JWT claims)
		if combinedProjects, ok := projects.(*[]models.CombinedProjects); ok {
			userProjects = []string{}
			for _, proj := range *combinedProjects {
				userProjects = append(userProjects, proj.ProjectID)
			}
		} else {
			userProjects = []string{}
		}
	}

	userIsOwner, ok := isOwner.(bool)
	if !ok {
		userIsOwner = false
	}

	userRolePtr, ok := userRole.(*models.Role)
	var userRoleIs models.Role
	if ok && userRolePtr != nil {
		userRoleIs = *userRolePtr
	} else {
		userRoleIs = models.RoleViewer
	}

	userID, ok := UserID.(string)
	if !ok {
		userID = ""
	}

	user, ok := userName.(*string)
	if !ok {
		user = nil
	}

	userBaseGroup, ok := BaseGroup.(string)
	if !ok {
		userBaseGroup = ""
	}

	userDetails := models.UserDetails{
		Groups:    *userGroup,
		Role:      userRoleIs,
		IsOwner:   userIsOwner,
		UserID:    userID,
		Projects:  userProjects,
		UserName:  *user,
		BaseGroup: userBaseGroup,
	}

	return userDetails, nil
}

func checkRole(c *gin.Context, userDetail models.UserDetails) (err error) {
	method := c.Request.Method
	// Owner check (IsOwner is a boolean field, not a role enum)
	if userDetail.IsOwner {
		return nil
	}

	switch userDetail.Role {
	case models.RoleAdmin:
		return nil
	case models.RoleEditor:
		if method == "GET" || method == "POST" || method == "PUT" || method == "DELETE" {
			return nil
		}
		return errstr.ErrNotAuthorized
	case models.RoleViewer:
		if method == "GET" || method == "POST" {
			// Viewer can GET and POST (POST is used for readonly commands)
			return nil
		}
		return errstr.ErrNotAuthorized
	default:
		return errstr.ErrNotAuthorized
	}
}

func (h *Handler) handleDepRequest(c *gin.Context, depFunc DepFunc) {
	ctx := c.Request.Context()
	userDetails, _ := GetUserDetails(c)

	requestDetails := models.RequestDetails{
		GType:      models.GType(c.Query("gtype")),
		Name:       c.Param("name"),
		Collection: c.Query("collection"),
		Project:    c.Query("project"),
		Version:    c.Query("version"),
		User:       userDetails,
	}

	err := checkRole(c, userDetails)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	response, err := depFunc(ctx, requestDetails)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, response)
}

func extractMetadata(c *gin.Context) map[string]string {
	metadata := make(map[string]string)

	for key, values := range c.Request.URL.Query() {
		if len(values) > 0 && len(key) >= 9 && key[:9] == "metadata_" {
			metadata[key[9:]] = values[0]
		}
	}

	return metadata
}

func getParamOrQuery(c *gin.Context, key string) string {
	if value := c.Param(key); value != "" {
		return value
	}
	return c.Query(key)
}

func getOptionalParam(c *gin.Context, key string) string {
	if value := c.Param(key); value != "" {
		return value
	}
	return c.Query(key)
}

func decoderOp(c *gin.Context) (models.OperationClass, error) {
	var body models.Operations
	if c.Request.Method != MethodGet && c.Request.Method != MethodDelete {
		return decodeResourceOp(c)
	}
	return &body, nil
}

func decodeR(c *gin.Context) (models.ResourceClass, error) {
	var body models.DBResource
	if c.Request.Method != MethodGet && c.Request.Method != MethodDelete {
		return decodeResource(c)
	}
	return &body, nil
}

func decodeResource(c *gin.Context) (models.ResourceClass, error) {
	var body models.DBResource

	// Try to get cached body from middleware first
	if originalBody, exists := c.Get("_original_body"); exists {
		if bodyBytes, ok := originalBody.([]byte); ok {
			if err := json.Unmarshal(bodyBytes, &body); err != nil {
				return nil, err
			}
			return &body, nil
		}
	}

	// Fallback to normal BindJSON
	if err := c.BindJSON(&body); err != nil {
		return nil, err
	}
	return &body, nil
}

func decodeResourceOp(c *gin.Context) (models.OperationClass, error) {
	var body models.Operations

	// Try to get cached body from middleware first
	if originalBody, exists := c.Get("_original_body"); exists {
		if bodyBytes, ok := originalBody.([]byte); ok {
			if err := json.Unmarshal(bodyBytes, &body); err != nil {
				return nil, err
			}
			return &body, nil
		}
	}

	// Fallback to normal BindJSON
	if err := c.BindJSON(&body); err != nil {
		return nil, err
	}
	return &body, nil
}

// Job management handler methods

// ListJobs delegates to jobs handler
func (h *Handler) ListJobs(c *gin.Context) {
	h.Jobs.ListJobs(c)
}

// GetJob delegates to jobs handler
func (h *Handler) GetJob(c *gin.Context) {
	h.Jobs.GetJob(c)
}

// RetryJob delegates to jobs handler
func (h *Handler) RetryJob(c *gin.Context) {
	h.Jobs.RetryJob(c)
}

// GetStuckJobs delegates to jobs handler
func (h *Handler) GetStuckJobs(c *gin.Context) {
	h.Jobs.GetStuckJobs(c)
}

// GetWorkerStatus delegates to jobs handler
func (h *Handler) GetWorkerStatus(c *gin.Context) {
	h.Jobs.GetWorkerStatus(c)
}

// GetJobStats delegates to jobs handler
func (h *Handler) GetJobStats(c *gin.Context) {
	h.Jobs.GetJobStats(c)
}

// GetClientOpenStackInterfaces delegates to OpenStack handler
func (h *Handler) GetClientOpenStackInterfaces(c *gin.Context) {
	h.OpenStack.GetClientInterfaces(c)
}

// GetSubnetAvailableIPs delegates to OpenStack handler
func (h *Handler) GetSubnetAvailableIPs(c *gin.Context) {
	h.OpenStack.GetSubnetAvailableIPs(c)
}

// ================== SETTINGS WRAPPERS ==================
// Settings handlers wrapped with audit functionality

// User Management
func (h *Handler) SetUpdateUserWithAudit(c *gin.Context) {
	requestDetails, _ := h.getRequestDetails(c)
	h.setResourceAuditContext(c, requestDetails)

	// Capture changes before the update (same as handleRequest does for XDS resources)
	h.setAuditChanges(c)

	h.Settings.SetUpdateUser(c)
	if c.Writer.Status() >= 400 {
		h.setAuditResult(c, fmt.Errorf("user operation failed"))
	} else {
		h.setAuditResult(c, nil)
	}
}

func (h *Handler) DeleteUserWithAudit(c *gin.Context) {
	requestDetails, _ := h.getRequestDetails(c)
	h.setResourceAuditContext(c, requestDetails)
	h.Settings.DeleteUser(c)
	if c.Writer.Status() >= 400 {
		h.setAuditResult(c, fmt.Errorf("user deletion failed"))
	} else {
		h.setAuditResult(c, nil)
	}
}

// Group Management
func (h *Handler) SetUpdateGroupWithAudit(c *gin.Context) {
	requestDetails, _ := h.getRequestDetails(c)
	h.setResourceAuditContext(c, requestDetails)

	// Capture changes before the update
	h.setAuditChanges(c)

	h.Settings.SetUpdateGroup(c)
	if c.Writer.Status() >= 400 {
		h.setAuditResult(c, fmt.Errorf("group operation failed"))
	} else {
		h.setAuditResult(c, nil)
	}
}

func (h *Handler) DeleteGroupWithAudit(c *gin.Context) {
	requestDetails, _ := h.getRequestDetails(c)
	h.setResourceAuditContext(c, requestDetails)
	h.Settings.DeleteGroup(c)
	if c.Writer.Status() >= 400 {
		h.setAuditResult(c, fmt.Errorf("group deletion failed"))
	} else {
		h.setAuditResult(c, nil)
	}
}

// Project Management
func (h *Handler) SetUpdateProjectWithAudit(c *gin.Context) {
	requestDetails, _ := h.getRequestDetails(c)
	h.setResourceAuditContext(c, requestDetails)

	// Capture changes before the update
	h.setAuditChanges(c)

	h.Settings.SetUpdateProject(c)
	if c.Writer.Status() >= 400 {
		h.setAuditResult(c, fmt.Errorf("project operation failed"))
	} else {
		h.setAuditResult(c, nil)
	}
}

func (h *Handler) DeleteProjectWithAudit(c *gin.Context) {
	requestDetails, _ := h.getRequestDetails(c)
	h.setResourceAuditContext(c, requestDetails)
	h.Settings.DeleteProject(c)
	if c.Writer.Status() >= 400 {
		h.setAuditResult(c, fmt.Errorf("project deletion failed"))
	} else {
		h.setAuditResult(c, nil)
	}
}

// Token Management
func (h *Handler) SetTokenWithAudit(c *gin.Context) {
	// Set audit context manually since handleRequest expects ResourceClass operations
	// Get proper request details
	requestDetails, _ := h.getRequestDetails(c)
	h.setResourceAuditContext(c, requestDetails)

	// Call the settings handler which handles its own response
	h.Settings.SetToken(c)

	// Set audit result based on response status
	if c.Writer.Status() >= 400 {
		h.setAuditResult(c, fmt.Errorf("token creation failed"))
	} else {
		h.setAuditResult(c, nil)
	}
}

func (h *Handler) DeleteTokenWithAudit(c *gin.Context) {
	// Set audit context manually since handleRequest expects ResourceClass operations
	requestDetails, _ := h.getRequestDetails(c)
	h.setResourceAuditContext(c, requestDetails)

	// Call the settings handler which handles its own response
	h.Settings.DeleteToken(c)

	// Set audit result based on response status
	if c.Writer.Status() >= 400 {
		h.setAuditResult(c, fmt.Errorf("token deletion failed"))
	} else {
		h.setAuditResult(c, nil)
	}
}

// OpenRouter Token Management
func (h *Handler) SetOpenRouterTokenWithAudit(c *gin.Context) {
	requestDetails, _ := h.getRequestDetails(c)
	h.setResourceAuditContext(c, requestDetails)
	h.Settings.SetOpenRouterToken(c)
	if c.Writer.Status() >= 400 {
		h.setAuditResult(c, fmt.Errorf("openrouter token creation failed"))
	} else {
		h.setAuditResult(c, nil)
	}
}

func (h *Handler) UpdateOpenRouterTokenWithAudit(c *gin.Context) {
	requestDetails, _ := h.getRequestDetails(c)
	h.setResourceAuditContext(c, requestDetails)
	h.Settings.UpdateOpenRouterToken(c)
	if c.Writer.Status() >= 400 {
		h.setAuditResult(c, fmt.Errorf("openrouter token update failed"))
	} else {
		h.setAuditResult(c, nil)
	}
}

func (h *Handler) DeleteOpenRouterTokenWithAudit(c *gin.Context) {
	requestDetails, _ := h.getRequestDetails(c)
	h.setResourceAuditContext(c, requestDetails)
	h.Settings.DeleteOpenRouterToken(c)
	if c.Writer.Status() >= 400 {
		h.setAuditResult(c, fmt.Errorf("openrouter token deletion failed"))
	} else {
		h.setAuditResult(c, nil)
	}
}

// Discovery Token Management
func (h *Handler) GenerateDiscoveryTokenWithAudit(c *gin.Context) {
	requestDetails, _ := h.getRequestDetails(c)
	h.setResourceAuditContext(c, requestDetails)
	h.Settings.GenerateDiscoveryToken(c)
	if c.Writer.Status() >= 400 {
		h.setAuditResult(c, fmt.Errorf("discovery token generation failed"))
	} else {
		h.setAuditResult(c, nil)
	}
}

func (h *Handler) DeleteDiscoveryTokenWithAudit(c *gin.Context) {
	requestDetails, _ := h.getRequestDetails(c)
	h.setResourceAuditContext(c, requestDetails)
	h.Settings.DeleteDiscoveryToken(c)
	if c.Writer.Status() >= 400 {
		h.setAuditResult(c, fmt.Errorf("discovery token deletion failed"))
	} else {
		h.setAuditResult(c, nil)
	}
}

// Cloud Config Management
func (h *Handler) SetCloudWithAudit(c *gin.Context) {
	requestDetails, _ := h.getRequestDetails(c)
	h.setResourceAuditContext(c, requestDetails)
	h.Settings.SetCloud(c)
	if c.Writer.Status() >= 400 {
		h.setAuditResult(c, fmt.Errorf("cloud config creation failed"))
	} else {
		h.setAuditResult(c, nil)
	}
}

func (h *Handler) UpdateCloudWithAudit(c *gin.Context) {
	requestDetails, _ := h.getRequestDetails(c)
	h.setResourceAuditContext(c, requestDetails)
	h.Settings.UpdateCloud(c)
	if c.Writer.Status() >= 400 {
		h.setAuditResult(c, fmt.Errorf("cloud config update failed"))
	} else {
		h.setAuditResult(c, nil)
	}
}

func (h *Handler) DeleteCloudWithAudit(c *gin.Context) {
	requestDetails, _ := h.getRequestDetails(c)
	h.setResourceAuditContext(c, requestDetails)
	h.Settings.DeleteCloud(c)
	if c.Writer.Status() >= 400 {
		h.setAuditResult(c, fmt.Errorf("cloud config deletion failed"))
	} else {
		h.setAuditResult(c, nil)
	}
}

// updateAuditContextFromResponse updates audit context with response data for CREATE operations
func (h *Handler) updateAuditContextFromResponse(c *gin.Context, response any) {
	if response == nil {
		return
	}

	path := c.Request.URL.Path

	// Handle scenario CREATE responses
	if strings.Contains(path, "/api/v3/scenario/scenarios") {
		h.updateScenarioAuditFromResponse(c, response)
	}
	// Add other resource types here as needed
}

// ================== LDAP CONFIGURATION AUDIT WRAPPERS ==================

// SetLDAPConfigWithAudit wraps SetLDAPConfig with audit logging
func (h *Handler) SetLDAPConfigWithAudit(c *gin.Context) {
	requestDetails, _ := h.getRequestDetails(c)
	h.setLDAPAuditContext(c, requestDetails)
	h.Settings.SetLDAPConfig(c)
	if c.Writer.Status() >= 400 {
		h.setAuditResult(c, fmt.Errorf("LDAP config creation failed"))
	} else {
		h.setAuditResult(c, nil)
	}
}

// UpdateLDAPConfigWithAudit wraps UpdateLDAPConfig with audit logging
func (h *Handler) UpdateLDAPConfigWithAudit(c *gin.Context) {
	requestDetails, _ := h.getRequestDetails(c)
	h.setLDAPAuditContext(c, requestDetails)
	h.Settings.UpdateLDAPConfig(c)
	if c.Writer.Status() >= 400 {
		h.setAuditResult(c, fmt.Errorf("LDAP config update failed"))
	} else {
		h.setAuditResult(c, nil)
	}
}

// DeleteLDAPConfigWithAudit wraps DeleteLDAPConfig with audit logging
func (h *Handler) DeleteLDAPConfigWithAudit(c *gin.Context) {
	requestDetails, _ := h.getRequestDetails(c)
	h.setLDAPAuditContext(c, requestDetails)
	h.Settings.DeleteLDAPConfig(c)
	if c.Writer.Status() >= 400 {
		h.setAuditResult(c, fmt.Errorf("LDAP config deletion failed"))
	} else {
		h.setAuditResult(c, nil)
	}
}

// TestLDAPConfigWithAudit wraps TestLDAPConfig (no audit for test endpoints)
func (h *Handler) TestLDAPConfigWithAudit(c *gin.Context) {
	h.Settings.TestLDAPConfig(c)
}

// TestLDAPAuthWithAudit wraps TestLDAPAuth (no audit for test endpoints)
func (h *Handler) TestLDAPAuthWithAudit(c *gin.Context) {
	h.Settings.TestLDAPAuth(c)
}

// updateScenarioAuditFromResponse extracts scenario ID and name from CREATE response
func (h *Handler) updateScenarioAuditFromResponse(c *gin.Context, response any) {
	var scenarioID, scenarioName string

	// Response could be direct Scenario object or wrapped in gin.H
	if responseMap, ok := response.(map[string]any); ok {
		// Try to get data field (if wrapped)
		if data, exists := responseMap["data"]; exists {
			if dataMap, ok := data.(map[string]any); ok {
				// Extract from wrapped data
				if id, exists := dataMap["id"]; exists {
					scenarioID = fmt.Sprintf("%v", id)
				}
				if name, exists := dataMap["name"]; exists {
					if nameStr, ok := name.(string); ok {
						scenarioName = nameStr
					}
				}
			}
		} else {
			// Try direct response format
			if id, exists := responseMap["id"]; exists {
				scenarioID = fmt.Sprintf("%v", id)
			}
			if name, exists := responseMap["name"]; exists {
				if nameStr, ok := name.(string); ok {
					scenarioName = nameStr
				}
			}
		}
	}

	// Also try if response is models.Scenario directly
	if scenario, ok := response.(*models.Scenario); ok {
		scenarioID = scenario.ID.Hex()
		scenarioName = scenario.Name
	}

	// Update audit context with extracted info
	if scenarioID != "" || scenarioName != "" {
		audit.SetAuditResource(c, "scenarios", scenarioID, scenarioName, c.Query("project"))
	}
}
