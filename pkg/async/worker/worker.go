// Package worker provides async job processing workers for background tasks
// including ACME certificates, upgrades, and WAF operations.
package worker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	bridgeClient "github.com/CloudNativeWorks/elchi-backend/controller/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async/job"
	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/services"
)

// ClientCommandHandler interface for sending commands to clients
// This avoids import cycle with controller/client/handlers package
type ClientCommandHandler interface {
	HandleSendCommand(ctx context.Context, op models.OperationClass, requestDetails models.RequestDetails) (any, error)
}

// Worker represents a single job processor
type Worker struct {
	id             string
	config         *WorkerConfig
	poolConfig     *PoolConfig
	jobManager     *job.Manager
	pokeService    *bridge.PokeServiceClient
	dbContext      *db.AppContext
	wafProcessor   WAFProcessor
	shieldDeployer ShieldDeployer
	commandHandler ClientCommandHandler
	logger         *logger.Logger
	isProcessing   bool
	processMutex   sync.RWMutex
	lastActivity   time.Time
}

// NewWorker creates a new worker
func NewWorker(id string, poolConfig *PoolConfig, jobManager *job.Manager, pokeService *bridge.PokeServiceClient, dbContext *db.AppContext, wafProcessor WAFProcessor, shieldDeployer ShieldDeployer, commandHandler ClientCommandHandler) *Worker {
	// Create WorkerConfig from poolConfig
	workerConfig := &WorkerConfig{
		PokeService:        pokeService,
		JobManager:         jobManager,
		DBContext:          dbContext,
		WAFProcessor:       wafProcessor,
		ShieldDeployer:     shieldDeployer,
		MaxConcurrentPokes: poolConfig.MaxConcurrentPokes,
	}

	return &Worker{
		id:             id,
		config:         workerConfig,
		poolConfig:     poolConfig,
		jobManager:     jobManager,
		pokeService:    pokeService,
		dbContext:      dbContext,
		wafProcessor:   wafProcessor,
		shieldDeployer: shieldDeployer,
		commandHandler: commandHandler,
		logger:         logger.NewLogger(fmt.Sprintf("worker-%s", id)),
	}
}

// Run starts the worker loop
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.poolConfig.PollInterval)
	defer ticker.Stop()

	w.logger.Infof("Worker %s started", w.id)

	for {
		select {
		case <-ctx.Done():
			w.logger.Infof("Worker %s stopping", w.id)
			return
		case <-ticker.C:
			w.processNextJob(ctx)
		}
	}
}

// processNextJob claims and processes the next available job
func (w *Worker) processNextJob(ctx context.Context) {
	// Try to claim a job
	claimedJob, err := w.jobManager.ClaimJob(ctx, w.id)
	if err != nil {
		w.logger.Errorf("Error claiming job: %v", err)
		return
	}

	if claimedJob == nil {
		// No job available
		return
	}

	w.setProcessing(true)
	defer w.setProcessing(false)

	// Process the job
	w.logger.Infof("Processing job %s", claimedJob.JobID)

	// Start heartbeat goroutine
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	go w.sendHeartbeat(heartbeatCtx, claimedJob.ID)
	defer cancelHeartbeat()

	// Mark job as running
	now := time.Now()
	err = w.jobManager.UpdateJob(ctx, claimedJob.ID.Hex(), map[string]any{
		"$set": map[string]any{
			"status":     job.JobStatusRunning,
			"started_at": now,
		},
	})
	if err != nil {
		w.logger.Errorf("Error updating job status: %v", err)
		return
	}

	// Process based on job type
	switch claimedJob.Type {
	case job.JobTypeSnapshotUpdate:
		w.processSnapshotUpdateJob(ctx, claimedJob)
	case job.JobTypeWAFPropagation:
		w.processWAFPropagationJob(ctx, claimedJob)
	case job.JobTypeResourceUpgrade:
		w.processResourceUpgradeJob(ctx, claimedJob)
	case job.JobTypeACMEVerification:
		w.processACMEVerificationJob(ctx, claimedJob)
	case job.JobTypeShieldDeploy:
		w.processShieldDeployJob(ctx, claimedJob)
	default:
		w.logger.Errorf("Unknown job type: %s", claimedJob.Type)
		if err := w.jobManager.FailJob(ctx, claimedJob.ID, fmt.Errorf("unknown job type")); err != nil {
			w.logger.Errorf("Failed to mark job as failed: %v", err)
		}
	}
}

