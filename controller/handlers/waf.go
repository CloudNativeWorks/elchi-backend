package handlers

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/CloudNativeWorks/elchi-backend/controller/waf"
	asyncPkg "github.com/CloudNativeWorks/elchi-backend/pkg/async"
	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// WAFHandler wraps the WAF handler
type WAFHandler struct {
	crsHandler     *waf.WAFHandler
	crudService    *waf.WAFCRUDService
	versionService *waf.WAFVersionService
	pokeService    *bridge.PokeServiceClient
	asyncSystem    asyncPkg.AsyncJobSystem
	logger         *logger.Logger
	parentHandler  *Handler // Reference to parent Handler for audit functions
}

// NewWAFHandler creates a new WAF handler wrapper. The version pruner runs
// for the lifetime of the process.
func NewWAFHandler(dbContext *db.AppContext, pokeService *bridge.PokeServiceClient, asyncSystem asyncPkg.AsyncJobSystem, logger *logger.Logger) *WAFHandler {
	versionService := waf.NewWAFVersionService(dbContext, logger)
	versionService.StartVersionPruner(context.Background())
	return &WAFHandler{
		crsHandler:     waf.NewWAFHandler(),
		crudService:    waf.NewWAFCRUDService(dbContext, logger, asyncSystem),
		versionService: versionService,
		pokeService:    pokeService,
		asyncSystem:    asyncSystem,
		logger:         logger,
		// parentHandler will be set after Handler initialization
	}
}

// SetParentHandler sets the parent Handler reference (for audit functions)
func (h *WAFHandler) SetParentHandler(parent *Handler) {
	h.parentHandler = parent
}

// ========== CRS Rules Endpoints ==========

// GetCRSRules handles GET /api/v3/waf/crs
func (h *WAFHandler) GetCRSRules(c *gin.Context) {
	h.crsHandler.GetCRSRules(c)
}

// GetCRSVersions handles GET /api/v3/waf/crs/versions
func (h *WAFHandler) GetCRSVersions(c *gin.Context) {
	h.crsHandler.GetCRSVersions(c)
}

// GetCRSRuleByID handles GET /api/v3/waf/crs/:crs_version/:rule_id
func (h *WAFHandler) GetCRSRuleByID(c *gin.Context) {
	h.crsHandler.GetCRSRuleByID(c)
}

// ========== WAF Config CRUD Endpoints (with Audit) ==========

