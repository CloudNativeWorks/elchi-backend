package gslb

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/metrics"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// BucketType defines the type of timer bucket
type BucketType string

const (
	BucketTypeNormal BucketType = "normal" // Standard probe bucket (10s-300s)
)

// TimerBucket represents a single probe interval bucket with dedicated timer
// Each bucket runs independently on its configured interval
type TimerBucket struct {
	// Configuration
	interval   int        // Probe interval in seconds (10, 20, 30, etc.)
	bucketType BucketType // Always BucketTypeNormal

	// Shard assignment (shared across all buckets - not exclusive!)
	ownedShards []ShardOwnership
	shardsMu    sync.RWMutex

	// Record tracking (per shard, filtered by interval)
	recordsByFQDN map[string]*RecordInfo // Fast lookup by FQDN
	recordsMu     sync.RWMutex

	// Worker pool (dedicated to this bucket)
	workerPool *BucketWorkerPool

	// Timer and lifecycle
	ticker   *time.Ticker
	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once          // Ensures stopCh is closed only once
	ctx      context.Context    // Bucket lifecycle context (for cleanup during shutdown)
	cancel   context.CancelFunc // Cancel function for context cleanup

	// Dependencies
	ipHealthManager *IPHealthManager
	metricsPusher   *metrics.MetricsPusher
	controllerID    string

	// Logger
	logger *logger.Logger

	// Stats (atomic fields for concurrent access)
	runCount         int64 // atomic
	totalIPsProbed   int64 // atomic
	errorCount       int64 // atomic
	corruptedIPCount int64 // atomic - count of IPs with empty IP address (data corruption)

	// Stats (mutex-protected fields)
	statsMu        sync.RWMutex
	lastRun        time.Time
	cycleStartTime time.Time

	// Rebalance coordination
	rebalancePending bool
	rebalanceMu      sync.Mutex
}

// RecordInfo contains cached information about a GSLB record
type RecordInfo struct {
	RecordID   primitive.ObjectID
	FQDN       string
	Probe      *models.GSLBProbe
	IPCount    int       // Cached IP count (updated on cycle)
	LastUpdate time.Time // Last time this record was loaded
}

// ProbeKey uniquely identifies a probe task by IP and probe configuration
// Used for config-based deduplication: Same IP with different configs = different probe tasks
type ProbeKey struct {
	IP       string
	Type     string // http, https, tcp
	Port     int
	Path     string
	Interval int
}

// BuildProbeKey creates a ProbeKey from IP and probe configuration
// Used for config-based deduplication logic
func BuildProbeKey(ip string, probe *models.GSLBProbe) ProbeKey {
	return ProbeKey{
		IP:       ip,
		Type:     probe.Type,
		Port:     probe.Port,
		Path:     probe.Path,
		Interval: probe.Interval,
	}
}

// Matches checks if this ProbeKey matches another ProbeKey
// Used to determine if two records share the same probe configuration
// Note: IP is NOT compared - only probe configuration (Type, Port, Path, Interval)
func (pk ProbeKey) Matches(other ProbeKey) bool {
	return pk.Type == other.Type &&
		pk.Port == other.Port &&
		pk.Path == other.Path &&
		pk.Interval == other.Interval
}

// ProbeTaskGroup - REMOVED: No longer used after isolation fix
// Each record+IP combination now gets its own independent probe task
// NO fan-out, NO deduplication - complete record isolation

