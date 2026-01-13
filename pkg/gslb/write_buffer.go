package gslb

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// HealthStateUpdate represents a single health state update to be written to MongoDB
// These updates are batched together for performance
type HealthStateUpdate struct {
	RecordID         primitive.ObjectID // Needed for unique (record_id, ip) filter
	IP               string
	HealthState      models.HealthState
	BackoffUntil     time.Time
	CurrentBackoff   int64
	ResponseCode     int
	ResponseTime     float64
	LastStatusChange time.Time
	Timestamp        time.Time
	ProbeType        string // "http", "https", "tcp" - needed to determine if ResponseCode should be persisted
	ErrorMessage     string // Error message for failed probes (stored in history for troubleshooting)
	ClearManualReset bool   // If true, clear manual_reset_at field (used after manual reset detection)
}

// WriteBuffer batches health state updates for MongoDB bulk write operations
// This reduces MongoDB write load by 25x (100 updates = 1 BulkWrite instead of 100 individual writes)
type WriteBuffer struct {
	// Configuration
	maxSize       int           // Maximum buffer size before forcing flush (default: 100)
	flushInterval time.Duration // Periodic flush interval (default: 5s)

	// Buffer state
	updates []HealthStateUpdate
	mu      sync.Mutex

	// MongoDB
	db     *mongo.Database
	logger *logger.Logger

	// Context for cancellation propagation
	ctx context.Context

	// Metrics tracking (atomic counters)
	flushCount       int64   // Total flush operations
	totalUpdates     int64   // Total updates written
	flushErrors      int64   // Total flush errors
	flushDurationSum float64 // Sum of flush durations (for average calculation)
	flushDurationMu  sync.Mutex

	// Control channels
	stopCh chan struct{}
	doneCh chan struct{}

	// PERFORMANCE FIX: Batched immediate writes (non-blocking)
	immediateQueue     chan HealthStateUpdate // Queue for immediate writes
	immediateFlushDone chan struct{}          // Signals immediate flush goroutine stopped
}

// NewWriteBuffer creates a new write buffer instance with periodic flushing
//
// Parameters:
//   - ctx: Parent context for cancellation propagation
//   - db: MongoDB database instance
//   - logger: Logger instance
//   - maxSize: Maximum buffer size before forcing flush (recommended: 100)
//   - flushInterval: Periodic flush interval (recommended: 5s)
func NewWriteBuffer(ctx context.Context, db *mongo.Database, logger *logger.Logger, maxSize int, flushInterval time.Duration) *WriteBuffer {
	wb := &WriteBuffer{
		maxSize:       maxSize,
		flushInterval: flushInterval,
		updates:       make([]HealthStateUpdate, 0, maxSize),
		db:            db,
		logger:        logger,
		ctx:           ctx, // Store parent context for shutdown propagation
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),

		// PERFORMANCE FIX: Batched immediate writes
		immediateQueue:     make(chan HealthStateUpdate, 1000), // Buffered queue for immediate writes
		immediateFlushDone: make(chan struct{}),
	}

	// Start periodic flush goroutine
	go wb.periodicFlush()

	// PERFORMANCE FIX: Start immediate write processor (batches immediate writes)
	go wb.processImmediateWrites()

	return wb
}

// Add queues a health state update for batched write
// This is a non-blocking operation (buffered in memory)
//
// Note: If buffer is full (>= maxSize), triggers immediate flush
func (wb *WriteBuffer) Add(update HealthStateUpdate) {
	wb.mu.Lock()

	// Add to buffer
	wb.updates = append(wb.updates, update)

	// If buffer full, flush immediately (outside of lock to avoid blocking)
	if len(wb.updates) >= wb.maxSize {
		wb.logger.Debugf("Write buffer full (%d updates), triggering immediate flush", len(wb.updates))
		// Copy updates and release lock before flush
		updatesCopy := make([]HealthStateUpdate, len(wb.updates))
		copy(updatesCopy, wb.updates)
		wb.updates = wb.updates[:0] // Clear buffer

		wb.mu.Unlock()
		wb.flushUpdates(updatesCopy)
		return // Early return - no defer unlock needed
	}

	wb.mu.Unlock()
}

// periodicFlush runs in background goroutine and flushes buffer every N seconds
func (wb *WriteBuffer) periodicFlush() {
	ticker := time.NewTicker(wb.flushInterval)
	defer ticker.Stop()
	defer close(wb.doneCh)

	for {
		select {
		case <-ticker.C:
			wb.flush()
		case <-wb.stopCh:
			// Final flush before shutdown
			wb.flush()
			wb.logger.Info("Write buffer periodic flush stopped")
			return
		}
	}
}

