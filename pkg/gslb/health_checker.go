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

// HealthChecker manages GSLB health checking with bucket-based timer system
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

	// Bucket-based timer system
	bucketScheduler *BucketScheduler // Multi-bucket orchestrator
	resultQueue     chan ProbeResult // Shared result queue for all buckets

	// In-memory counters (NOT persisted to MongoDB)
	counterManager *CounterManager

	// Metrics tracking (atomic counters for concurrent access)
	probeSuccessCount int64    // Total successful probes
	probeFailureCount int64    // Total failed probes
	probeErrorCounts  sync.Map // Map[string]int64 - error type → count

	// Latency tracking (for P50, P95, P99 calculation)
	probeLatencySum   int64      // Sum of all probe latencies in microseconds (for average)
	probeLatencyCount int64      // Total probe count (for average calculation)
	probeLatencyMin   int64      // Minimum latency in microseconds (atomic)
	probeLatencyMax   int64      // Maximum latency in microseconds (atomic)
	probeLatencyMu    sync.Mutex // Protects min/max updates

	// Shutdown and goroutine tracking
	done chan struct{}
	wg   sync.WaitGroup // Tracks background goroutines for clean shutdown

	// State tracking (for idempotent Start())
	runningMu sync.RWMutex
	running   bool
}

// NewHealthChecker creates a new health checker instance with bucket-based timer system
func NewHealthChecker(
	appContext *db.AppContext,
	shardManager *ShardManager,
	ipHealthManager *IPHealthManager,
	writeBuffer *WriteBuffer,
	metricsPusher *metrics.MetricsPusher,
	logger *logger.Logger,
) *HealthChecker {
	ctx, cancel := context.WithCancel(context.Background())

	// Create shared result queue for all buckets
	resultQueue := make(chan ProbeResult, 20000)

	// Create CPU-aware configuration
	cpuConfig := NewCPUConfig()
	logger.Infof("CPU-aware config: %d cores detected", cpuConfig.GetNumCPU())

	// Create probe executor (shared across all bucket worker pools)
	executor := NewDefaultProbeExecutor(logger)

	// Create bucket scheduler with all timer buckets
	bucketScheduler := NewBucketScheduler(
		ipHealthManager,
		shardManager,
		cpuConfig,
		resultQueue,
		executor,
		metricsPusher,
		logger,
	)

	return &HealthChecker{
		ctx:             ctx,
		cancel:          cancel,
		db:              appContext.Client,
		logger:          logger,
		shardManager:    shardManager,
		ipHealthManager: ipHealthManager,
		writeBuffer:     writeBuffer,
		metricsPusher:   metricsPusher,
		bucketScheduler: bucketScheduler,
		resultQueue:     resultQueue,
		counterManager:  NewCounterManager(logger),
		done:            make(chan struct{}),
	}
}

// Start begins the health check loop with bucket-based timer system
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

	hc.logger.Infof("🚀 Starting GSLB Health Checker (bucket-based timer system)")

	// Start bucket scheduler (all timer buckets)
	if err := hc.bucketScheduler.Start(); err != nil {
		// Mark as not running on failure
		hc.runningMu.Lock()
		hc.running = false
		hc.runningMu.Unlock()

		// This is expected when no GSLB records exist yet
		hc.logger.Infof("⏸️  GSLB Health Checker in standby mode")
		return fmt.Errorf("failed to start bucket scheduler: %w", err)
	}

	// Start multiple result processor goroutines (sharded by IP hash)
	// PERFORMANCE FIX: 8 processors instead of 1 → 8x throughput
	// Each processor handles a shard of IPs to avoid race conditions
	numProcessors := 8
	for i := 0; i < numProcessors; i++ {
		processorID := i
		hc.wg.Add(1)
		go func() {
			defer hc.wg.Done()
			hc.processResultsSharded(processorID, numProcessors)
		}()
	}
	hc.logger.Infof("✅ Started %d result processors (sharded by IP hash)", numProcessors)

	// Start periodic metrics pusher (system-level metrics every 30s)
	hc.startMetricsPusher()

	hc.logger.Infof("✅ GSLB Health Checker started successfully")
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

	// Stop bucket scheduler (stops all timer buckets and worker pools)
	hc.bucketScheduler.Stop()

	// Close result queue (signals processResults channel is done)
	close(hc.resultQueue)

	// Wait for processResults() goroutine to finish draining
	hc.wg.Wait()

	// Flush write buffer
	hc.writeBuffer.FlushSync()

	hc.logger.Infof("✅ Health checker stopped")
}

