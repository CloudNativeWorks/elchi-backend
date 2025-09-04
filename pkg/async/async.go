package async

import (
	"context"
	"fmt"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/async/analysis"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async/job"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async/worker"
	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// AsyncJobSystem is the main interface for the async job system
type AsyncJobSystem interface {
	// Core job management
	CreateJob(ctx context.Context, req *CreateJobRequest) (*Job, error)
	GetJob(ctx context.Context, jobID string) (*Job, error)
	GetJobByHumanID(ctx context.Context, humanID string) (*Job, error)
	CancelJob(ctx context.Context, jobID string) error
	ListJobs(ctx context.Context, filter *JobFilter) ([]*Job, error)
	CountJobs(ctx context.Context, filter *JobFilter) (int64, error)
	
	// Resource dependency analysis
	AnalyzeDependencies(ctx context.Context, req *AnalysisRequest) (*DependencyAnalysis, error)
	
	// Worker management
	StartWorkers(ctx context.Context, config *WorkerConfig) error
	StopWorkers(ctx context.Context) error
	GetWorkerStatus(ctx context.Context) (*WorkerStatus, error)
	
	// Job retry operations
	RetryJob(ctx context.Context, jobID string, reason string) (*Job, error)
	RetryFailedSnapshots(ctx context.Context, jobID string) (*Job, error)
	GetStuckJobs(ctx context.Context) ([]*Job, error)
	
	// Fast response methods
	CreatePreliminaryJob(ctx context.Context, req *CreateJobRequest) (*Job, error)
	PerformBackgroundAnalysis(jobID string, resource interface{}, requestDetails interface{}, project string)
	
	// Statistics and monitoring
	GetJobStats(ctx context.Context) (*JobStats, error)
	GetJobStatsFiltered(ctx context.Context, filter *JobFilter) (*JobStats, error)
	
	// Internal access
	GetJobManager() JobManagerInterface
	
	// Integration hooks
	SetPokeService(service *bridge.PokeServiceClient) error
}

// Config holds the configuration for the async job system
type Config struct {
	MongoDB        *mongo.Database
	DBContext      *db.AppContext
	WorkerCount    int
	BatchSize      int
	PollInterval   int // seconds
	JobTTL         int // seconds
	Logger         *logger.Logger
}

// asyncJobSystem is the concrete implementation
type asyncJobSystem struct {
	config      *Config
	jobManager  JobManagerInterface
	workerPool  WorkerPool
	analyzer    DependencyAnalyzer
	logger      *logger.Logger
	pokeService *bridge.PokeServiceClient
}

// NewAsyncJobSystem creates a new async job system instance
func NewAsyncJobSystem(config *Config) (AsyncJobSystem, error) {
	if config.MongoDB == nil {
		return nil, fmt.Errorf("MongoDB connection is required")
	}
	
	if config.Logger == nil {
		config.Logger = logger.NewLogger("async-job-system")
	}
	
	// Set defaults
	if config.WorkerCount == 0 {
		config.WorkerCount = 5
	}
	if config.BatchSize == 0 {
		config.BatchSize = 10
	}
	if config.PollInterval == 0 {
		config.PollInterval = 2
	}
	if config.JobTTL == 0 {
		config.JobTTL = 300 // 5 minutes
	}
	
	system := &asyncJobSystem{
		config: config,
		logger: config.Logger,
	}
	
	// Initialize components
	system.jobManager = job.NewManager(config.MongoDB, config.Logger)
	system.workerPool = worker.NewPool(&worker.PoolConfig{
		MongoDB:            config.MongoDB,
		DBContext:          config.DBContext,
		WorkerCount:        config.WorkerCount,
		BatchSize:          config.BatchSize,
		PollInterval:       time.Duration(config.PollInterval) * time.Second,
		Logger:             config.Logger,
		MaxConcurrentPokes: 5,
	})
	system.analyzer = analysis.NewAnalyzer(config.DBContext, config.Logger)
	
	return system, nil
}

// CreateJob creates a new background job
func (s *asyncJobSystem) CreateJob(ctx context.Context, req *CreateJobRequest) (*Job, error) {
	return s.jobManager.CreateJob(ctx, req)
}

// GetJob retrieves a job by ID
func (s *asyncJobSystem) GetJob(ctx context.Context, jobID string) (*Job, error) {
	return s.jobManager.GetJob(ctx, jobID)
}

// GetJobByHumanID retrieves a job by human-friendly ID
func (s *asyncJobSystem) GetJobByHumanID(ctx context.Context, humanID string) (*Job, error) {
	return s.jobManager.GetJobByHumanID(ctx, humanID)
}

// CancelJob cancels a pending or running job
func (s *asyncJobSystem) CancelJob(ctx context.Context, jobID string) error {
	return s.jobManager.CancelJob(ctx, jobID)
}

// ListJobs lists jobs with optional filtering
func (s *asyncJobSystem) ListJobs(ctx context.Context, filter *JobFilter) ([]*Job, error) {
	return s.jobManager.ListJobs(ctx, filter)
}

// CountJobs counts total jobs matching the filter
func (s *asyncJobSystem) CountJobs(ctx context.Context, filter *JobFilter) (int64, error) {
	return s.jobManager.CountJobs(ctx, filter)
}

// AnalyzeDependencies analyzes resource dependencies
func (s *asyncJobSystem) AnalyzeDependencies(ctx context.Context, req *AnalysisRequest) (*DependencyAnalysis, error) {
	// Convert to analysis package types
	analysisReq := &analysis.AnalysisRequest{
		ResourceType: req.ResourceType,
		ResourceName: req.ResourceName,
		Project:      req.Project,
		Version:      req.Version,
		Action:       req.Action,
	}
	
	result, err := s.analyzer.Analyze(ctx, analysisReq)
	if err != nil {
		return nil, err
	}
	
	// Convert back to main package types
	return &DependencyAnalysis{
		SourceResource:    result.SourceResource,
		AffectedListeners: result.AffectedListeners,
		DependencyChain:   result.DependencyChain,
		DurationMS:        result.DurationMS,
	}, nil
}

// StartWorkers starts the worker pool
func (s *asyncJobSystem) StartWorkers(ctx context.Context, config *WorkerConfig) error {
	// Convert to worker package config
	workerConfig := &worker.WorkerConfig{
		PokeService:        config.PokeService.(*bridge.PokeServiceClient),
		JobManager:         s.jobManager.(*job.Manager),
		DBContext:          config.DBContext.(*db.AppContext),
		MaxConcurrentPokes: config.MaxConcurrentPokes,
	}
	return s.workerPool.Start(ctx, workerConfig)
}

// StopWorkers stops the worker pool
func (s *asyncJobSystem) StopWorkers(ctx context.Context) error {
	return s.workerPool.Stop(ctx)
}

// GetWorkerStatus gets the current worker pool status
func (s *asyncJobSystem) GetWorkerStatus(ctx context.Context) (*WorkerStatus, error) {
	status, err := s.workerPool.GetStatus(ctx)
	if err != nil {
		return nil, err
	}
	
	// Convert to main package types
	return &WorkerStatus{
		ActiveWorkers:  status.ActiveWorkers,
		ProcessingJobs: status.ProcessingJobs,
		QueueSize:      status.QueueSize,
		LastActivity:   status.LastActivity,
	}, nil
}

// RetryJob retries a failed or cancelled job
func (s *asyncJobSystem) RetryJob(ctx context.Context, jobID string, reason string) (*Job, error) {
	return s.jobManager.RetryJob(ctx, jobID, reason)
}

// RetryFailedSnapshots retries only failed snapshots in a job
func (s *asyncJobSystem) RetryFailedSnapshots(ctx context.Context, jobID string) (*Job, error) {
	return s.jobManager.RetryFailedSnapshots(ctx, jobID)
}

// GetStuckJobs finds jobs that are stuck (no heartbeat)
func (s *asyncJobSystem) GetStuckJobs(ctx context.Context) ([]*Job, error) {
	return s.jobManager.GetStuckJobs(ctx)
}

// GetJobStats returns basic job statistics
func (s *asyncJobSystem) GetJobStats(ctx context.Context) (*JobStats, error) {
	// Get recent jobs (last 10)
	recentFilter := &JobFilter{
		Limit: 10,
	}
	recentJobs, err := s.jobManager.ListJobs(ctx, recentFilter)
	if err != nil {
		return nil, fmt.Errorf("failed to get recent jobs: %w", err)
	}
	
	// Count jobs by status, resource, and user
	statusCounts := make(map[JobStatus]int64)
	resourceCounts := make(map[string]int64)
	userCounts := make(map[string]int64)
	
	// Get all jobs to count (we could optimize this with aggregation later)
	allFilter := &JobFilter{
		Limit: 1000, // Reasonable limit for stats
	}
	allJobs, err := s.jobManager.ListJobs(ctx, allFilter)
	if err != nil {
		return nil, fmt.Errorf("failed to get jobs for stats: %w", err)
	}
	
	// Count by status, resource, and user
	for _, job := range allJobs {
		statusCounts[job.Status]++
		if job.Metadata != nil && job.Metadata.SourceResource != nil {
			resourceCounts[job.Metadata.SourceResource.Name]++
		}
		if job.Metadata != nil && job.Metadata.TriggerUser != nil {
			userCounts[job.Metadata.TriggerUser.Username]++
		}
	}
	
	// Get worker status
	workerStatus, err := s.GetWorkerStatus(ctx)
	if err != nil {
		// Don't fail completely, just set defaults
		workerStatus = &WorkerStatus{
			ActiveWorkers:  0,
			ProcessingJobs: 0,
			QueueSize:      0,
			LastActivity:   time.Now(),
		}
	}
	
	return &JobStats{
		TotalJobs:      int64(len(allJobs)),
		JobsByStatus:   statusCounts,
		JobsByResource: resourceCounts,
		JobsByUser:     userCounts,
		RecentJobs:     recentJobs,
		QueueSize:      workerStatus.QueueSize,
		ActiveWorkers:  workerStatus.ActiveWorkers,
		ProcessingJobs: workerStatus.ProcessingJobs,
		LastUpdated:    time.Now(),
	}, nil
}

// GetJobStatsFiltered returns job statistics with applied filters
func (s *asyncJobSystem) GetJobStatsFiltered(ctx context.Context, filter *JobFilter) (*JobStats, error) {
	// Get all matching jobs for counting (with reasonable limit)
	countFilter := &JobFilter{
		Status:           filter.Status,
		Type:             filter.Type,
		Project:          filter.Project,
		CreatedBy:        filter.CreatedBy,
		ResourceName:     filter.ResourceName,
		AffectedListener: filter.AffectedListener,
		Username:         filter.Username,
		StartDate:        filter.StartDate,
		EndDate:          filter.EndDate,
		Limit:            5000, // Reasonable limit for stats counting
	}
	
	filteredJobs, err := s.jobManager.ListJobs(ctx, countFilter)
	if err != nil {
		return nil, fmt.Errorf("failed to get jobs for filtered stats: %w", err)
	}
	
	// Count jobs by status, resource, and user from filtered results
	statusCounts := make(map[JobStatus]int64)
	resourceCounts := make(map[string]int64)
	userCounts := make(map[string]int64)
	
	for _, job := range filteredJobs {
		statusCounts[job.Status]++
		if job.Metadata != nil && job.Metadata.SourceResource != nil {
			resourceCounts[job.Metadata.SourceResource.Name]++
		}
		if job.Metadata != nil && job.Metadata.TriggerUser != nil {
			userCounts[job.Metadata.TriggerUser.Username]++
		}
	}
	
	// Get worker status (not affected by filters)
	workerStatus, err := s.GetWorkerStatus(ctx)
	if err != nil {
		// Don't fail completely, just set defaults
		workerStatus = &WorkerStatus{
			ActiveWorkers:  0,
			ProcessingJobs: 0,
			QueueSize:      0,
			LastActivity:   time.Now(),
		}
	}
	
	return &JobStats{
		TotalJobs:      int64(len(filteredJobs)),
		JobsByStatus:   statusCounts,
		JobsByResource: resourceCounts,
		JobsByUser:     userCounts,
		RecentJobs:     nil, // Removed recent jobs
		QueueSize:      workerStatus.QueueSize,
		ActiveWorkers:  workerStatus.ActiveWorkers,
		ProcessingJobs: workerStatus.ProcessingJobs,
		LastUpdated:    time.Now(),
	}, nil
}

// CreatePreliminaryJob creates a job without dependency analysis for fast response
func (s *asyncJobSystem) CreatePreliminaryJob(ctx context.Context, req *CreateJobRequest) (*Job, error) {
	return s.jobManager.(*job.Manager).CreatePreliminaryJob(ctx, req)
}

// PerformBackgroundAnalysis performs dependency analysis in background and updates job
func (s *asyncJobSystem) PerformBackgroundAnalysis(jobID string, resource interface{}, requestDetails interface{}, project string) {
	go func() {
		ctx := context.Background()
		startTime := time.Now()
		
		// Convert jobID to ObjectID
		objID, err := primitive.ObjectIDFromHex(jobID)
		if err != nil {
			s.logger.Errorf("Invalid job ID for background analysis: %s", jobID)
			return
		}
		
		// Cast interfaces to proper types based on expected structure
		var gtype models.GType
		var resourceName, version string
		
		// Extract resource information from interface{}
		if resourceClass, ok := resource.(models.ResourceClass); ok {
			gtype = resourceClass.GetGeneral().GType
			resourceName = resourceClass.GetGeneral().Name
			version = resourceClass.GetGeneral().Version
		} else {
			s.logger.Errorf("Failed to cast resource interface for job %s", jobID)
			return
		}
		
		// Create analysis request
		analysisReq := &AnalysisRequest{
			ResourceType: gtype,
			ResourceName: resourceName,
			Project:      project,
			Version:      version,
			Action:       "UPDATE",
		}
		
		// Perform actual dependency analysis
		analysisResult := &job.DependencyAnalysisResult{
			AffectedListeners: []string{},
			DependencyChain:   []string{},
			DurationMS:        time.Since(startTime).Milliseconds(),
		}
		
		// Run the analysis
		if analysis, err := s.AnalyzeDependencies(ctx, analysisReq); err != nil {
			s.logger.Errorf("Failed to analyze dependencies for job %s: %v", jobID, err)
		} else {
			analysisResult.AffectedListeners = analysis.AffectedListeners
			analysisResult.DependencyChain = analysis.DependencyChain
			analysisResult.DurationMS = time.Since(startTime).Milliseconds()
		}
		
		// Update job with analysis results
		if err := s.jobManager.(*job.Manager).CompleteAnalysis(ctx, objID, analysisResult); err != nil {
			s.logger.Errorf("Failed to complete background analysis for job %s: %v", jobID, err)
		} else {
			s.logger.Infof("Completed background analysis for job %s: %d affected listeners", jobID, len(analysisResult.AffectedListeners))
		}
	}()
}

// GetJobManager returns the internal job manager
func (s *asyncJobSystem) GetJobManager() JobManagerInterface {
	return s.jobManager
}

// SetPokeService sets the poke service for snapshot updates
func (s *asyncJobSystem) SetPokeService(service *bridge.PokeServiceClient) error {
	if service == nil {
		return fmt.Errorf("poke service cannot be nil")
	}
	s.pokeService = service
	return nil
}