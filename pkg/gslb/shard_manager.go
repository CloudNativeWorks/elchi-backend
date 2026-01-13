package gslb

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// ShardOwnership represents ownership of a shard+sub-shard combination
// Two-tier sharding: 128 top-level shards × 8 sub-shards = 1,024 logical shards
type ShardOwnership struct {
	ShardID    int // Top-level shard (0-127)
	SubShardID int // Sub-shard (0-7)
}

// ShardManager manages GSLB shard ownership and leasing with two-tier sharding support
type ShardManager struct {
	ctx          context.Context
	cancel       context.CancelFunc
	repository   ShardRepository
	controllerID string
	logger       *logger.Logger

	// Owned shards tracking (two-tier: shard_id + sub_shard_id)
	mu               sync.RWMutex
	ownedShards      []ShardOwnership // List of owned shard+sub-shard combinations
	targetShardCount int              // Target number of logical shards per controller

	// Shutdown and ready signals
	done  chan struct{}
	ready chan struct{} // Signals when initial shard acquisition is complete

	// Event channels
	shardAcquired chan int // Signals when shards are acquired (sends shard count)
}

// NewShardManager creates a new shard manager
func NewShardManager(appContext *db.AppContext, controllerID string) *ShardManager {
	ctx, cancel := context.WithCancel(context.Background())

	return &ShardManager{
		ctx:              ctx,
		cancel:           cancel,
		repository:       NewMongoShardRepository(appContext.Client, logger.NewLogger("gslb/shard-repository")),
		controllerID:     controllerID,
		logger:           logger.NewLogger("gslb/shard-manager"),
		ownedShards:      []ShardOwnership{},
		targetShardCount: 0,
		done:             make(chan struct{}),
		ready:            make(chan struct{}),
		shardAcquired:    make(chan int, 10), // Buffered channel for shard acquisition events
	}
}

// Start begins the shard management loop
func (sm *ShardManager) Start() {
	sm.logger.Infof("🚀 Starting GSLB Shard Manager (Two-Tier) for controller: %s", sm.controllerID)

	// Initial shard acquisition
	if err := sm.acquireShards(); err != nil {
		sm.logger.Errorf("Failed to acquire initial shards: %v", err)
	}

	// Signal that initial acquisition is complete (even if it failed)
	close(sm.ready)

	// Start lease renewal loop
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Renew leases and rebalance
			if err := sm.renewLeases(); err != nil {
				sm.logger.Errorf("Failed to renew leases: %v", err)
			}

		case <-sm.ctx.Done():
			sm.logger.Infof("Shard manager stopping...")
			sm.releaseShards()
			close(sm.done)
			return
		}
	}
}

// Stop gracefully stops the shard manager
func (sm *ShardManager) Stop() {
	sm.logger.Infof("Stopping shard manager...")
	sm.cancel()
	<-sm.done
	sm.logger.Infof("Shard manager stopped")
}

// WaitReady blocks until initial shard acquisition is complete
// Replaces arbitrary sleep with proper synchronization
func (sm *ShardManager) WaitReady(ctx context.Context) error {
	select {
	case <-sm.ready:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("timed out waiting for shard manager ready: %w", ctx.Err())
	}
}

// GetOwnedShards returns the list of currently owned shard+sub-shard combinations
func (sm *ShardManager) GetOwnedShards() []ShardOwnership {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	// Return a copy to prevent external modification
	shards := make([]ShardOwnership, len(sm.ownedShards))
	copy(shards, sm.ownedShards)
	return shards
}

// GetShardAcquisitionChannel returns the channel for shard acquisition events
// System can listen to this channel to be notified when shards are acquired
func (sm *ShardManager) GetShardAcquisitionChannel() <-chan int {
	return sm.shardAcquired
}

