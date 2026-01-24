package waf

import (
	"context"
	"errors"
	"fmt"
	"time"

	asyncPkg "github.com/CloudNativeWorks/elchi-backend/pkg/async"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async/job"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	WAFCollection = "waf"
)

// WAFCRUDService handles CRUD operations for WAF configurations
type WAFCRUDService struct {
	dbContext   *db.AppContext
	logger      *logger.Logger
	asyncSystem asyncPkg.AsyncJobSystem
}

// NewWAFCRUDService creates a new WAF CRUD service
func NewWAFCRUDService(dbContext *db.AppContext, logger *logger.Logger, asyncSystem asyncPkg.AsyncJobSystem) *WAFCRUDService {
	return &WAFCRUDService{
		dbContext:   dbContext,
		logger:      logger,
		asyncSystem: asyncSystem,
	}
}

// validateWAFConfigData validates WAF configuration data
func validateWAFConfigData(data *WAFConfigData) error {
	// Validate directives_map has at least one entry
	if len(data.DirectivesMap) == 0 {
		return fmt.Errorf("directives_map cannot be empty")
	}

	// Validate default_directives exists in directives_map
	if _, exists := data.DirectivesMap[data.DefaultDirectives]; !exists {
		return fmt.Errorf("default_directives '%s' not found in directives_map", data.DefaultDirectives)
	}

	// Validate per_authority_directives references exist in directives_map
	for authority, directiveKey := range data.PerAuthorityDirectives {
		if _, exists := data.DirectivesMap[directiveKey]; !exists {
			return fmt.Errorf("per_authority_directives[%s] references '%s' which not found in directives_map", authority, directiveKey)
		}
	}

	return nil
}

// Create creates a new WAF configuration
func (s *WAFCRUDService) Create(ctx context.Context, req WAFConfigRequest, userDetails models.UserDetails) (*WAFConfig, error) {
	now := time.Now()

	config := &WAFConfig{
		ID:        primitive.NewObjectID(),
		Name:      req.Name,
		CreatedAt: now,
		UpdatedAt: now,
		Project:   req.Project,
		Data:      req.Data,
	}

	// Initialize empty maps if nil
	if config.Data.MetricLabels == nil {
		config.Data.MetricLabels = make(map[string]string)
	}
	if config.Data.PerAuthorityDirectives == nil {
		config.Data.PerAuthorityDirectives = make(map[string]string)
	}

	// Validate config data
	if err := validateWAFConfigData(&config.Data); err != nil {
		return nil, err
	}

	_, err := s.dbContext.Client.Collection(WAFCollection).InsertOne(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create WAF config: %w", err)
	}

	s.logger.Infof("User '%s' created WAF config '%s' (ID: %s) in project '%s'",
		userDetails.UserName, config.Name, config.ID.Hex(), config.Project)

	return config, nil
}

