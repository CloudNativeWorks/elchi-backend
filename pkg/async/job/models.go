package job

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// JobType represents the type of background job
type JobType string

const (
	JobTypeSnapshotUpdate   JobType = "SNAPSHOT_UPDATE"
	JobTypeWAFPropagation   JobType = "WAF_PROPAGATION"
	JobTypeResourceUpgrade  JobType = "RESOURCE_UPGRADE"
	JobTypeACMEVerification JobType = "ACME_VERIFICATION"
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
	ID               primitive.ObjectID  `bson:"_id,omitempty" json:"id"`
	JobID            string              `bson:"job_id" json:"job_id"` // Human-friendly ID (EC-1, EC-2, etc.)
	Type             JobType             `bson:"type" json:"type"`
	Status           JobStatus           `bson:"status" json:"status"`
	Progress         *JobProgress        `bson:"progress" json:"progress"`
	Metadata         *JobMetadata        `bson:"metadata" json:"metadata"`
	ExecutionDetails *ExecutionDetails   `bson:"execution_details,omitempty" json:"execution_details,omitempty"`
	WorkerInfo       *WorkerInfo         `bson:"worker_info,omitempty" json:"worker_info,omitempty"`
	RetryInfo        *RetryInfo          `bson:"retry_info,omitempty" json:"retry_info,omitempty"`
	ParentJobID      *primitive.ObjectID `bson:"parent_job_id,omitempty" json:"parent_job_id,omitempty"` // Parent job that must complete before this job can process
	CreatedBy        string              `bson:"created_by" json:"created_by"`                           // User ObjectID as string
	CreatedAt        time.Time           `bson:"created_at" json:"created_at"`
	StartedAt        *time.Time          `bson:"started_at,omitempty" json:"started_at,omitempty"`
	CompletedAt      *time.Time          `bson:"completed_at,omitempty" json:"completed_at,omitempty"`
	Error            string              `bson:"error,omitempty" json:"error,omitempty"`
	Project          string              `bson:"project" json:"project"` // Project ObjectID as string
	Version          string              `bson:"version" json:"version"`
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
	SourceResource    *SourceResource  `bson:"source_resource,omitempty" json:"source_resource,omitempty"`
	TriggerUser       *TriggerUser     `bson:"trigger_user" json:"trigger_user"`
	AffectedListeners []string         `bson:"affected_listeners" json:"affected_listeners"`
	TotalAffected     int              `bson:"total_affected" json:"total_affected"`
	AnalysisDuration  int64            `bson:"analysis_duration_ms" json:"analysis_duration_ms"`
	WAFConfig         *WAFConfigMeta   `bson:"waf_config,omitempty" json:"waf_config,omitempty"`         // For WAF_PROPAGATION jobs
	AffectedWASM      []string         `bson:"affected_wasm,omitempty" json:"affected_wasm,omitempty"`   // For WAF_PROPAGATION jobs
	UpgradeConfig     *UpgradeMetadata `bson:"upgrade_config,omitempty" json:"upgrade_config,omitempty"` // For RESOURCE_UPGRADE jobs
	ACMEMetadata      *ACMEJobMetadata `bson:"acme,omitempty" json:"acme,omitempty"`                     // For ACME_VERIFICATION jobs
}

// WAFConfigMeta contains metadata about the WAF config that triggered propagation
type WAFConfigMeta struct {
	Name    string `bson:"name" json:"name"`
	Project string `bson:"project" json:"project"`
}

// ACMEJobMetadata contains metadata for ACME DNS verification jobs
type ACMEJobMetadata struct {
	CertificateID   string   `bson:"certificate_id" json:"certificate_id"`       // ObjectID of acme_certificates document
	CertificateName string   `bson:"certificate_name" json:"certificate_name"`   // Secret name
	Domains         []string `bson:"domains" json:"domains"`                     // Domains being verified
	Environment     string   `bson:"environment" json:"environment"`             // "staging" or "production"
	DNSProvider     string   `bson:"dns_provider" json:"dns_provider"`           // "godaddy" or "google"
	ACMEAccountID   string   `bson:"acme_account_id" json:"acme_account_id"`     // ObjectID reference
	DNSCredentialID string   `bson:"dns_credential_id" json:"dns_credential_id"` // ObjectID reference
	Project         string   `bson:"project" json:"project"`                     // Project ID
	Versions        []string `bson:"versions" json:"versions"`                   // Secret versions to create
	IsRenewal       bool     `bson:"is_renewal" json:"is_renewal"`               // true if this is a renewal operation
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
	// Projects is a snapshot of the user's project IDs at the moment the
	// job was triggered. The async worker uses it to mint internal forward
	// tokens with the same scope so Editor/Viewer authorization (which is
	// project-scoped) still passes when the worker forwards to a different
	// controller pod. Captured at trigger time so later membership changes
	// do not change the running job's effective scope.
	Projects []string `bson:"projects,omitempty" json:"projects,omitempty"`
}