// acquireShards attempts to acquire logical shards (initial acquisition)
func (sm *ShardManager) acquireShards() error {
	ctx, cancel := context.WithTimeout(sm.ctx, 30*time.Second)
	defer cancel()

	// Calculate target logical shards per controller
	if err := sm.calculateTargetShards(ctx); err != nil {
		return fmt.Errorf("failed to calculate target shards: %w", err)
	}

	// Find available shards via repository
	limit := sm.targetShardCount / 8
	if limit == 0 {
		limit = 1
	}

	shards, err := sm.repository.FindAvailableShards(ctx, sm.controllerID, limit)
	if err != nil {
		return fmt.Errorf("failed to find available shards: %w", err)
	}

	if len(shards) == 0 {
		sm.logger.Warnf("No available shards to acquire")
		return nil
	}

	// Build candidate list and shard IDs to acquire
	candidateShards := []ShardOwnership{}
	shardIDs := []int{}
	processedTopLevel := make(map[int]bool)

	for _, shard := range shards {
		if processedTopLevel[shard.ShardID] {
			continue
		}
		processedTopLevel[shard.ShardID] = true
		shardIDs = append(shardIDs, shard.ShardID)

		// For this top-level shard, we logically own all 8 sub-shards
		for subShardID := 0; subShardID < 8; subShardID++ {
			candidateShards = append(candidateShards, ShardOwnership{
				ShardID:    shard.ShardID,
				SubShardID: subShardID,
			})

			if len(candidateShards) >= sm.targetShardCount {
				break
			}
		}

		if len(candidateShards) >= sm.targetShardCount {
			break
		}
	}

	sm.logger.Infof("Executing bulk acquisition for %d top-level shards (%d logical shards)...", len(shardIDs), len(candidateShards))

	// Acquire shards via repository with retry
	modifiedCount, err := sm.repository.AcquireShardsWithRetry(ctx, shardIDs, sm.controllerID, models.GSLBShardLeaseTTL, 3)
	if err != nil {
		return fmt.Errorf("failed to acquire shards: %w", err)
	}

	// Verify which shards were successfully acquired
	var acquiredShards []ShardOwnership
	if modifiedCount > 0 {
		acquiredShards, err = sm.verifyShardsAcquired(ctx, candidateShards)
		if err != nil {
			sm.logger.Errorf("Failed to verify acquired shards: %v", err)
			return err
		}
	}

	// Update owned shards list
	sm.mu.Lock()
	sm.ownedShards = acquiredShards
	ownedCount := len(acquiredShards)
	sm.mu.Unlock()

	sm.logger.Infof("✅ Initial acquisition complete: %d logical shards across %d top-level shards (modified: %d)",
		len(acquiredShards), len(shardIDs), modifiedCount)

	// Emit shard acquisition event (notify health checker to start if shards > 0)
	if ownedCount > 0 {
		select {
		case sm.shardAcquired <- ownedCount:
			sm.logger.Debugf("🔔 Shard acquisition event emitted: %d shards", ownedCount)
		default:
			sm.logger.Warn("⚠️  Shard acquisition event channel full, skipping notification")
		}
	}

	return nil
}

// renewLeases renews leases for owned shards and performs rebalancing
func (sm *ShardManager) renewLeases() error {
	ctx, cancel := context.WithTimeout(sm.ctx, 30*time.Second)
	defer cancel()

	// Recalculate target shards (controller count may have changed)
	if err := sm.calculateTargetShards(ctx); err != nil {
		return fmt.Errorf("failed to calculate target shards: %w", err)
	}

	sm.mu.RLock()
	currentCount := len(sm.ownedShards)
	sm.mu.RUnlock()

	// Determine rebalancing action
	switch {
	case currentCount > sm.targetShardCount:
		return sm.rebalanceScaleDown(ctx, currentCount-sm.targetShardCount)
	case currentCount < sm.targetShardCount:
		return sm.rebalanceScaleUp(ctx, sm.targetShardCount-currentCount)
	default:
		return sm.renewOwnedShardLeases(ctx)
	}
}

