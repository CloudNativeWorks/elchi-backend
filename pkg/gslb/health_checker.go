package gslb

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/metrics"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// HealthChecker manages GSLB health checking with Time Wheel scheduler
type HealthChecker struct {
	ctx    context.Context
	cancel context.CancelFunc

	// Dependencies
	db              *mongo.Database
	logger          *logger.Logger
	shardManager    *ShardManager
	ipHealthManager *IPHealthManager
	writeBuffer     *WriteBuffer
	metricsPusher   *metrics.MetricsPusher

	// Time Wheel scheduler - per-IP scheduling with 1-second granularity
	timeWheel     *TimeWheel         // Linux kernel-style time wheel
	resultQueues  []chan ProbeResult // Per-processor result queues (sharded)
	numProcessors int                // Number of result processors
	workerPool    *WorkerPool        // Single shared worker pool
	executor      ProbeExecutor      // Probe executor for immediate re-probes

	// In-memory counters (NOT persisted to MongoDB)
	counterManager *CounterManager

	// Metrics tracking (atomic counters for concurrent access)
	probeSuccessCount int64    // Total successful probes
	probeFailureCount int64    // Total failed probes
	probeErrorCounts  sync.Map // Map[string]int64 - error type -> count

	// Latency tracking (for P50, P95, P99 calculation)
	probeLatencySum   int64 // Sum of all probe latencies in microseconds (for average)
	probeLatencyCount int64 // Total probe count (for average calculation)
	probeLatencyMin   int64 // Minimum latency in microseconds (atomic CAS)
	probeLatencyMax   int64 // Maximum latency in microseconds (atomic CAS)

	// Shutdown and goroutine tracking
	done chan struct{}
	wg   sync.WaitGroup // Tracks background goroutines for clean shutdown

	// State tracking (for idempotent Start())
	runningMu sync.RWMutex
	running   bool
}

// NewHealthChecker creates a new health checker instance with Time Wheel scheduler
func NewHealthChecker(
	appContext *db.AppContext,
	shardManager *ShardManager,
	ipHealthManager *IPHealthManager,
	writeBuffer *WriteBuffer,
	metricsPusher *metrics.MetricsPusher,
	logger *logger.Logger,
) *HealthChecker {
	ctx, cancel := context.WithCancel(context.Background())

	// Create CPU-aware configuration
	cpuConfig := NewCPUConfig()
	logger.Infof("CPU-aware config: %d cores detected", cpuConfig.GetNumCPU())

	// Create probe executor
	executor := NewDefaultProbeExecutor(logger)

	// This prevents Go channel round-robin distribution from breaking sharding
	numProcessors := 8
	resultQueues := make([]chan ProbeResult, numProcessors)
	for i := 0; i < numProcessors; i++ {
		resultQueues[i] = make(chan ProbeResult, 10000) // 10k capacity per processor
	}

	// Create single shared worker pool for Time Wheel
	// Workers will send results to the correct queue based on IP hash
	// Use max workers based on CPU count
	workerLimits := cpuConfig.GetWorkerLimits(10) // Use 10s as base interval
	workerPool := NewWorkerPool(
		1, // interval (not used in Time Wheel context)
		workerLimits.MinWorkers,
		workerLimits.MaxWorkers,
		workerLimits.MinWorkers*10, // queue size
		resultQueues,               // Pass all queues to worker pool
		numProcessors,              // Number of result processors
		executor,
		logger,
	)

	// Create Time Wheel
	timeWheel := NewTimeWheel(ctx, ipHealthManager, workerPool, logger)

	return &HealthChecker{
		ctx:             ctx,
		cancel:          cancel,
		db:              appContext.Client,
		logger:          logger,
		shardManager:    shardManager,
		ipHealthManager: ipHealthManager,
		writeBuffer:     writeBuffer,
		metricsPusher:   metricsPusher,
		timeWheel:       timeWheel,
		workerPool:      workerPool,
		executor:        executor,
		resultQueues:    resultQueues,
		numProcessors:   numProcessors,
		counterManager:  NewCounterManager(logger),
		done:            make(chan struct{}),
	}
}

// Start begins the health check loop with Time Wheel scheduler
// This method is idempotent - calling it multiple times has no effect if already running
func (hc *HealthChecker) Start() error {
	// Check if already running (idempotency)
	hc.runningMu.Lock()
	if hc.running {
		hc.runningMu.Unlock()
		hc.logger.Debugf("Health checker already running, skipping Start()")
		return nil
	}
	hc.running = true
	hc.runningMu.Unlock()

	hc.logger.Infof("Starting GSLB Health Checker (Time Wheel scheduler)")

	// Load records into Time Wheel
	ctx, cancel := context.WithTimeout(hc.ctx, 30*time.Second)
	shards := hc.shardManager.GetOwnedShards()

	// Collect all records from all intervals
	var allRecords []*models.GSLBRecord
	intervals := []int{10, 20, 30, 60, 90, 120, 180, 300}
	for _, interval := range intervals {
		records, err := hc.ipHealthManager.GetRecordsByShards(ctx, shards, interval)
		if err != nil {
			cancel()
			hc.logger.Errorf("Failed to load records for interval %ds: %v", interval, err)
			return fmt.Errorf("failed to load records: %w", err)
		}
		for i := range records {
			allRecords = append(allRecords, &records[i])
		}
	}
	cancel()

	if len(allRecords) == 0 {
		hc.logger.Infof("No GSLB records found, Time Wheel in standby mode")
	} else {
		hc.logger.Infof("Loading %d records into Time Wheel...", len(allRecords))
		if err := hc.timeWheel.LoadRecords(hc.ctx, allRecords); err != nil {
			return fmt.Errorf("failed to load records into Time Wheel: %w", err)
		}
	}

	// Start Time Wheel
	hc.timeWheel.Start()

	// Start multiple result processor goroutines (one per queue)
	// Each processor reads from its dedicated queue (pre-sharded by worker pool)
	for i := 0; i < hc.numProcessors; i++ {
		processorID := i
		hc.wg.Add(1)
		go func() {
			defer hc.wg.Done()
			hc.processResults(processorID)
		}()
	}
	hc.logger.Infof("Started %d result processors (pre-sharded queues)", hc.numProcessors)

	// Start periodic metrics pusher (system-level metrics every 30s)
	hc.startMetricsPusher()

	hc.logger.Infof("GSLB Health Checker started successfully")
	return nil
}

