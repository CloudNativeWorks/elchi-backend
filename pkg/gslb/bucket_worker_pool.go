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
	taskContextKey          contextKey = "task"               // ProbeTask
	manualRecordIDKey       contextKey = "manual_record_id"   // primitive.ObjectID
	manualHealthStateKey    contextKey = "manual_health_state" // models.HealthState
	isReprobeKey            contextKey = "is_reprobe"         // bool
	isWarningMonitorKey     contextKey = "is_warning_monitor" // bool
)

// BucketWorkerPool manages a dedicated worker pool for a specific bucket
// Each bucket has its own isolated worker pool with CPU-aware limits
type BucketWorkerPool struct {
	// Configuration
	bucketInterval int // Parent bucket interval (for logging/monitoring)
	minWorkers     int // CPU-aware minimum workers
	maxWorkers     int // CPU-aware maximum workers

	// State
	currentWorkers int
	mu             sync.RWMutex

	// Channels
	probeQueue    chan ProbeTask   // Dedicated queue for this bucket
	resultQueue   chan ProbeResult // Shared result processor
	workerControl chan struct{}    // Signal to stop individual workers
	stopCh        chan struct{}    // Signal pool shutdown
	doneCh        chan struct{}    // Signal auto-scaler stopped
	stopOnce      sync.Once        // Ensures stopCh is closed only once

	// Probe executor (shared across all buckets)
	executor ProbeExecutor

	// Logger
	logger *logger.Logger

	// Worker tracking
	workerWg sync.WaitGroup

	// Auto-scaler
	autoScaler *BucketAutoScaler

	// Stats (atomic counters for concurrent access)
	totalProbes    int64
	peakQueueDepth int
	scaleUpCount   int64
	scaleDownCount int64
}