// flush flushes current buffer to MongoDB (called periodically or when buffer full)
func (wb *WriteBuffer) flush() {
	wb.mu.Lock()
	if len(wb.updates) == 0 {
		wb.mu.Unlock()
		return
	}

	// Copy updates and clear buffer
	updatesCopy := make([]HealthStateUpdate, len(wb.updates))
	copy(updatesCopy, wb.updates)
	wb.updates = wb.updates[:0] // Clear buffer
	wb.mu.Unlock()

	// Flush outside of lock
	wb.flushUpdates(updatesCopy)
}

// buildUpdateDocument builds MongoDB update document for health state update
// Extracted common logic to avoid duplication between flush and immediate write
func (wb *WriteBuffer) buildUpdateDocument(update HealthStateUpdate) bson.M {
	// Special case: Clear manual_reset_at field AND backoff fields (used after manual reset detection)
	// CRITICAL FIX: Must also write BackoffUntil=0 and CurrentBackoff=0 to clear backoff state
	if update.ClearManualReset {
		return bson.M{
			"$set": bson.M{
				"updated_at":      update.Timestamp,
				"backoff_until":   update.BackoffUntil,   // Write zero time to clear backoff
				"current_backoff": update.CurrentBackoff, // Write 0 to reset backoff duration
			},
			"$unset": bson.M{
				"manual_reset_at": "", // Clear the manual reset timestamp
			},
		}
	}

	// Build $set document with health state fields
	setDoc := bson.M{
		"health_state":    update.HealthState,
		"updated_at":      update.Timestamp,
		"backoff_until":   update.BackoffUntil,   // Always write backoff fields (including zero values for reset)
		"current_backoff": update.CurrentBackoff, // This ensures backoff is cleared when IP recovers
	}

	// Add optional fields if they have values
	if !update.LastStatusChange.IsZero() {
		setDoc["last_status_change"] = update.LastStatusChange
	}

	// Build final update document with $set at top level
	finalUpdate := bson.M{"$set": setDoc}

	// Always add history entry for state changes
	// Add history for ANY probe result (success or failure)
	historyEntry := models.GSLBStatusHistory{
		State:        update.HealthState.String(),
		DateTime:     update.Timestamp,
		ResponseTime: update.ResponseTime,
	}

	// Only include ResponseCode for HTTP/HTTPS probes, NOT for TCP
	// TCP uses ResponseCode=200 internally as success marker but shouldn't persist it
	if update.ProbeType != "tcp" && update.ResponseCode > 0 {
		historyEntry.ResponseCode = update.ResponseCode
	}

	// Include error message if probe failed (helps with troubleshooting)
	if update.ErrorMessage != "" {
		historyEntry.ErrorMessage = update.ErrorMessage
	}

	finalUpdate["$push"] = bson.M{
		"status_history": bson.M{
			"$each":  []models.GSLBStatusHistory{historyEntry},
			"$slice": -models.GSLBMaxStatusHistorySize, // Keep only last 50
		},
	}

	return finalUpdate
}

// flushUpdates performs the actual MongoDB BulkWrite operation with retry logic
func (wb *WriteBuffer) flushUpdates(updates []HealthStateUpdate) {
	if len(updates) == 0 {
		return
	}

	// Skip flush if database is not configured (e.g., in unit tests)
	if wb.db == nil {
		wb.logger.Debugf("Skipping flush of %d updates (no database configured)", len(updates))
		return
	}

	// Track flush start time for metrics
	flushStart := time.Now()

	collection := wb.db.Collection("gslb_ip_health")

	// Build bulk write operations
	bulkOps := make([]mongo.WriteModel, 0, len(updates))

	for _, update := range updates {
		// Use shared helper to build update document
		finalUpdate := wb.buildUpdateDocument(update)

		// Create update operation
		// Filter by BOTH record_id AND ip to prevent cross-contamination
		// Without record_id, updates for one record affect ALL records using the same IP
		filter := bson.M{
			"record_id": update.RecordID,
			"ip":        update.IP,
		}

		op := mongo.NewUpdateOneModel().
			SetFilter(filter).
			SetUpdate(finalUpdate)

		bulkOps = append(bulkOps, op)
	}

	// Execute BulkWrite with exponential backoff retry (3 attempts)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		// Use wb.ctx as parent for proper cancellation on shutdown
		ctx, cancel := context.WithTimeout(wb.ctx, 10*time.Second)
		_, err := collection.BulkWrite(ctx, bulkOps)
		cancel()

		if err == nil {
			// Track flush metrics
			atomic.AddInt64(&wb.flushCount, 1)
			atomic.AddInt64(&wb.totalUpdates, int64(len(updates)))

			flushDuration := time.Since(flushStart).Seconds()
			wb.flushDurationMu.Lock()
			wb.flushDurationSum += flushDuration
			wb.flushDurationMu.Unlock()

			return
		}

		lastErr = err

		// Exponential backoff: 1s, 2s, 4s
		if attempt < 2 {
			backoff := time.Duration(math.Pow(2, float64(attempt))) * time.Second
			wb.logger.Warnf("BulkWrite failed (attempt %d/3), retrying in %v: %v", attempt+1, backoff, err)
			time.Sleep(backoff)
		}
	}

	// After 3 failures, log error and drop updates
	// This is acceptable to prevent blocking health checker
	atomic.AddInt64(&wb.flushErrors, 1)
	wb.logger.Errorf("❌ Failed to flush %d updates after 3 attempts: %v", len(updates), lastErr)
}

