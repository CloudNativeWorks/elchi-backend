package gslb

import (
	"context"
	"fmt"
	"sync"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/metrics"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// AllowedIntervals defines the pre-configured bucket intervals (in seconds)
// Users MUST choose from these intervals - no custom values allowed
// NOTE: Using models.AllowedProbeIntervals for consistency
var AllowedIntervals = models.AllowedProbeIntervals

// BucketScheduler manages all timer buckets and coordinates probe scheduling
// This is the central orchestrator for the bucket-based system
type BucketScheduler struct {
	// Pre-defined timer buckets (one per allowed interval)
	buckets map[int]*TimerBucket // key: interval in seconds

	// Dependencies
	ipHealthManager *IPHealthManager
	shardManager    *ShardManager
	cpuConfig       *CPUConfig
	metricsPusher   *metrics.MetricsPusher // Metrics pusher for Registry

	// Shared result processor channel
	resultQueue chan ProbeResult

	// Probe executor (shared across all buckets)
	executor ProbeExecutor

	// Logger
	logger *logger.Logger

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.RWMutex
}

// NewBucketScheduler creates a new bucket scheduler with all configured buckets
//
// Parameters:
//   - ipHealthManager: IP health manager instance
//   - shardManager: Shard manager instance
//   - cpuConfig: CPU-aware configuration
//   - resultQueue: Shared result channel for all buckets
//   - executor: ProbeExecutor implementation
//   - metricsPusher: Metrics pusher for Registry (optional)
//   - logger: Logger instance
func NewBucketScheduler(
	ipHealthManager *IPHealthManager,
	shardManager *ShardManager,
	cpuConfig *CPUConfig,
	resultQueue chan ProbeResult,
	executor ProbeExecutor,
	metricsPusher *metrics.MetricsPusher,
	logger *logger.Logger,
) *BucketScheduler {
	ctx, cancel := context.WithCancel(context.Background())

	bs := &BucketScheduler{
		buckets:         make(map[int]*TimerBucket),
		ipHealthManager: ipHealthManager,
		shardManager:    shardManager,
		cpuConfig:       cpuConfig,
		metricsPusher:   metricsPusher,
		resultQueue:     resultQueue,
		executor:        executor,
		logger:          logger,
		ctx:             ctx,
		cancel:          cancel,
	}

	// Create all normal buckets
	for _, interval := range AllowedIntervals {
		workerLimits := cpuConfig.GetWorkerLimits(interval)
		if workerLimits.MaxWorkers == 0 {
			logger.Warnf("No worker limits configured for interval %ds, skipping bucket", interval)
			continue
		}

		bucket := NewTimerBucket(
			interval,
			BucketTypeNormal,
			workerLimits,
			resultQueue,
			ipHealthManager,
			executor,
			metricsPusher,
			shardManager.controllerID,
			logger,
		)

		bs.buckets[interval] = bucket
	}

	// ⚡ FastFail bucket REMOVED - replaced with immediate re-probe on state transition
	// See health_checker.go::executeImmediateReProbe() for new implementation
	// Benefits: No periodic MongoDB queries, no stale data issues, exact threshold control

	logger.Infof("🗂️  Bucket scheduler created with %d normal buckets (CPU: %d cores)",
		len(bs.buckets), cpuConfig.GetNumCPU())

	return bs
}

// Start initializes all buckets and starts their timers
func (bs *BucketScheduler) Start() error {
	bs.logger.Infof("🚀 Starting bucket scheduler...")

	// Get current shard ownership
	ownedShards := bs.shardManager.GetOwnedShards()
	if len(ownedShards) == 0 {
		bs.logger.Infof("⏸️  Bucket scheduler standby mode (no shards owned yet)")
		bs.logger.Infof("Waiting for GSLB records to be created or shard rebalance")
		return fmt.Errorf("no shards owned, cannot start scheduler")
	}

	// Distribute shards across buckets and load records
	if err := bs.RebalanceShards(ownedShards); err != nil {
		return fmt.Errorf("failed to rebalance shards: %w", err)
	}

	// Start all normal buckets
	for interval, bucket := range bs.buckets {
		bucket.Start()
		bs.logger.Debugf("Started bucket %ds", interval)
	}

	bs.logger.Infof("✅ Bucket scheduler started with %d buckets", len(bs.buckets))

	return nil
}

// RebalanceShards distributes owned shards across buckets and loads records
// This is called:
//   - During initialization
//   - When shard ownership changes (controller scale-up/down)
func (bs *BucketScheduler) RebalanceShards(ownedShards []ShardOwnership) error {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	bs.logger.Infof("🔄 Rebalancing shards (%d shards owned)", len(ownedShards))

	// Strategy: All buckets get ALL shards (not exclusive assignment)
	// Each bucket filters records by probe.interval at query time
	// This ensures no idle buckets and balanced load

	ctx := context.Background()

	// Parallel load using goroutines + WaitGroup
	var wg sync.WaitGroup
	var mu sync.Mutex
	loadErrors := 0

	// Assign shards and load records in parallel for all buckets
	for interval, bucket := range bs.buckets {
		wg.Add(1)
		go func(interval int, bucket *TimerBucket) {
			defer wg.Done()

			bucket.SetOwnedShards(ownedShards)

			// Load records for this interval
			if err := bucket.LoadRecords(ctx, bs.ipHealthManager); err != nil {
				mu.Lock()
				bs.logger.Errorf("Failed to load records for bucket %ds: %v", interval, err)
				loadErrors++
				mu.Unlock()
				return
			}

			recordCount := bucket.GetRecordCount()
			mu.Lock()
			bs.logger.Debugf("Bucket %ds: %d records loaded", interval, recordCount)
			mu.Unlock()
		}(interval, bucket)
	}

	// Wait for all buckets to finish loading
	wg.Wait()

	bs.logger.Infof("✅ Shard rebalancing complete (%d/%d buckets loaded successfully)", len(bs.buckets)-loadErrors, len(bs.buckets))

	if loadErrors > 0 {
		return fmt.Errorf("%d buckets failed to load records", loadErrors)
	}

	return nil
}

// AddRecordToBucket adds a record to the appropriate bucket based on probe interval
// This is called when a new GSLB record is created
func (bs *BucketScheduler) AddRecordToBucket(recordID primitive.ObjectID, fqdn string, probe *models.GSLBProbe) error {
	bs.mu.RLock()
	defer bs.mu.RUnlock()

	bucket, exists := bs.buckets[probe.Interval]
	if !exists {
		return fmt.Errorf("no bucket for interval %ds (allowed: %v)", probe.Interval, AllowedIntervals)
	}

	bucket.AddRecord(recordID, fqdn, probe)

	bs.logger.Debugf("Added record %s to bucket %ds", fqdn, probe.Interval)

	return nil
}

// RemoveRecordFromBucket removes a record from its bucket
// This is called when a GSLB record is deleted
func (bs *BucketScheduler) RemoveRecordFromBucket(fqdn string, interval int) error {
	bs.mu.RLock()
	defer bs.mu.RUnlock()

	bucket, exists := bs.buckets[interval]
	if !exists {
		return fmt.Errorf("no bucket for interval %ds", interval)
	}

	bucket.RemoveRecord(fqdn)

	bs.logger.Debugf("Removed record %s from bucket %ds", fqdn, interval)

	return nil
}

// UpdateRecordInterval moves a record from one bucket to another
// This is called when a user updates the probe interval
func (bs *BucketScheduler) UpdateRecordInterval(fqdn string, oldInterval int, newInterval int, recordID primitive.ObjectID, probe *models.GSLBProbe) error {
	bs.mu.RLock()
	defer bs.mu.RUnlock()

	// Remove from old bucket
	if oldBucket, exists := bs.buckets[oldInterval]; exists {
		oldBucket.RemoveRecord(fqdn)
		bs.logger.Debugf("Removed record %s from old bucket %ds", fqdn, oldInterval)
	}

	// Add to new bucket
	newBucket, exists := bs.buckets[newInterval]
	if !exists {
		return fmt.Errorf("no bucket for new interval %ds (allowed: %v)", newInterval, AllowedIntervals)
	}

	newBucket.AddRecord(recordID, fqdn, probe)

	bs.logger.Infof("Moved record %s from bucket %ds → %ds", fqdn, oldInterval, newInterval)

	return nil
}

// AddToFastFail - DEPRECATED: FastFail bucket removed, replaced with immediate re-probe
// Kept for backward compatibility, but does nothing
func (bs *BucketScheduler) AddToFastFail(ip string, probe *models.GSLBProbe) {
	// No-op: FastFail bucket removed
	// WARNING IPs are now re-probed immediately via health_checker.go::executeImmediateReProbe()
}

// RemoveFromFastFail - DEPRECATED: FastFail bucket removed, replaced with immediate re-probe
// Kept for backward compatibility, but does nothing
func (bs *BucketScheduler) RemoveFromFastFail(ip string) {
	// No-op: FastFail bucket removed
}

// GetBucketStats returns statistics for all buckets
type BucketSchedulerStats struct {
	TotalBuckets   int
	BucketStats    []TimerBucketStats
	TotalRecords   int
	TotalIPsProbed int64
	TotalWorkers   int
	OwnedShards    int
}

func (bs *BucketScheduler) GetStats() *BucketSchedulerStats {
	bs.mu.RLock()
	defer bs.mu.RUnlock()

	stats := &BucketSchedulerStats{
		TotalBuckets: len(bs.buckets),
		BucketStats:  make([]TimerBucketStats, 0, len(bs.buckets)),
	}

	// Collect stats from all buckets
	for _, bucket := range bs.buckets {
		bucketStats := bucket.GetStats()
		stats.BucketStats = append(stats.BucketStats, bucketStats)
		stats.TotalRecords += bucketStats.RecordCount
		stats.TotalIPsProbed += bucketStats.TotalIPsProbed
		stats.TotalWorkers += bucketStats.WorkerPoolStats.CurrentWorkers
	}

	// Shard count
	stats.OwnedShards = len(bs.shardManager.GetOwnedShards())

	return stats
}

// ValidateInterval checks if an interval is in the allowed list
func (bs *BucketScheduler) ValidateInterval(interval int) error {
	for _, allowed := range AllowedIntervals {
		if interval == allowed {
			return nil
		}
	}

	return fmt.Errorf("invalid interval %d, allowed: %v", interval, AllowedIntervals)
}

// GetAllowedIntervals returns the list of allowed bucket intervals
func (bs *BucketScheduler) GetAllowedIntervals() []int {
	return AllowedIntervals
}

// ReloadAllBuckets forces immediate reload of all bucket records from database
// This is called after GSLB record create/update/delete operations via API
// to ensure changes are immediately visible without waiting for periodic reload
func (bs *BucketScheduler) ReloadAllBuckets() error {
	bs.mu.RLock()
	defer bs.mu.RUnlock()

	ctx := context.Background()

	// Parallel reload using goroutines + WaitGroup
	var wg sync.WaitGroup
	var mu sync.Mutex
	reloadCount := 0
	var lastErr error

	// Reload all normal buckets in parallel
	for interval, bucket := range bs.buckets {
		wg.Add(1)
		go func(interval int, bucket *TimerBucket) {
			defer wg.Done()

			if err := bucket.LoadRecords(ctx, bs.ipHealthManager); err != nil {
				mu.Lock()
				bs.logger.Errorf("Failed to reload bucket %ds: %v", interval, err)
				lastErr = err
				mu.Unlock()
			} else {
				mu.Lock()
				reloadCount++
				mu.Unlock()
			}
		}(interval, bucket)
	}

	// Wait for all buckets to finish loading
	wg.Wait()

	if lastErr != nil {
		return fmt.Errorf("reloaded %d/%d buckets, last error: %w", reloadCount, len(bs.buckets), lastErr)
	}

	bs.logger.Infof("✅ Reloaded all %d buckets successfully", reloadCount)
	return nil
}

// Stop gracefully stops all buckets
func (bs *BucketScheduler) Stop() {
	bs.logger.Infof("Stopping bucket scheduler...")

	bs.mu.Lock()
	defer bs.mu.Unlock()

	// Stop all normal buckets
	for interval, bucket := range bs.buckets {
		bucket.Stop()
		bs.logger.Debugf("Stopped bucket %ds", interval)
	}

	// Cancel context
	bs.cancel()

	bs.logger.Infof("✅ Bucket scheduler stopped")
}