// IsRunning returns true if the health checker is currently running
// Thread-safe check for use by System's shard acquisition listener
func (hc *HealthChecker) IsRunning() bool {
	hc.runningMu.RLock()
	defer hc.runningMu.RUnlock()
	return hc.running
}

// ReloadAllRecords forces immediate reload of all bucket records from database
// This should be called after GSLB record create/update/delete operations
// to ensure changes are immediately visible without waiting for periodic reload
func (hc *HealthChecker) ReloadAllRecords() error {
	if hc.bucketScheduler == nil {
		return fmt.Errorf("bucket scheduler not initialized")
	}
	return hc.bucketScheduler.ReloadAllBuckets()
}

// processResultsSharded processes probe results with IP-based sharding
// PERFORMANCE FIX: Multiple processors (8) instead of single processor
// Each processor handles a shard of IPs based on hash(IP) % numProcessors
// This avoids race conditions (same IP always processed by same goroutine)
func (hc *HealthChecker) processResultsSharded(processorID, numProcessors int) {
	hc.logger.Infof("Result processor #%d started (shard: %d/%d)", processorID, processorID, numProcessors)

	processed := 0
	skipped := 0

	for result := range hc.resultQueue {
		// Check if shutdown is requested
		select {
		case <-hc.ctx.Done():
			// Shutdown requested - log stats and exit
			hc.logger.Infof("Result processor #%d stopping (processed: %d, skipped: %d)", processorID, processed, skipped)
			return
		default:
			// Continue normal processing
		}

		// Shard by IP hash to avoid race conditions on same IP
		// Use simple hash: sum of IP bytes modulo numProcessors
		ipHash := hashIP(result.IP)
		if ipHash%numProcessors != processorID {
			skipped++
			continue // Not this processor's shard
		}

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
			hc.evaluateStatusChangeForRecord(result, recordID, task.Probe)
		}

		processed++
	}

	hc.logger.Infof("Result processor #%d stopped (processed: %d, skipped: %d)", processorID, processed, skipped)
}

// hashIP returns a consistent hash for an IP address
// Simple hash: sum all bytes of IP string
func hashIP(ip string) int {
	hash := 0
	for i := 0; i < len(ip); i++ {
		hash += int(ip[i])
	}
	return hash
}