// processSnapshotUpdateJob processes a snapshot update job
func (w *Worker) processSnapshotUpdateJob(ctx context.Context, j *job.Job) {
	// DEPENDENCY CHECK: If this job has a parent (e.g., ACME verification),
	// verify that the parent's secrets were actually written to MongoDB
	if j.ParentJobID != nil {
		if err := w.verifyParentJobCompleted(ctx, j); err != nil {
			w.logger.Errorf("Parent job dependency check failed for job %s: %v", j.JobID, err)
			if failErr := w.jobManager.FailJob(ctx, j.ID, fmt.Errorf("parent job dependency check failed: %w", err)); failErr != nil {
				w.logger.Errorf("Failed to mark job as failed: %v", failErr)
			}
			return
		}
		w.logger.Infof("Parent job dependency verified for job %s", j.JobID)
	}

	// Check if job has any work to do
	if j.Status == job.JobStatusNoWorkNeeded || len(j.Metadata.AffectedListeners) == 0 {
		w.logger.Infof("Job %s has no work needed", j.JobID)
		if err := w.jobManager.CompleteJob(ctx, j.ID, &job.ExecutionDetails{
			ProcessedSnapshots: []job.SnapshotExecution{},
		}); err != nil {
			w.logger.Errorf("Failed to complete job: %v", err)
		}
		return
	}

	listeners := j.Metadata.AffectedListeners
	listenerCount := len(listeners)
	batchSize := w.poolConfig.BatchSize

	allSnapshots := []job.SnapshotExecution{}
	completed := 0
	failed := 0
	totalExecutions := 0 // Will be calculated dynamically as we discover multi-client deployments

	// Process in batches
	for i := 0; i < listenerCount; i += batchSize {
		end := i + batchSize
		if end > listenerCount {
			end = listenerCount
		}

		batch := listeners[i:end]
		batchResults := w.processBatch(ctx, batch, j)

		// Update progress - count actual executions (may be > listeners due to multi-client)
		for _, result := range batchResults {
			allSnapshots = append(allSnapshots, result)
			totalExecutions++ // Count each execution
			if result.PokeStatus == job.PokeStatusSuccess {
				completed++
			} else {
				failed++
			}
		}

		// Update job progress - use actual execution count, not listener count
		progress := &job.JobProgress{
			Total:      totalExecutions,
			Completed:  completed,
			Failed:     failed,
			Percentage: float64(completed+failed) / float64(totalExecutions) * 100,
		}

		if err := w.jobManager.UpdateJobProgress(ctx, j.ID, progress); err != nil {
			w.logger.Errorf("Error updating job progress: %v", err)
		}
	}

	// Complete the job
	executionDetails := &job.ExecutionDetails{
		ProcessedSnapshots: allSnapshots,
	}

	if failed > 0 {
		if err := w.jobManager.FailJob(ctx, j.ID, fmt.Errorf("%d snapshots failed", failed)); err != nil {
			w.logger.Errorf("Failed to mark job as failed: %v", err)
		}
	} else {
		if err := w.jobManager.CompleteJob(ctx, j.ID, executionDetails); err != nil {
			w.logger.Errorf("Failed to complete job: %v", err)
		}
	}

	w.logger.Infof("Job %s completed: %d successful, %d failed", j.JobID, completed, failed)
}

