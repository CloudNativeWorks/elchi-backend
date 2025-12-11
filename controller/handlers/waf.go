package handlers

import (
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/controller/waf"
	asyncPkg "github.com/CloudNativeWorks/elchi-backend/pkg/async"
	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/gin-gonic/gin"
)

// WAFHandler wraps the WAF handler
type WAFHandler struct {
	crsHandler    *waf.WAFHandler
	crudService   *waf.WAFCRUDService
	pokeService   *bridge.PokeServiceClient
	asyncSystem   asyncPkg.AsyncJobSystem
	logger        *logger.Logger
	parentHandler *Handler // Reference to parent Handler for audit functions
}

// NewWAFHandler creates a new WAF handler wrapper
func NewWAFHandler(dbContext *db.AppContext, pokeService *bridge.PokeServiceClient, asyncSystem asyncPkg.AsyncJobSystem, logger *logger.Logger) *WAFHandler {
	return &WAFHandler{
		crsHandler:  waf.NewWAFHandler(),
		crudService: waf.NewWAFCRUDService(dbContext, logger, asyncSystem),
		pokeService: pokeService,
		asyncSystem: asyncSystem,
		logger:      logger,
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
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

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
		c.JSON(500, gin.H{"error": err.Error()})
		return
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