// Stop gracefully stops the health checker
func (hc *HealthChecker) Stop() {
	hc.logger.Infof("Stopping health checker...")

	// Mark as not running
	hc.runningMu.Lock()
	hc.running = false
	hc.runningMu.Unlock()

	// Cancel context (signals processResults to drain and exit)
	hc.cancel()

	// Stop Time Wheel
	hc.timeWheel.Stop()

	// Stop worker pool
	hc.workerPool.Stop()

	// Close all result queues (signals processResults goroutines to exit)
	for i := 0; i < hc.numProcessors; i++ {
		close(hc.resultQueues[i])
	}

	// Wait for all processResults() goroutines to finish draining
	hc.wg.Wait()

	// Flush write buffer
	hc.writeBuffer.FlushSync()

	// Stop counter manager cleanup goroutine (prevents memory leak)
	hc.counterManager.Stop()

	// Close probe executor (releases HTTP client pool connections)
	if hc.executor != nil {
		hc.executor.Close()
	}

	hc.logger.Infof("Health checker stopped")
}

// IsRunning returns true if the health checker is currently running
// Thread-safe check for use by System's shard acquisition listener
func (hc *HealthChecker) IsRunning() bool {
	hc.runningMu.RLock()
	defer hc.runningMu.RUnlock()
	return hc.running
}

// ReloadAllRecords forces immediate reload of all records from database into Time Wheel
// This should be called after GSLB record create/update/delete operations or shard rebalancing
// Fetches all records BEFORE clearing to prevent availability gap on DB errors
func (hc *HealthChecker) ReloadAllRecords() error {
	if hc.timeWheel == nil {
		return fmt.Errorf("time wheel not initialized")
	}

	// Fetch all GSLB records from all intervals BEFORE clearing
	// This prevents availability gap: if DB fails, time wheel keeps running with old data
	ctx, cancel := context.WithTimeout(hc.ctx, 30*time.Second)
	defer cancel()

	shards := hc.shardManager.GetOwnedShards()
	var allRecords []*models.GSLBRecord
	intervals := []int{10, 20, 30, 60, 90, 120, 180, 300}

	for _, interval := range intervals {
		records, err := hc.ipHealthManager.GetRecordsByShards(ctx, shards, interval)
		if err != nil {
			return fmt.Errorf("failed to load records for interval %ds: %w", interval, err)
		}
		for i := range records {
			allRecords = append(allRecords, &records[i])
		}
	}

	// Only clear AFTER successful fetch - prevents empty time wheel on DB errors
	hc.timeWheel.ClearAll()

	// Reload into Time Wheel
	return hc.timeWheel.LoadRecords(ctx, allRecords)
}

// processResults processes probe results from a dedicated queue
// No sharding check needed - workers already routed to correct queue
// Each processor reads from its own pre-sharded queue
func (hc *HealthChecker) processResults(processorID int) {
	hc.logger.Infof("Result processor #%d started (dedicated queue)", processorID)

	processed := 0

	// Read from this processor's dedicated queue
	// Do NOT check ctx.Done() inside the loop.
	// Rely on channel close for termination. This ensures all queued results
	// are processed before exit. Stop() sequence guarantees:
	// 1. cancel() stops Time Wheel and workers (no new sends)
	// 2. workerPool.Stop() waits for all workers to finish
	// 3. close(resultQueues[i]) terminates this for-range loop
	for result := range hc.resultQueues[processorID] {
		// Extract task from context (contains RecordIDs for fan-out)
		if result.Context == nil {
			hc.logger.Errorf("ProbeResult has nil Context for IP %s - skipping (worker bug)", result.IP)
			continue
		}

		task, ok := result.Context.Value(taskContextKey).(ProbeTask)
		if !ok {
			hc.logger.Errorf("ProbeResult missing task in context for IP %s - skipping", result.IP)
			continue
		}

		// Fan out result to ALL records using this IP+config
		for _, recordID := range task.RecordIDs {
			newState, consecutiveFailures, isWarningMonitor := hc.evaluateStatusChangeForRecord(result, recordID, task.Probe)

			// Reschedule task in Time Wheel based on new state
			if err := hc.timeWheel.HandleProbeResult(recordID, result.IP, newState, result.Success, consecutiveFailures, task.Probe, isWarningMonitor); err != nil {
				hc.logger.Warnf("Failed to reschedule %s (record: %s): %v", result.IP, recordID.Hex()[:8], err)
			}
		}

		processed++
	}

	hc.logger.Infof("Result processor #%d stopped (processed: %d)", processorID, processed)
}

