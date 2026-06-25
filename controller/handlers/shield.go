package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/controller/shield"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async"
	"github.com/CloudNativeWorks/elchi-backend/pkg/audit"
	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/clickhouse"
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
	// dbContext gives the events read API access to the shared ClickHouse client
	// (shield audit table) and Mongo (project authorization). Nil ClickHouse →
	// the events endpoints return 503 (graceful degradation, like inventory).
	dbContext *db.AppContext
}

// NewShieldHandler builds the shield handler over the policy store and async job
// system (used to enqueue deploy jobs).
func NewShieldHandler(dbContext *db.AppContext, asyncSystem async.AsyncJobSystem, logger *logger.Logger) *ShieldHandler {
	return &ShieldHandler{
		crudService: shield.NewCRUDService(dbContext, logger),
		enqueuer:    shield.NewEnqueuer(asyncSystem, logger),
		logger:      logger,
		dbContext:   dbContext,
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

// shieldEventsSlots bounds concurrent ClickHouse queries from the events
// endpoints so a hammering client (the summary fires expensive scans) can't
// monopolize the shared connection pool that inventory and others also use. 16
// is generous for normal multi-user dashboards; excess gets a transient 503.
var shieldEventsSlots = make(chan struct{}, 16)

// acquireEventsSlot reserves a query slot, or writes 503 and returns ok=false.
func acquireEventsSlot(c *gin.Context) (release func(), ok bool) {
	select {
	case shieldEventsSlots <- struct{}{}:
		return func() { <-shieldEventsSlots }, true
	default:
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "shield events temporarily busy; retry shortly"})
		return nil, false
	}
}

// Shield events feed/window caps (mirrors the inventory detail endpoints).
const (
	shieldEventsDefaultRangeMs = int64(24 * time.Hour / time.Millisecond) // last 24h
	shieldEventsMaxRangeDays   = 30
	shieldEventsDefaultLimit   = 50
	shieldEventsMaxLimit       = 500
)

// shieldEventsFilter builds the ClickHouse filter from the request's common
// query params (shared by the feed and the summary). Time window + project are
// validated by the caller; this only maps the optional narrowing knobs.
func shieldEventsFilter(c *gin.Context, project string, from, to time.Time) clickhouse.ShieldEventsFilter {
	return clickhouse.ShieldEventsFilter{
		ProjectID:    project,
		NodeID:       c.Query("node_id"),
		Instance:     c.Query("instance"),
		Engine:       c.Query("engine"),
		Action:       c.Query("action"),
		Severity:     c.Query("severity"),
		Host:         c.Query("host"),
		Method:       c.Query("method"),
		Path:         c.Query("path"),
		FindingsOnly: c.Query("findings_only") == "true",
		From:         from,
		To:           to,
	}
}

// resolveShieldEventsRequest runs the shared gate for all events endpoints:
// admin/owner role, ClickHouse availability, a required + authorized project,
// the parsed time window, and finally a concurrency slot. It writes the error
// response and returns ok=false when any check fails. On success the caller MUST
// `defer release()` to free the slot.
func (h *ShieldHandler) resolveShieldEventsRequest(c *gin.Context) (project string, from, to time.Time, release func(), ok bool) {
	if !h.isAdmin(c) {
		return "", time.Time{}, time.Time{}, nil, false
	}
	if h.dbContext == nil || h.dbContext.Clickhouse == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "clickhouse not configured; shield events unavailable"})
		return "", time.Time{}, time.Time{}, nil, false
	}
	project = c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project is required"})
		return "", time.Time{}, time.Time{}, nil, false
	}
	_, userDetails := h.parentHandler.getRequestDetails(c)
	if err := authorization.ValidateRequestProject(c.Request.Context(), h.dbContext.Client, userDetails, project); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"message": err.Error()})
		return "", time.Time{}, time.Time{}, nil, false
	}
	from, to, perr := parseTimeWindow(c, shieldEventsDefaultRangeMs, shieldEventsMaxRangeDays)
	if perr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": perr.Error()})
		return "", time.Time{}, time.Time{}, nil, false
	}
	release, slotOK := acquireEventsSlot(c)
	if !slotOK {
		return "", time.Time{}, time.Time{}, nil, false
	}
	return project, from, to, release, true
}

// ListShieldEvents handles GET /api/v3/shield/events?project=&... — a project-
// scoped, filterable feed of shield security decisions (what edges blocked or
// detected) read from the central ClickHouse. Admin/Owner-gated like the other
// shield read endpoints. Optional filters: node_id, engine, action, severity,
// host, method, path, findings_only, from/to, limit/offset, include_total.
func (h *ShieldHandler) ListShieldEvents(c *gin.Context) {
	project, from, to, release, ok := h.resolveShieldEventsRequest(c)
	if !ok {
		return
	}
	defer release()
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", strconv.Itoa(shieldEventsDefaultLimit)))
	if limit <= 0 {
		limit = shieldEventsDefaultLimit
	}
	if limit > shieldEventsMaxLimit {
		limit = shieldEventsMaxLimit
	}
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if offset < 0 {
		offset = 0
	}

	filter := shieldEventsFilter(c, project, from, to)
	filter.Limit = limit
	filter.Offset = offset
	filter.IncludeTotal = c.Query("include_total") == "true"

	events, total, err := h.dbContext.Clickhouse.QueryShieldEvents(c.Request.Context(), filter)
	if err != nil {
		h.logger.Errorf("shield events query failed: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"message": "failed to query shield events"})
		return
	}
	c.JSON(http.StatusOK, buildPaginationResponse(events, total, int64(limit), int64(offset)))
}

// ShieldEventsSummary handles GET /api/v3/shield/events/summary?project=&... —
// aggregate counts (by engine/action/severity) + a per-action time series for
// the dashboard cards and chart, over the same filters as the feed.
func (h *ShieldHandler) ShieldEventsSummary(c *gin.Context) {
	project, from, to, release, ok := h.resolveShieldEventsRequest(c)
	if !ok {
		return
	}
	defer release()
	filter := shieldEventsFilter(c, project, from, to)
	// ~60 buckets across the window for the chart (the query clamps to >= 60s).
	bucketSeconds := int(to.Sub(from).Seconds()) / 60

	summary, err := h.dbContext.Clickhouse.QueryShieldEventsSummary(c.Request.Context(), filter, bucketSeconds)
	if err != nil {
		h.logger.Errorf("shield events summary query failed: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"message": "failed to query shield events summary"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": summary})
}

// ShieldEventsFacets handles GET /api/v3/shield/events/facets?project=&... — the
// distinct engine/action/severity/host/node values in the window, so the UI can
// build filter dropdowns from real data. Same gate as the feed.
func (h *ShieldHandler) ShieldEventsFacets(c *gin.Context) {
	project, from, to, release, ok := h.resolveShieldEventsRequest(c)
	if !ok {
		return
	}
	defer release()
	facets, err := h.dbContext.Clickhouse.QueryShieldEventsFacets(c.Request.Context(), shieldEventsFilter(c, project, from, to))
	if err != nil {
		h.logger.Errorf("shield events facets query failed: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"message": "failed to query shield event facets"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": facets})
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
	if errors.Is(err, shield.ErrDeployAlreadyQueued) {
		// Benign: a PENDING deploy already exists and reads the policy store at
		// execution time, so it will deliver this change too.
		return gin.H{"enqueued": true, "deduped": true, "message": err.Error()}
	}
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
