package waf

import (
	"context"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud"
	"github.com/CloudNativeWorks/elchi-backend/controller/poker"
	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// WAFUpdateResult contains the result of updating a WASM extension with new WAF config
type WAFUpdateResult struct {
	ExtensionName     string   `json:"extension_name"`
	CanonicalName     string   `json:"canonical_name"`
	Collection        string   `json:"collection"`
	Success           bool     `json:"success"`
	Error             string   `json:"error,omitempty"`
	AffectedListeners []string `json:"affected_listeners,omitempty"`
}

// WAFPropagationService handles propagating WAF config updates to dependent WASM extensions
type WAFPropagationService struct {
	dbContext *db.AppContext
	logger    *logger.Logger
}

// NewWAFPropagationService creates a new WAF propagation service
func NewWAFPropagationService(dbContext *db.AppContext, logger *logger.Logger) *WAFPropagationService {
	return &WAFPropagationService{
		dbContext: dbContext,
		logger:    logger,
	}
}

// TriggerDependencyAnalysis triggers the dependency analysis for the updated WASM extension
// This follows the same flow as XDS resource updates
func (s *WAFPropagationService) TriggerDependencyAnalysis(ctx context.Context, wasmExt WASMExtensionInfo, resource models.ResourceClass, pokeService *bridge.PokeServiceClient, userDetails models.UserDetails) *poker.Processed {
	// Build request details for dependency analysis
	requestDetails := models.RequestDetails{
		Name:          wasmExt.Name,
		Collection:    wasmExt.Collection,
		Version:       wasmExt.Version,
		Project:       wasmExt.Project,
		SaveOrPublish: "publish", // Critical: must be "publish" to trigger dependency analysis
		User:          userDetails,
	}

	// Use the same dependency analysis system as XDS updates
	// This will:
	// 1. Find all downstream resources that depend on this extension
	// 2. Find the listeners that need to be updated
	// 3. Trigger snapshot generation via control-plane
	// 4. Update Envoy configurations
	changedResources := crud.HandleResourceChange(
		ctx,
		resource,
		requestDetails,
		s.dbContext,
		wasmExt.Project,
		pokeService,
	)

	return changedResources
}