// FlushImmediate queues an update for immediate (batched) write
// PERFORMANCE FIX: Non-blocking async operation (was synchronous before)
// Updates are batched together in dedicated processor goroutine (50ms window)
// This prevents blocking the result processor while maintaining fast writes
func (wb *WriteBuffer) FlushImmediate(ctx context.Context, update HealthStateUpdate) error {
	// ✅ CRITICAL FIX: Remove pending buffered writes for this IP BEFORE immediate write
	// Problem: Batch buffer may contain old state (e.g., PASSING→WARNING)
	// Timeline:
	//   1. PASSING→WARNING added to batch buffer (5s delay)
	//   2. WARNING monitoring executes immediate probes
	//   3. WARNING→CRITICAL immediate write succeeds
	//   4. 5s timer fires → old WARNING entry flushes → overrides CRITICAL!
	// Solution: Clear pending entries for this IP before immediate write
	wb.RemovePending(update.RecordID, update.IP)

	// ✅ CRITICAL FIX: Always use synchronous write for FlushImmediate
	// Problem: Queue-based approach has 0-50ms delay (ticker-based flush)
	// This causes race conditions in WARNING monitoring:
	//   1. CRITICAL state transition calls FlushImmediate()
	//   2. Update goes to queue (not written yet)
	//   3. monitorWarningState() checks DB → sees stale WARNING state
	//   4. Loop continues (should have stopped)
	//   5. 50ms later, update is flushed → history shows CRITICAL but state field shows WARNING
	//
	// Solution: Use synchronous write to ensure DB is updated BEFORE returning
	// This ensures subsequent DB checks see the fresh CRITICAL state
	return wb.flushImmediateSync(ctx, update)
}

// RemovePending removes all pending buffered updates for a specific IP
// This prevents old buffered writes from overriding newer immediate writes
// CRITICAL for manual reset and WARNING monitoring scenarios
func (wb *WriteBuffer) RemovePending(recordID primitive.ObjectID, ip string) int {
	wb.mu.Lock()
	defer wb.mu.Unlock()

	removed := 0
	newUpdates := make([]HealthStateUpdate, 0, len(wb.updates))

	for _, update := range wb.updates {
		// Keep updates for different IPs or records
		if update.RecordID != recordID || update.IP != ip {
			newUpdates = append(newUpdates, update)
		} else {
			removed++
		}
	}

	wb.updates = newUpdates

	if removed > 0 {
		wb.logger.Debugf("🧹 Removed %d pending buffered updates for IP %s (record: %s) before immediate write",
			removed, ip, recordID.Hex()[:8])
	}

	return removed
}

// flushImmediateSync performs synchronous immediate write (fallback only)
// This is the old implementation, now used only when queue is full
func (wb *WriteBuffer) flushImmediateSync(ctx context.Context, update HealthStateUpdate) error {
	collection := wb.db.Collection("gslb_ip_health")

	// Reuse shared helper to build update document
	finalUpdate := wb.buildUpdateDocument(update)

	// Filter by BOTH record_id AND ip to prevent cross-contamination
	filter := bson.M{
		"record_id": update.RecordID,
		"ip":        update.IP,
	}

	result, err := collection.UpdateOne(ctx, filter, finalUpdate)

	if err != nil {
		wb.logger.Errorf("Immediate flush failed for IP %s: %v", update.IP, err)
		return err
	}

	// Log if no document was matched (silent failure detection)
	if result.MatchedCount == 0 {
		wb.logger.Warnf("⚠️  ImmediateFlush: No document matched filter (record_id=%s, ip=%s)",
			update.RecordID.Hex(), update.IP)
	}

	return nil
}