// evaluateStatusChangeForRecord evaluates probe result for a specific record and updates health state
// Per-record evaluation: same IP can have different health states across different records
// Implements tri-state health model with circuit breaker
// evaluateStatusChangeForRecord orchestrates health state evaluation for a single probe result
// This is the main entry point that coordinates the evaluation workflow
func (hc *HealthChecker) evaluateStatusChangeForRecord(result ProbeResult, recordID primitive.ObjectID, probe *models.GSLBProbe) {
	// Step 1: Get current IP health from database
	ipHealth, err := hc.getIPHealthFromDB(recordID, result.IP)
	if err != nil {
		hc.logger.Errorf("Failed to get IP health for %s: %v", result.IP, err)
		return
	}

	// Step 2: Validate probe configuration (prevents crashes from invalid config)
	if !hc.validateProbeConfig(probe, result.IP, ipHealth.RecordID) {
		return
	}

	// Step 2.5: Track probe metrics (success/failure, error types, latency)
	hc.trackProbeMetrics(result)

	// Step 3: Update counters and evaluate new health state
	// ✅ FIX: Use in-memory counter state instead of DB state for re-probes
	// Re-probes happen in ~100ms intervals, faster than write buffer flush (1s)
	// Using DB state causes infinite loop because it reads stale "passing" state
	//
	// ✅ MANUAL RESET FIX: Check if this is a manual re-probe (has manual_health_state in context)
	// CRITICAL: Only use manual state for the SPECIFIC record that was manually changed
	// Other records sharing the same IP should use their own counter state
	var oldState models.HealthState
	manualRecordID, hasManualRecord := result.Context.Value(manualRecordIDKey).(primitive.ObjectID)
	manualState, hasManualState := result.Context.Value(manualHealthStateKey).(models.HealthState)

	if hasManualState && hasManualRecord && manualRecordID == recordID {
		// This is THE record that was manually changed - use the state admin set via API
		oldState = manualState
		hc.logger.Debugf("⚡ Using manual health state for comparison: %s (record: %s)",
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
		hc.logger.Debugf("⚡ Manual reset processing: RecordID=%s, IP=%s, oldState=%s, newState=%s, stateChanged=%v, failures=%d, successes=%d",
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
		// CRITICAL FIX: Also clear BackoffUntil so IP isn't stuck in backoff from previous critical state
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
		hc.logger.Debugf("⚡ Manual reset: Cleared reset flag and backoff, continuing with probe result processing (oldState=%s, newState=%s, changed=%v)",
			oldState, newState, stateChanged)
	}

	if newState == models.HealthStateCritical && !result.Success {
		// Critical state: Handle backoff logic (may update even if state unchanged)
		hc.handleCriticalStateBackoff(ipHealth, result, oldState, newState, stateChanged, consecutiveFailures, probe)
		return
	}

	// Step 5: Handle non-critical state transitions
	// CRITICAL: Always call handleNonCriticalStateTransition() for WARNING state failures
	// This ensures continuous re-probe until CRITICAL or recovery
	if stateChanged {
		hc.handleNonCriticalStateTransition(ipHealth, result, oldState, newState, consecutiveFailures, consecutiveSuccesses)
	} else if (newState == models.HealthStateWarning && !result.Success) || wasManualReset {
		// ⚡ CRITICAL FIX: Even if state didn't change, we need to update in these cases:
		// 1. WARNING state with probe failure - triggers continuous re-probe
		// 2. Manual reset - probe result must be recorded even if state unchanged
		// 3. PASSING state with probe failure after manual reset - counter incremented
		hc.handleNonCriticalStateTransition(ipHealth, result, oldState, newState, consecutiveFailures, consecutiveSuccesses)
	}
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
		hc.logger.Errorf("❌ IP %s has no valid probe configuration (record_id: %s) - skipping evaluation",
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
	// ✅ FAN-OUT FIX: Check if THIS SPECIFIC RECORD was manually reset
	// When same IP is used in multiple records, only reset counter for the record admin changed
	// Other records sharing this IP should keep their existing counter state
	var manualResetAt time.Time
	manualRecordID, hasManualRecord := result.Context.Value(manualRecordIDKey).(primitive.ObjectID)
	if hasManualRecord && manualRecordID == recordID {
		// This is THE record that admin manually changed - use its manual_reset_at
		manualResetAt = ipHealth.ManualResetAt
	}
	// else: Different record sharing same IP - don't trigger manual reset logic

	// Get or initialize counter
	_, _, wasManualReset = hc.counterManager.GetOrInitialize(
		recordID,
		result.IP,
		ipHealth.HealthState,
		probe,
		BackoffInfo{
			CurrentBackoff: ipHealth.CurrentBackoff,
			BackoffUntil:   ipHealth.BackoffUntil,
		},
		manualResetAt, // Only pass manual_reset_at for the specific record admin changed
	)

	// MANUAL RESET HANDLING:
	// When manual reset is detected:
	// 1. Counter is already reset to 0 by CounterManager.GetOrInitialize()
	// 2. We process the current probe result normally (update counter)
	// 3. We skip database write to prevent overwriting admin's manual state
	//
	// This ensures:
	// - First probe after reset: Counter goes 0 → 1 (if failed)
	// - State transition happens on second failure (WARNING → CRITICAL with threshold=2)
	// - Admin's manual state persists until natural state transition occurs

	// Normal flow: Update counter based on probe result
	consecutiveFailures, consecutiveSuccesses = hc.counterManager.Update(recordID, result.IP, result.Success)

	// Determine new health state with ONE-WAY PROGRESSION rule
	// Pass current state to prevent backwards transitions (CRITICAL→WARNING, WARNING→PASSING while failing)
	newState = models.DetermineHealthState(consecutiveFailures, probe, ipHealth.HealthState)

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
		hc.logger.Debugf("🔧 Backoff updated for %s: %ds → %ds (failures: %d, interval: %ds, state: %s→%s)",
			result.IP, oldBackoff, ipHealth.CurrentBackoff, consecutiveFailures, probe.Interval, oldState, newState)
	}

	ipHealth.UpdatedAt = time.Now()

	// Create update for immediate write
	update := hc.buildHealthStateUpdate(ipHealth, result, newState)

	// Log state transition
	if stateChanged {
		hc.logger.Infof("🔄 State transition: %s %s → %s (failures: %d) - Circuit breaker activated: backoff %ds until %v",
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
	hc.logger.Infof("🔄 State transition: %s %s → %s (failures: %d, successes: %d)",
		result.IP, oldState, newState, consecutiveFailures, consecutiveSuccesses)
	hc.pushStateTransitionMetric(oldState, newState)

	// ⚡ IMMEDIATE RE-PROBE: Check BEFORE database write logic
	// Trigger immediate re-probe in two scenarios:
	// 1. PASSING → WARNING transition (initial degradation detected)
	// 2. WARNING state with probe failure (continue monitoring until CRITICAL or recovery)
	//
	// CRITICAL: This check must happen BEFORE shouldWrite check, because WARNING → WARNING
	// returns shouldWrite=false, which would cause early return and skip re-probe!
	//
	// This ensures fast failover regardless of threshold configuration:
	// - warning_threshold=1, critical_threshold=2: 1 re-probe → CRITICAL
	// - warning_threshold=1, critical_threshold=8: 7 re-probes → CRITICAL
	// Result: WARNING state duration = ~100ms × (critical_threshold - warning_threshold)
	shouldReProbe := false
	var reProbeReason string

	// ✅ CRITICAL FIX: Check is_reprobe flag BEFORE all re-probe triggers
	// This prevents infinite re-probe loops across ALL state transitions
	isReProbe := false
	if result.Context != nil {
		if val, ok := result.Context.Value(isReprobeKey).(bool); ok {
			isReProbe = val
		}
	}

	if oldState == models.HealthStatePassing && newState == models.HealthStateWarning {
		// ✅ FIXED: Check flag to prevent re-probe loops on PASSING→WARNING
		if isReProbe {
			hc.logger.Debugf("⚡ Skipping re-probe for %s: PASSING→WARNING from re-probe", result.IP)
		} else {
			shouldReProbe = true
			reProbeReason = "PASSING → WARNING transition"
		}
	} else if newState == models.HealthStateWarning && !result.Success {
		// ✅ CRITICAL FIX: Check if this is from WARNING monitoring loop
		// WARNING monitoring loop probes should NOT trigger additional re-probes
		isWarningMonitor := false
		if result.Context != nil {
			if val, ok := result.Context.Value(isWarningMonitorKey).(bool); ok {
				isWarningMonitor = val
			}
		}

		// ✅ CRITICAL FIX: WARNING state needs continuous monitoring to detect CRITICAL
		// Only skip re-probe if BOTH: is_reprobe=true AND this is FIRST transition to WARNING
		// Allow re-probes if already in WARNING state (ongoing monitoring for CRITICAL detection)
		switch {
		case isReProbe && oldState == models.HealthStatePassing:
			// First re-probe after PASSING→WARNING, don't trigger another immediately
			hc.logger.Debugf("⚡ Skipping re-probe for %s: PASSING→WARNING from re-probe", result.IP)
		case isWarningMonitor:
			// This probe is from WARNING monitoring loop - don't trigger another re-probe
			hc.logger.Debugf("⚡ Skipping re-probe for %s: probe from WARNING monitoring loop", result.IP)
		default:
			// Either: not a re-probe, OR already was in WARNING (continue monitoring)
			shouldReProbe = true
			reProbeReason = "WARNING state failure (monitoring until CRITICAL or recovery)"
		}
	}

	if shouldReProbe {
		hc.logger.Infof("⚡ Triggering immediate re-probe for %s (%s)", result.IP, reProbeReason)

		// Validate probe config exists
		if result.Probe == nil {
			hc.logger.Warnf("Cannot re-probe %s: missing probe config in result", result.IP)
			return
		}

		// Schedule immediate re-probe (async to avoid blocking result processor)
		// Track goroutine with WaitGroup for graceful shutdown
		hc.wg.Add(1)
		go func() {
			defer hc.wg.Done()
			hc.executeImmediateReProbe(ipHealth, result)
		}()
	}

	// Check if database write is needed (after re-probe check!)
	shouldWrite, isImmediate := models.ShouldWriteToDatabase(oldState, newState)

	// ✅ CRITICAL FIX: WARNING monitoring probes MUST write to DB for race condition prevention
	// Problem: WARNING→WARNING transitions return shouldWrite=false from ShouldWriteToDatabase()
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
		hc.logger.Debugf("⚡ Forcing immediate write for WARNING monitoring probe: %s (state: %s)",
			result.IP, newState)
	}

	if !shouldWrite {
		return
	}

	// ✅ CRITICAL FIX: Force immediate write if re-probe is scheduled
	// Reason: Immediate re-probe will write next state in ~100ms, we must ensure this state
	// is persisted BEFORE that to avoid write order race condition
	// Without this, buffered write (5s delay) may arrive AFTER re-probe's immediate write,
	// causing DB state to revert (e.g., CRITICAL → WARNING override)
	if shouldReProbe && !isImmediate {
		isImmediate = true
		hc.logger.Debugf("⚡ Forcing immediate write for %s %s → %s (re-probe scheduled)",
			result.IP, oldState, newState)
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

// executeImmediateReProbe executes an immediate re-probe for WARNING IPs
// This replaces the periodic FastFail bucket approach with event-driven probing
//
// MONITORING STRATEGY (after manual health state change):
// 1. Manual PASSING → Immediate probe → SUCCESS: Stay PASSING, done
// 2. Manual PASSING → Immediate probe → FAIL: WARNING → Continue monitoring
// 3. WARNING monitoring: Probe every 200ms until SUCCESS (PASSING) or 3 failures (CRITICAL)
//
// PERFORMANCE NOTE: Re-probe is processed DIRECTLY (bypasses result queue) for:
// ✅ Zero latency - No queue waiting, immediate failover detection
// ✅ Guaranteed execution - Result never lost in queue contention
// ✅ Minimal overhead - No goroutine spawn, no channel operations
// ✅ Same code path - Uses existing evaluateStatusChangeForRecord()
func (hc *HealthChecker) executeImmediateReProbe(ipHealth *models.GSLBIPHealth, result ProbeResult) {
	// Small delay to ensure write buffer has chance to flush
	// This prevents probe from seeing stale counter values
	time.Sleep(100 * time.Millisecond)

	// CRITICAL: Check for nil context before accessing
	if result.Context == nil {
		hc.logger.Errorf("⚡ Cannot re-probe %s: result context is nil", result.IP)
		return
	}

	// Extract original task from result context to get RecordIDs
	originalTask, ok := result.Context.Value(taskContextKey).(ProbeTask)
	if !ok {
		hc.logger.Errorf("⚡ Cannot re-probe %s: missing task in original result context", result.IP)
		return
	}

	// Execute probe with timeout from probe config
	// Use hc.ctx as parent for proper cancellation on shutdown
	probeCtx, cancel := context.WithTimeout(hc.ctx,
		time.Duration(result.Probe.Timeout*float64(time.Second)))
	defer cancel()

	// Create probe task with RecordIDs from original task (CRITICAL for fan-out)
	task := ProbeTask{
		IPHealth:  ipHealth,
		Probe:     result.Probe,
		RecordIDs: originalTask.RecordIDs, // ✅ FIXED: Copy RecordIDs for fan-out
	}

	// Execute the re-probe using the same executor as normal buckets
	reProbeResult := hc.bucketScheduler.executor.ExecuteProbe(probeCtx, task.IPHealth, task.Probe)

	// Error handling: Log probe failures for diagnostics
	if !reProbeResult.Success && reProbeResult.Error != nil {
		hc.logger.Warnf("⚡ Re-probe failed for %s: %v (response_code: %d, response_time: %.3fs)",
			result.IP, reProbeResult.Error, reProbeResult.ResponseCode, reProbeResult.ResponseTime)
	}

	// ✅ CRITICAL: Attach CLEAN task context to re-probe result (NO manual_health_state!)
	// Immediate re-probe should NOT inherit manual state from original probe
	// This ensures state transitions are evaluated correctly based on actual probe results
	// Use hc.ctx for proper shutdown propagation
	reProbeResult.Context = context.WithValue(hc.ctx, taskContextKey, task)
	// ✅ CRITICAL FIX: Mark this as a re-probe to prevent infinite re-probe loops
	reProbeResult.Context = context.WithValue(reProbeResult.Context, isReprobeKey, true)

	// ✅ PERFORMANCE FIX: Process re-probe result DIRECTLY instead of queue
	// This eliminates queue latency and guarantees immediate failover detection
	// Fan out result to ALL records using this IP+config
	processedRecords := 0
	for _, recordID := range task.RecordIDs {
		// Process result directly using same evaluation logic
		// evaluateStatusChangeForRecord is safe to call even if probe failed
		hc.evaluateStatusChangeForRecord(reProbeResult, recordID, task.Probe)
		processedRecords++
	}

	// Log completion with detailed status
	if reProbeResult.Success {
		hc.logger.Debugf("⚡ Re-probe completed for %s (success, %d records processed)", result.IP, processedRecords)
	} else {
		hc.logger.Infof("⚡ Re-probe completed for %s (failed, %d records updated to reflect failure)", result.IP, processedRecords)
	}
}

// TriggerImmediateReProbeForManualChange triggers an immediate re-probe for a manually changed IP
// This is called from the API handler after a manual state change to verify actual health status
// Prevents manually-changed IPs from staying in incorrect state until next bucket cycle
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
		hc.logger.Debugf("⚡ Skipping manual re-probe for %s: probe disabled", ip)
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

	// ✅ ISOLATION FIX: Manual re-probe should ONLY affect the specific record admin changed
	// Do NOT fan-out to other records sharing this IP - they should maintain their own state
	// Each record is independent and follows its own probe schedule

	// Execute probe with timeout from probe config
	probeCtx, cancel := context.WithTimeout(hc.ctx,
		time.Duration(record.Probe.Timeout*float64(time.Second)))
	defer cancel()

	// Execute the probe using the same executor as normal buckets
	result := hc.bucketScheduler.executor.ExecuteProbe(probeCtx, &ipHealth, record.Probe)

	// Log probe execution
	if !result.Success && result.Error != nil {
		hc.logger.Infof("⚡ Manual re-probe executed for %s (record: %s): FAILED (response_code: %d, response_time: %.3fs, error: %v)",
			ip, recordID.Hex()[:8], result.ResponseCode, result.ResponseTime, result.Error)
	} else {
		hc.logger.Infof("⚡ Manual re-probe executed for %s (record: %s): SUCCESS (response_code: %d, response_time: %.3fs)",
			ip, recordID.Hex()[:8], result.ResponseCode, result.ResponseTime)
	}

	// Create task context with ONLY the manually changed record
	task := ProbeTask{
		IPHealth:  &ipHealth,
		Probe:     record.Probe,
		RecordIDs: []primitive.ObjectID{recordID}, // ✅ ONLY this record
	}

	// CRITICAL: Add manual health state to context for the specific record that was manually changed
	resultCtx := context.WithValue(hc.ctx, taskContextKey, task)
	resultCtx = context.WithValue(resultCtx, manualRecordIDKey, recordID) // Track which record was manually changed
	resultCtx = context.WithValue(resultCtx, manualHealthStateKey, manualHealthState)
	// ✅ CRITICAL FIX: Mark this as a re-probe to prevent infinite re-probe loops
	resultCtx = context.WithValue(resultCtx, isReprobeKey, true)
	result.Context = resultCtx

	// ✅ ISOLATION: Process result ONLY for the manually changed record
	hc.evaluateStatusChangeForRecord(result, recordID, record.Probe)

	// ✅ NEW: WARNING state monitoring loop
	// CRITICAL: Only start monitoring if manual state was PASSING or WARNING
	// Do NOT monitor if manual state was already CRITICAL (admin explicitly set it as unhealthy)
	if !result.Success && manualHealthState != models.HealthStateCritical {
		// Manual PASSING/WARNING + probe FAIL → Start monitoring
		// Will continue until SUCCESS (PASSING) or CRITICAL threshold reached
		hc.logger.Debugf("⚡ Starting WARNING monitoring for %s (record: %s, manual state: %s, probe failed)",
			ip, recordID.Hex()[:8], manualHealthState)
		// ✅ ISOLATION: Pass only this record ID for monitoring
		hc.monitorWarningState(ctx, recordID, ip, record.Probe, []primitive.ObjectID{recordID})
	} else if !result.Success && manualHealthState == models.HealthStateCritical {
		// Manual CRITICAL + probe FAIL → Already CRITICAL, no monitoring needed
		// Admin explicitly marked this as unhealthy, respect that decision
		hc.logger.Infof("⚡ Skipping WARNING monitoring for %s (record: %s, manual state: CRITICAL, already unhealthy)",
			ip, recordID.Hex()[:8])
	}

	hc.logger.Infof("⚡ Manual re-probe completed for %s (record: %s)", ip, recordID.Hex()[:8])

	return nil
}

// monitorWarningState continuously monitors WARNING state IPs until they become PASSING or CRITICAL
// Called after manual health state change when initial probe fails
//
// MONITORING STRATEGY:
// - Probe every 200ms (non-aggressive interval)
// - Continue until: SUCCESS (→ PASSING) OR critical_threshold reached (→ CRITICAL)
// - Maximum probes: critical_threshold (e.g., 3 for typical config)
// - Each probe updates all records sharing this IP+config (fan-out)
func (hc *HealthChecker) monitorWarningState(ctx context.Context, recordID primitive.ObjectID, ip string, probe *models.GSLBProbe, allRecords []primitive.ObjectID) {
	// Validate probe configuration
	if probe == nil || probe.CriticalThreshold <= 1 {
		hc.logger.Warnf("⚡ Cannot monitor WARNING state for %s: invalid probe config", ip)
		return
	}

	hc.logger.Infof("⚡ Starting WARNING state monitoring for %s (will probe every 200ms until PASSING or CRITICAL)", ip)

	// Maximum attempts = critical_threshold (e.g., 3)
	// We already did 1 probe, so do (critical_threshold - 1) more
	maxAttempts := probe.CriticalThreshold - 1
	if maxAttempts <= 0 {
		return // Already at or past critical threshold
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Wait 200ms between probes (non-aggressive)
		select {
		case <-time.After(200 * time.Millisecond):
			// Continue
		case <-ctx.Done():
			hc.logger.Warnf("⚡ WARNING monitoring cancelled for %s: context done", ip)
			return
		case <-hc.ctx.Done():
			hc.logger.Warnf("⚡ WARNING monitoring cancelled for %s: health checker stopping", ip)
			return
		}

		// Execute probe with timeout
		probeCtx, cancel := context.WithTimeout(hc.ctx,
			time.Duration(probe.Timeout*float64(time.Second)))

		// Build minimal IPHealth for probe
		ipHealth := models.GSLBIPHealth{
			RecordID: recordID,
			IP:       ip,
		}

		// Execute probe
		result := hc.bucketScheduler.executor.ExecuteProbe(probeCtx, &ipHealth, probe)
		cancel()

		// Log probe result
		if result.Success {
			hc.logger.Infof("⚡ WARNING monitoring probe #%d for %s: SUCCESS (recovered to PASSING)", attempt, ip)
		} else {
			hc.logger.Infof("⚡ WARNING monitoring probe #%d for %s: FAILED (consecutive failures: %d/%d)",
				attempt, ip, attempt+1, probe.CriticalThreshold)
		}

		// Create task context for result processing
		task := ProbeTask{
			IPHealth:  &ipHealth,
			Probe:     probe,
			RecordIDs: allRecords,
		}

		resultCtx := context.WithValue(hc.ctx, taskContextKey, task)
		// Mark as re-probe to prevent infinite loops
		resultCtx = context.WithValue(resultCtx, isReprobeKey, true)
		// ✅ CRITICAL FIX: Mark as WARNING monitoring probe to prevent additional re-probe triggers
		resultCtx = context.WithValue(resultCtx, isWarningMonitorKey, true)
		result.Context = resultCtx

		// Process result for all records FIRST (may transition to CRITICAL)
		for _, rid := range allRecords {
			hc.evaluateStatusChangeForRecord(result, rid, probe)
		}

		// ✅ RACE CONDITION FIX: Check persisted state AFTER processing
		// If ANY record for this IP transitioned to CRITICAL during processing, stop monitoring
		// This must be AFTER evaluateStatusChangeForRecord() to avoid stale reads
		// CRITICAL: Use fresh DB read to detect state written by evaluateStatusChangeForRecord()
		shouldStopMonitoring := false
		for _, rid := range allRecords {
			currentIPHealth, err := hc.getIPHealthFromDB(rid, ip)
			if err != nil {
				hc.logger.Debugf("⚡ WARNING monitoring: Could not get IP health for record %s: %v", rid.Hex(), err)
				continue
			}

			// If ANY record shows CRITICAL, stop monitoring (one-way progression rule)
			if currentIPHealth.HealthState == models.HealthStateCritical {
				hc.logger.Infof("⚡ WARNING monitoring stopped for %s: IP transitioned to CRITICAL (record: %s)", ip, rid.Hex()[:8])
				shouldStopMonitoring = true
				break
			}
		}

		if shouldStopMonitoring {
			return
		}

		// Check if we should stop monitoring
		if result.Success {
			hc.logger.Infof("⚡ WARNING monitoring completed for %s: Recovered to PASSING after %d probes", ip, attempt)
			return
		}

		// Check if we reached CRITICAL threshold
		// attempt+1 because we count the initial probe
		if attempt+1 >= probe.CriticalThreshold {
			hc.logger.Infof("⚡ WARNING monitoring completed for %s: Reached CRITICAL threshold after %d total probes", ip, attempt+1)
			return
		}
	}

	hc.logger.Infof("⚡ WARNING monitoring completed for %s after %d probes (max attempts reached)", ip, maxAttempts)
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
		ErrorMessage:   errorMessage, // ✅ RE-ENABLED
	}
}

// GetStats returns health checker statistics for monitoring
type HealthCheckerStats struct {
	OwnedShards      int
	TotalIPs         int
	HealthyIPs       int
	WarningIPs       int
	CriticalIPs      int
	BackoffActiveIPs int
	BucketStats      *BucketSchedulerStats // Bucket scheduler stats
	WriteBufferStats BufferStats
	ResultQueueDepth int // Shared result queue depth
	ResultQueueCap   int // Shared result queue capacity
}

// GetStats returns current health checker statistics
func (hc *HealthChecker) GetStats() (*HealthCheckerStats, error) {
	// Use hc.ctx as parent for proper cancellation
	ctx, cancel := context.WithTimeout(hc.ctx, 5*time.Second)
	defer cancel()

	stats := &HealthCheckerStats{
		OwnedShards:      len(hc.shardManager.GetOwnedShards()),
		BucketStats:      hc.bucketScheduler.GetStats(),
		WriteBufferStats: hc.writeBuffer.GetStats(),
		ResultQueueDepth: len(hc.resultQueue),
		ResultQueueCap:   cap(hc.resultQueue),
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
	key := fmt.Sprintf("%s:%s", recordID.Hex(), ip)
	hc.counterManager.mu.RLock()
	counter, exists := hc.counterManager.counters[key]
	hc.counterManager.mu.RUnlock()

	// No counter yet - use DB state (first probe)
	if !exists || counter == nil {
		return ipHealth.HealthState
	}

	// Compute state from counter using threshold logic
	// Same logic as evaluateAndUpdateState but without side effects
	failures := counter.ConsecutiveFailures

	if probe == nil {
		return ipHealth.HealthState // Fallback to DB
	}

	// Apply tri-state threshold logic
	if failures >= probe.CriticalThreshold {
		return models.HealthStateCritical
	}
	if failures >= probe.WarningThreshold {
		return models.HealthStateWarning
	}
	return models.HealthStatePassing
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
		hc.logger.Warnf("⚠️  Probe latency overflow detected for %s: capped at %ds", result.IP, maxLatencyMicros/1_000_000)
	}

	// Atomic add with overflow protection (sum + latency check)
	currentSum := atomic.LoadInt64(&hc.probeLatencySum)
	if currentSum > 0 && latencyMicros > (1<<63-1)-currentSum {
		// Would overflow - reset counters to prevent corruption
		hc.logger.Warnf("⚠️  Latency sum overflow detected, resetting counters")
		atomic.StoreInt64(&hc.probeLatencySum, latencyMicros)
		atomic.StoreInt64(&hc.probeLatencyCount, 1)
	} else {
		atomic.AddInt64(&hc.probeLatencySum, latencyMicros)
		atomic.AddInt64(&hc.probeLatencyCount, 1)
	}

	// Update min/max latency (thread-safe with mutex)
	hc.probeLatencyMu.Lock()
	if hc.probeLatencyMin == 0 || latencyMicros < hc.probeLatencyMin {
		hc.probeLatencyMin = latencyMicros
	}
	if latencyMicros > hc.probeLatencyMax {
		hc.probeLatencyMax = latencyMicros
	}
	hc.probeLatencyMu.Unlock()
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
