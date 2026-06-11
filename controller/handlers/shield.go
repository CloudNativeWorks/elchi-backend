package handlers

import (
	"errors"
	"net/http"

	"github.com/CloudNativeWorks/elchi-backend/controller/shield"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async"
	"github.com/CloudNativeWorks/elchi-backend/pkg/audit"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	client "github.com/CloudNativeWorks/elchi-proto/client"
	"github.com/gin-gonic/gin"
)

// ShieldHandler exposes the elchi-shield policy store (CRUD). Any create/update/
// delete enqueues an async SHIELD_DEPLOY job that pushes the project's MERGED
// policy set (full-sync) to the project's connected clients — there is no manual
// per-policy/per-client deploy. The HTTP request returns immediately with the
// job id; the worker performs the push (and a (re)connecting client is brought up
// to the current desired state by a connect-triggered job).
type ShieldHandler struct {
	crudService   *shield.CRUDService
	enqueuer      *shield.Enqueuer
	logger        *logger.Logger
	parentHandler *Handler
}

// NewShieldHandler builds the shield handler over the policy store and async job
// system (used to enqueue deploy jobs).
func NewShieldHandler(dbContext *db.AppContext, asyncSystem async.AsyncJobSystem, logger *logger.Logger) *ShieldHandler {
	return &ShieldHandler{
		crudService: shield.NewCRUDService(dbContext, logger),
		enqueuer:    shield.NewEnqueuer(asyncSystem, logger),
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

// setShieldAudit stamps the audit entry for a shield policy mutation. The global
// AuditMiddleware persists the entry only when an action is set, so without this
// the security-policy mutations would go unaudited (unlike GSLB/LDAP/settings).
func (h *ShieldHandler) setShieldAudit(c *gin.Context, action, policyID, policyName, project string) {
	if h.parentHandler == nil || h.parentHandler.AuditService == nil {
		return
	}
	audit.SetAuditResource(c, "shield_policies", policyID, policyName, project)
	audit.SetAuditAction(c, action)
}

func shieldStatus(err error) int {
	switch {
	case errors.Is(err, shield.ErrPolicyNotFound):
		return http.StatusNotFound
	case errors.Is(err, shield.ErrPolicyNameTaken), errors.Is(err, shield.ErrPolicyPathConflict):
		return http.StatusConflict
	case errors.Is(err, shield.ErrPolicyInvalid):
		return http.StatusBadRequest
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
	h.setShieldAudit(c, "CREATE_SHIELD_POLICY", "", req.Name, req.Project)
	policy, err := h.crudService.Create(c.Request.Context(), req)
	h.parentHandler.setAuditResult(c, err)
	if err != nil {
		c.JSON(shieldStatus(err), gin.H{"message": err.Error()})
		return
	}
	audit.SetAuditResource(c, "shield_policies", policy.ID.Hex(), policy.Name, req.Project)
	c.JSON(http.StatusCreated, gin.H{"data": policy.ToResponse(), "deploy": h.enqueueDeploy(c, req.Project)})
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
	h.setShieldAudit(c, "UPDATE_SHIELD_POLICY", c.Param("policy_id"), req.Name, req.Project)
	policy, err := h.crudService.Update(c.Request.Context(), c.Param("policy_id"), req)
	h.parentHandler.setAuditResult(c, err)
	if err != nil {
		c.JSON(shieldStatus(err), gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": policy.ToResponse(), "deploy": h.enqueueDeploy(c, req.Project)})
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

// ListShieldPolicies handles GET /api/v3/shield/policies?project=. Inline file
// content is omitted (it can be MiBs); GET /policies/:policy_id returns it.
func (h *ShieldHandler) ListShieldPolicies(c *gin.Context) {
	policies, err := h.crudService.ListMeta(c.Request.Context(), c.Query("project"))
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
	h.setShieldAudit(c, "DELETE_SHIELD_POLICY", c.Param("policy_id"), "", project)
	err := h.crudService.Delete(c.Request.Context(), c.Param("policy_id"), project)
	h.parentHandler.setAuditResult(c, err)
	if err != nil {
		c.JSON(shieldStatus(err), gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted", "deploy": h.enqueueDeploy(c, project)})
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
	h.setShieldAudit(c, "SYNC_SHIELD_POLICIES", "", "", body.Project)
	h.parentHandler.setAuditResult(c, nil)
	c.JSON(http.StatusOK, gin.H{"data": h.enqueueDeploy(c, body.Project)})
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

// ShieldFiles handles GET /api/v3/shield/files?project=&client_id=. It reads back a
// connected edge client's live on-disk shield file set (path/sha256/mode) by
// dispatching GET_SHIELD_CONFIG — a command dispatch, so it is admin/owner-gated.
// Useful to confirm what a given edge actually has applied vs the desired bundle.
func (h *ShieldHandler) ShieldFiles(c *gin.Context) {
	if !h.isAdmin(c) {
		return
	}
	clientID := c.Query("client_id")
	if clientID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "client_id is required"})
		return
	}
	resp, err := h.dispatch(c, "", c.Query("project"), client.SubCommandType_GET_SHIELD_CONFIG, []string{clientID}, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error(), "data": resp})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": resp})
}

// enqueueDeploy enqueues an async SHIELD_DEPLOY job for the project's connected
// clients and returns the job id for the response body. It never returns an error
// to the caller — a store mutation succeeds regardless of deploy outcome; the
// worker performs the render + push and records the job's terminal status. The
// triggering admin is carried onto the job so the worker's command authorization
// runs as that identity.
func (h *ShieldHandler) enqueueDeploy(c *gin.Context, project string) any {
	_, userDetails := h.parentHandler.getRequestDetails(c)
	jobID, err := h.enqueuer.EnqueueProjectDeploy(c.Request.Context(), project, userDetails, shield.ReasonPolicyChange)
	if err != nil {
		h.logger.Errorf("shield deploy: enqueue job for project %s: %v", project, err)
		return gin.H{"enqueued": false, "error": err.Error()}
	}
	return gin.H{"enqueued": true, "deploy_job": jobID}
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
