package handlers

import (
	"errors"
	"net/http"

	"github.com/CloudNativeWorks/elchi-backend/controller/shield"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	client "github.com/CloudNativeWorks/elchi-proto/client"
	"github.com/gin-gonic/gin"
)

// ShieldHandler exposes the elchi-shield policy store (CRUD). Any create/update/
// delete auto-deploys the project's MERGED policy set (full-sync) to the project's
// connected clients — there is no manual per-policy/per-client deploy.
type ShieldHandler struct {
	crudService   *shield.CRUDService
	logger        *logger.Logger
	parentHandler *Handler
}

// NewShieldHandler builds the shield handler.
func NewShieldHandler(dbContext *db.AppContext, logger *logger.Logger) *ShieldHandler {
	return &ShieldHandler{
		crudService: shield.NewCRUDService(dbContext, logger),
		logger:      logger,
	}
}

// SetParentHandler wires the parent for request-detail/command access.
func (h *ShieldHandler) SetParentHandler(parent *Handler) { h.parentHandler = parent }

func (h *ShieldHandler) isAdmin(c *gin.Context) bool {
	_, userDetails := h.parentHandler.getRequestDetails(c)
	if userDetails.IsOwner || userDetails.Role == models.RoleAdmin {
		return true
	}
	c.JSON(http.StatusForbidden, gin.H{"message": "only Admin and Owner can manage shield policies"})
	return false
}

func shieldStatus(err error) int {
	switch {
	case errors.Is(err, shield.ErrPolicyNotFound):
		return http.StatusNotFound
	case errors.Is(err, shield.ErrPolicyNameTaken):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// CreateShieldPolicy handles POST /api/v3/shield/policies and auto-deploys.
func (h *ShieldHandler) CreateShieldPolicy(c *gin.Context) {
	if !h.isAdmin(c) {
		return
	}
	var req shield.ShieldPolicyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	policy, err := h.crudService.Create(c.Request.Context(), req)
	if err != nil {
		c.JSON(shieldStatus(err), gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": policy.ToResponse(), "deploy": h.deployProject(c, req.Project)})
}

// UpdateShieldPolicy handles PUT /api/v3/shield/policies/:policy_id and auto-deploys.
func (h *ShieldHandler) UpdateShieldPolicy(c *gin.Context) {
	if !h.isAdmin(c) {
		return
	}
	var req shield.ShieldPolicyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	policy, err := h.crudService.Update(c.Request.Context(), c.Param("policy_id"), req)
	if err != nil {
		c.JSON(shieldStatus(err), gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": policy.ToResponse(), "deploy": h.deployProject(c, req.Project)})
}

// GetShieldPolicy handles GET /api/v3/shield/policies/:policy_id?project=.
func (h *ShieldHandler) GetShieldPolicy(c *gin.Context) {
	policy, err := h.crudService.GetByID(c.Request.Context(), c.Param("policy_id"), c.Query("project"))
	if err != nil {
		c.JSON(shieldStatus(err), gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": policy.ToResponse()})
}

// ListShieldPolicies handles GET /api/v3/shield/policies?project=.
func (h *ShieldHandler) ListShieldPolicies(c *gin.Context) {
	policies, err := h.crudService.List(c.Request.Context(), c.Query("project"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
		return
	}
	out := make([]shield.ShieldPolicyResponse, 0, len(policies))
	for i := range policies {
		out = append(out, policies[i].ToResponse())
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

// DeleteShieldPolicy handles DELETE /api/v3/shield/policies/:policy_id?project= and
// re-deploys the remaining merged set (which removes the deleted files from edges).
func (h *ShieldHandler) DeleteShieldPolicy(c *gin.Context) {
	if !h.isAdmin(c) {
		return
	}
	project := c.Query("project")
	if err := h.crudService.Delete(c.Request.Context(), c.Param("policy_id"), project); err != nil {
		c.JSON(shieldStatus(err), gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted", "deploy": h.deployProject(c, project)})
}

// SyncShieldProject handles POST /api/v3/shield/sync — re-push the project's merged
// policy set to its connected clients (e.g. after clients reconnect).
func (h *ShieldHandler) SyncShieldProject(c *gin.Context) {
	if !h.isAdmin(c) {
		return
	}
	var body struct {
		Project string `json:"project" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": h.deployProject(c, body.Project)})
}

// ShieldStatus handles GET /api/v3/shield/status?project=&client_id=. It queries a
// connected edge client's shield service status — a command dispatch, so it is
// admin/owner-gated (not an open store read).
func (h *ShieldHandler) ShieldStatus(c *gin.Context) {
	if !h.isAdmin(c) {
		return
	}
	clientID := c.Query("client_id")
	if clientID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "client_id is required"})
		return
	}
	resp, err := h.dispatch(c, "", c.Query("project"), client.SubCommandType_GET_SHIELD_STATUS, []string{clientID}, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error(), "data": resp})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": resp})
}

// deployProject renders the project's MERGED policy set into one full-sync bundle
// and pushes it to the project's connected clients. It never returns an error to
// the caller — a store mutation succeeds regardless of deploy outcome; the result
// (versions/per-client status/errors) is returned for the response body.
func (h *ShieldHandler) deployProject(c *gin.Context, project string) any {
	ctx := c.Request.Context()
	policies, err := h.crudService.List(ctx, project)
	if err != nil {
		h.logger.Errorf("shield deploy: list policies for project %s: %v", project, err)
		return gin.H{"deployed": false, "error": err.Error()}
	}
	cfg, err := shield.MergePolicies(policies)
	if err != nil {
		h.logger.Errorf("shield deploy: merge policies for project %s: %v", project, err)
		return gin.H{"deployed": false, "error": err.Error()}
	}
	clientIDs, err := h.crudService.ListConnectedClientIDs(ctx, project)
	if err != nil {
		h.logger.Errorf("shield deploy: list clients for project %s: %v", project, err)
		return gin.H{"deployed": false, "error": err.Error()}
	}
	if len(clientIDs) == 0 {
		return gin.H{"deployed": true, "clients": 0, "version": cfg.Version, "message": "no connected clients in project"}
	}
	resp, err := h.dispatch(c, "", project, client.SubCommandType_UPDATE_SHIELD_CONFIG, clientIDs, &cfg)
	if err != nil {
		h.logger.Errorf("shield deploy to project %s: %v", project, err)
		return gin.H{"deployed": false, "version": cfg.Version, "clients": len(clientIDs), "error": err.Error(), "results": resp}
	}
	return gin.H{"deployed": true, "version": cfg.Version, "clients": len(clientIDs), "results": resp}
}

// dispatch builds a SHIELD Operation and routes it through the client command
// handler, reusing its processor/responser, parallel fan-out, and authorization.
func (h *ShieldHandler) dispatch(c *gin.Context, name, project string, sub client.SubCommandType, targets []string, cfg *models.ShieldConfigJSON) (any, error) {
	clients := make([]models.ServiceClients, 0, len(targets))
	for _, cid := range targets {
		clients = append(clients, models.ServiceClients{ClientID: cid})
	}
	op := &models.Operations{
		Type:     models.CommandTypeJSON(client.CommandType_SHIELD),
		SubType:  models.SubCommandTypeJSON(sub),
		Clients:  clients,
		Command:  models.Command{Project: project, Name: name},
		ShieldOp: &models.RequestShieldJSON{Config: cfg},
	}
	requestDetails, _ := h.parentHandler.getRequestDetails(c)
	return h.parentHandler.Client.Handler.HandleSendCommand(c.Request.Context(), op, requestDetails)
}
