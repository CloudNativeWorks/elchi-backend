package handlers

import (
	"context"
	"fmt"
	"net/http"

	"github.com/CloudNativeWorks/elchi-backend/controller/api/auth"
	"github.com/CloudNativeWorks/elchi-backend/controller/bridge"
	"github.com/CloudNativeWorks/elchi-backend/controller/client"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/custom"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/extension"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/scenario"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/controller/dependency"
	"github.com/CloudNativeWorks/elchi-backend/controller/service"
	"github.com/CloudNativeWorks/elchi-backend/pkg/errstr"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"

	"github.com/gin-gonic/gin"
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
	XDS        *xds.AppHandler
	Extension  *extension.AppHandler
	Custom     *custom.AppHandler
	Auth       *auth.AppHandler
	dependency *dependency.AppHandler
	Bridge     *bridge.AppHandler
	Scenario   *scenario.AppHandler
	Client     *client.AppHandler
	Service    *service.AppHandler
}

func NewHandler(xds *xds.AppHandler, extension *extension.AppHandler, custom *custom.AppHandler, auth *auth.AppHandler, dependency *dependency.AppHandler, stats *bridge.AppHandler, scenario *scenario.AppHandler, client *client.AppHandler, service *service.AppHandler) *Handler {
	return &Handler{
		XDS:        xds,
		Extension:  extension,
		Custom:     custom,
		Auth:       auth,
		dependency: dependency,
		Bridge:     stats,
		Scenario:   scenario,
		Client:     client,
		Service:    service,
	}
}

// This function handles a request in the Handler struct.
// It retrieves the necessary data from the context, including the groups and isOwner parameters.
// It then sets the requestDetails struct with the given parameters and decodes the resource.
// It then calls the resFunc with the resource and requestDetails, and stores the response in the response variable.
// Finally, it returns the response as a JSON object with the status OK.
func (h *Handler) handleRequest(c *gin.Context, resFunc ResFunc) {
	ctx := c.Request.Context()
	requestDetails, userDetails := h.getRequestDetails(c)

	if err := checkRole(c, userDetails); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	response, err := h.dynamicFuncs(c, ctx, resFunc, requestDetails)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error(), "data": response})
		return
	}

	c.JSON(http.StatusOK, response)
}

func (h *Handler) handleOpRequest(c *gin.Context, opFunc OpFunc) {
	ctx := c.Request.Context()
	requestDetails, userDetails := h.getRequestDetails(c)
	requestDetails.ClientID = getParamOrQuery(c, "client_id")
	requestDetails.ServiceID = getParamOrQuery(c, "service_id")
	requestDetails.FromClient = c.Query("from_client")


	if err := checkRole(c, userDetails); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	response, err := h.dynamicOpFuncs(c, ctx, opFunc, requestDetails)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error(), "data": response})
		return
	}

	c.JSON(http.StatusOK, response)
}

func (h *Handler) getRequestDetails(c *gin.Context) (models.RequestDetails, models.UserDetails) {
	userDetails, _ := GetUserDetails(c)

	requestDetails := models.RequestDetails{
		CanonicalName:  getParamOrQuery(c, "canonical_name"),
		Collection:     getParamOrQuery(c, "collection"),
		Version:        getParamOrQuery(c, "version"),
		Category:       c.Query("category"),
		ResourceID:     c.Query("resource_id"),
		Name:           c.Param("name"),
		SaveOrPublish:  c.Query("save_or_publish"),
		Project:        c.Query("project"),
		Metadata:       extractMetadata(c),
		Type:           models.KnownTYPES(getOptionalParam(c, "type")),
		GType:          models.GTypes(c.Query("gtype")),
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
		GType:      models.GTypes(c.Query("gtype")),
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
	fmt.Println(c)
	if c.Request.Method != MethodGet && c.Request.Method != MethodDelete {
		return decodeResource(c)
	}
	return &body, nil
}

func decodeResource(c *gin.Context) (models.ResourceClass, error) {
	var body models.DBResource
	if err := c.BindJSON(&body); err != nil {
		return nil, err
	}
	return &body, nil
}

func decodeResourceOp(c *gin.Context) (models.OperationClass, error) {
	var body models.Operations
	if err := c.BindJSON(&body); err != nil {
		return nil, err
	}

	return &body, nil
}