// hashIP returns a consistent non-negative hash for an IP address using FNV-1a
// FNV-1a provides much better distribution than simple byte sum,
// especially for IPs in the same subnet (e.g., 10.0.1.1 vs 10.0.1.2)
func hashIP(ip string) int {
	// FNV-1a inline (avoids hash.Hash allocation)
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for i := 0; i < len(ip); i++ {
		h ^= uint64(ip[i])
		h *= prime64
	}
	// Mask sign bit to ensure non-negative result (negative modulo panics on slice index)
	return int(h & 0x7fffffffffffffff)
}

// evaluateStatusChangeForRecord evaluates probe result for a specific record and updates health state
// Per-record evaluation: same IP can have different health states across different records
// Implements tri-state health model with circuit breaker
// Returns: (newState, consecutiveFailures, isWarningMonitor)
func (hc *HealthChecker) evaluateStatusChangeForRecord(result ProbeResult, recordID primitive.ObjectID, probe *models.GSLBProbe) (models.HealthState, int, bool) {
	// Step 1: Get current IP health - prefer cached from batch fetch (avoids N+1 DB query)
	var ipHealth *models.GSLBIPHealth
	if cached, ok := result.Context.Value(cachedIPHealthKey).(*models.GSLBIPHealth); ok && cached != nil && cached.RecordID == recordID {
		ipHealth = cached
	} else {
		// Fallback to DB query (manual re-probe or different record in fan-out)
		var err error
		ipHealth, err = hc.getIPHealthFromDB(recordID, result.IP)
		if err != nil {
			hc.logger.Errorf("Failed to get IP health for %s: %v", result.IP, err)
			return models.HealthStateCritical, 0, false
		}
	}

	// Step 2: Validate probe configuration (prevents crashes from invalid config)
	if !hc.validateProbeConfig(probe, result.IP, ipHealth.RecordID) {
		return ipHealth.HealthState, 0, false
	}

	// Step 2.5: Track probe metrics (success/failure, error types, latency)
	hc.trackProbeMetrics(result)

	// If probe config changed (detected by executeCurrentSlot reading fresh config from DB),
	// reset the in-memory counter to start fresh. Without this, stale ConsecutiveFailures from the old
	// probe config would immediately trigger backoff even though the config was just changed.
	if configChanged, ok := result.Context.Value(probeConfigChangedKey).(bool); ok && configChanged {
		hc.counterManager.Reset(recordID, result.IP)
		hc.logger.Infof("Probe config changed, counter reset for %s (record: %s)", result.IP, recordID.Hex()[:8])
	}

	var oldState models.HealthState
	manualRecordID, hasManualRecord := result.Context.Value(manualRecordIDKey).(primitive.ObjectID)
	manualState, hasManualState := result.Context.Value(manualHealthStateKey).(models.HealthState)

	if hasManualState && hasManualRecord && manualRecordID == recordID {
		oldState = manualState
		hc.logger.Debugf("Using manual health state for comparison: %s (record: %s)",
			manualState.String(), recordID.Hex())
	} else {
		// Normal probe OR different record sharing same IP - use counter state
		oldState = hc.getCurrentStateFromCounter(recordID, result.IP, ipHealth, probe)
	}

	newState, consecutiveFailures, consecutiveSuccesses, wasManualReset := hc.evaluateHealthState(
		ipHealth, result, recordID, probe,
	)

	// Step 4: Handle state transitions
	stateChanged := oldState != newState

	// Debug log for manual reset scenario
	if wasManualReset {
		hc.logger.Debugf("Manual reset processing: RecordID=%s, IP=%s, oldState=%s, newState=%s, stateChanged=%v, failures=%d, successes=%d",
			recordID.Hex(), result.IP, oldState, newState, stateChanged, consecutiveFailures, consecutiveSuccesses)
	}

	// MANUAL RESET POST-PROCESSING:
	// Counter was already reset and probe result was processed
	// Now we need to:
	// 1. Clear manual_reset_at flag in DB (immediate flush)
	// 2. Continue processing - probe result should update state naturally
	// 3. This allows the system to transition from manual state based on actual probe results
	if wasManualReset {
		// Clear manual_reset_at AND backoff fields to prevent repeated detection and enable immediate probing
		// Also clear BackoffUntil so IP isn't stuck in backoff from previous critical state
		// IMPORTANT: Use IMMEDIATE flush, not batched write
		// Reason: If we use batched write, immediate re-probe will see stale manual_reset_at
		update := HealthStateUpdate{
			RecordID:         recordID,
			IP:               result.IP,
			Timestamp:        time.Now(),
			ClearManualReset: true,        // Signal to write buffer to unset manual_reset_at
			BackoffUntil:     time.Time{}, // Clear backoff (zero time = no backoff)
			CurrentBackoff:   0,           // Reset backoff duration to 0
		}

		// IMMEDIATE flush to ensure next probe (including immediate re-probe) sees cleared flags
		// Use hc.ctx for proper shutdown propagation
		if err := hc.writeBuffer.FlushImmediate(hc.ctx, update); err != nil {
			hc.logger.Errorf("Failed to clear manual_reset_at and backoff for IP %s: %v", result.IP, err)
		}

		// Continue processing regardless of state change
		// This ensures probe results update the state naturally after manual reset
		hc.logger.Debugf("Manual reset: Cleared reset flag and backoff, continuing with probe result processing (oldState=%s, newState=%s, changed=%v)",
			oldState, newState, stateChanged)
	}

	// Determine if this is a WARNING monitor (continuous re-probe)
	isWarningMonitor := newState == models.HealthStateWarning && !result.Success

	if newState == models.HealthStateCritical && !result.Success {
		// Critical state: Handle backoff logic (may update even if state unchanged)
		hc.handleCriticalStateBackoff(ipHealth, result, oldState, newState, stateChanged, consecutiveFailures, probe)
		return newState, consecutiveFailures, false
	}

	// Step 5: Handle non-critical state transitions
	// CRITICAL: Always call handleNonCriticalStateTransition() for WARNING state failures
	// This ensures continuous re-probe until CRITICAL or recovery
	if stateChanged {
		hc.handleNonCriticalStateTransition(ipHealth, result, oldState, newState, consecutiveFailures, consecutiveSuccesses)
	} else if (newState == models.HealthStateWarning && !result.Success) || wasManualReset {
		// Even if state didn't change, we need to update in these cases:
		// 1. WARNING state with probe failure - triggers continuous re-probe
		// 2. Manual reset - probe result must be recorded even if state unchanged
		// 3. PASSING state with probe failure after manual reset - counter incremented
		hc.handleNonCriticalStateTransition(ipHealth, result, oldState, newState, consecutiveFailures, consecutiveSuccesses)
	}

	return newState, consecutiveFailures, isWarningMonitor
}