// NewTimerBucket creates a new timer bucket
//
// Parameters:
//   - interval: Probe interval in seconds
//   - bucketType: Always BucketTypeNormal (kept for API compatibility)
//   - workerLimits: CPU-aware worker min/max
//   - resultQueue: Shared result channel
//   - ipHealthManager: IP health manager instance
//   - executor: ProbeExecutor implementation
//   - metricsPusher: Metrics pusher for Registry (optional)
//   - controllerID: Controller ID for metric labels
//   - logger: Logger instance
func NewTimerBucket(
	interval int,
	bucketType BucketType,
	workerLimits WorkerLimits,
	resultQueue chan ProbeResult,
	ipHealthManager *IPHealthManager,
	executor ProbeExecutor,
	metricsPusher *metrics.MetricsPusher,
	controllerID string,
	logger *logger.Logger,
) *TimerBucket {
	queueSize := workerLimits.MaxWorkers * 10 // 10x buffer

	ctx, cancel := context.WithCancel(context.Background())

	tb := &TimerBucket{
		interval:        interval,
		bucketType:      bucketType,
		ownedShards:     []ShardOwnership{},
		recordsByFQDN:   make(map[string]*RecordInfo),
		stopCh:          make(chan struct{}),
		doneCh:          make(chan struct{}),
		ctx:             ctx,
		cancel:          cancel,
		ipHealthManager: ipHealthManager,
		metricsPusher:   metricsPusher,
		controllerID:    controllerID,
		logger:          logger,
	}

	// Create dedicated worker pool for this bucket
	tb.workerPool = NewBucketWorkerPool(
		interval,
		workerLimits.MinWorkers,
		workerLimits.MaxWorkers,
		queueSize,
		resultQueue,
		executor,
		logger,
	)

	return tb
}

// Start begins the bucket's probe cycle timer
func (tb *TimerBucket) Start() {
	tb.ticker = time.NewTicker(time.Duration(tb.interval) * time.Second)

	go func() {
		defer close(tb.doneCh)

		for {
			select {
			case <-tb.ticker.C:
				tb.runBucketCycle()

			case <-tb.stopCh:
				return
			}
		}
	}()

	// DISABLED: Too verbose on startup (8 buckets = 8 logs)
	// tb.logger.Infof("⏰ Bucket %ds (%s) started", tb.interval, tb.bucketType)
}

