package job

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// JobType represents the type of background job
type JobType string

const (
	JobTypeSnapshotUpdate JobType = "SNAPSHOT_UPDATE"
)

// JobStatus represents the current status of a job
type JobStatus string

const (
	JobStatusAnalyzing    JobStatus = "ANALYZING"
	JobStatusPending      JobStatus = "PENDING"
	JobStatusClaimed      JobStatus = "CLAIMED"
	JobStatusRunning      JobStatus = "RUNNING"
	JobStatusCompleted    JobStatus = "COMPLETED"
	JobStatusFailed       JobStatus = "FAILED"
	JobStatusCancelled    JobStatus = "CANCELLED"
	JobStatusNoWorkNeeded JobStatus = "NO_WORK_NEEDED"
)

// ActionType represents the type of action that triggered the job
type ActionType string

const (
	ActionTypeCreate          ActionType = "CREATE"
	ActionTypeUpdate          ActionType = "UPDATE"
	ActionTypeDelete          ActionType = "DELETE"
	ActionTypeDiscoveryUpdate ActionType = "DISCOVERY_UPDATE"
)

// PokeStatus represents the status of a snapshot poke operation
type PokeStatus string

const (
	PokeStatusPending PokeStatus = "PENDING"
	PokeStatusSuccess PokeStatus = "SUCCESS"
	PokeStatusFailed  PokeStatus = "FAILED"
)

// Job represents a background job in the system
type Job struct {
	ID               primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	JobID            string             `bson:"job_id" json:"job_id"` // Human-friendly ID (EC-1, EC-2, etc.)
	Type             JobType            `bson:"type" json:"type"`
	Status           JobStatus          `bson:"status" json:"status"`
	Progress         *JobProgress       `bson:"progress" json:"progress"`
	Metadata         *JobMetadata       `bson:"metadata" json:"metadata"`
	ExecutionDetails *ExecutionDetails  `bson:"execution_details,omitempty" json:"execution_details,omitempty"`
	WorkerInfo       *WorkerInfo        `bson:"worker_info,omitempty" json:"worker_info,omitempty"`
	RetryInfo        *RetryInfo         `bson:"retry_info,omitempty" json:"retry_info,omitempty"`
	CreatedBy        string             `bson:"created_by" json:"created_by"` // User ObjectID as string
	CreatedAt        time.Time          `bson:"created_at" json:"created_at"`
	StartedAt        *time.Time         `bson:"started_at,omitempty" json:"started_at,omitempty"`
	CompletedAt      *time.Time         `bson:"completed_at,omitempty" json:"completed_at,omitempty"`
	Error            string             `bson:"error,omitempty" json:"error,omitempty"`
	Project          string             `bson:"project" json:"project"` // Project ObjectID as string
	Version          string             `bson:"version" json:"version"`
}

// JobProgress tracks the progress of a job
type JobProgress struct {
	Total      int     `bson:"total" json:"total"`
	Completed  int     `bson:"completed" json:"completed"`
	Failed     int     `bson:"failed" json:"failed"`
	Percentage float64 `bson:"percentage" json:"percentage"`
}

// JobMetadata contains metadata about the job
type JobMetadata struct {
	SourceResource    *SourceResource `bson:"source_resource" json:"source_resource"`
	TriggerUser       *TriggerUser    `bson:"trigger_user" json:"trigger_user"`
	AffectedListeners []string        `bson:"affected_listeners" json:"affected_listeners"`
	TotalAffected     int             `bson:"total_affected" json:"total_affected"`
	AnalysisDuration  int64           `bson:"analysis_duration_ms" json:"analysis_duration_ms"`
}

// SourceResource represents the resource that triggered the job
type SourceResource struct {
	Type       string     `bson:"type" json:"type"` // GType string
	Name       string     `bson:"name" json:"name"`
	Collection string     `bson:"collection" json:"collection"`
	Action     ActionType `bson:"action" json:"action"`
	ProjectID  string     `bson:"project_id" json:"project_id"` // Project ObjectID as string
	Version    string     `bson:"version" json:"version"`
}

// TriggerUser represents the user who triggered the job
type TriggerUser struct {
	ID          string `bson:"id" json:"id"` // User ObjectID as string or "system"
	Username    string `bson:"username" json:"username"`
	DisplayName string `bson:"display_name" json:"display_name"`
	Role        string `bson:"role" json:"role"`
}

// ExecutionDetails contains details about job execution
type ExecutionDetails struct {
	ProcessedSnapshots []SnapshotExecution `bson:"processed_snapshots" json:"processed_snapshots"`
}

// SnapshotExecution represents a single snapshot update execution
type SnapshotExecution struct {
	NodeID       string     `bson:"node_id" json:"node_id"`
	ListenerName string     `bson:"listener_name" json:"listener_name"`
	PokeStatus   PokeStatus `bson:"poke_status" json:"poke_status"`
	PokeSentAt   time.Time  `bson:"poke_sent_at" json:"poke_sent_at"`
	Error        *string    `bson:"error,omitempty" json:"error,omitempty"`
}

// WorkerInfo contains information about the worker processing the job
type WorkerInfo struct {
	WorkerID  string    `bson:"worker_id" json:"worker_id"`
	ClaimedAt time.Time `bson:"claimed_at" json:"claimed_at"`
	Heartbeat time.Time `bson:"heartbeat" json:"heartbeat"`
	TTL       int       `bson:"ttl" json:"ttl"` // seconds
}

// RetryInfo contains information about job retries
type RetryInfo struct {
	OriginalJobID primitive.ObjectID `bson:"original_job_id" json:"original_job_id"`
	RetryCount    int                `bson:"retry_count" json:"retry_count"`
	RetryReason   string             `bson:"retry_reason" json:"retry_reason"`
	RetryType     string             `bson:"retry_type" json:"retry_type"` // full, failed_only, pending_only
}

// CreateJobRequest represents a request to create a new job
type CreateJobRequest struct {
	Type     JobType      `json:"type"`
	Status   JobStatus    `json:"status"`
	Metadata *JobMetadata `json:"metadata"`
}

// DependencyAnalysisResult holds the result of dependency analysis
type DependencyAnalysisResult struct {
	AffectedListeners []string `json:"affected_listeners"`
	DependencyChain   []string `json:"dependency_chain"`
	DurationMS        int64    `json:"duration_ms"`
}

// JobFilter represents filters for listing jobs
type JobFilter struct {
	Status           []JobStatus `json:"status,omitempty"`
	Type             []JobType   `json:"type,omitempty"`
	Project          string      `json:"project,omitempty"`
	CreatedBy        string      `json:"created_by,omitempty"`
	ResourceName     string      `json:"resource_name,omitempty"`     // Filter by source resource name
	AffectedListener string      `json:"affected_listener,omitempty"` // Filter by affected listener name
	Username         string      `json:"username,omitempty"`          // Filter by trigger username
	StartDate        string      `json:"start_date,omitempty"`        // Filter by date range start (YYYY-MM-DD)
	EndDate          string      `json:"end_date,omitempty"`          // Filter by date range end (YYYY-MM-DD)
	Limit            int         `json:"limit,omitempty"`
	Offset           int         `json:"offset,omitempty"`
}