// getIPHealthFromDB retrieves IP health record from MongoDB
func (hc *HealthChecker) getIPHealthFromDB(recordID primitive.ObjectID, ip string) (*models.GSLBIPHealth, error) {
	ctx, cancel := context.WithTimeout(hc.ctx, 5*time.Second)
	defer cancel()

	collection := hc.db.Collection("gslb_ip_health")
	var ipHealth models.GSLBIPHealth

	err := collection.FindOne(ctx, bson.M{
		"record_id": recordID,
		"ip":        ip,
	}).Decode(&ipHealth)

	return &ipHealth, err
}

// validateProbeConfig ensures probe configuration is valid before evaluation
func (hc *HealthChecker) validateProbeConfig(probe *models.GSLBProbe, ip string, recordID primitive.ObjectID) bool {
	if probe == nil || probe.WarningThreshold <= 0 || probe.CriticalThreshold <= 0 {
		hc.logger.Errorf("IP %s has no valid probe configuration (record_id: %s) - skipping evaluation",
			ip, recordID.Hex())
		return false
	}
	return true
}

// evaluateHealthState updates counters and determines new health state
// Returns: (newState, consecutiveFailures, consecutiveSuccesses, wasManualReset)
func (hc *HealthChecker) evaluateHealthState(
	ipHealth *models.GSLBIPHealth,
	result ProbeResult,
	recordID primitive.ObjectID,
	probe *models.GSLBProbe,
) (newState models.HealthState, consecutiveFailures, consecutiveSuccesses int, wasManualReset bool) {
	// Check if THIS SPECIFIC RECORD was manually reset
	// When same IP is used in multiple records, only reset counter for the record admin changed
	// Other records sharing this IP should keep their existing counter state
	var manualResetAt time.Time
	var backoffInfo BackoffInfo

	manualRecordID, hasManualRecord := result.Context.Value(manualRecordIDKey).(primitive.ObjectID)
	if hasManualRecord && manualRecordID == recordID {
		// This is THE record that admin manually changed - use its manual_reset_at
		manualResetAt = ipHealth.ManualResetAt

		// If manual reset exists, also clear backoff info
		// This prevents using stale 80s backoff from previous CRITICAL state
		// Manual PASS should start fresh: no backoff, no failures
		if !manualResetAt.IsZero() {
			backoffInfo = BackoffInfo{
				CurrentBackoff: 0,
				BackoffUntil:   time.Time{}, // Zero time = no backoff
			}
		} else {
			// No manual reset - use current backoff
			backoffInfo = BackoffInfo{
				CurrentBackoff: ipHealth.CurrentBackoff,
				BackoffUntil:   ipHealth.BackoffUntil,
			}
		}
	} else {
		// Different record sharing same IP - use current backoff
		backoffInfo = BackoffInfo{
			CurrentBackoff: ipHealth.CurrentBackoff,
			BackoffUntil:   ipHealth.BackoffUntil,
		}
	}
	// else: Different record sharing same IP - don't trigger manual reset logic

	// Get or initialize counter
	_, _, wasManualReset = hc.counterManager.GetOrInitialize(
		recordID,
		result.IP,
		ipHealth.HealthState,
		probe,
		backoffInfo,   // Use cleared backoff for manual reset, current backoff otherwise
		manualResetAt, // Only pass manual_reset_at for the specific record admin changed
	)

	// MANUAL RESET HANDLING:
	// When manual reset is detected:
	// 1. Counter is already reset to 0 by CounterManager.GetOrInitialize()
	// 2. We process the current probe result normally (update counter)
	// 3. We skip database write to prevent overwriting admin's manual state
	//
	// This ensures:
	// - First probe after reset: Counter goes 0 -> 1 (if failed)
	// - State transition happens on second failure (WARNING -> CRITICAL with threshold=2)
	// - Admin's manual state persists until natural state transition occurs

	// Normal flow: Update counter based on probe result
	consecutiveFailures, consecutiveSuccesses = hc.counterManager.Update(recordID, result.IP, result.Success)

	// Determine new health state with RECOVERY support
	// Uses passing_threshold for anti-flapping (requires multiple successes to become PASSING)
	newState = models.DetermineHealthStateWithRecovery(consecutiveFailures, consecutiveSuccesses, probe, ipHealth.HealthState)

	return newState, consecutiveFailures, consecutiveSuccesses, wasManualReset
}