// runBucketCycle executes a single probe cycle for this bucket
func (tb *TimerBucket) runBucketCycle() {
	cycleStart := time.Now()

	// Update time-based stats with mutex
	tb.statsMu.Lock()
	tb.cycleStartTime = cycleStart
	tb.statsMu.Unlock()

	// Check for pending rebalance
	tb.rebalanceMu.Lock()
	if tb.rebalancePending {
		tb.logger.Infof("Bucket %ds: Rebalance pending, completing cycle first", tb.interval)
	}
	tb.rebalanceMu.Unlock()

	// Normal bucket flow: Load records and probe their IPs
	// PERFORMANCE DEBUG: Track timing for each phase
	phaseStart := time.Now()

	// Get current record list (thread-safe copy)
	tb.recordsMu.RLock()
	if len(tb.recordsByFQDN) == 0 {
		tb.recordsMu.RUnlock()
		return
	}

	records := make([]*RecordInfo, 0, len(tb.recordsByFQDN))
	recordMap := make(map[primitive.ObjectID]*RecordInfo) // For fast lookup after batch query
	for _, record := range tb.recordsByFQDN {
		records = append(records, record)
		recordMap[record.RecordID] = record
	}
	tb.recordsMu.RUnlock()
	recordCopyDuration := time.Since(phaseStart)

	totalIPs := 0
	submittedIPs := 0
	skippedIPs := 0

	// ✅ CRITICAL FIX: NO DEDUPLICATION - Each record+IP must be independent
	// Even if multiple records share the same IP with same config,
	// each record maintains its own state, counter, and backoff
	var probeTasks []ProbeTask

	// Collect all record IDs for batch query
	recordIDs := make([]primitive.ObjectID, 0, len(records))
	for _, record := range records {
		// Skip if probe is disabled (probe config exists but disabled)
		if record.Probe != nil && !record.Probe.IsEnabled() {
			// Don't count these as errors - probe is intentionally paused
			continue
		}
		recordIDs = append(recordIDs, record.RecordID)
	}

	// Batch query: Get ALL IPs for ALL records in ONE query (N+1 fix)
	// Use bucket context so query gets cancelled on shutdown
	phaseStart = time.Now()
	ipsByRecord, err := tb.ipHealthManager.GetIPsByRecordIDs(tb.ctx, recordIDs)
	batchQueryDuration := time.Since(phaseStart)
	if err != nil {
		// Check if error is due to context cancellation (shutdown)
		if tb.ctx.Err() != nil {
			tb.logger.Debugf("Bucket %ds: Batch query cancelled (shutdown)", tb.interval)
			return
		}
		tb.logger.Errorf("Bucket %ds: Failed to get IPs (batch query): %v", tb.interval, err)
		atomic.AddInt64(&tb.errorCount, 1)
		return
	}

	// Check for shutdown before processing IPs
	select {
	case <-tb.ctx.Done():
		tb.logger.Debugf("Bucket %ds: Cycle aborted (shutdown before IP processing)", tb.interval)
		return
	default:
	}

	// Process each record's IPs
	phaseStart = time.Now()
	for recordID, ips := range ipsByRecord {
		record := recordMap[recordID]
		if record == nil {
			// Shouldn't happen, but defensive programming
			continue
		}

		totalIPs += len(ips)

		// Probe each IP - EACH record+IP gets its own probe task
		for _, ip := range ips {
			// CRITICAL: Skip if IP address is empty (data corruption defense)
			if ip.IP == "" {
				tb.logger.Warnf("Bucket %ds: Skipping IP health record with empty IP address (RecordID: %s, FQDN: %s)",
					tb.interval, ip.RecordID.Hex(), ip.FQDN)
				atomic.AddInt64(&tb.corruptedIPCount, 1) // Track corruption metric
				skippedIPs++
				continue
			}

			// Skip if in backoff (circuit breaker)
			// CRITICAL: This check is per record+IP, so record 10 can be in backoff
			// while record 12 (same IP) is NOT in backoff
			if ip.IsInBackoff(tb.interval) {
				skippedIPs++
				continue
			}

			// ✅ ISOLATION FIX: Create separate probe task for THIS record+IP combination
			// NO deduplication, NO fan-out to other records
			// Each record maintains completely independent health state
			task := ProbeTask{
				IPHealth:  &ip,
				Probe:     record.Probe,
				RecordIDs: []primitive.ObjectID{record.RecordID}, // ONLY this record
			}
			probeTasks = append(probeTasks, task)
		}

		// Update cached IP count
		record.IPCount = len(ips)
	}
	ipProcessingDuration := time.Since(phaseStart)

	// Submit all probe tasks to worker pool
	phaseStart = time.Now()
	for _, task := range probeTasks {
		// TEMPORARILY DISABLED: Context check for performance testing
		// select {
		// case <-tb.ctx.Done():
		// 	tb.logger.Debugf("Bucket %ds: Cycle aborted (shutdown during task submission)", tb.interval)
		// 	return
		// default:
		// }

		if tb.workerPool.Submit(task) {
			submittedIPs++
		} else {
			skippedIPs++
		}
	}
	taskSubmissionDuration := time.Since(phaseStart)

	elapsed := time.Since(cycleStart)
	completionPercent := float64(elapsed) / float64(tb.interval*int(time.Second)) * 100

	// Update stats atomically
	tb.statsMu.Lock()
	tb.lastRun = cycleStart
	tb.statsMu.Unlock()

	atomic.AddInt64(&tb.totalIPsProbed, int64(submittedIPs))

	// Cycle summary log (DEBUG level) - shows probe activity per cycle
	tb.logger.Debugf("🔄 Bucket %ds cycle: %d records, %d total IPs → %d probed, %d skipped (backoff/duplicate) | Duration: %v (%.1f%% of interval)",
		tb.interval, len(records), totalIPs, submittedIPs, skippedIPs, elapsed, completionPercent)

	// Warning if completion time too high (KEEP - important for scaling alerts)
	if completionPercent > 80 {
		tb.logger.Warnf("⚠️  Bucket %ds completion time high (%.1f%%) | Phase timings: RecordCopy=%v BatchQuery=%v IPProcessing=%v TaskSubmit=%v Total=%v",
			tb.interval, completionPercent,
			recordCopyDuration, batchQueryDuration, ipProcessingDuration, taskSubmissionDuration, elapsed)
	}

	// Push bucket cycle metrics to Registry
	tb.pushBucketCycleMetrics(len(records), totalIPs, submittedIPs, skippedIPs, elapsed, completionPercent)

	// Apply pending rebalance after cycle completion
	// Without this, old records remain in memory causing memory leak
	tb.rebalanceMu.Lock()
	if tb.rebalancePending {
		tb.logger.Infof("Bucket %ds: Applying rebalance now (cycle complete)", tb.interval)
		tb.rebalancePending = false
		tb.rebalanceMu.Unlock()

		// Reload records from database to pick up new shard assignments
		// This ensures we only probe IPs in our newly assigned shards
		if err := tb.LoadRecords(tb.ctx, tb.ipHealthManager); err != nil {
			tb.logger.Errorf("Bucket %ds: Failed to reload records after rebalance: %v", tb.interval, err)
		} else {
			tb.logger.Infof("Bucket %ds: Rebalance complete, records reloaded", tb.interval)
		}
	} else {
		tb.rebalanceMu.Unlock()
	}
}

