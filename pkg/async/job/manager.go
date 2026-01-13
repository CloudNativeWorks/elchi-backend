package job

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/security"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Manager implements job management operations
type Manager struct {
	db     *mongo.Database
	logger *logger.Logger
}

// NewManager creates a new job manager
func NewManager(db *mongo.Database, logger *logger.Logger) *Manager {
	return &Manager{
		db:     db,
		logger: logger,
	}
}

// generateJobID generates a human-friendly job ID (EC-1, EC-2, etc.)
func (m *Manager) generateJobID(ctx context.Context) (string, error) {
	collection := m.db.Collection("job_counters")

	// Atomically increment counter and get next sequence number
	result := collection.FindOneAndUpdate(
		ctx,
		bson.M{"_id": "elchi_config_jobs"},
		bson.M{"$inc": bson.M{"seq": 1}},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After),
	)

	var counter struct {
		Seq int `bson:"seq"`
	}

	if err := result.Decode(&counter); err != nil {
		return "", fmt.Errorf("failed to generate job ID: %w", err)
	}

	return fmt.Sprintf("EC-%d", counter.Seq), nil
}

// CreateJob creates a new background job
func (m *Manager) CreateJob(ctx context.Context, req *CreateJobRequest) (*Job, error) {
	jobID, err := m.generateJobID(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now()

	// Extract project and version based on job type
	var project, version string
	if req.Metadata.SourceResource != nil {
		project = req.Metadata.SourceResource.ProjectID
		version = req.Metadata.SourceResource.Version
	} else if req.Metadata.ACMEMetadata != nil {
		// For Let's Encrypt verification jobs
		project = req.Metadata.ACMEMetadata.Project
		version = "" // Let's Encrypt jobs don't have a version
	}

	job := &Job{
		ID:       primitive.NewObjectID(),
		JobID:    jobID,
		Type:     req.Type,
		Status:   req.Status,
		Metadata: req.Metadata,
		Progress: &JobProgress{
			Total:      req.Metadata.TotalAffected,
			Completed:  0,
			Failed:     0,
			Percentage: 0.0,
		},
		CreatedBy: req.Metadata.TriggerUser.ID,
		CreatedAt: now,
		Project:   project,
		Version:   version,
	}

	// If no work needed, mark as completed immediately
	if req.Status == JobStatusNoWorkNeeded {
		job.CompletedAt = &now
		job.Progress.Percentage = 100.0
	}

	// Insert into MongoDB
	collection := m.db.Collection("background_jobs")
	_, err = collection.InsertOne(ctx, job)
	if err != nil {
		return nil, fmt.Errorf("failed to create job: %w", err)
	}

	// Log with appropriate resource name based on job type
	var resourceName string
	if req.Metadata.SourceResource != nil {
		resourceName = req.Metadata.SourceResource.Name
	} else if req.Metadata.ACMEMetadata != nil {
		resourceName = req.Metadata.ACMEMetadata.CertificateName
	} else {
		resourceName = "unknown"
	}

	m.logger.Infof("Created job %s (ID: %s) for %s", jobID, job.ID.Hex(), resourceName)
	return job, nil
}

// CreateJobWithParent creates a new background job with a parent job reference
// This is used to enforce dependency ordering - the child job should not process
// until the parent job has completed and its database writes are visible
func (m *Manager) CreateJobWithParent(ctx context.Context, req *CreateJobRequest, parentJobID *primitive.ObjectID) (*Job, error) {
	jobID, err := m.generateJobID(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now()

	// Extract project and version based on job type
	var project, version string
	if req.Metadata.SourceResource != nil {
		project = req.Metadata.SourceResource.ProjectID
		version = req.Metadata.SourceResource.Version
	} else if req.Metadata.ACMEMetadata != nil {
		// For Let's Encrypt verification jobs
		project = req.Metadata.ACMEMetadata.Project
		version = "" // Let's Encrypt jobs don't have a version
	}

	job := &Job{
		ID:          primitive.NewObjectID(),
		JobID:       jobID,
		Type:        req.Type,
		Status:      req.Status,
		Metadata:    req.Metadata,
		ParentJobID: parentJobID, // Link to parent job for dependency tracking
		Progress: &JobProgress{
			Total:      req.Metadata.TotalAffected,
			Completed:  0,
			Failed:     0,
			Percentage: 0.0,
		},
		CreatedBy: req.Metadata.TriggerUser.ID,
		CreatedAt: now,
		Project:   project,
		Version:   version,
	}

	// If no work needed, mark as completed immediately
	if req.Status == JobStatusNoWorkNeeded {
		job.CompletedAt = &now
		job.Progress.Percentage = 100.0
	}

	// Insert into MongoDB
	collection := m.db.Collection("background_jobs")
	_, err = collection.InsertOne(ctx, job)
	if err != nil {
		return nil, fmt.Errorf("failed to create job: %w", err)
	}

	// Log with appropriate resource name based on job type
	var resourceName string
	if req.Metadata.SourceResource != nil {
		resourceName = req.Metadata.SourceResource.Name
	} else if req.Metadata.ACMEMetadata != nil {
		resourceName = req.Metadata.ACMEMetadata.CertificateName
	} else {
		resourceName = "unknown"
	}

	m.logger.Infof("Created job %s (ID: %s) for %s with parent job %s",
		jobID, job.ID.Hex(), resourceName, parentJobID.Hex())
	return job, nil
}

// GetJob retrieves a job by its ObjectID
func (m *Manager) GetJob(ctx context.Context, jobID string) (*Job, error) {
	objID, err := primitive.ObjectIDFromHex(jobID)
	if err != nil {
		return nil, fmt.Errorf("invalid job ID: %w", err)
	}

	var job Job
	collection := m.db.Collection("background_jobs")
	err = collection.FindOne(ctx, bson.M{"_id": objID}).Decode(&job)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("job not found")
		}
		return nil, err
	}

	return &job, nil
}