// handleCriticalStateBackoff handles circuit breaker logic for critical IPs
func (hc *HealthChecker) handleCriticalStateBackoff(
	ipHealth *models.GSLBIPHealth,
	result ProbeResult,
	oldState, newState models.HealthState,
	stateChanged bool,
	consecutiveFailures int,
	probe *models.GSLBProbe,
) {
	// Update state BEFORE SetBackoff (SetBackoff uses ipHealth.HealthState)
	ipHealth.HealthState = newState
	if stateChanged {
		ipHealth.LastStatusChange = time.Now()
	}

	// Apply adaptive backoff based on probe interval
	oldBackoff := ipHealth.CurrentBackoff
	ipHealth.SetBackoff(consecutiveFailures, probe.CriticalThreshold, probe.Interval)

	// Log backoff changes on state transitions
	if stateChanged && oldBackoff != ipHealth.CurrentBackoff {
		hc.logger.Debugf("Backoff updated for %s: %ds -> %ds (failures: %d, interval: %ds, state: %s->%s)",
			result.IP, oldBackoff, ipHealth.CurrentBackoff, consecutiveFailures, probe.Interval, oldState, newState)
	}

	ipHealth.UpdatedAt = time.Now()

	// Create update for immediate write
	update := hc.buildHealthStateUpdate(ipHealth, result, newState)

	// Log state transition
	if stateChanged {
		hc.logger.Infof("State transition: %s %s -> %s (failures: %d) - Circuit breaker activated: backoff %ds until %v",
			result.IP, oldState, newState, consecutiveFailures, ipHealth.CurrentBackoff, ipHealth.BackoffUntil.Format("15:04:05"))
		hc.pushStateTransitionMetric(oldState, newState)
	}

	// Always immediate write for backoff updates (prevents duplicate probes)
	if err := hc.writeBuffer.FlushImmediate(hc.ctx, update); err != nil {
		hc.logger.Errorf("Immediate write failed for IP %s: %v", update.IP, err)
	}
}

// handleNonCriticalStateTransition handles state changes for warning/passing states
func (hc *HealthChecker) handleNonCriticalStateTransition(
	ipHealth *models.GSLBIPHealth,
	result ProbeResult,
	oldState, newState models.HealthState,
	consecutiveFailures, consecutiveSuccesses int,
) {
	// Log state transition
	hc.logger.Infof("State transition: %s %s -> %s (failures: %d, successes: %d)",
		result.IP, oldState, newState, consecutiveFailures, consecutiveSuccesses)
	hc.pushStateTransitionMetric(oldState, newState)

	// NOTE: All automatic WARNING state scheduling is now handled by Time Wheel with interval/2
	// No immediate re-probe needed for automatic PASSING -> WARNING transition
	// Time Wheel reschedules WARNING IPs at half interval (e.g., 5s for 10s interval)
	// This gives endpoints time to recover instead of aggressive 100ms probing
	//
	// IMPORTANT: Manual PASSING changes still trigger immediate re-probe via
	// TriggerImmediateReProbeForManualChange() which is called from API handler

	// Check if database write is needed
	shouldWrite, isImmediate := models.ShouldWriteToDatabase(oldState, newState)

	// WARNING monitoring probes MUST write to DB for race condition prevention
	// Problem: WARNING->WARNING transitions return shouldWrite=false from ShouldWriteToDatabase()
	// This causes monitorWarningState() DB check to read stale state even after CRITICAL transition
	// Solution: Force write for WARNING monitoring probes to ensure DB is updated before next check
	isWarningMonitorProbe := false
	if result.Context != nil {
		if val, ok := result.Context.Value(isWarningMonitorKey).(bool); ok {
			isWarningMonitorProbe = val
		}
	}

	if isWarningMonitorProbe {
		shouldWrite = true
		isImmediate = true // MUST be immediate for monitorWarningState() DB check to see fresh state
		hc.logger.Debugf("Forcing immediate write for WARNING monitoring probe: %s (state: %s)",
			result.IP, newState)
	}

	if !shouldWrite {
		return
	}

	// Update health state
	ipHealth.HealthState = newState
	ipHealth.LastStatusChange = time.Now()
	ipHealth.UpdatedAt = time.Now()

	// Reset backoff when transitioning away from critical
	if newState != models.HealthStateCritical {
		ipHealth.ResetBackoff()
	}

	// Create update
	update := hc.buildHealthStateUpdate(ipHealth, result, newState)
	update.LastStatusChange = ipHealth.LastStatusChange

	// Write to database (immediate or buffered)
	if isImmediate {
		// Use hc.ctx for proper shutdown propagation
		if err := hc.writeBuffer.FlushImmediate(hc.ctx, update); err != nil {
			hc.logger.Errorf("Immediate write failed for IP %s: %v", update.IP, err)
		}
	} else {
		hc.writeBuffer.Add(update)
	}
}

