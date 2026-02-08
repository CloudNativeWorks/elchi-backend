package gslb

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

// contextKey is a custom type for context keys to avoid collisions
type contextKey string

// Context keys for storing values in ProbeResult.Context
const (
	taskContextKey        contextKey = "task"                 // ProbeTask
	manualRecordIDKey     contextKey = "manual_record_id"    // primitive.ObjectID
	manualHealthStateKey  contextKey = "manual_health_state" // models.HealthState
	isReprobeKey          contextKey = "is_reprobe"          // bool
	isWarningMonitorKey   contextKey = "is_warning_monitor"  // bool
	probeConfigChangedKey contextKey = "probe_config_changed" // bool - probe config changed since last load (triggers counter reset)
	cachedIPHealthKey     contextKey = "cached_ip_health"     // *models.GSLBIPHealth - batch-fetched IP health to avoid N+1 DB query
)

// WorkerPool manages a dedicated worker pool for Time Wheel scheduling
// This is a single shared pool used by Time Wheel for all probe execution
type WorkerPool struct {
	// Configuration
	interval   int // Interval identifier (for logging/monitoring)
	minWorkers int // CPU-aware minimum workers
	maxWorkers int // CPU-aware maximum workers

	// State
	currentWorkers int
	mu             sync.RWMutex

	// Queues
	probeQueue    *DynamicQueue[ProbeTask] // Dynamic queue for Time Wheel probes
	resultQueues  []chan ProbeResult       // Per-processor result queues (sharded by IP hash)
	numProcessors int                      // Number of result processors
	workerControl chan struct{}            // Signal to stop individual workers
	stopCh        chan struct{}            // Signal pool shutdown
	doneCh        chan struct{}            // Signal auto-scaler stopped
	stopOnce      sync.Once                // Ensures stopCh is closed only once

	// Probe executor (shared across Time Wheel)
	executor ProbeExecutor

	// Logger
	logger *logger.Logger

	// Worker tracking
	workerWg sync.WaitGroup

	// Auto-scaler
	autoScaler *AutoScaler

	// Stats (atomic counters for concurrent access)
	totalProbes    int64
	peakQueueDepth atomic.Int64
	scaleUpCount   atomic.Int64
	scaleDownCount atomic.Int64
}

// AutoScaler manages automatic worker scaling for the worker pool
type AutoScaler struct {
	// Completion target
	targetCompletionPercent float64 // Target: 80% completion time

	// Queue thresholds
	scaleUpThreshold   float64 // Queue depth % to trigger scale up (70%)
	scaleDownThreshold float64 // Queue depth % to trigger scale down (20%)

	// Rate limiting
	lastScaleAction  time.Time
	minScaleInterval time.Duration // Minimum time between scale actions (10s)

	// Emergency scaling
	emergencyScaleThreshold time.Duration // Queue full for this long triggers emergency (30s)
	lastEmergencyScale      time.Time
}

// NewWorkerPool creates a new dedicated worker pool for Time Wheel
//
// Parameters:
//   - interval: Interval identifier in seconds (for monitoring/logging)
//   - minWorkers: CPU-aware minimum worker count
//   - maxWorkers: CPU-aware maximum worker count
//   - queueSize: Probe queue buffer size (recommended: 10x maxWorkers)
//   - resultQueues: Per-processor result channels (sharded by IP hash)
//   - numProcessors: Number of result processors
//   - executor: ProbeExecutor implementation
//   - logger: Logger instance
func NewWorkerPool(
	interval int,
	minWorkers int,
	maxWorkers int,
	queueSize int,
	resultQueues []chan ProbeResult,
	numProcessors int,
	executor ProbeExecutor,
	logger *logger.Logger,
) *WorkerPool {
	// Calculate dynamic queue capacities
	initialCapacity := queueSize
	maxCapacity := maxWorkers * 50 // 50x max workers = generous headroom

	pool := &WorkerPool{
		interval:       interval,
		minWorkers:     minWorkers,
		maxWorkers:     maxWorkers,
		currentWorkers: 0,
		probeQueue:     NewDynamicQueue[ProbeTask](initialCapacity, maxCapacity),
		resultQueues:   resultQueues,
		numProcessors:  numProcessors,
		workerControl:  make(chan struct{}),
		stopCh:         make(chan struct{}),
		doneCh:         make(chan struct{}),
		executor:       executor,
		logger:         logger,
		autoScaler: &AutoScaler{
			targetCompletionPercent: 0.8, // 80% of interval
			scaleUpThreshold:        0.7, // 70% queue depth
			scaleDownThreshold:      0.2, // 20% queue depth
			minScaleInterval:        10 * time.Second,
			emergencyScaleThreshold: 30 * time.Second,
		},
	}

	// Start minimum workers
	pool.spawnWorkers(minWorkers)

	// Start auto-scaling monitor
	go pool.autoScaleMonitor()

	pool.logger.Debugf("Worker pool started for interval %ds (min: %d, max: %d, queue: %d)",
		interval, minWorkers, maxWorkers, queueSize)

	return pool
}