// verifyParentJobCompleted verifies that a parent job has completed and its database writes are visible
// This prevents race conditions where child jobs (e.g., snapshot updates) start processing before
// parent jobs (e.g., ACME certificate verification) have committed their writes to MongoDB
func (w *Worker) verifyParentJobCompleted(ctx context.Context, childJob *job.Job) error {
	// 1. Load the parent job
	parentJob, err := w.jobManager.GetJob(ctx, childJob.ParentJobID.Hex())
	if err != nil {
		return fmt.Errorf("failed to load parent job: %w", err)
	}

	// 2. Verify parent job is completed
	if parentJob.Status != job.JobStatusCompleted {
		return fmt.Errorf("parent job %s is not completed (status: %s)", parentJob.JobID, parentJob.Status)
	}

	// 3. Verify parent job completed BEFORE this child job was created
	// This ensures the parent's completion timestamp is reliable
	if parentJob.CompletedAt == nil {
		return fmt.Errorf("parent job %s has no completion timestamp", parentJob.JobID)
	}

	if parentJob.CompletedAt.After(childJob.CreatedAt) {
		return fmt.Errorf("parent job %s completed AFTER child job was created (race condition)", parentJob.JobID)
	}

	// 4. For ACME-triggered snapshot jobs, verify the secret was actually updated
	if parentJob.Type == job.JobTypeACMEVerification && childJob.Metadata.SourceResource != nil {
		secretName := childJob.Metadata.SourceResource.Name
		version := childJob.Metadata.SourceResource.Version
		project := childJob.Metadata.SourceResource.ProjectID

		// Query secrets collection to verify the resource exists and was updated after parent job completion
		collection := w.dbContext.Client.Collection("secrets")

		var secret struct {
			General struct {
				UpdatedAt time.Time `bson:"updated_at"`
			} `bson:"general"`
		}

		err := collection.FindOne(ctx, map[string]any{
			"general.name":    secretName,
			"general.version": version,
			"general.project": project,
		}).Decode(&secret)
		if err != nil {
			return fmt.Errorf("secret %s (version: %s) not found in secrets collection: %w", secretName, version, err)
		}

		// Verify the secret was updated around the time of parent job completion
		// Allow 5 second window before parent completion (for clock skew) and require update within 30 seconds after
		minUpdateTime := parentJob.CompletedAt.Add(-5 * time.Second)
		maxUpdateTime := parentJob.CompletedAt.Add(30 * time.Second)

		if secret.General.UpdatedAt.Before(minUpdateTime) {
			return fmt.Errorf("secret %s was last updated at %s, which is before parent job completion at %s",
				secretName, secret.General.UpdatedAt.Format(time.RFC3339), parentJob.CompletedAt.Format(time.RFC3339))
		}

		if secret.General.UpdatedAt.After(maxUpdateTime) {
			w.logger.Warnf("Secret %s was updated at %s, which is suspiciously late after parent job completion at %s",
				secretName, secret.General.UpdatedAt.Format(time.RFC3339), parentJob.CompletedAt.Format(time.RFC3339))
			// Don't fail - just warn, as this might be a clock skew issue
		}

		w.logger.Debugf("Secret %s verified: updated at %s (parent job completed at %s)",
			secretName, secret.General.UpdatedAt.Format(time.RFC3339), parentJob.CompletedAt.Format(time.RFC3339))
	}

	// 5. All checks passed
	return nil
}

// processBatch processes a batch of listeners
func (w *Worker) processBatch(ctx context.Context, batch []string, j *job.Job) []job.SnapshotExecution {
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, w.poolConfig.MaxConcurrentPokes)
	// Pre-allocate results slice - will expand as we discover multi-client deployments
	results := make([]job.SnapshotExecution, 0, len(batch))
	resultsMutex := sync.Mutex{}

	for _, listenerName := range batch {
		wg.Add(1)
		go func(ln string) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// Get ALL clients for this listener
			clients := w.getAllClientsForListener(ln, j.Project, j.Version)

			// If no clients (unmanaged listener), send single poke
			if len(clients) == 0 {
				nodeID := fmt.Sprintf("%s::%s", ln, j.Project)
				execution := w.executeSinglePoke(ctx, nodeID, j.Project, j.Version, ln, "")
				resultsMutex.Lock()
				results = append(results, execution)
				resultsMutex.Unlock()
				return
			}

			// IMPORTANT: Filter duplicate downstream addresses
			// Scenario: Same listener deployed to multiple clients with SAME IP
			// Example: Client A and B both have IP 10.55.34.123
			// We should only send ONE poke to 10.55.34.123, not two
			uniqueDownstreamAddresses := make(map[string]bool)
			uniqueClients := []models.ServiceClients{}

			for _, client := range clients {
				if client.DownstreamAddress != "" && !uniqueDownstreamAddresses[client.DownstreamAddress] {
					uniqueDownstreamAddresses[client.DownstreamAddress] = true
					uniqueClients = append(uniqueClients, client)
				}
			}

			// For managed listeners, send poke to EACH UNIQUE downstream address
			if len(clients) != len(uniqueClients) {
				w.logger.Infof("MULTI-CLIENT: Listener '%s' has %d clients, but only %d unique IPs (filtered %d duplicates)",
					ln, len(clients), len(uniqueClients), len(clients)-len(uniqueClients))
			} else {
				w.logger.Infof("MULTI-CLIENT: Listener '%s' has %d clients, sending %d pokes", ln, len(clients), len(uniqueClients))
			}

			for i, client := range uniqueClients {
				nodeID := fmt.Sprintf("%s::%s::%s", ln, j.Project, client.DownstreamAddress)
				w.logger.Infof("MULTI-CLIENT: [%d/%d] Sending poke to %s", i+1, len(uniqueClients), nodeID)

				execution := w.executeSinglePoke(ctx, nodeID, j.Project, j.Version, ln, client.DownstreamAddress)

				resultsMutex.Lock()
				results = append(results, execution)
				resultsMutex.Unlock()
			}
		}(listenerName)
	}

	wg.Wait()
	return results
}