// Update updates an existing WAF configuration and creates async job for propagation
func (s *WAFCRUDService) Update(ctx context.Context, id string, req WAFConfigRequest, userDetails models.UserDetails) (*WAFConfig, *WAFConfig, *asyncPkg.Job, error) {
	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("invalid ID: %w", err)
	}

	// Get old config for comparison
	oldConfig, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, nil, nil, err
	}

	// Initialize empty maps if nil
	if req.Data.MetricLabels == nil {
		req.Data.MetricLabels = make(map[string]string)
	}
	if req.Data.PerAuthorityDirectives == nil {
		req.Data.PerAuthorityDirectives = make(map[string]string)
	}

	// Validate config data
	if err := validateWAFConfigData(&req.Data); err != nil {
		return nil, nil, nil, err
	}

	update := bson.M{
		"$set": bson.M{
			"name":       req.Name,
			"updated_at": time.Now(),
			"project":    req.Project,
			"data":       req.Data,
		},
	}

	result := s.dbContext.Client.Collection(WAFCollection).FindOneAndUpdate(
		ctx,
		bson.M{"_id": objectID},
		update,
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	)

	if result.Err() != nil {
		if errors.Is(result.Err(), mongo.ErrNoDocuments) {
			return nil, nil, nil, fmt.Errorf("WAF config not found")
		}
		return nil, nil, nil, fmt.Errorf("failed to update WAF config: %w", result.Err())
	}

	var newConfig WAFConfig
	if err := result.Decode(&newConfig); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to decode updated config: %w", err)
	}

	s.logger.Infof("Successfully updated WAF config '%s', creating async job for propagation", newConfig.Name)

	// Find all WASM extensions using this WAF
	wasmExtensions, err := FindWASMExtensionsUsingWAF(ctx, s.dbContext.Client, newConfig.Name, newConfig.Project)
	if err != nil {
		s.logger.Errorf("Failed to find WASM extensions: %v", err)
		return oldConfig, &newConfig, nil, nil
	}

	// If no WASM extensions use this WAF, no job needed
	if len(wasmExtensions) == 0 {
		s.logger.Infof("No WASM extensions use WAF '%s', no propagation needed", newConfig.Name)
		return oldConfig, &newConfig, nil, nil
	}

	// Extract WASM names for job metadata
	wasmNames := make([]string, len(wasmExtensions))
	for i, ext := range wasmExtensions {
		wasmNames[i] = ext.Name
	}

	// Create async job for WAF propagation
	// Note: Each WASM extension will use its own version during processing
	jobReq := &asyncPkg.CreateJobRequest{
		Type:   job.JobTypeWAFPropagation,
		Status: job.JobStatusPending,
		Metadata: &asyncPkg.JobMetadata{
			SourceResource: &asyncPkg.SourceResource{
				Type:       "WAF_CONFIG",
				Name:       newConfig.Name,
				Collection: WAFCollection,
				Action:     job.ActionTypeUpdate,
				ProjectID:  newConfig.Project,
				Version:    "v0", // General version for WAF propagation jobs
			},
			WAFConfig: &job.WAFConfigMeta{
				Name:    newConfig.Name,
				Project: newConfig.Project,
			},
			AffectedWASM:  wasmNames,
			TotalAffected: len(wasmNames),
			TriggerUser: &asyncPkg.TriggerUser{
				ID:       userDetails.UserID,
				Username: userDetails.UserName,
				Role:     string(userDetails.Role),
			},
		},
	}

	job, err := s.asyncSystem.CreateJob(ctx, jobReq)
	if err != nil {
		s.logger.Errorf("Failed to create WAF propagation job: %v", err)
		// Don't fail the entire update if job creation fails
		return oldConfig, &newConfig, nil, nil
	}

	s.logger.Infof("Created WAF propagation job %s for %d WASM extensions", job.JobID, len(wasmNames))
	return oldConfig, &newConfig, job, nil
}

// Delete deletes a WAF configuration
func (s *WAFCRUDService) Delete(ctx context.Context, id string, userDetails models.UserDetails) (*WAFConfig, error) {
	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, fmt.Errorf("invalid ID: %w", err)
	}

	// Get config before deletion for audit
	config, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	// Check if WAF is being used by any WASM extensions
	usedBy, err := FindWASMExtensionsUsingWAF(ctx, s.dbContext.Client, config.Name, config.Project)
	if err != nil {
		return nil, fmt.Errorf("failed to check WAF usage: %w", err)
	}

	if len(usedBy) > 0 {
		// Build list of extension names for error message
		extensionNames := make([]string, len(usedBy))
		for i, ext := range usedBy {
			extensionNames[i] = ext.CanonicalName
		}
		return nil, fmt.Errorf("WAF config '%s' is being used by %d WASM extension(s): %v. Please remove WAF reference from these extensions before deleting",
			config.Name, len(usedBy), extensionNames)
	}

	result, err := s.dbContext.Client.Collection(WAFCollection).DeleteOne(ctx, bson.M{"_id": objectID})
	if err != nil {
		return nil, fmt.Errorf("failed to delete WAF config: %w", err)
	}

	if result.DeletedCount == 0 {
		return nil, fmt.Errorf("WAF config not found")
	}

	s.logger.Infof("User '%s' deleted WAF config '%s' (ID: %s) from project '%s'",
		userDetails.UserName, config.Name, config.ID.Hex(), config.Project)

	return config, nil
}

// GetByID retrieves a WAF configuration by ID
func (s *WAFCRUDService) GetByID(ctx context.Context, id string) (*WAFConfig, error) {
	objectID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, fmt.Errorf("invalid ID: %w", err)
	}

	var config WAFConfig
	err = s.dbContext.Client.Collection(WAFCollection).FindOne(ctx, bson.M{"_id": objectID}).Decode(&config)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("WAF config not found")
		}
		return nil, fmt.Errorf("failed to get WAF config: %w", err)
	}

	return &config, nil
}

// List retrieves WAF configurations with optional filters
func (s *WAFCRUDService) List(ctx context.Context, project string) ([]WAFConfig, error) {
	filter := bson.M{}

	if project != "" {
		filter["project"] = project
	}

	cursor, err := s.dbContext.Client.Collection(WAFCollection).Find(
		ctx,
		filter,
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list WAF configs: %w", err)
	}
	defer cursor.Close(ctx)

	var configs []WAFConfig
	if err := cursor.All(ctx, &configs); err != nil {
		return nil, fmt.Errorf("failed to decode WAF configs: %w", err)
	}

	return configs, nil
}