// GetJobByHumanID retrieves a job by its human-friendly ID (EC-1, EC-2, etc.)
func (m *Manager) GetJobByHumanID(ctx context.Context, humanID string) (*Job, error) {
	var job Job
	collection := m.db.Collection("background_jobs")
	err := collection.FindOne(ctx, bson.M{"job_id": humanID}).Decode(&job)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("job not found: %s", humanID)
		}
		return nil, err
	}

	return &job, nil
}

// UpdateJob updates a job with the provided update document
func (m *Manager) UpdateJob(ctx context.Context, jobID string, update interface{}) error {
	objID, err := primitive.ObjectIDFromHex(jobID)
	if err != nil {
		return fmt.Errorf("invalid job ID: %w", err)
	}

	collection := m.db.Collection("background_jobs")
	result, err := collection.UpdateOne(ctx, bson.M{"_id": objID}, update)
	if err != nil {
		return err
	}

	if result.MatchedCount == 0 {
		return fmt.Errorf("job not found")
	}

	return nil
}

// CancelJob cancels a pending or running job
func (m *Manager) CancelJob(ctx context.Context, jobID string) error {
	now := time.Now()
	update := bson.M{
		"$set": bson.M{
			"status":       JobStatusCancelled,
			"completed_at": now,
		},
	}

	return m.UpdateJob(ctx, jobID, update)
}