// rebalanceScaleDown releases excess shards when scaling down
func (sm *ShardManager) rebalanceScaleDown(ctx context.Context, count int) error {
	sm.logger.Infof("🔄 Rebalancing: Releasing %d logical shards (current: %d, target: %d)",
		count, count+sm.targetShardCount, sm.targetShardCount)

	sm.mu.Lock()
	// Release oldest N shards
	releasedShards := make([]ShardOwnership, count)
	copy(releasedShards, sm.ownedShards[:count])
	sm.ownedShards = append([]ShardOwnership(nil), sm.ownedShards[count:]...)
	sm.mu.Unlock()

	// Get unique top-level shard IDs to release
	uniqueShardIDs := make(map[int]bool)
	for _, ownership := range releasedShards {
		uniqueShardIDs[ownership.ShardID] = true
	}

	shardIDs := make([]int, 0, len(uniqueShardIDs))
	for shardID := range uniqueShardIDs {
		shardIDs = append(shardIDs, shardID)
	}

	// Release shards via repository
	if err := sm.repository.ReleaseShardsWithIDs(ctx, shardIDs, sm.controllerID); err != nil {
		sm.logger.Errorf("Failed to release shards: %v", err)
		return err
	}

	sm.logger.Debugf("✅ Released %d logical shards across %d top-level shards", len(releasedShards), len(shardIDs))

	// Renew leases for remaining shards
	return sm.renewOwnedShardLeases(ctx)
}

// rebalanceScaleUp acquires additional shards when scaling up
func (sm *ShardManager) rebalanceScaleUp(ctx context.Context, needed int) error {
	sm.logger.Infof("🔄 Rebalancing: Acquiring %d more logical shards (current: %d, target: %d)",
		needed, len(sm.ownedShards), sm.targetShardCount)

	// Find available shards via repository
	limit := needed / 8
	if limit == 0 {
		limit = 1
	}

	shards, err := sm.repository.FindAvailableShards(ctx, sm.controllerID, limit)
	if err != nil {
		return fmt.Errorf("failed to find available shards: %w", err)
	}

	if len(shards) == 0 {
		sm.logger.Debugf("No available shards to acquire")
		return sm.renewOwnedShardLeases(ctx)
	}

	// Build candidate list and shard IDs to acquire
	candidateShards := []ShardOwnership{}
	shardIDs := []int{}
	processedTopLevel := make(map[int]bool)

	for _, shard := range shards {
		if processedTopLevel[shard.ShardID] {
			continue
		}
		processedTopLevel[shard.ShardID] = true
		shardIDs = append(shardIDs, shard.ShardID)

		// For this top-level shard, we logically own all 8 sub-shards
		for subShardID := 0; subShardID < 8; subShardID++ {
			candidateShards = append(candidateShards, ShardOwnership{
				ShardID:    shard.ShardID,
				SubShardID: subShardID,
			})

			if len(candidateShards) >= needed {
				break
			}
		}

		if len(candidateShards) >= needed {
			break
		}
	}

	// Acquire shards via repository with retry
	modifiedCount, err := sm.repository.AcquireShardsWithRetry(ctx, shardIDs, sm.controllerID, models.GSLBShardLeaseTTL, 3)
	if err != nil {
		return err
	}

	if modifiedCount == 0 {
		sm.logger.Debugf("No shards acquired (all owned by other controllers)")
		return sm.renewOwnedShardLeases(ctx)
	}

	// Verify which shards were successfully acquired
	newShards, err := sm.verifyShardsAcquired(ctx, candidateShards)
	if err != nil {
		return err
	}

	// Update owned shards atomically
	sm.mu.Lock()
	if len(newShards) > 0 {
		sm.ownedShards = append(sm.ownedShards, newShards...)
	}
	totalOwned := len(sm.ownedShards)
	// Take snapshot for renewal (prevents race with concurrent modifications)
	ownedSnapshot := make([]ShardOwnership, totalOwned)
	copy(ownedSnapshot, sm.ownedShards)
	sm.mu.Unlock()

	if len(newShards) > 0 {
		sm.logger.Infof("✅ Acquired %d logical shards (total owned: %d)", len(newShards), totalOwned)

		// Emit shard acquisition event
		select {
		case sm.shardAcquired <- totalOwned:
			sm.logger.Debugf("🔔 Shard acquisition event emitted after rebalance")
		default:
			sm.logger.Warn("⚠️  Shard acquisition event channel full")
		}
	} else {
		sm.logger.Debugf("No shards acquired (all owned by other controllers)")
	}

	// Renew leases using snapshot (prevents TOCTOU race)
	return sm.renewOwnedShardLeasesFromSnapshot(ctx, ownedSnapshot)
}