// CreateWAFConfigWithAudit handles POST /api/v3/waf/config
func (h *WAFHandler) CreateWAFConfigWithAudit(c *gin.Context) {
	// Only Admin and Owner can create WAF configs
	userDetails, _ := GetUserDetails(c)
	if !userDetails.IsOwner && userDetails.Role != models.RoleAdmin {
		if h.parentHandler != nil {
			h.parentHandler.setAuditResult(c, fmt.Errorf("insufficient privileges"))
		}
		c.JSON(403, gin.H{"message": "Only Admin and Owner can create WAF configurations"})
		return
	}

	var req waf.WAFConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		if h.parentHandler != nil {
			h.parentHandler.setAuditResult(c, err)
		}
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// Set audit context AFTER parsing request body
	if h.parentHandler != nil {
		requestDetails, _ := h.parentHandler.getRequestDetails(c)
		h.parentHandler.setResourceAuditContext(c, requestDetails)
	}

	config, err := h.crudService.Create(c.Request.Context(), req, userDetails)
	if err != nil {
		if h.parentHandler != nil {
			h.parentHandler.setAuditResult(c, err)
		}
		if errors.Is(err, waf.ErrWAFNameTaken) {
			c.JSON(409, gin.H{"code": "WAF_NAME_TAKEN", "error": err.Error()})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// Capture the initial state as v1 so the version history is complete
	// from the very first save. Without this, the first PUT would create v1
	// representing the post-update state and the original create state would
	// be irrecoverable.
	h.versionService.RecordSnapshot(c.Request.Context(), config, userDetails.UserID, userDetails.UserName)

	if h.parentHandler != nil {
		h.parentHandler.setAuditResult(c, nil)
	}
	c.JSON(201, config.ToResponse())
}

// UpdateWAFConfigWithAudit handles PUT /api/v3/waf/config/:config_id
func (h *WAFHandler) UpdateWAFConfigWithAudit(c *gin.Context) {
	// Only Admin and Owner can update WAF configs
	userDetails, _ := GetUserDetails(c)
	if !userDetails.IsOwner && userDetails.Role != models.RoleAdmin {
		if h.parentHandler != nil {
			h.parentHandler.setAuditResult(c, fmt.Errorf("insufficient privileges"))
		}
		c.JSON(403, gin.H{"message": "Only Admin and Owner can update WAF configurations"})
		return
	}

	id := c.Param("config_id")

	var req waf.WAFConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		if h.parentHandler != nil {
			h.parentHandler.setAuditResult(c, err)
		}
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// Set audit context AFTER parsing request body
	if h.parentHandler != nil {
		requestDetails, _ := h.parentHandler.getRequestDetails(c)
		h.parentHandler.setResourceAuditContext(c, requestDetails)
		// Capture changes for audit (compares old vs new WAF config)
		h.parentHandler.setAuditChanges(c)
	}

	oldConfig, newConfig, job, err := h.crudService.Update(c.Request.Context(), id, req, userDetails)
	if err != nil {
		if h.parentHandler != nil {
			h.parentHandler.setAuditResult(c, err)
		}
		if errors.Is(err, waf.ErrWAFNameTaken) {
			c.JSON(409, gin.H{"code": "WAF_NAME_TAKEN", "error": err.Error()})
			return
		}
		if errors.Is(err, waf.ErrWAFIdentityImmutable) {
			c.JSON(400, gin.H{"code": "WAF_IDENTITY_IMMUTABLE", "error": err.Error()})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// Best-effort snapshot. Failures are logged inside the service and never
	// fail the user request.
	if newConfig != nil {
		h.versionService.RecordSnapshot(c.Request.Context(), newConfig, userDetails.UserID, userDetails.UserName)
	}

	// Build response
	response := gin.H{
		"config":  newConfig.ToResponse(),
		"message": "WAF config updated successfully",
	}

	// Add old config for audit purposes
	if oldConfig != nil {
		response["old_config"] = oldConfig.ToResponse()
	}

	// Add job information if propagation was needed
	if job != nil {
		response["job"] = gin.H{
			"job_id":        job.JobID,
			"status":        job.Status,
			"affected_wasm": len(job.Metadata.AffectedWASM),
			"message":       "WAF propagation job created. WASM extensions will be updated in background.",
		}
	} else {
		response["message"] = "WAF config updated successfully. No WASM extensions use this WAF."
	}

	if h.parentHandler != nil {
		h.parentHandler.setAuditResult(c, nil)
	}
	c.JSON(200, response)
}

// DeleteWAFConfigWithAudit handles DELETE /api/v3/waf/config/:config_id
func (h *WAFHandler) DeleteWAFConfigWithAudit(c *gin.Context) {
	// Only Admin and Owner can delete WAF configs
	userDetails, _ := GetUserDetails(c)

	// Set audit context (DELETE has no body, safe to call early)
	if h.parentHandler != nil {
		requestDetails, _ := h.parentHandler.getRequestDetails(c)
		h.parentHandler.setResourceAuditContext(c, requestDetails)
	}
	if !userDetails.IsOwner && userDetails.Role != models.RoleAdmin {
		if h.parentHandler != nil {
			h.parentHandler.setAuditResult(c, fmt.Errorf("insufficient privileges"))
		}
		c.JSON(403, gin.H{"message": "Only Admin and Owner can delete WAF configurations"})
		return
	}

	id := c.Param("config_id")

	deletedConfig, err := h.crudService.Delete(c.Request.Context(), id, userDetails)
	if err != nil {
		if h.parentHandler != nil {
			h.parentHandler.setAuditResult(c, err)
		}
		var inUse *waf.WAFInUseError
		if errors.As(err, &inUse) {
			c.JSON(409, gin.H{
				"code":       "WAF_IN_USE",
				"error":      inUse.Error(),
				"references": inUse.References,
			})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	response := gin.H{
		"message": "WAF config deleted successfully",
	}

	// Include deleted config info for audit
	if deletedConfig != nil {
		response["deleted_config"] = deletedConfig.ToResponse()
	}

	if h.parentHandler != nil {
		h.parentHandler.setAuditResult(c, nil)
	}
	c.JSON(200, response)
}

// GetWAFConfig handles GET /api/v3/waf/config/:config_id
func (h *WAFHandler) GetWAFConfig(c *gin.Context) {
	id := c.Param("config_id")

	config, err := h.crudService.GetByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, config.ToResponse())
}

// ListWAFConfigs handles GET /api/v3/waf/config
func (h *WAFHandler) ListWAFConfigs(c *gin.Context) {
	project := c.Query("project")

	configs, err := h.crudService.List(c.Request.Context(), project)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// Convert to response format
	responses := make([]waf.WAFConfigResponse, len(configs))
	for i, config := range configs {
		responses[i] = config.ToResponse()
	}

	c.JSON(200, gin.H{
		"configs": responses,
		"total":   len(responses),
	})
}

// ========== WAF Config Versioning Endpoints ==========

// ListWAFVersions handles GET /api/v3/waf/config/:config_id/versions[?limit=N]
func (h *WAFHandler) ListWAFVersions(c *gin.Context) {
	configID, err := primitive.ObjectIDFromHex(c.Param("config_id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid config_id"})
		return
	}

	limit := int64(20)
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			limit = n
		}
	}

	versions, err := h.versionService.List(c.Request.Context(), configID, limit)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"versions": versions,
		"total":    len(versions),
	})
}