// ExecutionDetails contains details about job execution
type ExecutionDetails struct {
	ProcessedSnapshots []SnapshotExecution `bson:"processed_snapshots" json:"processed_snapshots"`
	ACMEResult         *ACMEExecution      `bson:"acme,omitempty" json:"acme,omitempty"` // For ACME_VERIFICATION jobs
}

// SnapshotExecution represents a single snapshot update execution
type SnapshotExecution struct {
	NodeID       string     `bson:"node_id" json:"node_id"`
	ListenerName string     `bson:"listener_name" json:"listener_name"`
	PokeStatus   PokeStatus `bson:"poke_status" json:"poke_status"`
	PokeSentAt   time.Time  `bson:"poke_sent_at" json:"poke_sent_at"`
	Error        *string    `bson:"error,omitempty" json:"error,omitempty"`
}

// ACMEExecution represents the result of an ACME verification job
type ACMEExecution struct {
	CertificateID string     `bson:"certificate_id" json:"certificate_id"` // ObjectID of certificate
	SecretName    string     `bson:"secret_name" json:"secret_name"`       // Name of created secret
	Domains       []string   `bson:"domains" json:"domains"`               // Verified domains
	Status        string     `bson:"status" json:"status"`                 // "active" or "verification_failed"
	IssuedAt      *time.Time `bson:"issued_at,omitempty" json:"issued_at,omitempty"`
	ExpiresAt     *time.Time `bson:"expires_at,omitempty" json:"expires_at,omitempty"`
	ErrorMessage  *string    `bson:"error_message,omitempty" json:"error_message,omitempty"`
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
	// RetryInfo is set by RetryJob / RetryFailedSnapshots so the manager
	// can persist retry context atomically in the same InsertOne as the
	// new job document. Writing it via a follow-up UpdateOne opens a
	// race window where a worker may claim the job before retry_info is
	// visible, causing the upgrade worker's retry-skip filter to see a
	// nil RetryInfo and re-notify every client (gratuitous restart).
	RetryInfo *RetryInfo `json:"retry_info,omitempty"`
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

// UpgradeMetadata contains metadata for resource upgrade jobs
type UpgradeMetadata struct {
	TargetVersion   string `bson:"target_version" json:"target_version"`
	ValidateClients bool   `bson:"validate_clients" json:"validate_clients"`
	UpdateBootstrap bool   `bson:"update_bootstrap" json:"update_bootstrap"`
	DryRun          bool   `bson:"dry_run" json:"dry_run"`
	Analysis          *UpgradeAnalysisResult `bson:"analysis,omitempty" json:"analysis,omitempty"`
	ClientResponses   []any                  `bson:"client_responses,omitempty" json:"client_responses,omitempty"`   // Raw responses from each client
	CreatedResources  []ResourceRef          `bson:"created_resources,omitempty" json:"created_resources,omitempty"` // Resources created during upgrade
	BootstrapUpdates  []BootstrapUpdate      `bson:"bootstrap_updates,omitempty" json:"bootstrap_updates,omitempty"` // Bootstrap updates performed

	// LockKey is a deterministic string ("upgrade::{project}::{sorted listener names}")
	// used by a partial unique index on background_jobs to reject a second
	// concurrent upgrade against the same listener set. Index filter limits
	// uniqueness to active statuses (ANALYZING/PENDING/CLAIMED/RUNNING) so
	// terminal jobs no longer block fresh upgrades on the same target.
	LockKey string `bson:"lock_key,omitempty" json:"lock_key,omitempty"`
}

// UpgradeAnalysisResult contains the result of upgrade dependency analysis
type UpgradeAnalysisResult struct {
	// Listener tracking
	ListenersToUpgrade    []string `bson:"listeners_to_upgrade" json:"listeners_to_upgrade"`                           // All listeners being upgraded
	ListenersAlreadyExist []string `bson:"listeners_already_exist,omitempty" json:"listeners_already_exist,omitempty"` // Listeners that exist in target version

	// Per-listener analysis details
	ListenerDetails []ListenerAnalysis `bson:"listener_details" json:"listener_details"` // Detailed analysis per listener

	// Aggregate dependency stats (across all listeners)
	UpstreamDependencies int                `bson:"upstream_dependencies" json:"upstream_dependencies"`
	MissingInTarget      int                `bson:"missing_in_target" json:"missing_in_target"`
	ExistingInTarget     int                `bson:"existing_in_target" json:"existing_in_target"`
	MissingResources     []MissingResource  `bson:"missing_resources" json:"missing_resources"`
	SkippedResources     []ExistingResource `bson:"skipped_resources,omitempty" json:"skipped_resources,omitempty"`

	// Bootstrap tracking
	BootstrapRequired bool     `bson:"bootstrap_required" json:"bootstrap_required"`
	BootstrapNames    []string `bson:"bootstrap_names,omitempty" json:"bootstrap_names,omitempty"`

	// Client validation
	ClientsValidated    int      `bson:"clients_validated" json:"clients_validated"`
	IncompatibleClients []string `bson:"incompatible_clients,omitempty" json:"incompatible_clients,omitempty"`

	// Summary
	Summary string `bson:"summary" json:"summary"` // Human-readable summary
}

// ListenerAnalysis contains analysis details for a single listener
type ListenerAnalysis struct {
	ListenerName          string             `bson:"listener_name" json:"listener_name"`
	ExistsInTarget        bool               `bson:"exists_in_target" json:"exists_in_target"`
	UpstreamDependencies  int                `bson:"upstream_dependencies" json:"upstream_dependencies"`
	MissingResources      []MissingResource  `bson:"missing_resources" json:"missing_resources"`
	ExistingResources     []ExistingResource `bson:"existing_resources" json:"existing_resources"`
	BootstrapRequired     bool               `bson:"bootstrap_required" json:"bootstrap_required"`
	BootstrapNames        []string           `bson:"bootstrap_names,omitempty" json:"bootstrap_names,omitempty"`
	RequiresClientUpgrade bool               `bson:"requires_client_upgrade" json:"requires_client_upgrade"` // True if listener has connected clients
	ConnectedClients      int                `bson:"connected_clients" json:"connected_clients"`             // Number of connected clients
	TotalClients          int                `bson:"total_clients" json:"total_clients"`                     // Total deployed clients
}

// ExistingResource represents a resource that already exists in target version
type ExistingResource struct {
	GType      string `bson:"gtype" json:"gtype"`
	Name       string `bson:"name" json:"name"`
	Collection string `bson:"collection" json:"collection"`
}

// MissingResource represents a resource that needs to be created during upgrade
type MissingResource struct {
	GType      string   `bson:"gtype" json:"gtype"`
	Name       string   `bson:"name" json:"name"`
	Collection string   `bson:"collection" json:"collection"`
	DependsOn  []string `bson:"depends_on" json:"depends_on"`
}

// ResourceRef represents a reference to a created/existing resource
type ResourceRef struct {
	Collection string `bson:"collection" json:"collection"`
	ID         string `bson:"id" json:"id"`
	Name       string `bson:"name" json:"name"`
	GType      string `bson:"gtype" json:"gtype"`
	Skipped    bool   `bson:"skipped" json:"skipped"` // Marks if resource was skipped (already existed)
}

// ResourceInfo represents information about a resource
type ResourceInfo struct {
	Name      string `bson:"name" json:"name"`
	GType     string `bson:"gtype" json:"gtype"`
	Version   string `bson:"version" json:"version"`
	ProjectID string `bson:"project_id" json:"project_id"`
}

// BootstrapUpdate represents a bootstrap update operation
type BootstrapUpdate struct {
	BootstrapName string `bson:"bootstrap_name" json:"bootstrap_name"`
	ListenerName  string `bson:"listener_name" json:"listener_name"`
	FromVersion   string `bson:"from_version" json:"from_version"`
	ToVersion     string `bson:"to_version" json:"to_version"`
	Success       bool   `bson:"success" json:"success"`
	Error         string `bson:"error,omitempty" json:"error,omitempty"`
}