// verifyShardsAcquired checks which shards from the candidate list were successfully acquired
func (sm *ShardManager) verifyShardsAcquired(
	ctx context.Context,
	candidateShards []ShardOwnership,
) ([]ShardOwnership, error) {
	// Get unique top-level shard IDs we tried to acquire
	topLevelShards := make(map[int]bool)
	for _, candidate := range candidateShards {
		topLevelShards[candidate.ShardID] = true
	}

	candidateShardIDs := make([]int, 0, len(topLevelShards))
	for shardID := range topLevelShards {
		candidateShardIDs = append(candidateShardIDs, shardID)
	}

	// Verify ownership via repository
	ownedShardIDs, err := sm.repository.VerifyShardsOwned(ctx, candidateShardIDs, sm.controllerID)
	if err != nil {
		return nil, err
	}

	// Build set for quick lookup
	acquiredTopLevel := make(map[int]bool)
	for _, shardID := range ownedShardIDs {
		acquiredTopLevel[shardID] = true
	}

	// For each acquired top-level shard, add all candidate sub-shards
	newShards := []ShardOwnership{}
	for _, candidate := range candidateShards {
		if acquiredTopLevel[candidate.ShardID] {
			newShards = append(newShards, candidate)
		}
	}

	return newShards, nil
}

// renewOwnedShardLeases renews leases for all currently owned shards
func (sm *ShardManager) renewOwnedShardLeases(ctx context.Context) error {
	sm.mu.RLock()
	ownedShards := make([]ShardOwnership, len(sm.ownedShards))
	copy(ownedShards, sm.ownedShards)
	sm.mu.RUnlock()

	return sm.renewOwnedShardLeasesFromSnapshot(ctx, ownedShards)
}

// renewOwnedShardLeasesFromSnapshot renews leases for a snapshot of owned shards
// Used to prevent TOCTOU race conditions when ownedShards might be modified concurrently
func (sm *ShardManager) renewOwnedShardLeasesFromSnapshot(ctx context.Context, ownedShards []ShardOwnership) error {
	if len(ownedShards) == 0 {
		return nil // No shards to renew
	}

	// Get unique top-level shard IDs
	uniqueShardIDs := make(map[int]bool)
	for _, ownership := range ownedShards {
		uniqueShardIDs[ownership.ShardID] = true
	}

	shardIDs := make([]int, 0, len(uniqueShardIDs))
	for shardID := range uniqueShardIDs {
		shardIDs = append(shardIDs, shardID)
	}

	// Renew leases via repository
	modifiedCount, err := sm.repository.RenewLeases(ctx, shardIDs, sm.controllerID, models.GSLBShardLeaseTTL)
	if err != nil {
		return err
	}

	sm.logger.Debugf("Renewed leases for %d top-level shards (%d documents updated)",
		len(shardIDs), modifiedCount)

	return nil
}

// releaseShards releases all owned shards during shutdown
func (sm *ShardManager) releaseShards() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sm.mu.RLock()
	ownedCount := len(sm.ownedShards)
	sm.mu.RUnlock()

	if ownedCount == 0 {
		sm.logger.Infof("No shards to release")
		return
	}

	// Release all shards via repository
	if err := sm.repository.ReleaseAllShards(ctx, sm.controllerID); err != nil {
		sm.logger.Errorf("Failed to release shards: %v", err)
	}

	sm.mu.Lock()
	sm.ownedShards = []ShardOwnership{}
	sm.mu.Unlock()
}

