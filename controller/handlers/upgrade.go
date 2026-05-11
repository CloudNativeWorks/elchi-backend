package handlers

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/controller/upgrade"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async/job"
	"github.com/CloudNativeWorks/elchi-backend/pkg/audit"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

const analysisHeartbeatInterval = 30 * time.Second

// UpgradeHandler handles resource upgrade operations
type UpgradeHandler struct {
	dbContext      *db.AppContext
	asyncSystem    async.AsyncJobSystem
	AuditService   *audit.Service
	commandHandler upgrade.CommandHandler
}

// NewUpgradeHandler creates a new upgrade handler
func NewUpgradeHandler(dbContext *db.AppContext, asyncSystem async.AsyncJobSystem, auditService *audit.Service, commandHandler upgrade.CommandHandler) *UpgradeHandler {
	return &UpgradeHandler{
		dbContext:      dbContext,
		asyncSystem:    asyncSystem,
		AuditService:   auditService,
		commandHandler: commandHandler,
	}
}

// UpgradeRequest represents the upgrade request body
type UpgradeRequest struct {
	ResourceType  string   `json:"resource_type" binding:"required"`
	ResourceNames []string `json:"resource_names" binding:"required,min=1"` // Array of listener names
	Project       string   `json:"project" binding:"required"`
	FromVersion   string   `json:"from_version" binding:"required"`
	ToVersion     string   `json:"to_version" binding:"required"`
	Options       struct {
		AutoCreateMissing bool `json:"auto_create_missing"`
		ValidateClients   bool `json:"validate_clients"`
		UpdateBootstrap   bool `json:"update_bootstrap"`
		DryRun            bool `json:"dry_run"`
	} `json:"options"`
}

// UpgradeResource handles POST /api/v3/resource/upgrade
func (h *UpgradeHandler) UpgradeResource(c *gin.Context) {
	var req UpgradeRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// Validate resource type - only listener upgrades are supported
	if req.ResourceType != "listener" {
		c.JSON(400, gin.H{"error": "Only listener upgrades are supported. resource_type must be 'listener'"})
		return
	}

	// Reject no-op upgrades: same version produces nothing but needless
	// systemd restarts on every connected client (binary path doesn't change
	// but elchi-client still calls RestartService).
	if req.FromVersion == req.ToVersion {
		c.JSON(400, gin.H{"error": "from_version and to_version must differ"})
		return
	}

	// Set audit context (join all listener names for audit)
	listenerNamesStr := fmt.Sprintf("%v", req.ResourceNames) // Convert array to string representation
	h.setUpgradeAuditContext(c, req.ResourceType, listenerNamesStr, req.Project, req.FromVersion, req.ToVersion)

	// Get user details for job trigger
	userDetails, err := GetUserDetails(c)
	if err != nil {
		h.setAuditResult(c, err)
		c.JSON(401, gin.H{"error": "User not authenticated"})
		return
	}

	// Compute a deterministic lock key so the background_jobs partial unique
	// index can reject a second concurrent upgrade against the same listener
	// set. Sorted join makes the key order-independent.
	sortedListeners := make([]string, len(req.ResourceNames))
	copy(sortedListeners, req.ResourceNames)
	sort.Strings(sortedListeners)
	lockKey := fmt.Sprintf("upgrade::%s::%s", req.Project, strings.Join(sortedListeners, ","))

	// Create job metadata with proper Listener GType
	jobMeta := &job.JobMetadata{
		SourceResource: &job.SourceResource{
			Type:       string(models.Listener),            // Always use full GType for listeners
			Name:       req.ResourceNames[0],               // First listener name as primary source
			Collection: models.Listener.CollectionString(), // "listeners"
			Action:     job.ActionTypeUpdate,               // Upgrade is an update action
			Version:    req.FromVersion,
			ProjectID:  req.Project,
		},
		AffectedListeners: req.ResourceNames, // All listener names
		TotalAffected:     len(req.ResourceNames),
		TriggerUser: &job.TriggerUser{
			ID:          userDetails.UserID,
			Username:    userDetails.UserName,
			DisplayName: userDetails.UserName,
			Role:        string(userDetails.Role),
		},
		UpgradeConfig: &job.UpgradeMetadata{
			TargetVersion:     req.ToVersion,
			AutoCreateMissing: req.Options.AutoCreateMissing,
			ValidateClients:   req.Options.ValidateClients,
			UpdateBootstrap:   req.Options.UpdateBootstrap,
			DryRun:            req.Options.DryRun,
			LockKey:           lockKey,
		},
	}

	// Create job with ANALYZING status
	// Add (DRY) suffix to job type if dry-run mode
	jobType := job.JobTypeResourceUpgrade
	if req.Options.DryRun {
		jobType = job.JobType(string(job.JobTypeResourceUpgrade) + "(DRY)")
	}

	createdJob, err := h.asyncSystem.CreateJob(c.Request.Context(), &job.CreateJobRequest{
		Type:     jobType,
		Status:   job.JobStatusAnalyzing,
		Metadata: jobMeta,
	})
	if err != nil {
		// Partial unique index on lock_key rejects concurrent upgrades.
		if mongo.IsDuplicateKeyError(err) {
			h.setAuditResult(c, err)
			c.JSON(409, gin.H{"error": "an upgrade is already in flight for the requested listener(s); wait for it to finish or retry it"})
			return
		}
		h.setAuditResult(c, err)
		c.JSON(500, gin.H{"error": "Failed to create upgrade job"})
		return
	}

	// Set audit success with job details
	audit.SetAuditChanges(c, map[string]any{
		"job_id":          createdJob.JobID,
		"listener_names":  req.ResourceNames,
		"total_listeners": len(req.ResourceNames),
		"from_version":    req.FromVersion,
		"to_version":      req.ToVersion,
		"options":         req.Options,
	})
	h.setAuditResult(c, nil)

	// Start dependency analysis in background
	go h.analyzeUpgradeDependencies(context.Background(), createdJob)

	c.JSON(200, gin.H{
		"success":                    true,
		"job_id":                     createdJob.JobID,
		"job_object_id":              createdJob.ID.Hex(),
		"status":                     createdJob.Status,
		"message":                    "Upgrade job created. Analyzing dependencies...",
		"estimated_duration_seconds": 60,
	})
}