// ListJobs lists jobs with optional filtering
func (m *Manager) ListJobs(ctx context.Context, filter *JobFilter) ([]*Job, error) {
	collection := m.db.Collection("background_jobs")

	// Build filter
	mongoFilter := bson.M{}
	if len(filter.Status) > 0 {
		mongoFilter["status"] = bson.M{"$in": filter.Status}
	}
	if len(filter.Type) > 0 {
		mongoFilter["type"] = bson.M{"$in": filter.Type}
	}
	if filter.Project != "" {
		mongoFilter["project"] = filter.Project
	}
	if filter.CreatedBy != "" {
		mongoFilter["created_by"] = filter.CreatedBy
	}

	// New filters
	if filter.ResourceName != "" {
		if sanitizedResourceName, valid := security.SanitizeSearchInput(filter.ResourceName); valid && sanitizedResourceName != "" {
			mongoFilter["metadata.source_resource.name"] = bson.M{"$regex": sanitizedResourceName, "$options": "i"}
		}
	}
	if filter.AffectedListener != "" {
		mongoFilter["metadata.affected_listeners"] = bson.M{"$in": []string{filter.AffectedListener}}
	}
	if filter.Username != "" {
		if sanitizedUsername, valid := security.SanitizeSearchInput(filter.Username); valid && sanitizedUsername != "" {
			mongoFilter["metadata.trigger_user.username"] = bson.M{"$regex": sanitizedUsername, "$options": "i"}
		}
	}

	// Date range filter
	if filter.StartDate != "" || filter.EndDate != "" {
		dateFilter := bson.M{}
		if filter.StartDate != "" {
			if startTime, err := time.Parse("2006-01-02", filter.StartDate); err == nil {
				dateFilter["$gte"] = primitive.NewDateTimeFromTime(startTime)
			}
		}
		if filter.EndDate != "" {
			if endTime, err := time.Parse("2006-01-02", filter.EndDate); err == nil {
				// Add 24 hours to include the entire end date
				endOfDay := endTime.Add(24*time.Hour - time.Nanosecond)
				dateFilter["$lte"] = primitive.NewDateTimeFromTime(endOfDay)
			}
		}
		if len(dateFilter) > 0 {
			mongoFilter["created_at"] = dateFilter
		}
	}

	// Set options
	opts := options.Find().SetSort(bson.D{primitive.E{Key: "created_at", Value: -1}})
	if filter.Limit > 0 {
		opts.SetLimit(int64(filter.Limit))
	}
	if filter.Offset > 0 {
		opts.SetSkip(int64(filter.Offset))
	}

	cursor, err := collection.Find(ctx, mongoFilter, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var jobs []*Job
	if err := cursor.All(ctx, &jobs); err != nil {
		return nil, err
	}

	return jobs, nil
}

// CountJobs counts total jobs matching the filter
func (m *Manager) CountJobs(ctx context.Context, filter *JobFilter) (int64, error) {
	collection := m.db.Collection("background_jobs")

	// Build filter (same logic as ListJobs)
	mongoFilter := bson.M{}
	if len(filter.Status) > 0 {
		mongoFilter["status"] = bson.M{"$in": filter.Status}
	}
	if len(filter.Type) > 0 {
		mongoFilter["type"] = bson.M{"$in": filter.Type}
	}
	if filter.Project != "" {
		mongoFilter["project"] = filter.Project
	}
	if filter.CreatedBy != "" {
		mongoFilter["created_by"] = filter.CreatedBy
	}

	// New filters (same as ListJobs)
	if filter.ResourceName != "" {
		if sanitizedResourceName, valid := security.SanitizeSearchInput(filter.ResourceName); valid && sanitizedResourceName != "" {
			mongoFilter["metadata.source_resource.name"] = bson.M{"$regex": sanitizedResourceName, "$options": "i"}
		}
	}
	if filter.AffectedListener != "" {
		mongoFilter["metadata.affected_listeners"] = bson.M{"$in": []string{filter.AffectedListener}}
	}
	if filter.Username != "" {
		if sanitizedUsername, valid := security.SanitizeSearchInput(filter.Username); valid && sanitizedUsername != "" {
			mongoFilter["metadata.trigger_user.username"] = bson.M{"$regex": sanitizedUsername, "$options": "i"}
		}
	}

	// Date range filter (same as ListJobs)
	if filter.StartDate != "" || filter.EndDate != "" {
		dateFilter := bson.M{}
		if filter.StartDate != "" {
			if startTime, err := time.Parse("2006-01-02", filter.StartDate); err == nil {
				dateFilter["$gte"] = primitive.NewDateTimeFromTime(startTime)
			}
		}
		if filter.EndDate != "" {
			if endTime, err := time.Parse("2006-01-02", filter.EndDate); err == nil {
				// Add 24 hours to include the entire end date
				endOfDay := endTime.Add(24*time.Hour - time.Nanosecond)
				dateFilter["$lte"] = primitive.NewDateTimeFromTime(endOfDay)
			}
		}
		if len(dateFilter) > 0 {
			mongoFilter["created_at"] = dateFilter
		}
	}

	count, err := collection.CountDocuments(ctx, mongoFilter)
	if err != nil {
		return 0, err
	}

	return count, nil
}

// ClaimJob claims a pending job for processing
func (m *Manager) ClaimJob(ctx context.Context, workerID string) (*Job, error) {
	collection := m.db.Collection("background_jobs")

	// Find and claim a job atomically
	filter := bson.M{
		"status": JobStatusPending,
		"$or": bson.A{
			bson.M{"worker_info.ttl": bson.M{"$lt": time.Now()}},
			bson.M{"worker_info": bson.M{"$exists": false}},
		},
	}

	update := bson.M{
		"$set": bson.M{
			"status": JobStatusClaimed,
			"worker_info": bson.M{
				"worker_id":  workerID,
				"claimed_at": time.Now(),
				"heartbeat":  time.Now(),
				"ttl":        300, // 5 minutes
			},
		},
	}

	opts := options.FindOneAndUpdate().
		SetSort(bson.D{primitive.E{Key: "created_at", Value: 1}}).
		SetReturnDocument(options.After)

	var job Job
	result := collection.FindOneAndUpdate(ctx, filter, update, opts)
	if err := result.Decode(&job); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil // No job available
		}
		return nil, err
	}

	m.logger.Debugf("Worker %s claimed job %s", workerID, job.JobID)
	return &job, nil
}