// calculateTargetShards calculates how many logical shards each controller should own
// With two-tier sharding: total logical shards = 128 × 8 = 1,024
func (sm *ShardManager) calculateTargetShards(ctx context.Context) error {
	// Count active controllers via repository
	controllerCount, err := sm.repository.CountActiveControllers(ctx)
	if err != nil {
		return fmt.Errorf("failed to count active controllers: %w", err)
	}

	// Always include ourselves
	if controllerCount == 0 {
		controllerCount = 1 // At least one controller (us)
	}

	// Calculate target: ceil(total_logical_shards / controller_count)
	// Total logical shards = 128 × 8 = 1,024
	totalLogicalShards := models.GSLBNumShards * 8
	sm.targetShardCount = (totalLogicalShards + controllerCount - 1) / controllerCount

	// DISABLED: Too verbose - runs every 30s
	// sm.logger.Debugf("📊 Target calculation: %d controllers active, target %d logical shards/controller (total: %d)",
	// 	controllerCount, sm.targetShardCount, totalLogicalShards)
	return nil
}

// InitializeShards creates all shards in MongoDB if they don't exist
// Uses BulkWrite for optimal performance (128 operations in 1 request instead of 128 separate requests)
func InitializeShards(ctx context.Context, db *mongo.Database, logger *logger.Logger) error {
	collection := db.Collection("gslb_shards")

	// Build bulk write operations
	bulkOps := make([]mongo.WriteModel, 0, models.GSLBNumShards)
	now := time.Now()
	expiredTime := now.Add(-1 * time.Hour) // Expired lease

	for i := 0; i < models.GSLBNumShards; i++ {
		filter := bson.M{"shard_id": i}
		update := bson.M{
			"$setOnInsert": bson.M{
				"shard_id":       i,
				"controller_id":  "",
				"lease_expiry":   expiredTime,
				"last_heartbeat": now,
			},
		}

		op := mongo.NewUpdateOneModel().
			SetFilter(filter).
			SetUpdate(update).
			SetUpsert(true)

		bulkOps = append(bulkOps, op)
	}

	// Execute BulkWrite (128 operations in single MongoDB request)
	_, err := collection.BulkWrite(ctx, bulkOps)
	if err != nil {
		return fmt.Errorf("failed to initialize shards via BulkWrite: %w", err)
	}

	logger.Infof("✅ Initialized %d GSLB shards via BulkWrite (1,024 logical shards with 8 sub-shards each)", models.GSLBNumShards)
	return nil
}

// normalizeFQDN ensures consistent FQDN format for shard calculation
// This must match the normalization in controller/crud/xds/set_xds.go and controller/handlers/gslb.go
func normalizeFQDN(fqdn string) string {
	normalized := strings.ToLower(strings.TrimSpace(fqdn))
	if !strings.HasSuffix(normalized, ".") {
		normalized += "."
	}
	return normalized
}

// CalculateShardID calculates the top-level shard ID (0-127) for a given FQDN
func CalculateShardID(fqdn string) int {
	normalized := normalizeFQDN(fqdn)
	h := fnv.New32a()
	h.Write([]byte(normalized))
	return int(h.Sum32() % uint32(models.GSLBNumShards))
}

// CalculateSubShardID calculates the sub-shard ID (0-7) for a given FQDN+IP combination
func CalculateSubShardID(fqdn, ip string) int {
	normalized := normalizeFQDN(fqdn)
	combined := normalized + ip
	h := fnv.New32a()
	h.Write([]byte(combined))
	return int(h.Sum32() % 8)
}

// GetLogicalShardID returns the logical shard ID (0-1023) from shard_id and sub_shard_id
// Formula: shard_id * 8 + sub_shard_id
func GetLogicalShardID(shardID, subShardID int) int {
	return shardID*8 + subShardID
}
