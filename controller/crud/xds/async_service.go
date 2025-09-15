package xds

import (
	"context"
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async/job"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// AsyncXDSService handles async processing for XDS resources
type AsyncXDSService struct {
	handler *AppHandler
}

// NewAsyncXDSService creates a new async XDS service
func NewAsyncXDSService(handler *AppHandler) *AsyncXDSService {
	return &AsyncXDSService{handler: handler}
}

// ProcessAsyncUpdate handles async processing with fast response and background analysis
func (s *AsyncXDSService) ProcessAsyncUpdate(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails, project string) (gin.H, error) {
	// Initialize async job system
	asyncSystem, err := s.initializeAsyncSystem()
	if err != nil {
		s.handler.Logger.Errorf("Failed to initialize async system: %v", err)
		return s.fallbackToSyncProcessing(ctx, resource, requestDetails, project)
	}

	// Set poke service for async system
	if err := asyncSystem.SetPokeService(s.handler.PokeService); err != nil {
		s.handler.Logger.Errorf("Failed to set poke service: %v", err)
		return s.fallbackToSyncProcessing(ctx, resource, requestDetails, project)
	}

	// Create preliminary job immediately (fast response)
	preliminaryJob, err := s.createPreliminaryJob(ctx, asyncSystem, resource, requestDetails, project)
	if err != nil {
		s.handler.Logger.Errorf("Failed to create preliminary job: %v", err)
		return s.fallbackToSyncProcessing(ctx, resource, requestDetails, project)
	}

	// Start background dependency analysis
	asyncSystem.PerformBackgroundAnalysis(preliminaryJob.ID.Hex(), resource, requestDetails, project)

	s.handler.Logger.Infof("Created preliminary job %s for %s resource %s, analyzing dependencies in background",
		preliminaryJob.JobID, resource.GetGeneral().GType, requestDetails.Name)

	return s.buildFastResponse(preliminaryJob), nil
}

// initializeAsyncSystem initializes the async job system
func (s *AsyncXDSService) initializeAsyncSystem() (async.AsyncJobSystem, error) {
	return async.NewAsyncJobSystem(&async.Config{
		MongoDB:     s.handler.Context.Client,
		DBContext:   s.handler.Context,
		WorkerCount: 3,
		BatchSize:   5,
	})
}

// analyzeDependencies analyzes resource dependencies
func (s *AsyncXDSService) analyzeDependencies(ctx context.Context, asyncSystem async.AsyncJobSystem, resource models.ResourceClass, requestDetails models.RequestDetails, project string) (*async.DependencyAnalysis, error) {
	analysisReq := &async.AnalysisRequest{
		ResourceType: resource.GetGeneral().GType,
		ResourceName: requestDetails.Name,
		Project:      project,
		Version:      requestDetails.Version,
		Action:       "UPDATE",
	}

	return asyncSystem.AnalyzeDependencies(ctx, analysisReq)
}

// createAsyncJob creates a background job for processing
func (s *AsyncXDSService) createAsyncJob(ctx context.Context, asyncSystem async.AsyncJobSystem, resource models.ResourceClass, requestDetails models.RequestDetails, analysis *async.DependencyAnalysis, project string) (*async.Job, error) {
	// Determine initial status
	initialStatus := job.JobStatusPending
	if len(analysis.AffectedListeners) == 0 {
		initialStatus = job.JobStatusNoWorkNeeded
	}

	jobReq := &async.CreateJobRequest{
		Type:   job.JobTypeSnapshotUpdate,
		Status: initialStatus,
		Metadata: &job.JobMetadata{
			SourceResource: &job.SourceResource{
				Type:       resource.GetGeneral().GType.String(),
				Name:       requestDetails.Name,
				Collection: requestDetails.Collection,
				Action:     job.ActionType("UPDATE"),
				ProjectID:  project,
				Version:    requestDetails.Version,
			},
			TriggerUser: &job.TriggerUser{
				ID:       requestDetails.User.UserID,
				Username: requestDetails.User.UserName,
				Role:     string(requestDetails.User.Role),
			},
			AffectedListeners: analysis.AffectedListeners,
			TotalAffected:     len(analysis.AffectedListeners),
			AnalysisDuration:  analysis.DurationMS,
		},
	}

	return asyncSystem.CreateJob(ctx, jobReq)
}

// createPreliminaryJob creates a job without dependency analysis for fast response
func (s *AsyncXDSService) createPreliminaryJob(ctx context.Context, asyncSystem async.AsyncJobSystem, resource models.ResourceClass, requestDetails models.RequestDetails, project string) (*async.Job, error) {
	jobReq := &async.CreateJobRequest{
		Type:   job.JobTypeSnapshotUpdate,
		Status: job.JobStatusAnalyzing, // Will be set automatically by CreatePreliminaryJob
		Metadata: &job.JobMetadata{
			SourceResource: &job.SourceResource{
				Type:       resource.GetGeneral().GType.String(),
				Name:       requestDetails.Name,
				Collection: requestDetails.Collection,
				Action:     job.ActionType("UPDATE"),
				ProjectID:  project,
				Version:    requestDetails.Version,
			},
			TriggerUser: &job.TriggerUser{
				ID:       requestDetails.User.UserID,
				Username: requestDetails.User.UserName,
				Role:     string(requestDetails.User.Role),
			},
			AffectedListeners: []string{}, // Will be populated by background analysis
			TotalAffected:     0,          // Will be updated after analysis
			AnalysisDuration:  0,          // Will be set after analysis completes
		},
	}

	return asyncSystem.CreatePreliminaryJob(ctx, jobReq)
}

// fallbackToSyncProcessing provides fallback to synchronous processing
func (s *AsyncXDSService) fallbackToSyncProcessing(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails, project string) (gin.H, error) {
	changedResources := crud.HandleResourceChange(ctx, resource, requestDetails, s.handler.Context, project, s.handler.PokeService)
	return gin.H{"message": "Success", "data": changedResources}, nil
}

// buildSuccessResponse builds the success response with job information
func (s *AsyncXDSService) buildSuccessResponse(backgroundJob *async.Job, analysis *async.DependencyAnalysis) gin.H {
	return gin.H{
		"message": "Success",
		"data": gin.H{
			"task_id":              backgroundJob.JobID,
			"affected_listeners":   analysis.AffectedListeners,
			"dependency_chain":     analysis.DependencyChain,
			"status":               "processing",
			"message":              fmt.Sprintf("Background job created to update %d listeners", len(analysis.AffectedListeners)),
			"analysis_duration_ms": analysis.DurationMS,
		},
	}
}

// buildFastResponse builds the fast response with preliminary job information
func (s *AsyncXDSService) buildFastResponse(preliminaryJob *async.Job) gin.H {
	return gin.H{
		"message": "Success",
		"data": gin.H{
			"task_id": preliminaryJob.JobID,
			"status":  "analyzing",
			"message": "Resource saved successfully, dependency analysis in progress",
		},
	}
}