// BucketAutoScaler manages automatic worker scaling for a bucket
type BucketAutoScaler struct {
	// Completion target
	targetCompletionPercent float64 // Target: 80% of bucket interval

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

// NewBucketWorkerPool creates a new dedicated worker pool for a bucket
//
// Parameters:
//   - bucketInterval: The bucket's interval in seconds (for monitoring)
//   - minWorkers: CPU-aware minimum worker count
//   - maxWorkers: CPU-aware maximum worker count
//   - queueSize: Probe queue buffer size (recommended: 10x maxWorkers)
//   - resultQueue: Shared result channel (all buckets write here)
//   - executor: ProbeExecutor implementation
//   - logger: Logger instance
func NewBucketWorkerPool(
	bucketInterval int,
	minWorkers int,
	maxWorkers int,
	queueSize int,
	resultQueue chan ProbeResult,
	executor ProbeExecutor,
	logger *logger.Logger,
) *BucketWorkerPool {
	pool := &BucketWorkerPool{
		bucketInterval: bucketInterval,
		minWorkers:     minWorkers,
		maxWorkers:     maxWorkers,
		currentWorkers: 0,
		probeQueue:     make(chan ProbeTask, queueSize),
		resultQueue:    resultQueue,
		workerControl:  make(chan struct{}),
		stopCh:         make(chan struct{}),
		doneCh:         make(chan struct{}),
		executor:       executor,
		logger:         logger,
		autoScaler: &BucketAutoScaler{
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

	pool.logger.Debugf("Bucket %ds worker pool started (min: %d, max: %d, queue: %d)",
		bucketInterval, minWorkers, maxWorkers, queueSize)

	return pool
}

// Submit adds a probe task to the queue (non-blocking)
// Returns false if queue is full (probe dropped)
func (bwp *BucketWorkerPool) Submit(task ProbeTask) bool {
	select {
	case bwp.probeQueue <- task:
		return true
	default:
		bwp.logger.Warnf("Bucket %ds queue full, dropping probe for IP %s",
			bwp.bucketInterval, task.IPHealth.IP)
		return false
	}
}

// spawnWorkers creates N new worker goroutines
func (bwp *BucketWorkerPool) spawnWorkers(count int) {
	bwp.mu.Lock()
	defer bwp.mu.Unlock()

	for i := 0; i < count; i++ {
		bwp.workerWg.Add(1)
		go bwp.worker()
		bwp.currentWorkers++
	}

	bwp.logger.Debugf("Bucket %ds spawned %d workers (total: %d)",
		bwp.bucketInterval, count, bwp.currentWorkers)
}

// worker is the main worker goroutine that processes probe tasks
func (bwp *BucketWorkerPool) worker() {
	defer bwp.workerWg.Done()

	for {
		select {
		case task, ok := <-bwp.probeQueue:
			if !ok {
				// Channel closed, exit gracefully
				return
			}

			// Validate task before processing
			if task.IPHealth == nil {
				bwp.logger.Errorf("Bucket %ds: Received probe task with nil IPHealth, skipping", bwp.bucketInterval)
				continue
			}

			if task.IPHealth.IP == "" {
				bwp.logger.Errorf("Bucket %ds: Received probe task with empty IP (RecordID: %s), skipping",
					bwp.bucketInterval, task.IPHealth.RecordID.Hex())
				continue
			}

			if task.Probe == nil {
				bwp.logger.Errorf("Bucket %ds: Received probe task with nil Probe config for IP %s, skipping",
					bwp.bucketInterval, task.IPHealth.IP)
				continue
			}

			// Skip probe if disabled (probe config exists but is disabled)
			if !task.Probe.IsEnabled() {
				bwp.logger.Debugf("Skipping probe for IP %s (probe disabled)", task.IPHealth.IP)
				continue
			}

			// Execute probe with timeout context
			ctx, cancel := context.WithTimeout(context.Background(),
				time.Duration(task.Probe.Timeout*float64(time.Second)))
			result := bwp.executor.ExecuteProbe(ctx, task.IPHealth, task.Probe)
			cancel()

			// Attach task to result context
			result.Context = context.WithValue(context.Background(), taskContextKey, task)

			// Send result to shared result queue
			select {
			case bwp.resultQueue <- result:
				atomic.AddInt64(&bwp.totalProbes, 1)
			default:
				bwp.logger.Warnf("Result queue full, dropping result for IP %s", result.IP)
			}

		case <-bwp.workerControl:
			// Worker kill signal
			return

		case <-bwp.stopCh:
			// Pool shutdown signal
			return
		}
	}
}

// autoScaleMonitor monitors queue depth and triggers scaling actions
func (bwp *BucketWorkerPool) autoScaleMonitor() {
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
func (bwp *BucketWorkerPool) checkAndScale(queueFullSince *time.Time) {
	bwp.mu.RLock()
	queueDepth := len(bwp.probeQueue)
	queueCapacity := cap(bwp.probeQueue)
	currentWorkers := bwp.currentWorkers
	bwp.mu.RUnlock()

	// Update peak queue depth
	if queueDepth > bwp.peakQueueDepth {
		bwp.peakQueueDepth = queueDepth
	}

	// Calculate queue pressure (guard against division by zero)
	var queuePercent float64
	if queueCapacity > 0 {
		queuePercent = float64(queueDepth) / float64(queueCapacity)
	} else {
		// Queue capacity is zero (misconfiguration) - skip scaling
		bwp.logger.Warnf("Bucket %ds: Queue capacity is 0, skipping auto-scale check", bwp.bucketInterval)
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
			emergencyScaleAmount := currentWorkers / 2 // Add 50% more workers
			if emergencyScaleAmount < 10 {
				emergencyScaleAmount = 10
			}

			// Ensure we don't exceed maxWorkers
			if currentWorkers+emergencyScaleAmount > bwp.maxWorkers {
				emergencyScaleAmount = bwp.maxWorkers - currentWorkers
			}

			// Only spawn if we can actually add workers
			if emergencyScaleAmount > 0 {
				bwp.logger.Warnf("⚠️ Bucket %ds EMERGENCY SCALE-UP: Queue full for %v, adding %d workers",
					bwp.bucketInterval, time.Since(*queueFullSince), emergencyScaleAmount)

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
			bwp.scaleUpCount++
			bwp.autoScaler.lastScaleAction = time.Now()
		}
		return
	}

	// Scale down condition - check atomically under lock to prevent race
	if queuePercent < bwp.autoScaler.scaleDownThreshold {
		bwp.mu.Lock()
		// Re-check currentWorkers under lock (prevents TOCTOU race)
		if bwp.currentWorkers > bwp.minWorkers {
			scaleAmount := bwp.currentWorkers / 10 // Remove 10%
			if scaleAmount < 5 {
				scaleAmount = 5 // Minimum scale-down increment
			}

			// Additional guard: don't scale down below minWorkers
			if scaleAmount > bwp.currentWorkers-bwp.minWorkers {
				scaleAmount = bwp.currentWorkers - bwp.minWorkers
			}

			if scaleAmount > 0 {
				// Kill workers (already updates currentWorkers atomically)
				for i := 0; i < scaleAmount; i++ {
					bwp.workerControl <- struct{}{}
					bwp.currentWorkers--
				}

				bwp.scaleDownCount++
				bwp.logger.Debugf("Bucket %ds killed %d workers (total: %d)",
					bwp.bucketInterval, scaleAmount, bwp.currentWorkers)
				bwp.autoScaler.lastScaleAction = time.Now()
			}
		}
		bwp.mu.Unlock()
	}
}

// calculateScaleUpAmount determines how many workers to add based on queue pressure
func (bwp *BucketWorkerPool) calculateScaleUpAmount(queueDepth, currentWorkers int) int {
	// Scale by 20% of current workers OR 10% of queue depth, whichever is larger
	scaleByPercent := currentWorkers / 5 // 20%
	scaleByQueue := queueDepth / 10      // 10% of queue

	scaleAmount := scaleByPercent
	if scaleByQueue > scaleAmount {
		scaleAmount = scaleByQueue
	}

	// Ensure minimum scale increment
	if scaleAmount < 10 {
		scaleAmount = 10
	}

	// Don't exceed max workers
	if currentWorkers+scaleAmount > bwp.maxWorkers {
		scaleAmount = bwp.maxWorkers - currentWorkers
	}

	return scaleAmount
}

// GetStats returns current pool statistics
type BucketWorkerPoolStats struct {
	BucketInterval int
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

func (bwp *BucketWorkerPool) GetStats() BucketWorkerPoolStats {
	bwp.mu.RLock()
	defer bwp.mu.RUnlock()

	return BucketWorkerPoolStats{
		BucketInterval: bwp.bucketInterval,
		CurrentWorkers: bwp.currentWorkers,
		MinWorkers:     bwp.minWorkers,
		MaxWorkers:     bwp.maxWorkers,
		QueueDepth:     len(bwp.probeQueue),
		QueueCapacity:  cap(bwp.probeQueue),
		TotalProbes:    atomic.LoadInt64(&bwp.totalProbes),
		PeakQueueDepth: bwp.peakQueueDepth,
		ScaleUpCount:   bwp.scaleUpCount,
		ScaleDownCount: bwp.scaleDownCount,
	}
}

// Stop gracefully stops the worker pool
func (bwp *BucketWorkerPool) Stop() {
	bwp.logger.Debugf("Stopping bucket %ds worker pool...", bwp.bucketInterval)

	// Signal shutdown (signals auto-scaler and workers)
	// Use sync.Once to prevent panic from double-close
	bwp.stopOnce.Do(func() {
		close(bwp.stopCh)
	})

	// Wait for auto-scaler to stop
	<-bwp.doneCh

	// Drain probe queue with timeout to prevent goroutine leak
	// Workers may be blocked writing to resultQueue if it's full
	drainTimeout := time.After(5 * time.Second)
	drained := false

drainLoop:
	for {
		select {
		case <-bwp.probeQueue:
			// Discard remaining probes
		case <-drainTimeout:
			remaining := len(bwp.probeQueue)
			if remaining > 0 {
				bwp.logger.Warnf("Bucket %ds: Drain timeout, dropping %d remaining probes", bwp.bucketInterval, remaining)
			}
			break drainLoop
		default:
			// Queue empty
			drained = true
			break drainLoop
		}
	}

	if drained {
		bwp.logger.Debugf("Bucket %ds: Probe queue drained successfully", bwp.bucketInterval)
	}

	// Close probe queue to unblock workers waiting on it
	// This ensures workers exit cleanly without hanging on channel reads
	close(bwp.probeQueue)

	// Wait for all workers to finish
	bwp.workerWg.Wait()

	bwp.logger.Debugf("Bucket %ds worker pool stopped", bwp.bucketInterval)
}

// GetQueueDepth returns current queue depth (for monitoring)
func (bwp *BucketWorkerPool) GetQueueDepth() int {
	return len(bwp.probeQueue)
}

// GetCurrentWorkers returns current worker count (for monitoring)
func (bwp *BucketWorkerPool) GetCurrentWorkers() int {
	bwp.mu.RLock()
	defer bwp.mu.RUnlock()
	return bwp.currentWorkers
}