// processImmediateWrites processes queued immediate writes in batches
// PERFORMANCE FIX: Batches immediate writes instead of writing one-by-one
// Flushes when: (1) batch reaches 100 updates, OR (2) 50ms timeout
// This reduces MongoDB write overhead while maintaining low latency
func (wb *WriteBuffer) processImmediateWrites() {
	defer close(wb.immediateFlushDone)

	batch := make([]HealthStateUpdate, 0, 100)
	ticker := time.NewTicker(50 * time.Millisecond) // 50ms max delay
	defer ticker.Stop()

	wb.logger.Infof("Immediate write processor started (batch size: 100, max delay: 50ms)")

	for {
		select {
		case update, ok := <-wb.immediateQueue:
			if !ok {
				// Channel closed - flush remaining batch and exit
				if len(batch) > 0 {
					wb.logger.Infof("Flushing final immediate batch: %d updates", len(batch))
					wb.flushUpdates(batch)
				}
				wb.logger.Infof("Immediate write processor stopped")
				return
			}

			// Add to batch
			batch = append(batch, update)

			// Flush if batch is full
			if len(batch) >= 100 {
				wb.flushUpdates(batch)
				batch = batch[:0] // Reset batch
			}

		case <-ticker.C:
			// Periodic flush (50ms timeout)
			if len(batch) > 0 {
				wb.flushUpdates(batch)
				batch = batch[:0] // Reset batch
			}

		case <-wb.stopCh:
			// Shutdown requested - flush remaining batch and exit
			if len(batch) > 0 {
				wb.logger.Infof("Shutdown: Flushing immediate batch (%d updates)", len(batch))
				wb.flushUpdates(batch)
			}
			wb.logger.Infof("Immediate write processor stopped (shutdown)")
			return
		}
	}
}

// FlushSync synchronously flushes the current buffer
// Used during graceful shutdown to ensure no data loss
func (wb *WriteBuffer) FlushSync() {
	wb.logger.Info("Synchronous flush of write buffer")
	wb.flush()
}

// Stop gracefully stops the write buffer
// Stops periodic flush and performs final flush
func (wb *WriteBuffer) Stop() {
	wb.logger.Info("Stopping write buffer...")

	// Signal shutdown to both processors
	close(wb.stopCh)

	// Close immediate queue to signal processor to stop
	close(wb.immediateQueue)

	// Wait for both processors to finish
	<-wb.doneCh               // Wait for periodic flush to stop
	<-wb.immediateFlushDone   // Wait for immediate processor to stop

	wb.logger.Info("Write buffer stopped (both processors finished)")
}

// Size returns the current number of buffered updates
func (wb *WriteBuffer) Size() int {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	return len(wb.updates)
}

// Stats returns buffer statistics for monitoring
type BufferStats struct {
	CurrentSize      int
	MaxSize          int
	Capacity         float64 // Percentage of buffer used (0-100)
	FlushCount       int64   // Total flush operations
	TotalUpdates     int64   // Total updates written
	FlushErrors      int64   // Total flush errors
	AvgFlushDuration float64 // Average flush duration in seconds
}

// GetStats returns current buffer statistics
func (wb *WriteBuffer) GetStats() BufferStats {
	wb.mu.Lock()
	size := len(wb.updates)
	wb.mu.Unlock()

	flushCount := atomic.LoadInt64(&wb.flushCount)
	totalUpdates := atomic.LoadInt64(&wb.totalUpdates)
	flushErrors := atomic.LoadInt64(&wb.flushErrors)

	wb.flushDurationMu.Lock()
	avgDuration := 0.0
	if flushCount > 0 {
		avgDuration = wb.flushDurationSum / float64(flushCount)
	}
	wb.flushDurationMu.Unlock()

	// Calculate capacity with division by zero protection
	capacity := 0.0
	if wb.maxSize > 0 {
		capacity = float64(size) / float64(wb.maxSize) * 100
	}

	return BufferStats{
		CurrentSize:      size,
		MaxSize:          wb.maxSize,
		Capacity:         capacity,
		FlushCount:       flushCount,
		TotalUpdates:     totalUpdates,
		FlushErrors:      flushErrors,
		AvgFlushDuration: avgDuration,
	}
}
