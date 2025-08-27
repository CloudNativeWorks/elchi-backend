package async

import (
	"context"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/async/analysis"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async/job"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async/worker"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Re-export job types for convenience
type (
	Job                = job.Job
	JobType            = job.JobType
	JobStatus          = job.JobStatus
	JobProgress        = job.JobProgress
	JobMetadata        = job.JobMetadata
	SourceResource     = job.SourceResource
	TriggerUser        = job.TriggerUser
	ExecutionDetails   = job.ExecutionDetails
	SnapshotExecution  = job.SnapshotExecution
	WorkerInfo         = job.WorkerInfo
	RetryInfo          = job.RetryInfo
	CreateJobRequest   = job.CreateJobRequest
	JobFilter          = job.JobFilter
	ActionType         = job.ActionType
	PokeStatus         = job.PokeStatus
)

// JobManagerInterface defines the interface for job management
type JobManagerInterface interface {
	CreateJob(ctx context.Context, req *CreateJobRequest) (*Job, error)
	GetJob(ctx context.Context, jobID string) (*Job, error)
	GetJobByHumanID(ctx context.Context, humanID string) (*Job, error)
	UpdateJob(ctx context.Context, jobID string, update interface{}) error
	CancelJob(ctx context.Context, jobID string) error
	ListJobs(ctx context.Context, filter *JobFilter) ([]*Job, error)
	CountJobs(ctx context.Context, filter *JobFilter) (int64, error)
	ClaimJob(ctx context.Context, workerID string) (*Job, error)
	UpdateJobProgress(ctx context.Context, jobID primitive.ObjectID, progress *JobProgress) error
	UpdateJobHeartbeat(ctx context.Context, jobID primitive.ObjectID) error
	CompleteJob(ctx context.Context, jobID primitive.ObjectID, executionDetails *ExecutionDetails) error
	FailJob(ctx context.Context, jobID primitive.ObjectID, err error) error
	RetryJob(ctx context.Context, jobID string, reason string) (*Job, error)
	RetryFailedSnapshots(ctx context.Context, jobID string) (*Job, error)
	GetStuckJobs(ctx context.Context) ([]*Job, error)
}

// WorkerPool interface for worker management
type WorkerPool interface {
	Start(ctx context.Context, config *worker.WorkerConfig) error
	Stop(ctx context.Context) error
	GetStatus(ctx context.Context) (*worker.WorkerStatus, error)
}

// DependencyAnalyzer interface for dependency analysis
type DependencyAnalyzer interface {
	Analyze(ctx context.Context, req *analysis.AnalysisRequest) (*analysis.DependencyAnalysis, error)
}

// WorkerConfig holds configuration for workers
type WorkerConfig struct {
	PokeService        interface{} // Will be *bridge.PokeServiceClient
	JobManager         JobManagerInterface
	DBContext          interface{} // Will be *db.AppContext
	MaxConcurrentPokes int
}

// WorkerStatus represents the status of the worker pool
type WorkerStatus struct {
	ActiveWorkers   int       `json:"active_workers"`
	ProcessingJobs  int       `json:"processing_jobs"`
	QueueSize       int       `json:"queue_size"`
	LastActivity    time.Time `json:"last_activity"`
}

// JobStats represents basic job statistics
type JobStats struct {
	TotalJobs       int64                    `json:"total_jobs"`
	JobsByStatus    map[JobStatus]int64      `json:"jobs_by_status"`
	JobsByType      map[JobType]int64        `json:"jobs_by_type"`
	RecentJobs      []*Job                   `json:"recent_jobs"`
	QueueSize       int                      `json:"queue_size"`
	ActiveWorkers   int                      `json:"active_workers"`
	ProcessingJobs  int                      `json:"processing_jobs"`
	LastUpdated     time.Time                `json:"last_updated"`
}

// AnalysisRequest represents a request to analyze dependencies
type AnalysisRequest struct {
	ResourceType models.GType `json:"resource_type"`
	ResourceName string       `json:"resource_name"`
	Project      string       `json:"project"`
	Version      string       `json:"version"`
	Action       string       `json:"action"`
}

// DependencyAnalysis represents the result of dependency analysis
type DependencyAnalysis struct {
	SourceResource    *SourceResource `json:"source_resource"`
	AffectedListeners []string        `json:"affected_listeners"`
	DependencyChain   []string        `json:"dependency_chain"`
	DurationMS        int64           `json:"duration_ms"`
}