// TriggerImmediateReProbeForManualChange triggers an immediate re-probe for a manually changed IP
// This is called from the API handler after a manual state change to verify actual health status
// Prevents manually-changed IPs from staying in incorrect state until next scheduled probe
//
// Parameters:
//   - manualHealthState: The health state that was manually set via API (NOT read from MongoDB)
//     This is critical to avoid race condition where MongoDB read returns stale state
func (hc *HealthChecker) TriggerImmediateReProbeForManualChange(ctx context.Context, recordID primitive.ObjectID, ip string, manualHealthState models.HealthState) error {
	// Fetch the GSLB record to get probe configuration
	collection := hc.db.Collection("gslb_records")
	var record models.GSLBRecord
	if err := collection.FindOne(ctx, bson.M{"_id": recordID}).Decode(&record); err != nil {
		return fmt.Errorf("failed to get GSLB record: %w", err)
	}

	// Verify probe is enabled
	if record.Probe == nil || !record.Probe.IsEnabled() {
		hc.logger.Debugf("Skipping manual re-probe for %s: probe disabled", ip)
		return nil
	}

	// Build minimal IPHealth struct for probe execution
	// We DON'T read from MongoDB to avoid race condition with manual state write
	// The probe only needs IP, RecordID, and FQDN - health state comes from manualHealthState parameter
	ipHealth := models.GSLBIPHealth{
		RecordID:    recordID,
		FQDN:        record.FQDN,
		IP:          ip,
		HealthState: manualHealthState, // Use the state from API parameter, NOT MongoDB
	}

	// Manual re-probe should ONLY affect the specific record admin changed
	// Do NOT fan-out to other records sharing this IP - they should maintain their own state
	// Each record is independent and follows its own probe schedule

	// Execute probe with timeout from probe config
	probeCtx, cancel := context.WithTimeout(hc.ctx,
		time.Duration(record.Probe.Timeout*float64(time.Second)))
	defer cancel()

	// Execute the probe using the same executor as Time Wheel probes
	result := hc.executor.ExecuteProbe(probeCtx, &ipHealth, record.Probe)

	// Log probe execution
	if !result.Success && result.Error != nil {
		hc.logger.Infof("Manual re-probe executed for %s (record: %s): FAILED (response_code: %d, response_time: %.3fs, error: %v)",
			ip, recordID.Hex()[:8], result.ResponseCode, result.ResponseTime, result.Error)
	} else {
		hc.logger.Infof("Manual re-probe executed for %s (record: %s): SUCCESS (response_code: %d, response_time: %.3fs)",
			ip, recordID.Hex()[:8], result.ResponseCode, result.ResponseTime)
	}

	// Create task context with ONLY the manually changed record
	task := ProbeTask{
		IPHealth:  &ipHealth,
		Probe:     record.Probe,
		RecordIDs: []primitive.ObjectID{recordID}, // ONLY this record
	}

	// Add manual health state to context for the specific record that was manually changed
	resultCtx := context.WithValue(hc.ctx, taskContextKey, task)
	resultCtx = context.WithValue(resultCtx, manualRecordIDKey, recordID) // Track which record was manually changed
	resultCtx = context.WithValue(resultCtx, manualHealthStateKey, manualHealthState)
	resultCtx = context.WithValue(resultCtx, isReprobeKey, true)
	result.Context = resultCtx

	// ISOLATION: Process result ONLY for the manually changed record
	newState, consecutiveFailures, isWarningMonitor := hc.evaluateStatusChangeForRecord(result, recordID, record.Probe)

	// CRITICAL: Reschedule in Time Wheel after manual re-probe
	// This ensures the IP continues normal probing cycle
	if err := hc.timeWheel.HandleProbeResult(recordID, result.IP, newState, result.Success, consecutiveFailures, record.Probe, isWarningMonitor); err != nil {
		hc.logger.Warnf("Failed to reschedule %s in Time Wheel after manual re-probe: %v", result.IP, err)
	}

	// NOTE: WARNING state monitoring is now handled by Time Wheel with interval/2 scheduling
	// HandleProbeResult above already scheduled the next probe at interval/2 for WARNING state
	// No need for aggressive 200ms monitoring loop - Time Wheel provides proper pacing

	hc.logger.Infof("Manual re-probe completed for %s (record: %s)", ip, recordID.Hex()[:8])

	return nil
}

// buildHealthStateUpdate creates a HealthStateUpdate from current state
func (hc *HealthChecker) buildHealthStateUpdate(
	ipHealth *models.GSLBIPHealth,
	result ProbeResult,
	newState models.HealthState,
) HealthStateUpdate {
	// Extract error message for troubleshooting
	var errorMessage string
	if result.Error != nil {
		errorMessage = result.Error.Error()
	}

	return HealthStateUpdate{
		RecordID:       ipHealth.RecordID,
		IP:             result.IP,
		HealthState:    newState,
		BackoffUntil:   ipHealth.BackoffUntil,
		CurrentBackoff: ipHealth.CurrentBackoff,
		ResponseCode:   result.ResponseCode,
		ResponseTime:   result.ResponseTime,
		Timestamp:      time.Now(),
		ProbeType:      result.Probe.Type,
		ErrorMessage:   errorMessage,
	}
}