// Submit adds a probe task to the queue
// Now ALWAYS succeeds (grows queue if needed, up to maxCapacity)
func (bwp *WorkerPool) Submit(task ProbeTask) bool {
	err := bwp.probeQueue.Enqueue(task)
	if err != nil {
		// Only fails if queue closed (shutdown)
		bwp.logger.Warnf("Interval %ds submit failed (queue closed): %v", bwp.interval, err)
		return false
	}
	return true // SUCCESS - no more drops!
}

// spawnWorkers creates N new worker goroutines
func (bwp *WorkerPool) spawnWorkers(count int) {
	bwp.mu.Lock()
	defer bwp.mu.Unlock()

	for range count {
		bwp.workerWg.Add(1)
		go bwp.worker()
		bwp.currentWorkers++
	}

	bwp.logger.Debugf("Interval %ds spawned %d workers (total: %d)",
		bwp.interval, count, bwp.currentWorkers)
}

// worker is the main worker goroutine that processes probe tasks
func (bwp *WorkerPool) worker() {
	defer bwp.workerWg.Done()

	for {
		// Try to dequeue a task (blocks until available or queue closed)
		task, err := bwp.probeQueue.Dequeue()
		if err != nil {
			// Queue closed, exit gracefully
			return
		}

		// Check for shutdown signals (non-blocking)
		select {
		case <-bwp.workerControl:
			// Worker kill signal - re-enqueue task to prevent loss
			if enqErr := bwp.probeQueue.Enqueue(task); enqErr != nil {
				bwp.logger.Warnf("Interval %ds: failed to re-enqueue task during scale-down (queue closed): %v", bwp.interval, enqErr)
			}
			return
		case <-bwp.stopCh:
			// Pool shutdown signal - re-enqueue task so remaining workers can process
			if enqErr := bwp.probeQueue.Enqueue(task); enqErr != nil {
				bwp.logger.Debugf("Interval %ds: failed to re-enqueue task during shutdown (queue closed): %v", bwp.interval, enqErr)
			}
			return
		default:
			// Continue with task processing
		}

		// Validate task before processing
		if task.IPHealth == nil {
			bwp.logger.Errorf("Interval %ds: Received probe task with nil IPHealth, skipping", bwp.interval)
			continue
		}

		if task.IPHealth.IP == "" {
			bwp.logger.Errorf("Interval %ds: Received probe task with empty IP (RecordID: %s), skipping",
				bwp.interval, task.IPHealth.RecordID.Hex())
			continue
		}

		if task.Probe == nil {
			bwp.logger.Errorf("Interval %ds: Received probe task with nil Probe config for IP %s, skipping",
				bwp.interval, task.IPHealth.IP)
			continue
		}

		// Skip probe if disabled (probe config exists but is disabled)
		if !task.Probe.IsEnabled() {
			bwp.logger.Debugf("Skipping probe for IP %s (probe disabled)", task.IPHealth.IP)
			continue
		}

		// Execute probe with timeout context
		// If task has a context (Time Wheel with manual state, re-probe flags), use it as base
		baseCtx := context.Background()
		if task.Context != nil {
			baseCtx = task.Context
		}

		ctx, cancel := context.WithTimeout(baseCtx,
			time.Duration(task.Probe.Timeout*float64(time.Second)))

		result := bwp.executor.ExecuteProbe(ctx, task.IPHealth, task.Probe)
		cancel()

		// Attach task to result context (merge with existing context if present)
		if task.Context != nil {
			result.Context = context.WithValue(task.Context, taskContextKey, task)
		} else {
			result.Context = context.WithValue(context.Background(), taskContextKey, task)
		}

		ipHash := hashIP(result.IP)
		processorID := ipHash % bwp.numProcessors

		// Send result to the correct processor's queue
		select {
		case bwp.resultQueues[processorID] <- result:
			atomic.AddInt64(&bwp.totalProbes, 1)
		default:
			bwp.logger.Warnf("Result queue #%d full, dropping result for IP %s", processorID, result.IP)
		}
	}
}