// SetOwnedShards updates the shards assigned to this bucket
// This is called during initial load and rebalancing
func (tb *TimerBucket) SetOwnedShards(shards []ShardOwnership) {
	tb.shardsMu.Lock()
	tb.ownedShards = make([]ShardOwnership, len(shards))
	copy(tb.ownedShards, shards)
	tb.shardsMu.Unlock()

	tb.logger.Debugf("Bucket %ds: Updated shard ownership (%d shards)", tb.interval, len(shards))
}

// LoadRecords loads GSLB records for this bucket's shards
// Only loads records with matching probe interval
func (tb *TimerBucket) LoadRecords(ctx context.Context, db *IPHealthManager) error {
	tb.shardsMu.RLock()
	shards := tb.ownedShards
	tb.shardsMu.RUnlock()

	if len(shards) == 0 {
		tb.logger.Debugf("Bucket %ds: No shards assigned, no records to load", tb.interval)
		return nil
	}

	// Query records for this bucket's interval
	records, err := db.GetRecordsByShards(ctx, shards, tb.interval)
	if err != nil {
		return fmt.Errorf("failed to load records: %w", err)
	}

	// Update record map
	tb.recordsMu.Lock()
	defer tb.recordsMu.Unlock()

	// Clear old records
	tb.recordsByFQDN = make(map[string]*RecordInfo, len(records))

	// Add new records
	for _, record := range records {
		tb.recordsByFQDN[record.FQDN] = &RecordInfo{
			RecordID:   record.ID,
			FQDN:       record.FQDN,
			Probe:      record.Probe,
			LastUpdate: time.Now(),
		}
	}

	// DISABLED: Too verbose (logs on every reload/rebalance)
	// tb.logger.Infof("Bucket %ds: Loaded %d records (interval: %ds)",
	// 	tb.interval, len(records), tb.interval)

	return nil
}

// AddRecord adds a single record to this bucket (for dynamic updates)
func (tb *TimerBucket) AddRecord(recordID primitive.ObjectID, fqdn string, probe *models.GSLBProbe) {
	tb.recordsMu.Lock()
	defer tb.recordsMu.Unlock()

	tb.recordsByFQDN[fqdn] = &RecordInfo{
		RecordID:   recordID,
		FQDN:       fqdn,
		Probe:      probe,
		LastUpdate: time.Now(),
	}

	tb.logger.Debugf("Bucket %ds: Added record %s", tb.interval, fqdn)
}

