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
}

func NewHandler(xds *xds.AppHandler, extension *extension.AppHandler, custom *custom.AppHandler, settings *settings.AppHandler, dependency *dependency.AppHandler, stats *bridge.AppHandler, scenario *scenario.AppHandler, client *client.AppHandler, service *service.AppHandler, discovery *discovery.DiscoveryHandler, jobs *JobHandler, registry *RegistryHandler, openstack *openstack.Handler, auditService *audit.Service) *Handler {
	return &Handler{
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
	}
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
		// Get database connection from any handler that has Context
		var db *mongo.Database
		if h.XDS != nil && h.XDS.Context != nil {
			db = h.XDS.Context.Client
		} else if h.Settings != nil && h.Settings.Context != nil {
			db = h.Settings.Context.Client
		}

		if db != nil {
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
		// Get database connection from any handler that has Context
		var db *mongo.Database
		if h.XDS != nil && h.XDS.Context != nil {
			db = h.XDS.Context.Client
		} else if h.Settings != nil && h.Settings.Context != nil {
			db = h.Settings.Context.Client
		}

		if db != nil {
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
	requestDetails.Token = c.GetHeader("token")
	requestDetails.RefreshToken = c.GetHeader("refresh-token")
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

func (h *Handler) getRequestDetails(c *gin.Context) (models.RequestDetails, models.UserDetails) {
	userDetails, _ := GetUserDetails(c)

	// Safely extract project ID using authorization helper
	projectID := authorization.ExtractProjectFromRequest(c)

	requestDetails := models.RequestDetails{
		CanonicalName:  getParamOrQuery(c, "canonical_name"),
		Collection:     getParamOrQuery(c, "collection"),
		Version:        getParamOrQuery(c, "version"),
		Category:       c.Query("category"),
		ResourceID:     c.Query("resource_id"),
		Name:           c.Param("name"),
		SaveOrPublish:  c.Query("save_or_publish"),
		Project:        projectID,
		Metadata:       extractMetadata(c),
		Type:           models.KnownTYPES(getOptionalParam(c, "type")),
		GType:          models.GType(c.Query("gtype")),
		User:           userDetails,
		WithServiceIPs: c.Query("with_service_ips"),
		ForMetrics:     c.Query("for_metrics"),
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

	userProjects, ok := projects.([]string)
	if !ok {
		userProjects = []string{}
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
	switch userDetail.Role {
	case models.RoleAdmin, models.RoleOwner:
		return nil
	case models.RoleEditor:
		if method == "GET" || method == "POST" || method == "PUT" || method == "DELETE" {
			return nil
		}
		return errstr.ErrNotAuthorized
	case models.RoleViewer:
		if method == "GET" {
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

// GetNetworkDetails delegates to OpenStack handler
func (h *Handler) GetNetworkDetails(c *gin.Context) {
	h.OpenStack.GetNetworkDetails(c)
}

// GetSubnetDetails delegates to OpenStack handler
func (h *Handler) GetSubnetDetails(c *gin.Context) {
	h.OpenStack.GetSubnetDetails(c)
}

// GetNetworkSubnets delegates to OpenStack handler
func (h *Handler) GetNetworkSubnets(c *gin.Context) {
	h.OpenStack.GetNetworkSubnets(c)
}

// ================== SETTINGS WRAPPERS ==================
// Settings handlers wrapped with audit functionality

// User Management
func (h *Handler) SetUpdateUserWithAudit(c *gin.Context) {
	h.handleRequest(c, func(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
		h.Settings.SetUpdateUser(c)
		return gin.H{"message": "User operation completed"}, nil
	})
}

func (h *Handler) DeleteUserWithAudit(c *gin.Context) {
	h.handleRequest(c, func(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
		h.Settings.DeleteUser(c)
		return gin.H{"message": "User deleted"}, nil
	})
}

// Group Management  
func (h *Handler) SetUpdateGroupWithAudit(c *gin.Context) {
	h.handleRequest(c, func(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
		h.Settings.SetUpdateGroup(c)
		return gin.H{"message": "Group operation completed"}, nil
	})
}

func (h *Handler) DeleteGroupWithAudit(c *gin.Context) {
	h.handleRequest(c, func(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
		h.Settings.DeleteGroup(c)
		return gin.H{"message": "Group deleted"}, nil
	})
}

// Project Management
func (h *Handler) SetUpdateProjectWithAudit(c *gin.Context) {
	h.handleRequest(c, func(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
		h.Settings.SetUpdateProject(c)
		return gin.H{"message": "Project operation completed"}, nil
	})
}

func (h *Handler) DeleteProjectWithAudit(c *gin.Context) {
	h.handleRequest(c, func(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
		h.Settings.DeleteProject(c)
		return gin.H{"message": "Project deleted"}, nil
	})
}

// Token Management
func (h *Handler) SetTokenWithAudit(c *gin.Context) {
	h.handleRequest(c, func(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
		h.Settings.SetToken(c)
		return gin.H{"message": "Token created"}, nil
	})
}

func (h *Handler) DeleteTokenWithAudit(c *gin.Context) {
	h.handleRequest(c, func(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
		h.Settings.DeleteToken(c)
		return gin.H{"message": "Token deleted"}, nil
	})
}

// OpenRouter Token Management
func (h *Handler) SetOpenRouterTokenWithAudit(c *gin.Context) {
	h.handleRequest(c, func(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
		h.Settings.SetOpenRouterToken(c)
		return gin.H{"message": "OpenRouter token created"}, nil
	})
}

func (h *Handler) UpdateOpenRouterTokenWithAudit(c *gin.Context) {
	h.handleRequest(c, func(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
		h.Settings.UpdateOpenRouterToken(c)
		return gin.H{"message": "OpenRouter token updated"}, nil
	})
}

func (h *Handler) DeleteOpenRouterTokenWithAudit(c *gin.Context) {
	h.handleRequest(c, func(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
		h.Settings.DeleteOpenRouterToken(c)
		return gin.H{"message": "OpenRouter token deleted"}, nil
	})
}

// Discovery Token Management
func (h *Handler) GenerateDiscoveryTokenWithAudit(c *gin.Context) {
	h.handleRequest(c, func(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
		h.Settings.GenerateDiscoveryToken(c)
		return gin.H{"message": "Discovery token generated"}, nil
	})
}

func (h *Handler) DeleteDiscoveryTokenWithAudit(c *gin.Context) {
	h.handleRequest(c, func(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
		h.Settings.DeleteDiscoveryToken(c)
		return gin.H{"message": "Discovery token deleted"}, nil
	})
}

// Cloud Config Management
func (h *Handler) SetCloudWithAudit(c *gin.Context) {
	h.handleRequest(c, func(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
		h.Settings.SetCloud(c)
		return gin.H{"message": "Cloud config created"}, nil
	})
}

func (h *Handler) UpdateCloudWithAudit(c *gin.Context) {
	h.handleRequest(c, func(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
		h.Settings.UpdateCloud(c)
		return gin.H{"message": "Cloud config updated"}, nil
	})
}

func (h *Handler) DeleteCloudWithAudit(c *gin.Context) {
	h.handleRequest(c, func(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
		h.Settings.DeleteCloud(c)
		return gin.H{"message": "Cloud config deleted"}, nil
	})
}