// HealthCheckerStats holds health checker statistics for monitoring
type HealthCheckerStats struct {
	OwnedShards      int
	TotalIPs         int
	HealthyIPs       int
	WarningIPs       int
	CriticalIPs      int
	BackoffActiveIPs int
	TimeWheelStats   TimeWheelStats  // Time Wheel scheduler stats
	WorkerPoolStats  WorkerPoolStats // Worker pool stats
	WriteBufferStats BufferStats
	ResultQueueDepth int // Shared result queue depth
	ResultQueueCap   int // Shared result queue capacity
}

// GetStats returns current health checker statistics
func (hc *HealthChecker) GetStats() (*HealthCheckerStats, error) {
	// Use hc.ctx as parent for proper cancellation
	ctx, cancel := context.WithTimeout(hc.ctx, 5*time.Second)
	defer cancel()

	// Calculate total queue depth and capacity across all processor queues
	totalQueueDepth := 0
	totalQueueCap := 0
	for i := 0; i < hc.numProcessors; i++ {
		totalQueueDepth += len(hc.resultQueues[i])
		totalQueueCap += cap(hc.resultQueues[i])
	}

	stats := &HealthCheckerStats{
		OwnedShards:      len(hc.shardManager.GetOwnedShards()),
		TimeWheelStats:   hc.timeWheel.Stats(),
		WorkerPoolStats:  hc.workerPool.GetStats(),
		WriteBufferStats: hc.writeBuffer.GetStats(),
		ResultQueueDepth: totalQueueDepth,
		ResultQueueCap:   totalQueueCap,
	}

	// Count IPs by health state
	collection := hc.db.Collection("gslb_ip_health")

	// Total IPs
	totalCount, err := collection.CountDocuments(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	stats.TotalIPs = int(totalCount)

	// Healthy IPs (passing + warning = non-critical)
	healthyCount, err := collection.CountDocuments(ctx, bson.M{
		"health_state": bson.M{"$ne": models.HealthStateCritical},
	})
	if err != nil {
		return nil, err
	}
	stats.HealthyIPs = int(healthyCount)

	// Warning IPs
	warningCount, err := collection.CountDocuments(ctx, bson.M{"health_state": models.HealthStateWarning})
	if err != nil {
		return nil, err
	}
	stats.WarningIPs = int(warningCount)

	// Critical IPs
	criticalCount, err := collection.CountDocuments(ctx, bson.M{"health_state": models.HealthStateCritical})
	if err != nil {
		return nil, err
	}
	stats.CriticalIPs = int(criticalCount)

	// Backoff active IPs
	backoffCount, err := collection.CountDocuments(ctx, bson.M{
		"backoff_until": bson.M{"$gt": time.Now()},
	})
	if err != nil {
		return nil, err
	}
	stats.BackoffActiveIPs = int(backoffCount)

	return stats, nil
}

// getCurrentStateFromCounter computes current health state from in-memory counter
// This avoids reading stale state from MongoDB during fast re-probe cycles
// Re-probes happen in ~100ms intervals, faster than write buffer flush (1s)
func (hc *HealthChecker) getCurrentStateFromCounter(
	recordID primitive.ObjectID,
	ip string,
	ipHealth *models.GSLBIPHealth,
	probe *models.GSLBProbe,
) models.HealthState {
	// Get counter (don't initialize, we're just reading)
	key := MakeIPKey(recordID, ip)
	hc.counterManager.mu.RLock()
	counter, exists := hc.counterManager.counters[key]
	hc.counterManager.mu.RUnlock()

	// No counter yet - use DB state (first probe)
	if !exists || counter == nil {
		return ipHealth.HealthState
	}

	if probe == nil {
		return ipHealth.HealthState // Fallback to DB
	}

	// Read counter fields under per-counter lock
	// Without this, concurrent Update() calls can modify these fields mid-read
	counter.mu.Lock()
	failures := counter.ConsecutiveFailures
	successes := counter.ConsecutiveSuccesses
	counter.mu.Unlock()

	// Use DetermineHealthStateWithRecovery for consistent state calculation
	// This ensures RECOVERY state is properly tracked when passing_threshold > 1
	// Without this, we'd always return PASSING when failures=0, missing RECOVERY state
	return models.DetermineHealthStateWithRecovery(
		failures,
		successes,
		probe,
		ipHealth.HealthState, // Current DB state for context (needed for RECOVERY logic)
	)
}

// trackProbeMetrics tracks probe success/failure and error types for metrics
// Called for every probe result - updates atomic counters
func (hc *HealthChecker) trackProbeMetrics(result ProbeResult) {
	if result.Success {
		atomic.AddInt64(&hc.probeSuccessCount, 1)
	} else {
		atomic.AddInt64(&hc.probeFailureCount, 1)

		// Track error type if available
		if result.Error != nil {
			errorType := categorizeProbeError(result.Error)

			// Log uncategorized errors for debugging (only first 10 to avoid spam)
			if errorType == "other" {
				val, _ := hc.probeErrorCounts.LoadOrStore("other", new(int64))
				currentCount := atomic.LoadInt64(val.(*int64))
				if currentCount < 10 {
					hc.logger.Warnf("Uncategorized probe error (IP: %s): %v", result.IP, result.Error)
				}
			}

			// Atomic increment for error type counter
			val, _ := hc.probeErrorCounts.LoadOrStore(errorType, new(int64))
			atomic.AddInt64(val.(*int64), 1)
		}
	}

	// Track probe latency (convert seconds to microseconds for precision)
	// Overflow protection: cap at 1,000 seconds (1 billion microseconds)
	// Normal probe timeouts are 10-30s, so 1,000s is a safe upper bound
	latencyMicros := int64(result.ResponseTime * 1_000_000)
	const maxLatencyMicros = 1_000_000_000 // 1,000 seconds in microseconds
	if latencyMicros > maxLatencyMicros {
		latencyMicros = maxLatencyMicros
		hc.logger.Warnf("Probe latency overflow detected for %s: capped at %ds", result.IP, maxLatencyMicros/1_000_000)
	}

	// Atomic add with overflow protection (sum + latency check)
	currentSum := atomic.LoadInt64(&hc.probeLatencySum)
	if currentSum > 0 && latencyMicros > (1<<63-1)-currentSum {
		// Would overflow - reset counters to prevent corruption
		hc.logger.Warnf("Latency sum overflow detected, resetting counters")
		atomic.StoreInt64(&hc.probeLatencySum, latencyMicros)
		atomic.StoreInt64(&hc.probeLatencyCount, 1)
	} else {
		atomic.AddInt64(&hc.probeLatencySum, latencyMicros)
		atomic.AddInt64(&hc.probeLatencyCount, 1)
	}

	// Update min/max latency (lock-free CAS loop - avoids mutex on hot path)
	for {
		currentMin := atomic.LoadInt64(&hc.probeLatencyMin)
		if currentMin != 0 && latencyMicros >= currentMin {
			break
		}
		if atomic.CompareAndSwapInt64(&hc.probeLatencyMin, currentMin, latencyMicros) {
			break
		}
	}
	for {
		currentMax := atomic.LoadInt64(&hc.probeLatencyMax)
		if latencyMicros <= currentMax {
			break
		}
		if atomic.CompareAndSwapInt64(&hc.probeLatencyMax, currentMax, latencyMicros) {
			break
		}
	}
}

// categorizeProbeError categorizes probe errors for metrics using Go standard patterns
// PERFORMANCE OPTIMIZED: Fast-path checks first, expensive string operations last
func categorizeProbeError(err error) string {
	if err == nil {
		return "unknown"
	}

	// FAST PATH 1: Context errors (most common, cheapest check)
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}

	// FAST PATH 2: Network timeout (very common)
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}

	// FAST PATH 3: DNS errors (common)
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsNotFound {
			return "dns_not_found"
		}
		return "dns_failure"
	}

	// FAST PATH 4: OpError with syscall (connection errors)
	var syscallErr *net.OpError
	if errors.As(err, &syscallErr) {
		// Check most common errors first
		if errors.Is(syscallErr.Err, syscall.ECONNREFUSED) {
			return "connection_refused"
		}
		if errors.Is(syscallErr.Err, syscall.ETIMEDOUT) {
			return "timeout"
		}
		if errors.Is(syscallErr.Err, syscall.ECONNRESET) {
			return "connection_reset"
		}
		if errors.Is(syscallErr.Err, syscall.ENETUNREACH) {
			return "network_unreachable"
		}
		if errors.Is(syscallErr.Err, syscall.EHOSTUNREACH) {
			return "host_unreachable"
		}
		if errors.Is(syscallErr.Err, syscall.EPIPE) {
			return "broken_pipe"
		}
	}

	// FAST PATH 5: URL errors (unwrap once, non-recursive for performance)
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return "timeout"
		}
		// Check wrapped error type (ONE level only - no recursion)
		if urlErr.Err != nil {
			// Quick check for common wrapped errors
			if errors.Is(urlErr.Err, syscall.ECONNREFUSED) {
				return "connection_refused"
			}
			if errors.Is(urlErr.Err, syscall.ETIMEDOUT) {
				return "timeout"
			}
		}
		// Don't recurse - too expensive
		return "url_error"
	}

	// SLOW PATH: String-based matching (ONLY if fast paths failed)
	// Get error string ONCE (allocation cost)
	errStr := err.Error()

	// Use simple byte scanning (faster than strings.Contains which allocates)
	// Check most common patterns first
	if len(errStr) > 0 {
		// Connection errors (most common in failed IPs)
		if strings.Contains(errStr, "connection refused") {
			return "connection_refused"
		}
		if strings.Contains(errStr, "timeout") || strings.Contains(errStr, "timed out") {
			return "timeout"
		}

		// HTTP errors (common)
		if strings.Contains(errStr, "unexpected status code") {
			return "http_status_mismatch"
		}
		if strings.Contains(errStr, "http request failed") {
			return "http_request_failed"
		}

		// TLS errors (less common)
		if strings.Contains(errStr, "tls") || strings.Contains(errStr, "certificate") {
			if strings.Contains(errStr, "handshake") {
				return "tls_handshake"
			}
			return "tls_certificate"
		}

		// TCP errors
		if strings.Contains(errStr, "tcp connection failed") {
			return "tcp_connection_failed"
		}
	}

	// Less common patterns (already checked above, but catch string variants)
	if strings.Contains(errStr, "eof") {
		return "connection_eof"
	}
	if strings.Contains(errStr, "no such host") {
		return "dns_not_found"
	}
	if strings.Contains(errStr, "i/o timeout") {
		return "timeout"
	}

	// If we still don't know, return "other" with error logged
	return "other"
}