// GetWAFVersion handles GET /api/v3/waf/config/:config_id/versions/:version
func (h *WAFHandler) GetWAFVersion(c *gin.Context) {
	configID, err := primitive.ObjectIDFromHex(c.Param("config_id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid config_id"})
		return
	}
	version, err := strconv.Atoi(c.Param("version"))
	if err != nil || version < 1 {
		c.JSON(400, gin.H{"error": "invalid version"})
		return
	}

	snap, err := h.versionService.Get(c.Request.Context(), configID, version)
	if err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, snap)
}

// RestoreWAFVersion handles POST /api/v3/waf/config/:config_id/versions/:version/restore
//
// Reads the snapshot, then runs it through the standard Update flow which
// re-validates, propagates to WASM extensions, and (via our hook) records a
// fresh version snapshot. Original version remains untouched in history.
func (h *WAFHandler) RestoreWAFVersion(c *gin.Context) {
	userDetails, _ := GetUserDetails(c)
	if !userDetails.IsOwner && userDetails.Role != models.RoleAdmin {
		if h.parentHandler != nil {
			h.parentHandler.setAuditResult(c, fmt.Errorf("insufficient privileges"))
		}
		c.JSON(403, gin.H{"message": "Only Admin and Owner can restore WAF configurations"})
		return
	}

	if h.parentHandler != nil {
		requestDetails, _ := h.parentHandler.getRequestDetails(c)
		h.parentHandler.setResourceAuditContext(c, requestDetails)
	}

	configID, err := primitive.ObjectIDFromHex(c.Param("config_id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid config_id"})
		return
	}
	version, err := strconv.Atoi(c.Param("version"))
	if err != nil || version < 1 {
		c.JSON(400, gin.H{"error": "invalid version"})
		return
	}

	snap, err := h.versionService.Get(c.Request.Context(), configID, version)
	if err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}

	// Restore reverts only the Data (sets/directives/labels/per-authority
	// routing). Name and Project come from the LIVE config so the WASM
	// extension references stay valid; restoring an old name would silently
	// orphan WASM refs and break the data plane.
	live, err := h.crudService.GetByID(c.Request.Context(), configID.Hex())
	if err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}
	req := waf.WAFConfigRequest{
		Name:    live.Name,
		Project: live.Project,
		Data:    snap.Data,
	}

	oldConfig, newConfig, job, err := h.crudService.Update(c.Request.Context(), configID.Hex(), req, userDetails)
	if err != nil {
		if h.parentHandler != nil {
			h.parentHandler.setAuditResult(c, err)
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	if newConfig != nil {
		h.versionService.RecordSnapshot(c.Request.Context(), newConfig, userDetails.UserID, userDetails.UserName)
	}

	response := gin.H{
		"config":           newConfig.ToResponse(),
		"restored_version": version,
		"message":          fmt.Sprintf("WAF config restored from version %d", version),
	}
	if oldConfig != nil {
		response["old_config"] = oldConfig.ToResponse()
	}
	if job != nil {
		response["job"] = gin.H{
			"job_id":        job.JobID,
			"status":        job.Status,
			"affected_wasm": len(job.Metadata.AffectedWASM),
		}
	}

	if h.parentHandler != nil {
		h.parentHandler.setAuditResult(c, nil)
	}
	c.JSON(200, response)
}
