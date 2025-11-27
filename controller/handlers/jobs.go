package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/controller/waf"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async/job"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async/worker"
	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// JobHandler handles job management API requests
type JobHandler struct {
	asyncSystem async.AsyncJobSystem
	dbContext   *db.AppContext
	logger      *logger.Logger
}

// NewJobHandler creates a new job handler
func NewJobHandler(dbContext *db.AppContext, pokeService *bridge.PokeServiceClient, handlerLogger *logger.Logger, clientHandler worker.ClientCommandHandler) *JobHandler {
	// Create WAF processor for async operations
	wafProcessor := waf.NewAsyncWAFProcessor(dbContext, pokeService, handlerLogger)

	// Initialize async system for job management
	asyncSystem, err := async.NewAsyncJobSystem(&async.Config{
		MongoDB:        dbContext.Client,
		DBContext:      dbContext,
		WAFProcessor:   wafProcessor,
		CommandHandler: clientHandler,
		WorkerCount:    5,
		BatchSize:      10,
	})
	if err != nil {
		logger.Fatalf("Failed to initialize async system: %v", err)
	}

	return &JobHandler{
		asyncSystem: asyncSystem,
		dbContext:   dbContext,
		logger:      logger.NewLogger("job-handler"),
	}
}

// GetAsyncSystem returns the async job system instance
func (h *JobHandler) GetAsyncSystem() async.AsyncJobSystem {
	return h.asyncSystem
}

// StartAsyncSystem starts the async job system workers
func (h *JobHandler) StartAsyncSystem(pokeService any) error {
	// Set poke service for registry integration
	if err := h.asyncSystem.SetPokeService(pokeService.(*bridge.PokeServiceClient)); err != nil {
		return fmt.Errorf("failed to set poke service: %w", err)
	}

	// Start workers - WAFProcessor already set in async system config
	workerConfig := &async.WorkerConfig{
		PokeService:        pokeService,
		JobManager:         h.asyncSystem.GetJobManager(),
		DBContext:          h.dbContext,
		MaxConcurrentPokes: 5,
	}

	if err := h.asyncSystem.StartWorkers(context.Background(), workerConfig); err != nil {
		return fmt.Errorf("failed to start workers: %w", err)
	}

	h.logger.Infof("Async job system started with workers")
	return nil
}

// ListJobs handles GET /api/v3/jobs
func (h *JobHandler) ListJobs(c *gin.Context) {
	ctx := c.Request.Context()

	// Get user details using the same method as other handlers
	userDetails, _ := GetUserDetails(c)

	// Build filter from query parameters
	filter := &async.JobFilter{}

	// Status filter
	if statusParam := c.Query("status"); statusParam != "" {
		status := job.JobStatus(statusParam)
		filter.Status = []job.JobStatus{status}
	}

	// Multiple status filter
	if statusesParam := c.QueryArray("statuses"); len(statusesParam) > 0 {
		var statuses []job.JobStatus
		for _, s := range statusesParam {
			statuses = append(statuses, job.JobStatus(s))
		}
		filter.Status = statuses
	}

	// Type filter
	if typeParam := c.Query("type"); typeParam != "" {
		jobType := job.JobType(typeParam)
		filter.Type = []job.JobType{jobType}
	}

	// Project filter (only show jobs for accessible projects)
	if projectParam := c.Query("project"); projectParam != "" {
		// Check if user has access to this project
		if err := authorization.ValidateRequestProject(c.Request.Context(), h.dbContext.Client, userDetails, projectParam); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "Access denied to project"})
			return
		}
		filter.Project = projectParam
	}

	// CreatedBy filter (users can see their own jobs, admins see all)
	if !userDetails.IsOwner && userDetails.Role != models.RoleAdmin {
		filter.CreatedBy = userDetails.UserID
	}

	// Resource name filter (partial match, case insensitive)
	if resourceNameParam := c.Query("resource_name"); resourceNameParam != "" {
		filter.ResourceName = resourceNameParam
	}

	// Affected listener filter (exact match)
	if listenerParam := c.Query("affected_listener"); listenerParam != "" {
		filter.AffectedListener = listenerParam
	}

	// Username filter (partial match, case insensitive)
	if usernameParam := c.Query("username"); usernameParam != "" {
		filter.Username = usernameParam
	}

	// Date range filters (YYYY-MM-DD format)
	if startDateParam := c.Query("start_date"); startDateParam != "" {
		filter.StartDate = startDateParam
	}
	if endDateParam := c.Query("end_date"); endDateParam != "" {
		filter.EndDate = endDateParam
	}

	// Pagination
	if limitParam := c.Query("limit"); limitParam != "" {
		if limit, err := strconv.Atoi(limitParam); err == nil && limit > 0 && limit <= 100 {
			filter.Limit = limit
		}
	}
	if filter.Limit == 0 {
		filter.Limit = 20 // Default limit
	}

	if offsetParam := c.Query("offset"); offsetParam != "" {
		if offset, err := strconv.Atoi(offsetParam); err == nil && offset >= 0 {
			filter.Offset = offset
		}
	}

	// Get jobs and total count from async system
	jobs, err := h.asyncSystem.ListJobs(ctx, filter)
	if err != nil {
		h.logger.Errorf("Failed to list jobs: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve jobs"})
		return
	}

	// Get total count for pagination
	totalCount, err := h.asyncSystem.CountJobs(ctx, filter)
	if err != nil {
		h.logger.Errorf("Failed to count jobs: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to count jobs"})
		return
	}

	// Calculate total pages
	totalPages := int(totalCount) / filter.Limit
	if int(totalCount)%filter.Limit != 0 {
		totalPages++
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Success",
		"data": gin.H{
			"jobs":         jobs,
			"count":        len(jobs),                          // Current page job count
			"total_count":  totalCount,                         // Total jobs in database
			"total_pages":  totalPages,                         // Total number of pages
			"limit":        filter.Limit,                       // Items per page
			"offset":       filter.Offset,                      // Current offset
			"current_page": (filter.Offset / filter.Limit) + 1, // Current page number
		},
	})
}

