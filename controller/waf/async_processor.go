package waf

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	bridgePkg "github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// AsyncWAFProcessor handles async WAF propagation operations
// This is designed to be called from worker without creating import cycles
type AsyncWAFProcessor struct {
	dbContext   *db.AppContext
	logger      *logger.Logger
	pokeService *bridgePkg.PokeServiceClient
}

// NewAsyncWAFProcessor creates a new async WAF processor
func NewAsyncWAFProcessor(dbContext *db.AppContext, pokeService *bridgePkg.PokeServiceClient, logger *logger.Logger) *AsyncWAFProcessor {
	return &AsyncWAFProcessor{
		dbContext:   dbContext,
		logger:      logger,
		pokeService: pokeService,
	}
}

// ProcessWAFPropagationJob processes a WAF propagation job with user context
// The user who triggered the WAF update is passed for proper audit and authorization
// Returns: (affectedListeners, failedWASM, error)
func (p *AsyncWAFProcessor) ProcessWAFPropagationJob(ctx context.Context, wafName, project string, wasmNames []string, userDetails models.UserDetails) ([]string, []string, error) {
	p.logger.Infof("Starting WAF propagation for WAF '%s', %d WASM extensions (triggered by: %s [%s])",
		wafName, len(wasmNames), userDetails.UserName, userDetails.Role)

	if len(wasmNames) == 0 {
		return []string{}, []string{}, nil
	}

	// Load WAF config from database
	wafConfig, err := p.loadWAFConfig(ctx, wafName, project)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load WAF config: %w", err)
	}

	// Track affected listeners and failures
	allAffectedListeners := make(map[string]bool)
	failedWASM := []string{}

	// Process each WASM extension
	for i, wasmName := range wasmNames {
		p.logger.Infof("[%d/%d] Processing WASM extension: %s", i+1, len(wasmNames), wasmName)

		listeners, err := p.processSingleWASM(ctx, wasmName, wafConfig, project, userDetails)
		if err != nil {
			p.logger.Errorf("Failed to process WASM %s: %v", wasmName, err)
			failedWASM = append(failedWASM, wasmName)
			continue
		}

		// Add to unique listeners
		for _, listener := range listeners {
			allAffectedListeners[listener] = true
		}

		p.logger.Infof("Successfully processed WASM %s, affected %d listener(s)", wasmName, len(listeners))
	}

	// Convert map to slice
	listenersList := make([]string, 0, len(allAffectedListeners))
	for listener := range allAffectedListeners {
		listenersList = append(listenersList, listener)
	}

	p.logger.Infof("WAF propagation completed: %d WASM processed, %d listeners affected, %d failed",
		len(wasmNames), len(listenersList), len(failedWASM))

	return listenersList, failedWASM, nil
}

// processSingleWASM processes a single WASM extension with new WAF config
func (p *AsyncWAFProcessor) processSingleWASM(ctx context.Context, wasmName string, wafConfig *WAFConfig, project string, userDetails models.UserDetails) ([]string, error) {
	// Search in both filters and extensions collections
	var dbResource models.DBResource
	var collection string
	var err error

	// Try filters first
	collection = "filters"
	err = p.findWASMInCollection(ctx, collection, wasmName, project, &dbResource)
	if err != nil {
		// Try extensions
		collection = "extensions"
		err = p.findWASMInCollection(ctx, collection, wasmName, project, &dbResource)
		if err != nil {
			return nil, fmt.Errorf("WASM extension '%s' not found in filters or extensions: %w", wasmName, err)
		}
	}

	// Re-inject WAF configuration
	injector := NewWAFWasmInjector(p.dbContext.Client, p.logger)
	updatedResource, err := injector.InjectWAFConfig(ctx, &dbResource, wafConfig.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to inject WAF config: %w", err)
	}

	// Update MongoDB with new resource configuration
	coll := p.dbContext.Client.Collection(collection)
	filter := bson.M{
		"general.name":    dbResource.General.Name,
		"general.project": dbResource.General.Project,
		"general.version": dbResource.General.Version,
	}

	update := bson.M{
		"$set": bson.M{
			"resource":           updatedResource.(*models.DBResource).Resource,
			"general.updated_at": primitive.NewDateTimeFromTime(time.Now()),
		},
	}

	_, err = coll.UpdateOne(ctx, filter, update)
	if err != nil {
		return nil, fmt.Errorf("failed to update WASM in database: %w", err)
	}

	// Trigger dependency analysis and snapshot update with user context
	affectedListeners := p.triggerDependencyAnalysis(ctx, &dbResource, collection, userDetails)

	return affectedListeners, nil
}

// findWASMInCollection searches for WASM extension in a specific collection
func (p *AsyncWAFProcessor) findWASMInCollection(ctx context.Context, collection, wasmName, project string, result *models.DBResource) error {
	coll := p.dbContext.Client.Collection(collection)
	filter := bson.M{
		"general.name":    wasmName,
		"general.project": project,
		"general.gtype":   models.HTTPWasm.String(),
	}

	return coll.FindOne(ctx, filter).Decode(result)
}

// loadWAFConfig loads WAF configuration from database
func (p *AsyncWAFProcessor) loadWAFConfig(ctx context.Context, configName, project string) (*WAFConfig, error) {
	collection := p.dbContext.Client.Collection(WAFCollection)

	var config WAFConfig
	err := collection.FindOne(ctx, bson.M{"name": configName, "project": project}).Decode(&config)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("WAF config '%s' not found in project '%s'", configName, project)
		}
		return nil, fmt.Errorf("failed to load WAF config: %w", err)
	}

	return &config, nil
}

// triggerDependencyAnalysis triggers dependency analysis for the updated WASM
// This will cause snapshot updates for affected listeners
func (p *AsyncWAFProcessor) triggerDependencyAnalysis(ctx context.Context, resource *models.DBResource, collection string, userDetails models.UserDetails) []string {
	// Use the propagation service's public method
	propagationService := NewWAFPropagationService(p.dbContext, p.logger)

	// Build WASMExtensionInfo from resource
	wasmInfo := WASMExtensionInfo{
		Name:          resource.General.Name,
		CanonicalName: resource.General.CanonicalName,
		Version:       resource.General.Version,
		Collection:    collection,
		Project:       resource.General.Project,
		GType:         resource.General.GType.String(),
	}

	p.logger.Debugf("WAF DEPENDENCY: Starting analysis for WASM '%s' (canonical: %s, gtype: %s)",
		wasmInfo.Name, wasmInfo.CanonicalName, wasmInfo.GType)

	// Trigger dependency analysis with actual user who triggered the WAF update
	changedResources := propagationService.TriggerDependencyAnalysis(ctx, wasmInfo, resource, p.pokeService, userDetails)

	if changedResources != nil {
		p.logger.Debugf("WAF DEPENDENCY: Analysis returned %d listeners, %d dependencies",
			len(changedResources.Listeners), len(changedResources.Depends))

		if changedResources.Listeners != nil {
			return changedResources.Listeners
		}
	} else {
		p.logger.Warnf("WAF DEPENDENCY: Analysis returned nil for WASM '%s'", wasmInfo.Name)
	}

	return []string{}
}