// autoScaleMonitor monitors queue depth and triggers scaling actions
func (bwp *WorkerPool) autoScaleMonitor() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	defer close(bwp.doneCh)

	queueFullSince := time.Time{}

	for {
		select {
		case <-ticker.C:
			bwp.checkAndScale(&queueFullSince)

		case <-bwp.stopCh:
			return
		}
	}
}

// checkAndScale evaluates current state and triggers scaling if needed
func (bwp *WorkerPool) checkAndScale(queueFullSince *time.Time) {
	// Get queue stats
	queueStats := bwp.probeQueue.Stats()
	queueDepth := queueStats.CurrentSize
	queueCapacity := queueStats.CurrentCapacity

	bwp.mu.RLock()
	currentWorkers := bwp.currentWorkers
	bwp.mu.RUnlock()

	// Update peak queue depth (atomic - accessed from autoScaleMonitor and GetStats)
	if int64(queueDepth) > bwp.peakQueueDepth.Load() {
		bwp.peakQueueDepth.Store(int64(queueDepth))
	}

	// Calculate queue pressure (guard against division by zero)
	var queuePercent float64
	if queueCapacity > 0 {
		queuePercent = float64(queueDepth) / float64(queueCapacity)
	} else {
		// Queue capacity is zero (misconfiguration) - skip scaling
		bwp.logger.Warnf("Interval %ds: Queue capacity is 0, skipping auto-scale check", bwp.interval)
		return
	}

	// Track queue full duration for emergency scaling
	if queuePercent > 0.95 {
		if queueFullSince.IsZero() {
			*queueFullSince = time.Now()
		}
	} else {
		*queueFullSince = time.Time{} // Reset
	}

	// Emergency scaling: Queue full for >30s
	if !queueFullSince.IsZero() && time.Since(*queueFullSince) > bwp.autoScaler.emergencyScaleThreshold {
		if currentWorkers < bwp.maxWorkers {
			emergencyScaleAmount := max(currentWorkers/2, 10) // Add 50% more workers, minimum 10

			// Ensure we don't exceed maxWorkers
			emergencyScaleAmount = min(emergencyScaleAmount, bwp.maxWorkers-currentWorkers)

			// Only spawn if we can actually add workers
			if emergencyScaleAmount > 0 {
				bwp.logger.Warnf("Interval %ds EMERGENCY SCALE-UP: Queue full for %v, adding %d workers",
					bwp.interval, time.Since(*queueFullSince), emergencyScaleAmount)

				bwp.spawnWorkers(emergencyScaleAmount)
				bwp.autoScaler.lastEmergencyScale = time.Now()
				*queueFullSince = time.Time{} // Reset after emergency action
			}
			return
		}
	}

	// Rate limiting: Don't scale too frequently
	if time.Since(bwp.autoScaler.lastScaleAction) < bwp.autoScaler.minScaleInterval {
		return
	}

	// Scale up condition
	if queuePercent > bwp.autoScaler.scaleUpThreshold && currentWorkers < bwp.maxWorkers {
		scaleAmount := bwp.calculateScaleUpAmount(queueDepth, currentWorkers)
		if scaleAmount > 0 {
			bwp.spawnWorkers(scaleAmount)
			bwp.scaleUpCount.Add(1)
			bwp.autoScaler.lastScaleAction = time.Now()
		}
		return
	}

	// Scale down condition - check atomically under lock to prevent race
	if queuePercent < bwp.autoScaler.scaleDownThreshold {
		bwp.mu.Lock()
		// Re-check currentWorkers under lock (prevents TOCTOU race)
		if bwp.currentWorkers > bwp.minWorkers {
			scaleAmount := max(bwp.currentWorkers/10, 5) // Remove 10%, minimum 5

			// Additional guard: don't scale down below minWorkers
			scaleAmount = min(scaleAmount, bwp.currentWorkers-bwp.minWorkers)

			if scaleAmount > 0 {
				// Worker control channel is unbuffered, blocking under lock causes deadlock
				workersToKill := scaleAmount
				bwp.autoScaler.lastScaleAction = time.Now()

				// Release lock BEFORE sending kill signals
				bwp.mu.Unlock()

				// Now send kill signals without holding lock (non-blocking for deadlock prevention)
				// Only count successfully killed workers to maintain consistency
				actualKilled := 0
			killLoop:
				for i := range workersToKill {
					select {
					case bwp.workerControl <- struct{}{}:
						// Signal sent successfully
						actualKilled++
					case <-time.After(100 * time.Millisecond):
						// Timeout - worker might be busy, skip remaining signals
						bwp.logger.Debugf("Interval %ds: Timeout sending kill signal %d/%d, skipping rest", bwp.interval, i+1, workersToKill)
						break killLoop
					}
				}

				// Update worker count AFTER we know how many were actually killed
				if actualKilled > 0 {
					bwp.mu.Lock()
					bwp.currentWorkers -= actualKilled
					bwp.scaleDownCount.Add(1)
					bwp.logger.Debugf("Interval %ds killed %d workers (attempted: %d, total: %d)",
						bwp.interval, actualKilled, workersToKill, bwp.currentWorkers)
					bwp.mu.Unlock()
				}
				return // Already unlocked
			}
		}
		bwp.mu.Unlock()
	}
}