// RemoveRecord removes a record from this bucket (for dynamic updates)
func (tb *TimerBucket) RemoveRecord(fqdn string) {
	tb.recordsMu.Lock()
	defer tb.recordsMu.Unlock()

	delete(tb.recordsByFQDN, fqdn)

	tb.logger.Debugf("Bucket %ds: Removed record %s", tb.interval, fqdn)
}

// GetRecord returns a record by FQDN (thread-safe)
func (tb *TimerBucket) GetRecord(fqdn string) (*RecordInfo, bool) {
	tb.recordsMu.RLock()
	defer tb.recordsMu.RUnlock()

	record, exists := tb.recordsByFQDN[fqdn]
	return record, exists
}

// GetRecordCount returns the number of records in this bucket
func (tb *TimerBucket) GetRecordCount() int {
	tb.recordsMu.RLock()
	defer tb.recordsMu.RUnlock()
	return len(tb.recordsByFQDN)
}

// ScheduleRebalance marks this bucket for rebalancing
// Actual rebalance happens after current cycle completes (graceful)
func (tb *TimerBucket) ScheduleRebalance() {
	tb.rebalanceMu.Lock()
	defer tb.rebalanceMu.Unlock()

	tb.rebalancePending = true
	tb.logger.Debugf("Bucket %ds: Rebalance scheduled (will apply after cycle)", tb.interval)
}

// GetStats returns current bucket statistics
type TimerBucketStats struct {
	Interval         int
	BucketType       string
	RecordCount      int
	ShardCount       int
	LastRun          time.Time
	RunCount         int64
	TotalIPsProbed   int64
	ErrorCount       int64
	CorruptedIPCount int64 // Count of IPs with empty IP address (data corruption)
	WorkerPoolStats  BucketWorkerPoolStats
	RebalancePending bool
}

func (tb *TimerBucket) GetStats() TimerBucketStats {
	tb.recordsMu.RLock()
	recordCount := len(tb.recordsByFQDN)
	tb.recordsMu.RUnlock()

	tb.shardsMu.RLock()
	shardCount := len(tb.ownedShards)
	tb.shardsMu.RUnlock()

	tb.rebalanceMu.Lock()
	rebalancePending := tb.rebalancePending
	tb.rebalanceMu.Unlock()

	// Read time-based stats with mutex
	tb.statsMu.RLock()
	lastRun := tb.lastRun
	tb.statsMu.RUnlock()

	return TimerBucketStats{
		Interval:         tb.interval,
		BucketType:       string(tb.bucketType),
		RecordCount:      recordCount,
		ShardCount:       shardCount,
		LastRun:          lastRun,
		RunCount:         atomic.LoadInt64(&tb.runCount),
		TotalIPsProbed:   atomic.LoadInt64(&tb.totalIPsProbed),
		ErrorCount:       atomic.LoadInt64(&tb.errorCount),
		CorruptedIPCount: atomic.LoadInt64(&tb.corruptedIPCount),
		WorkerPoolStats:  tb.workerPool.GetStats(),
		RebalancePending: rebalancePending,
	}
}

// Stop gracefully stops the bucket timer and worker pool
func (tb *TimerBucket) Stop() {
	tb.logger.Infof("Stopping bucket %ds...", tb.interval)

	// Cancel context to abort any in-flight operations
	if tb.cancel != nil {
		tb.cancel()
	}

	// Stop ticker
	if tb.ticker != nil {
		tb.ticker.Stop()
	}

	// Signal stop (use sync.Once to prevent double-close panic)
	tb.stopOnce.Do(func() {
		close(tb.stopCh)
	})

	// Wait for timer goroutine
	<-tb.doneCh

	// Stop worker pool
	tb.workerPool.Stop()

	tb.logger.Infof("Bucket %ds stopped", tb.interval)
}