// executePokeViaRegistry executes poke using existing registry routing system
func (w *Worker) executePokeViaRegistry(ctx context.Context, nodeID, project, version, listenerName, downstreamAddress string) error {
	// Use the same bridgeClient.PokeNode that existing system uses
	// This automatically handles:
	// 1. Registry lookup for which control-plane serves this node
	// 2. gRPC request routing to appropriate control-plane
	// 3. Header-based routing through ext_proc filter

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if w.pokeService == nil {
		return fmt.Errorf("poke service not configured")
	}

	response, err := bridgeClient.PokeNode(ctx, *w.pokeService, listenerName, project, version, downstreamAddress)
	if err != nil {
		return fmt.Errorf("registry poke failed: %w", err)
	}

	if response == nil {
		return fmt.Errorf("nil response from control-plane")
	}

	// Type assert to get the actual PokeResponse
	if pokeResp, ok := response.(*bridge.PokeResponse); ok {
		w.logger.Debugf("Poke response for %s: %s", nodeID, pokeResp.Message)
	} else {
		w.logger.Debugf("Poke response for %s: success (no message)", nodeID)
	}
	return nil
}

// getAllClientsForListener returns ALL clients for a managed listener
// Returns empty slice if listener is unmanaged or has no clients
func (w *Worker) getAllClientsForListener(listenerName, project, version string) []models.ServiceClients {
	clients := services.FetchDownstreamAddressFromService(w.dbContext.Client, listenerName, project, version)
	return clients
}

// executeSinglePoke executes a single poke and returns the execution result
func (w *Worker) executeSinglePoke(ctx context.Context, nodeID, project, version, listenerName, downstreamAddress string) job.SnapshotExecution {
	execution := job.SnapshotExecution{
		NodeID:       nodeID,
		ListenerName: listenerName,
		PokeStatus:   job.PokeStatusPending,
		PokeSentAt:   time.Now(),
	}

	// Execute poke through existing system
	err := w.executePokeViaRegistry(ctx, nodeID, project, version, listenerName, downstreamAddress)
	if err != nil {
		execution.PokeStatus = job.PokeStatusFailed
		errorMsg := err.Error()
		execution.Error = &errorMsg
		w.logger.Errorf("Poke failed for listener %s (node: %s): %v", listenerName, nodeID, err)
	} else {
		execution.PokeStatus = job.PokeStatusSuccess
		w.logger.Debugf("Poke successful for listener %s (node: %s)", listenerName, nodeID)
	}

	return execution
}

// sendHeartbeat sends periodic heartbeats for a job
func (w *Worker) sendHeartbeat(ctx context.Context, jobID primitive.ObjectID) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.jobManager.UpdateJobHeartbeat(ctx, jobID); err != nil {
				w.logger.Errorf("Error sending heartbeat for job %s: %v", jobID.Hex(), err)
			}
		}
	}
}

// IsProcessing returns whether the worker is currently processing a job
func (w *Worker) IsProcessing() bool {
	w.processMutex.RLock()
	defer w.processMutex.RUnlock()
	return w.isProcessing
}

// GetLastActivity returns the last activity time
func (w *Worker) GetLastActivity() time.Time {
	w.processMutex.RLock()
	defer w.processMutex.RUnlock()
	return w.lastActivity
}

// setProcessing sets the processing state
func (w *Worker) setProcessing(processing bool) {
	w.processMutex.Lock()
	defer w.processMutex.Unlock()
	w.isProcessing = processing
	if processing {
		w.lastActivity = time.Now()
	}
}
