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

// ShieldHandler exposes the elchi-shield policy store (CRUD) and the deploy path
// that pushes a stored policy to edge clients via the command stream.
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

// CreateShieldPolicy handles POST /api/v3/shield/policies.
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
	c.JSON(http.StatusCreated, gin.H{"data": policy.ToResponse()})
}

// UpdateShieldPolicy handles PUT /api/v3/shield/policies/:policy_id.
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
	c.JSON(http.StatusOK, gin.H{"data": policy.ToResponse()})
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

// DeleteShieldPolicy handles DELETE /api/v3/shield/policies/:policy_id?project=.
func (h *ShieldHandler) DeleteShieldPolicy(c *gin.Context) {
	if !h.isAdmin(c) {
		return
	}
	if err := h.crudService.Delete(c.Request.Context(), c.Param("policy_id"), c.Query("project")); err != nil {
		c.JSON(shieldStatus(err), gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

// DeployShieldPolicy handles POST /api/v3/shield/policies/:policy_id/deploy. It
// renders the stored policy into a config bundle and pushes it to the requested
// clients, reusing the command pipeline (processor/responser/fan-out/auth).
func (h *ShieldHandler) DeployShieldPolicy(c *gin.Context) {
	if !h.isAdmin(c) {
		return
	}
	var body struct {
		Project       string   `json:"project" binding:"required"`
		TargetClients []string `json:"target_clients" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	if len(body.TargetClients) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "target_clients is required"})
		return
	}
	policy, err := h.crudService.GetByID(c.Request.Context(), c.Param("policy_id"), body.Project)
	if err != nil {
		c.JSON(shieldStatus(err), gin.H{"message": err.Error()})
		return
	}
	cfg := policy.ToConfigJSON()
	resp, err := h.dispatch(c, policy.Name, body.Project, client.SubCommandType_UPDATE_SHIELD_CONFIG, body.TargetClients, &cfg)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error(), "data": resp})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": resp})
}

// StatusShieldPolicy handles GET /api/v3/shield/policies/:policy_id/status?project=&client_id=.
func (h *ShieldHandler) StatusShieldPolicy(c *gin.Context) {
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

// dispatch builds a SHIELD Operation and routes it through the client command
// handler, reusing its processor/responser, parallel fan-out, and authorization.
func (h *ShieldHandler) dispatch(c *gin.Context, name, project string, sub client.SubCommandType, targets []string, cfg *models.ShieldConfigJSON) (any, error) {
	clients := make([]models.ServiceClients, 0, len(targets))
	for _, cid := range targets {
		clients = append(clients, models.ServiceClients{ClientID: cid})
	}
	op := &models.Operations{
		Type:    models.CommandTypeJSON(client.CommandType_SHIELD),
		SubType: models.SubCommandTypeJSON(sub),
		Clients: clients,
		Command: models.Command{Project: project, Name: name},
		ShieldOp: &models.RequestShieldJSON{
			Operation: sub.String(),
			Config:    cfg,
		},
	}
	requestDetails, _ := h.parentHandler.getRequestDetails(c)
	return h.parentHandler.Client.Handler.HandleSendCommand(c.Request.Context(), op, requestDetails)
}