// GetJob handles GET /api/v3/jobs/:id
func (h *JobHandler) GetJob(c *gin.Context) {
	ctx := c.Request.Context()
	jobID := c.Param("id")

	if jobID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Job ID is required"})
		return
	}

	// Get user details using the same method as other handlers
	userDetails, _ := GetUserDetails(c)

	// Try to get job by human ID first (EC-1, EC-2, etc.)
	var jobData *async.Job
	var err error

	if jobID[:3] == "EC-" {
		jobData, err = h.asyncSystem.GetJobByHumanID(ctx, jobID)
	} else {
		jobData, err = h.asyncSystem.GetJob(ctx, jobID)
	}

	if err != nil {
		if err == mongo.ErrNoDocuments {
			c.JSON(http.StatusNotFound, gin.H{"error": "Job not found"})
			return
		}
		h.logger.Errorf("Failed to get job %s: %v", jobID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve job"})
		return
	}

	// Check access permissions
	if !userDetails.IsOwner && userDetails.Role != models.RoleAdmin {
		// Non-admin users can only see their own jobs or jobs in their accessible projects
		canAccess := false

		// Check if user created the job
		if jobData.Metadata.TriggerUser.ID == userDetails.UserID {
			canAccess = true
		}

		// Check if user has access to the project
		if !canAccess {
			if err := authorization.ValidateRequestProject(c.Request.Context(), h.dbContext.Client, userDetails, jobData.Project); err == nil {
				canAccess = true
			}
		}

		if !canAccess {
			c.JSON(http.StatusForbidden, gin.H{"error": "Access denied to this job"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Success",
		"data":    jobData,
	})
}

// RetryJob handles POST /api/v3/jobs/:id/retry
func (h *JobHandler) RetryJob(c *gin.Context) {
	ctx := c.Request.Context()
	jobID := c.Param("id")

	if jobID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Job ID is required"})
		return
	}

	// Get user details using the same method as other handlers
	userDetails, _ := GetUserDetails(c)

	// Parse request body for retry options
	var retryReq struct {
		Reason    string `json:"reason"`
		RetryType string `json:"retry_type"` // "full", "failed_only"
	}

	if err := c.ShouldBindJSON(&retryReq); err != nil {
		retryReq.Reason = "Manual retry"
		retryReq.RetryType = "full"
	}

	// Get the original job to check permissions
	var originalJob *async.Job
	var err error

	if jobID[:3] == "EC-" {
		originalJob, err = h.asyncSystem.GetJobByHumanID(ctx, jobID)
	} else {
		originalJob, err = h.asyncSystem.GetJob(ctx, jobID)
	}

	if err != nil {
		if err == mongo.ErrNoDocuments {
			c.JSON(http.StatusNotFound, gin.H{"error": "Job not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve job"})
		return
	}

	// Check permissions (only admins, owners, or job creators can retry)
	if !userDetails.IsOwner && userDetails.Role != models.RoleAdmin {
		if originalJob.Metadata.TriggerUser.ID != userDetails.UserID {
			c.JSON(http.StatusForbidden, gin.H{"error": "Access denied to retry this job"})
			return
		}
	}

	// Retry the job
	var newJob *async.Job

	if retryReq.RetryType == "failed_only" {
		newJob, err = h.asyncSystem.RetryFailedSnapshots(ctx, jobID)
	} else {
		newJob, err = h.asyncSystem.RetryJob(ctx, jobID, retryReq.Reason)
	}

	if err != nil {
		h.logger.Errorf("Failed to retry job %s: %v", jobID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retry job"})
		return
	}

	h.logger.Infof("User %s retried job %s, new job: %s", userDetails.UserName, jobID, newJob.JobID)

	c.JSON(http.StatusOK, gin.H{
		"message": "Success",
		"data": gin.H{
			"original_job_id": jobID,
			"new_job":         newJob,
			"retry_type":      retryReq.RetryType,
			"retry_reason":    retryReq.Reason,
		},
	})
}

// GetStuckJobs handles GET /api/v3/jobs/stuck
func (h *JobHandler) GetStuckJobs(c *gin.Context) {
	ctx := c.Request.Context()

	// Get user details using the same method as other handlers
	userDetails, _ := GetUserDetails(c)

	// All authenticated users can view stuck jobs (monitoring purposes)
	// Editor/Viewer can monitor job status for troubleshooting
	_ = userDetails // Keep for future use if filtering needed

	stuckJobs, err := h.asyncSystem.GetStuckJobs(ctx)
	if err != nil {
		h.logger.Errorf("Failed to get stuck jobs: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve stuck jobs"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Success",
		"data": gin.H{
			"stuck_jobs": stuckJobs,
			"count":      len(stuckJobs),
			"checked_at": time.Now(),
		},
	})
}

// GetWorkerStatus handles GET /api/v3/jobs/workers
func (h *JobHandler) GetWorkerStatus(c *gin.Context) {
	ctx := c.Request.Context()

	// Get user details using the same method as other handlers
	userDetails, _ := GetUserDetails(c)

	// All authenticated users can view worker status (readonly monitoring)
	// Editor/Viewer can monitor system health for troubleshooting
	_ = userDetails // Keep for future use if filtering needed

	status, err := h.asyncSystem.GetWorkerStatus(ctx)
	if err != nil {
		h.logger.Errorf("Failed to get worker status: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve worker status"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Success",
		"data":    status,
	})
}

// GetJobStats handles GET /api/v3/jobs/stats
func (h *JobHandler) GetJobStats(c *gin.Context) {
	ctx := c.Request.Context()

	// Get user details using the same method as other handlers
	userDetails, _ := GetUserDetails(c)

	// Check role permissions
	if err := checkRole(c, userDetails); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	// Build filter from query parameters (same as ListJobs, but without pagination)
	filter := &async.JobFilter{}

	// Status filter
	if statusParam := c.Query("status"); statusParam != "" {
		status := job.JobStatus(statusParam)
		filter.Status = []job.JobStatus{status}
	}

	// Multiple status filter
	if statusesParam := c.QueryArray("statuses"); len(statusesParam) > 0 {
		var statuses []job.JobStatus
		for _, s := range statusesParam {
			statuses = append(statuses, job.JobStatus(s))
		}
		filter.Status = statuses
	}

	// Type filter
	if typeParam := c.Query("type"); typeParam != "" {
		jobType := job.JobType(typeParam)
		filter.Type = []job.JobType{jobType}
	}

	// Project filter (only show stats for accessible projects)
	if projectParam := c.Query("project"); projectParam != "" {
		// Check if user has access to this project
		if err := authorization.ValidateRequestProject(c.Request.Context(), h.dbContext.Client, userDetails, projectParam); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "Access denied to project"})
			return
		}
		filter.Project = projectParam
	}

	// CreatedBy filter (users can see their own jobs, admins see all)
	if !userDetails.IsOwner && userDetails.Role != models.RoleAdmin {
		filter.CreatedBy = userDetails.UserID
	}

	// Resource name filter (partial match, case insensitive)
	if resourceNameParam := c.Query("resource_name"); resourceNameParam != "" {
		filter.ResourceName = resourceNameParam
	}

	// Affected listener filter (exact match)
	if listenerParam := c.Query("affected_listener"); listenerParam != "" {
		filter.AffectedListener = listenerParam
	}

	// Username filter (partial match, case insensitive)
	if usernameParam := c.Query("username"); usernameParam != "" {
		filter.Username = usernameParam
	}

	// Date range filters (YYYY-MM-DD format)
	if startDateParam := c.Query("start_date"); startDateParam != "" {
		filter.StartDate = startDateParam
	}
	if endDateParam := c.Query("end_date"); endDateParam != "" {
		filter.EndDate = endDateParam
	}

	// Get filtered stats from async system
	stats, err := h.asyncSystem.GetJobStatsFiltered(ctx, filter)
	if err != nil {
		h.logger.Errorf("Failed to get job stats: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve job statistics"})
		return
	}

	// Build response with applied filters info
	appliedFilters := gin.H{}
	if len(filter.Status) > 0 {
		appliedFilters["status"] = filter.Status
	}
	if len(filter.Type) > 0 {
		appliedFilters["type"] = filter.Type
	}
	if filter.Project != "" {
		appliedFilters["project"] = filter.Project
	}
	if filter.ResourceName != "" {
		appliedFilters["resource_name"] = filter.ResourceName
	}
	if filter.AffectedListener != "" {
		appliedFilters["affected_listener"] = filter.AffectedListener
	}
	if filter.Username != "" {
		appliedFilters["username"] = filter.Username
	}
	if filter.StartDate != "" {
		appliedFilters["start_date"] = filter.StartDate
	}
	if filter.EndDate != "" {
		appliedFilters["end_date"] = filter.EndDate
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Success",
		"data":    stats,
		"filters": appliedFilters,
	})
}