// calculateScaleUpAmount determines how many workers to add based on queue pressure
func (bwp *WorkerPool) calculateScaleUpAmount(queueDepth, currentWorkers int) int {
	// Scale by 20% of current workers OR 10% of queue depth, whichever is larger
	scaleByPercent := currentWorkers / 5 // 20%
	scaleByQueue := queueDepth / 10      // 10% of queue

	// Use max() for cleaner comparison (Go 1.21+)
	scaleAmount := max(scaleByPercent, scaleByQueue)

	// Ensure minimum scale increment
	scaleAmount = max(scaleAmount, 10)

	// Don't exceed max workers
	scaleAmount = min(scaleAmount, bwp.maxWorkers-currentWorkers)

	return scaleAmount
}

// WorkerPoolStats holds current pool statistics
type WorkerPoolStats struct {
	Interval       int
	CurrentWorkers int
	MinWorkers     int
	MaxWorkers     int
	QueueDepth     int
	QueueCapacity  int
	TotalProbes    int64
	PeakQueueDepth int
	ScaleUpCount   int64
	ScaleDownCount int64
}

func (bwp *WorkerPool) GetStats() WorkerPoolStats {
	bwp.mu.RLock()
	defer bwp.mu.RUnlock()

	queueStats := bwp.probeQueue.Stats()

	return WorkerPoolStats{
		Interval:       bwp.interval,
		CurrentWorkers: bwp.currentWorkers,
		MinWorkers:     bwp.minWorkers,
		MaxWorkers:     bwp.maxWorkers,
		QueueDepth:     queueStats.CurrentSize,
		QueueCapacity:  queueStats.CurrentCapacity,
		TotalProbes:    atomic.LoadInt64(&bwp.totalProbes),
		PeakQueueDepth: int(bwp.peakQueueDepth.Load()),
		ScaleUpCount:   bwp.scaleUpCount.Load(),
		ScaleDownCount: bwp.scaleDownCount.Load(),
	}
}

// Stop gracefully stops the worker pool
func (bwp *WorkerPool) Stop() {
	bwp.logger.Debugf("Stopping interval %ds worker pool...", bwp.interval)

	// Signal shutdown (signals auto-scaler and workers)
	// Use sync.Once to prevent panic from double-close
	bwp.stopOnce.Do(func() {
		close(bwp.stopCh)
	})

	// Wait for auto-scaler to stop
	<-bwp.doneCh

	// Close probe queue to unblock workers waiting on it
	// DynamicQueue.Close() will cause all Dequeue() calls to return ErrQueueClosed
	bwp.probeQueue.Close()

	// Log remaining items if any
	queueStats := bwp.probeQueue.Stats()
	if queueStats.CurrentSize > 0 {
		bwp.logger.Warnf("Interval %ds: Closing with %d remaining probes in queue",
			bwp.interval, queueStats.CurrentSize)
	} else {
		bwp.logger.Debugf("Interval %ds: Probe queue empty at shutdown", bwp.interval)
	}

	// Wait for all workers to finish
	bwp.workerWg.Wait()

	bwp.logger.Debugf("Interval %ds worker pool stopped", bwp.interval)
}

// GetQueueDepth returns current queue depth (for monitoring)
func (bwp *WorkerPool) GetQueueDepth() int {
	return bwp.probeQueue.Stats().CurrentSize
}

// GetCurrentWorkers returns current worker count (for monitoring)
func (bwp *WorkerPool) GetCurrentWorkers() int {
	bwp.mu.RLock()
	defer bwp.mu.RUnlock()
	return bwp.currentWorkers
}