// UpdateJobProgress updates the progress of a job
func (m *Manager) UpdateJobProgress(ctx context.Context, jobID primitive.ObjectID, progress *JobProgress) error {
	collection := m.db.Collection("background_jobs")

	update := bson.M{
		"$set": bson.M{
			"progress":              progress,
			"worker_info.heartbeat": time.Now(),
		},
	}

	_, err := collection.UpdateOne(ctx, bson.M{"_id": jobID}, update)
	return err
}

// UpdateJobMetadata updates the metadata of a job
func (m *Manager) UpdateJobMetadata(ctx context.Context, jobID primitive.ObjectID, metadata *JobMetadata) error {
	collection := m.db.Collection("background_jobs")

	update := bson.M{
		"$set": bson.M{
			"metadata":              metadata,
			"worker_info.heartbeat": time.Now(),
		},
	}

	_, err := collection.UpdateOne(ctx, bson.M{"_id": jobID}, update)
	return err
}

// UpdateJobHeartbeat updates the worker heartbeat for a job
func (m *Manager) UpdateJobHeartbeat(ctx context.Context, jobID primitive.ObjectID) error {
	collection := m.db.Collection("background_jobs")

	update := bson.M{
		"$set": bson.M{
			"worker_info.heartbeat": time.Now(),
		},
	}

	_, err := collection.UpdateOne(ctx, bson.M{"_id": jobID}, update)
	return err
}

// CompleteJob marks a job as completed
func (m *Manager) CompleteJob(ctx context.Context, jobID primitive.ObjectID, executionDetails *ExecutionDetails) error {
	collection := m.db.Collection("background_jobs")

	now := time.Now()
	update := bson.M{
		"$set": bson.M{
			"status":              JobStatusCompleted,
			"completed_at":        now,
			"execution_details":   executionDetails,
			"progress.percentage": 100.0,
		},
	}

	_, err := collection.UpdateOne(ctx, bson.M{"_id": jobID}, update)
	if err != nil {
		return err
	}

	m.logger.Infof("Job %s completed successfully", jobID.Hex())
	return nil
}

// FailJob marks a job as failed
func (m *Manager) FailJob(ctx context.Context, jobID primitive.ObjectID, jobError error) error {
	collection := m.db.Collection("background_jobs")

	now := time.Now()
	update := bson.M{
		"$set": bson.M{
			"status":       JobStatusFailed,
			"completed_at": now,
			"error":        jobError.Error(),
		},
	}

	_, err := collection.UpdateOne(ctx, bson.M{"_id": jobID}, update)
	if err != nil {
		return err
	}

	m.logger.Errorf("Job %s failed: %v", jobID.Hex(), jobError)
	return nil
}

// RetryJob creates a retry job from an existing job
func (m *Manager) RetryJob(ctx context.Context, jobID string, reason string) (*Job, error) {
	// Get original job
	originalJob, err := m.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}

	// Create retry job
	retryCount := 1
	if originalJob.RetryInfo != nil {
		retryCount = originalJob.RetryInfo.RetryCount + 1
	}

	req := &CreateJobRequest{
		Type:     originalJob.Type,
		Status:   JobStatusPending,
		Metadata: originalJob.Metadata,
	}

	retryJob, err := m.CreateJob(ctx, req)
	if err != nil {
		return nil, err
	}

	// Update retry info
	retryJob.RetryInfo = &RetryInfo{
		OriginalJobID: originalJob.ID,
		RetryCount:    retryCount,
		RetryReason:   reason,
		RetryType:     "full",
	}

	// Update in database
	collection := m.db.Collection("background_jobs")
	_, err = collection.UpdateOne(
		ctx,
		bson.M{"_id": retryJob.ID},
		bson.M{"$set": bson.M{"retry_info": retryJob.RetryInfo}},
	)
	if err != nil {
		return nil, err
	}

	m.logger.Infof("Created retry job %s for original job %s (retry #%d)", retryJob.JobID, originalJob.JobID, retryCount)
	return retryJob, nil
}

