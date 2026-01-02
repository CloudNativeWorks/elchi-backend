package worker

import (
	"context"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/async/job"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// WAFProcessor is an interface to avoid import cycles with controller/waf
// The actual implementation is in controller/waf/async_processor.go
type WAFProcessor interface {
	ProcessWAFPropagationJob(ctx context.Context, wafName, project string, wasmNames []string, userDetails models.UserDetails) ([]string, []string, error)
}

// processWAFPropagationJob processes a WAF propagation job
// This delegates to the WAF processor injected via interface
func (w *Worker) processWAFPropagationJob(ctx context.Context, j *job.Job) {
	w.logger.Infof("Starting WAF propagation job %s", j.JobID)

	// Validate job metadata
	if j.Metadata == nil || j.Metadata.WAFConfig == nil {
		w.logger.Errorf("Job %s has invalid metadata", j.JobID)
		if err := w.jobManager.FailJob(ctx, j.ID, fmt.Errorf("invalid job metadata")); err != nil {
			w.logger.Errorf("Failed to mark job as failed: %v", err)
		}
		return
	}

	wafMeta := j.Metadata.WAFConfig
	wasmNames := j.Metadata.AffectedWASM

	// Get the user who triggered this WAF update from job metadata
	triggerUser := j.Metadata.TriggerUser
	if triggerUser == nil {
		w.logger.Errorf("Job %s has no trigger user information", j.JobID)
		if err := w.jobManager.FailJob(ctx, j.ID, fmt.Errorf("missing trigger user information")); err != nil {
			w.logger.Errorf("Failed to mark job as failed: %v", err)
		}
		return
	}

	// Check if we have a WAF processor
	processor := w.getWAFProcessor()
	if processor == nil {
		w.logger.Errorf("WAF processor not available for job %s", j.JobID)
		if err := w.jobManager.FailJob(ctx, j.ID, fmt.Errorf("WAF processor not configured")); err != nil {
			w.logger.Errorf("Failed to mark job as failed: %v", err)
		}
		return
	}

	// Convert TriggerUser to models.UserDetails
	userDetails := models.UserDetails{
		UserID:   triggerUser.ID,
		UserName: triggerUser.Username,
		Role:     models.Role(triggerUser.Role),
		IsOwner:  triggerUser.Role == "owner",
	}

	// Process WAF propagation with actual user context
	affectedListeners, failedWASM, err := processor.ProcessWAFPropagationJob(ctx, wafMeta.Name, wafMeta.Project, wasmNames, userDetails)
	if err != nil {
		w.logger.Errorf("WAF propagation job %s failed: %v", j.JobID, err)
		if failErr := w.jobManager.FailJob(ctx, j.ID, err); failErr != nil {
			w.logger.Errorf("Failed to mark job as failed: %v", failErr)
		}
		return
	}

	// Update job metadata with results
	j.Metadata.AffectedListeners = affectedListeners
	j.Metadata.TotalAffected = len(affectedListeners)

	// Persist metadata update to database
	if err := w.jobManager.UpdateJobMetadata(ctx, j.ID, j.Metadata); err != nil {
		w.logger.Errorf("Failed to update job metadata: %v", err)
	}

	// Update progress
	progress := &job.JobProgress{
		Total:      len(wasmNames),
		Completed:  len(wasmNames) - len(failedWASM),
		Failed:     len(failedWASM),
		Percentage: float64(len(wasmNames)-len(failedWASM)) / float64(len(wasmNames)) * 100.0,
	}
	if err := w.jobManager.UpdateJobProgress(ctx, j.ID, progress); err != nil {
		w.logger.Errorf("Failed to update job progress: %v", err)
	}

	// Create SnapshotExecution records for each affected listener
	// Note: Snapshots were already triggered by dependency analysis in processor
	// We're just recording what was processed
	snapshots := w.createSnapshotExecutions(affectedListeners, j.Project)

	// Complete the job
	executionDetails := &job.ExecutionDetails{
		ProcessedSnapshots: snapshots,
	}

	if len(failedWASM) > 0 {
		w.logger.Warnf("WAF propagation job %s completed with %d failures", j.JobID, len(failedWASM))
		if len(failedWASM) == len(wasmNames) {
			// All failed
			if err := w.jobManager.FailJob(ctx, j.ID, fmt.Errorf("all WASM extensions failed to process")); err != nil {
				w.logger.Errorf("Failed to mark job as failed: %v", err)
			}
			return
		}
	}

	if err := w.jobManager.CompleteJob(ctx, j.ID, executionDetails); err != nil {
		w.logger.Errorf("Failed to complete job: %v", err)
	}
	w.logger.Infof("WAF propagation job %s completed: %d listeners affected, %d WASM failed",
		j.JobID, len(affectedListeners), len(failedWASM))
}

// getWAFProcessor returns the WAF processor instance
func (w *Worker) getWAFProcessor() WAFProcessor {
	return w.wafProcessor
}

// createSnapshotExecutions creates SnapshotExecution records for affected listeners
// Note: The actual snapshots have already been triggered by dependency analysis
// This just creates records for job tracking
// IMPORTANT: For multi-client deployments, this creates one record per listener,
// not per client. The actual poke was sent to ALL clients by dependency analysis.
func (w *Worker) createSnapshotExecutions(listeners []string, project string) []job.SnapshotExecution {
	snapshots := make([]job.SnapshotExecution, 0, len(listeners))

	for _, listenerName := range listeners {
		// Note: We use simple format here because:
		// 1. Dependency analysis already sent pokes to ALL clients
		// 2. This is just for tracking/reporting purposes
		// 3. Frontend shows listener-level status, not per-client status for WAF jobs
		nodeID := fmt.Sprintf("%s::%s", listenerName, project)

		execution := job.SnapshotExecution{
			NodeID:       nodeID,
			ListenerName: listenerName,
			PokeStatus:   job.PokeStatusSuccess, // Already poked by dependency analysis
		}

		snapshots = append(snapshots, execution)
	}

	return snapshots
}
