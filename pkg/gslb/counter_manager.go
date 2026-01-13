package gslb

import (
	"fmt"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// IPHealthCounter tracks consecutive probe results (in-memory only, not persisted)
type IPHealthCounter struct {
	ConsecutiveFailures  int
	ConsecutiveSuccesses int
	LastAccessed         time.Time // Track when counter was last used for cleanup
}

// CounterManager manages in-memory failure/success counters for IP health tracking
// Counters are NOT persisted to MongoDB - they're rebuilt on controller restart
// from the persisted HealthState
type CounterManager struct {
	counters map[string]*IPHealthCounter
	mu       sync.RWMutex
	logger   *logger.Logger

	// Cleanup state
	stopCleanup chan struct{}
	cleanupDone chan struct{}
}

// NewCounterManager creates a new counter manager instance
func NewCounterManager(logger *logger.Logger) *CounterManager {
	cm := &CounterManager{
		counters:    make(map[string]*IPHealthCounter),
		logger:      logger,
		stopCleanup: make(chan struct{}),
		cleanupDone: make(chan struct{}),
	}

	// Start periodic cleanup goroutine (every 1 hour)
	go cm.periodicCleanup()

	return cm
}

// buildKey creates a unique counter key from record ID and IP
func (cm *CounterManager) buildKey(recordID primitive.ObjectID, ip string) string {
	return fmt.Sprintf("%s:%s", recordID.Hex(), ip)
}

// BackoffInfo holds backoff state for manual reset detection
type BackoffInfo struct {
	CurrentBackoff int64
	BackoffUntil   time.Time
}

// GetOrInitialize retrieves existing counter or initializes new one based on current health state
// Returns (counter, isNewlyCreated, isManualReset)
func (cm *CounterManager) GetOrInitialize(
	recordID primitive.ObjectID,
	ip string,
	currentState models.HealthState,
	probe *models.GSLBProbe,
	backoffInfo BackoffInfo,
	manualResetAt time.Time, // Admin manual state change timestamp
) (*IPHealthCounter, bool, bool) {
	key := cm.buildKey(recordID, ip)

	cm.mu.Lock()
	defer cm.mu.Unlock()

	counter, exists := cm.counters[key]
	if !exists {
		// First probe after controller start - initialize counter
		counter = &IPHealthCounter{
			LastAccessed: time.Now(),
		}

		// Detect manual reset - check if admin recently changed state (within last 60s)
		isManualReset := !manualResetAt.IsZero() && time.Since(manualResetAt) < 60*time.Second

		if probe != nil {
			// ✅ CRITICAL FIX: Always initialize counter based on current state
			// For manual reset: Use state admin set (CRITICAL → 3, WARNING → 1, PASSING → 0)
			// For normal init: Infer from persisted state after controller restart
			counter.ConsecutiveFailures = cm.inferFailureCount(currentState, probe)
			if isManualReset {
				cm.logger.Debugf("Manual reset detected for %s - initializing counter to %d failures (state: %s)",
					ip, counter.ConsecutiveFailures, currentState.String())
			}
		}
		// else: No probe - start at 0

		cm.counters[key] = counter
		// Counter initialized silently (manual resets logged in caller)
		return counter, true, isManualReset
	}

	// Counter already exists (might have been created by Update during race condition)
	// DON'T override the counter - respect what Update already set
	// Just update access time and check for manual reset

	// Counter exists - update access time for cleanup tracking
	counter.LastAccessed = time.Now()

	// Check for manual reset - admin recently changed state (within last 60s)
	isManualReset := !manualResetAt.IsZero() && time.Since(manualResetAt) < 60*time.Second

	// ✅ CRITICAL FIX: If manual reset detected, set counter based on manual state
	// This ensures counter matches what admin set via API
	if isManualReset && probe != nil {
		newFailureCount := cm.inferFailureCount(currentState, probe)
		cm.logger.Debugf("Manual reset detected for %s (changed %v ago) - updating counter from %d to %d failures (state: %s)",
			ip, time.Since(manualResetAt).Round(time.Second), counter.ConsecutiveFailures, newFailureCount, currentState.String())
		counter.ConsecutiveFailures = newFailureCount
		counter.ConsecutiveSuccesses = 0
	}

	return counter, false, isManualReset
}

// inferFailureCount estimates consecutive failures based on persisted health state
// This allows counter to survive controller restart without losing progress
func (cm *CounterManager) inferFailureCount(state models.HealthState, probe *models.GSLBProbe) int {
	if probe == nil {
		return 0
	}

	switch state {
	case models.HealthStateCritical:
		return probe.CriticalThreshold
	case models.HealthStateWarning:
		return probe.WarningThreshold
	default: // HealthStatePassing
		return 0
	}
}

// Update increments counter based on probe result
// Returns (consecutiveFailures, consecutiveSuccesses)
// CRITICAL: Update is now safe to call even if GetOrInitialize wasn't called yet
// This prevents race condition where probe result arrives before counter initialization
func (cm *CounterManager) Update(recordID primitive.ObjectID, ip string, success bool) (int, int) {
	key := cm.buildKey(recordID, ip)

	cm.mu.Lock()
	defer cm.mu.Unlock()

	counter, exists := cm.counters[key]
	if !exists {
		// Race condition protection: Counter not initialized yet
		// Initialize at 0/0 to prevent incorrect state inference
		// NOTE: GetOrInitialize will be called later and won't override this
		cm.logger.Debugf("Counter not found for %s during Update (race condition), initializing at 0/0", ip)
		counter = &IPHealthCounter{
			LastAccessed: time.Now(),
		}
		cm.counters[key] = counter
	} else {
		// Update access time for cleanup tracking
		counter.LastAccessed = time.Now()
	}

	if success {
		counter.ConsecutiveSuccesses++
		counter.ConsecutiveFailures = 0
	} else {
		counter.ConsecutiveFailures++
		counter.ConsecutiveSuccesses = 0
	}

	return counter.ConsecutiveFailures, counter.ConsecutiveSuccesses
}

// Reset manually resets a counter to zero (used for testing or admin operations)
func (cm *CounterManager) Reset(recordID primitive.ObjectID, ip string) {
	key := cm.buildKey(recordID, ip)

	cm.mu.Lock()
	defer cm.mu.Unlock()

	if counter, exists := cm.counters[key]; exists {
		counter.ConsecutiveFailures = 0
		counter.ConsecutiveSuccesses = 0
	}
}

// GetStats returns current counter values (for debugging/monitoring)
func (cm *CounterManager) GetStats(recordID primitive.ObjectID, ip string) (failures, successes int, exists bool) {
	key := cm.buildKey(recordID, ip)

	cm.mu.RLock()
	defer cm.mu.RUnlock()

	counter, exists := cm.counters[key]
	if !exists {
		return 0, 0, false
	}

	return counter.ConsecutiveFailures, counter.ConsecutiveSuccesses, true
}

// periodicCleanup runs in background and removes stale counters every 5 minutes
// Counters not accessed in 10 minutes are considered stale (deleted IPs)
func (cm *CounterManager) periodicCleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	defer close(cm.cleanupDone)

	for {
		select {
		case <-ticker.C:
			cm.cleanupStaleCounters()
		case <-cm.stopCleanup:
			return
		}
	}
}

// cleanupStaleCounters removes counters not accessed in 10 minutes
// This prevents memory leaks from deleted IPs/records
// 10 minutes is safe: even 300s (5min) interval IPs are accessed every 5 min
func (cm *CounterManager) cleanupStaleCounters() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	threshold := time.Now().Add(-10 * time.Minute)
	removed := 0

	for key, counter := range cm.counters {
		if counter.LastAccessed.Before(threshold) {
			delete(cm.counters, key)
			removed++
		}
	}

	if removed > 0 {
		cm.logger.Infof("🧹 Cleaned up %d stale counters (total remaining: %d)", removed, len(cm.counters))
	}
}

// Stop gracefully stops the cleanup goroutine
func (cm *CounterManager) Stop() {
	close(cm.stopCleanup)
	<-cm.cleanupDone
}