// RetryFailedSnapshots creates a retry job for only failed snapshots
func (m *Manager) RetryFailedSnapshots(ctx context.Context, jobID string) (*Job, error) {
	// Get original job
	originalJob, err := m.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}

	// Extract failed listeners
	failedListeners := []string{}
	if originalJob.ExecutionDetails != nil {
		for _, snapshot := range originalJob.ExecutionDetails.ProcessedSnapshots {
			if snapshot.PokeStatus == PokeStatusFailed {
				failedListeners = append(failedListeners, snapshot.ListenerName)
			}
		}
	}

	if len(failedListeners) == 0 {
		return nil, fmt.Errorf("no failed snapshots to retry")
	}

	// Create retry job for failed snapshots only
	metadata := *originalJob.Metadata
	metadata.AffectedListeners = failedListeners
	metadata.TotalAffected = len(failedListeners)

	req := &CreateJobRequest{
		Type:     originalJob.Type,
		Status:   JobStatusPending,
		Metadata: &metadata,
	}

	retryJob, err := m.CreateJob(ctx, req)
	if err != nil {
		return nil, err
	}

	// Update retry info
	retryCount := 1
	if originalJob.RetryInfo != nil {
		retryCount = originalJob.RetryInfo.RetryCount + 1
	}

	retryJob.RetryInfo = &RetryInfo{
		OriginalJobID: originalJob.ID,
		RetryCount:    retryCount,
		RetryReason:   "failed_only",
		RetryType:     "failed_only",
	}

	// Update in database
	collection := m.db.Collection("background_jobs")
	_, err = collection.UpdateOne(
		ctx,
		bson.M{"_id": retryJob.ID},
		bson.M{"$set": bson.M{"retry_info": retryJob.RetryInfo}},
	)
	if err != nil {
		return nil, err
	}

	m.logger.Infof("Created retry job %s for %d failed snapshots from job %s", retryJob.JobID, len(failedListeners), originalJob.JobID)
	return retryJob, nil
}

// GetStuckJobs finds jobs that haven't had a heartbeat in 10 minutes
func (m *Manager) GetStuckJobs(ctx context.Context) ([]*Job, error) {
	collection := m.db.Collection("background_jobs")

	stuckThreshold := time.Now().Add(-10 * time.Minute)
	filter := bson.M{
		"$and": bson.A{
			bson.M{"status": bson.M{"$in": []JobStatus{JobStatusRunning, JobStatusClaimed}}},
			bson.M{"worker_info.heartbeat": bson.M{"$lt": stuckThreshold}},
		},
	}

	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var stuckJobs []*Job
	if err := cursor.All(ctx, &stuckJobs); err != nil {
		return nil, err
	}

	return stuckJobs, nil
}

// CreatePreliminaryJob creates a job without dependency analysis (fast response)
func (m *Manager) CreatePreliminaryJob(ctx context.Context, req *CreateJobRequest) (*Job, error) {
	// Force status to ANALYZING for preliminary jobs
	req.Status = JobStatusAnalyzing

	// Create job with minimal metadata (no affected listeners yet)
	return m.CreateJob(ctx, req)
}

// CompleteAnalysis completes the analysis phase and updates job status
func (m *Manager) CompleteAnalysis(ctx context.Context, jobID primitive.ObjectID, analysis *DependencyAnalysisResult) error {
	collection := m.db.Collection("background_jobs")

	// Determine next status based on analysis results
	nextStatus := JobStatusPending
	if len(analysis.AffectedListeners) == 0 {
		nextStatus = JobStatusNoWorkNeeded
	}

	now := time.Now()
	update := bson.M{
		"$set": bson.M{
			"status":                        nextStatus,
			"metadata.affected_listeners":   analysis.AffectedListeners,
			"metadata.total_affected":       len(analysis.AffectedListeners),
			"metadata.analysis_duration_ms": analysis.DurationMS,
			"metadata.dependency_chain":     analysis.DependencyChain,
			"progress.total":                len(analysis.AffectedListeners),
		},
	}

	// If no work needed, mark as completed immediately
	if nextStatus == JobStatusNoWorkNeeded {
		update["$set"].(bson.M)["completed_at"] = now
		update["$set"].(bson.M)["progress.percentage"] = 100.0
	}

	_, err := collection.UpdateOne(ctx, bson.M{"_id": jobID}, update)
	if err != nil {
		return err
	}

	m.logger.Infof("Job %s analysis completed: %d affected listeners", jobID.Hex(), len(analysis.AffectedListeners))
	return nil
}