// analyzeUpgradeDependencies analyzes dependencies in background
func (h *UpgradeHandler) analyzeUpgradeDependencies(ctx context.Context, j *job.Job) {
	jobManager := h.asyncSystem.GetJobManager()

	// Stamp ownership + heartbeat so a controller crash during analysis is
	// surfaced as a stuck job (and reaped by FailStuckAnalyzingJobs) rather
	// than leaving the job ANALYZING forever.
	workerID := fmt.Sprintf("analyzer-%s", j.ID.Hex())
	if err := jobManager.UpdateJob(ctx, j.ID.Hex(), map[string]any{
		"$set": map[string]any{
			"worker_info": map[string]any{
				"worker_id":  workerID,
				"claimed_at": time.Now(),
				"heartbeat":  time.Now(),
				"ttl":        300,
			},
		},
	}); err != nil {
		// Non-fatal: analysis can still proceed; only stuck detection is degraded.
		// (Logging is best-effort; the analyzer has its own logger.)
		_ = err
	}

	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go h.runAnalysisHeartbeat(hbCtx, jobManager, j.ID)

	analyzer := upgrade.NewUpgradeAnalyzer(h.dbContext, h.commandHandler)

	// Run analysis
	analysis, err := analyzer.AnalyzeDependencies(ctx, j)
	if err != nil {
		// Mark job as failed
		_ = jobManager.FailJob(ctx, j.ID, fmt.Errorf("dependency analysis failed: %w", err))
		return
	}

	// Update job with analysis results
	j.Metadata.UpgradeConfig.Analysis = analysis
	if err := jobManager.UpdateJob(ctx, j.ID.Hex(), map[string]any{
		"$set": map[string]any{
			"metadata": j.Metadata,
		},
	}); err != nil {
		_ = jobManager.FailJob(ctx, j.ID, fmt.Errorf("failed to update job metadata: %w", err))
		return
	}

	// If dry run, complete the job here with proper progress
	if j.Metadata.UpgradeConfig.DryRun {
		// Update progress to reflect completed analysis
		now := time.Now()
		_ = jobManager.UpdateJob(ctx, j.ID.Hex(), map[string]any{
			"$set": map[string]any{
				"started_at": now,
				"progress": &job.JobProgress{
					Total:      j.Metadata.TotalAffected,
					Completed:  j.Metadata.TotalAffected, // All analyzed = completed for dry-run
					Failed:     0,
					Percentage: 100.0,
				},
			},
		})
		_ = jobManager.CompleteJob(ctx, j.ID, &job.ExecutionDetails{})
		return
	}

	// Always transition to PENDING for worker to claim
	// Even if no dependencies need to be created, listener version must be updated
	_ = jobManager.UpdateJob(ctx, j.ID.Hex(), map[string]any{
		"$set": map[string]any{
			"status": job.JobStatusPending,
		},
	})
}

// runAnalysisHeartbeat keeps the job's worker_info.heartbeat fresh so the
// FailStuckAnalyzingJobs reaper does not mistakenly mark a long-running
// analysis as stuck. Exits when the caller cancels the context.
func (h *UpgradeHandler) runAnalysisHeartbeat(ctx context.Context, jm async.JobManagerInterface, jobID primitive.ObjectID) {
	ticker := time.NewTicker(analysisHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = jm.UpdateJobHeartbeat(ctx, jobID)
		}
	}
}

// setUpgradeAuditContext sets audit context for resource upgrade operations
func (h *UpgradeHandler) setUpgradeAuditContext(c *gin.Context, resourceType, resourceName, project, fromVersion, toVersion string) {
	if h.AuditService == nil {
		return
	}

	action := "UPGRADE_RESOURCE"
	audit.SetAuditAction(c, action)
	audit.SetAuditResource(c, resourceType, "", resourceName, project)

	// Store upgrade metadata for audit
	c.Set("upgrade_from_version", fromVersion)
	c.Set("upgrade_to_version", toVersion)
}

// setAuditResult sets audit result
func (h *UpgradeHandler) setAuditResult(c *gin.Context, err error) {
	if h.AuditService == nil {
		return
	}

	if err != nil {
		audit.SetAuditError(c, err.Error())
	} else {
		audit.SetAuditSuccess(c, true)
	}
}
