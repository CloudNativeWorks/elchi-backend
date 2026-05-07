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

// maxSetDescriptionLength bounds per-set descriptions (§3.2 of the redesign plan).
const maxSetDescriptionLength = 280

// normalizeData replaces nil slices/maps with empty ones so JSON output is
// stable ([] / {} rather than null) and downstream validators can iterate
// without nil checks.
func normalizeData(data *WAFConfigData) {
	if data.Sets == nil {
		data.Sets = []DirectiveSet{}
	}
	if data.MetricLabels == nil {
		data.MetricLabels = make(map[string]string)
	}
	if data.PerAuthorityDirectives == nil {
		data.PerAuthorityDirectives = make(map[string]string)
	}
}

// validateWAFConfigData validates WAF configuration data against the canonical
// schema. Validation runs after UnmarshalJSON has lifted whatever wire shape
// was sent into the canonical Sets[] form.
func validateWAFConfigData(data *WAFConfigData) error {
	if len(data.Sets) == 0 {
		return fmt.Errorf("sets cannot be empty")
	}

	if data.DefaultSet == "" {
		return fmt.Errorf("default_set is required")
	}

	seen := make(map[string]struct{}, len(data.Sets))
	defaultExists := false
	for i, s := range data.Sets {
		if s.Name == "" {
			return fmt.Errorf("sets[%d].name cannot be empty", i)
		}
		if _, dup := seen[s.Name]; dup {
			return fmt.Errorf("duplicate set name: '%s'", s.Name)
		}
		seen[s.Name] = struct{}{}
		if s.Name == data.DefaultSet {
			defaultExists = true
		}
		if len(s.Description) > maxSetDescriptionLength {
			return fmt.Errorf("sets[%d].description exceeds %d characters", i, maxSetDescriptionLength)
		}
	}

	if !defaultExists {
		return fmt.Errorf("default_set '%s' does not name an existing set", data.DefaultSet)
	}

	for authority, setName := range data.PerAuthorityDirectives {
		if _, exists := seen[setName]; !exists {
			return fmt.Errorf("per_authority_directives[%s] references unknown set '%s'", authority, setName)
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

	normalizeData(&config.Data)

	if err := validateWAFConfigData(&config.Data); err != nil {
		return nil, err
	}

	_, err := s.dbContext.Client.Collection(WAFCollection).InsertOne(ctx, config)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil, ErrWAFNameTaken
		}
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

	// Name and Project are referenced by WASM extensions via {project, name};
	// silently rewriting them would orphan those references and break the
	// data plane. Reject explicitly.
	if req.Name != oldConfig.Name || req.Project != oldConfig.Project {
		return nil, nil, nil, ErrWAFIdentityImmutable
	}

	normalizeData(&req.Data)

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
		if mongo.IsDuplicateKeyError(result.Err()) {
			return nil, nil, nil, ErrWAFNameTaken
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
		refs := make([]WAFUsageRef, len(usedBy))
		for i, ext := range usedBy {
			refs[i] = WAFUsageRef{
				Type: "wasm_extension",
				ID:   ext.ID,
				Name: ext.CanonicalName,
			}
		}
		return nil, &WAFInUseError{Name: config.Name, References: refs}
	}

	result, err := s.dbContext.Client.Collection(WAFCollection).DeleteOne(ctx, bson.M{"_id": objectID})
	if err != nil {
		return nil, fmt.Errorf("failed to delete WAF config: %w", err)
	}

	if result.DeletedCount == 0 {
		return nil, fmt.Errorf("WAF config not found")
	}

	// Cascade-delete version snapshots for this config so they don't pile up
	// as orphans. Best-effort: a failure here doesn't undo the config delete.
	if delRes, err := s.dbContext.Client.Collection(WAFVersionsCollection).DeleteMany(ctx, bson.M{"config_id": objectID}); err != nil {
		s.logger.Warnf("WAF cascade: version cleanup failed for config %s: %v", objectID.Hex(), err)
	} else if delRes.DeletedCount > 0 {
		s.logger.Infof("WAF cascade: pruned %d version snapshots for deleted config %s", delRes.DeletedCount, objectID.Hex())
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
