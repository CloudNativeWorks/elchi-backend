package gslb

import (
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
	LastAccessed         time.Time  // Track when counter was last used for cleanup
	mu                   sync.Mutex // Per-counter lock for fine-grained concurrency
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
	stopOnce    sync.Once // Prevents double-close panic on Stop()
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
// Uses shared MakeIPKey helper for consistency across components
func (cm *CounterManager) buildKey(recordID primitive.ObjectID, ip string) string {
	return MakeIPKey(recordID, ip)
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

	// First, get or create the counter under map lock
	cm.mu.Lock()
	counter, exists := cm.counters[key]
	isNewlyCreated := false
	if !exists {
		// First probe after controller start - initialize counter
		counter = &IPHealthCounter{
			LastAccessed: time.Now(),
		}
		cm.counters[key] = counter
		isNewlyCreated = true
	}
	cm.mu.Unlock()

	// Now operate on the counter under per-counter lock (fine-grained locking)
	// This prevents race between Update and GetOrInitialize on the same counter
	counter.mu.Lock()
	defer counter.mu.Unlock()

	// Detect manual reset - check if admin recently changed state (within last 60s)
	isManualReset := !manualResetAt.IsZero() && time.Since(manualResetAt) < 60*time.Second

	if isNewlyCreated {
		if probe != nil {
			// For manual reset: Use state admin set (CRITICAL -> 3, WARNING -> 1, PASSING -> 0)
			// For normal init: Infer from persisted state after controller restart
			counter.ConsecutiveFailures = cm.inferFailureCount(currentState, probe)
			if isManualReset {
				cm.logger.Debugf("Manual reset detected for %s - initializing counter to %d failures (state: %s)",
					ip, counter.ConsecutiveFailures, currentState.String())
			}
		}
		// else: No probe - start at 0
		return counter, true, isManualReset
	}

	// Counter already exists - update access time for cleanup tracking
	counter.LastAccessed = time.Now()

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

	// First, get or create the counter under map lock
	cm.mu.Lock()
	counter, exists := cm.counters[key]
	if !exists {
		// Race condition protection: Counter not initialized yet
		// Initialize at 0/0 to prevent incorrect state inference
		cm.logger.Debugf("Counter not found for %s during Update (race condition), initializing at 0/0", ip)
		counter = &IPHealthCounter{
			LastAccessed: time.Now(),
		}
		cm.counters[key] = counter
	}
	cm.mu.Unlock()

	// Now update the counter under per-counter lock (fine-grained locking)
	// This prevents race between Update and GetOrInitialize on the same counter
	counter.mu.Lock()
	defer counter.mu.Unlock()

	counter.LastAccessed = time.Now()

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

	cm.mu.RLock()
	counter, exists := cm.counters[key]
	cm.mu.RUnlock()

	if exists {
		counter.mu.Lock()
		counter.ConsecutiveFailures = 0
		counter.ConsecutiveSuccesses = 0
		counter.mu.Unlock()
	}
}

// GetStats returns current counter values (for debugging/monitoring)
func (cm *CounterManager) GetStats(recordID primitive.ObjectID, ip string) (failures, successes int, exists bool) {
	key := cm.buildKey(recordID, ip)

	cm.mu.RLock()
	counter, exists := cm.counters[key]
	cm.mu.RUnlock()

	if !exists {
		return 0, 0, false
	}

	counter.mu.Lock()
	failures = counter.ConsecutiveFailures
	successes = counter.ConsecutiveSuccesses
	counter.mu.Unlock()

	return failures, successes, true
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
	threshold := time.Now().Add(-10 * time.Minute)

	// First pass: identify stale counters under read lock
	cm.mu.RLock()
	staleKeys := make([]string, 0)
	for key, counter := range cm.counters {
		// Check LastAccessed under per-counter lock to prevent race
		counter.mu.Lock()
		isStale := counter.LastAccessed.Before(threshold)
		counter.mu.Unlock()

		if isStale {
			staleKeys = append(staleKeys, key)
		}
	}
	cm.mu.RUnlock()

	if len(staleKeys) == 0 {
		return
	}

	// Second pass: remove stale counters under write lock
	cm.mu.Lock()
	removed := 0
	for _, key := range staleKeys {
		// Double-check under lock (counter might have been accessed during our check)
		if counter, exists := cm.counters[key]; exists {
			counter.mu.Lock()
			isStillStale := counter.LastAccessed.Before(threshold)
			counter.mu.Unlock()

			if isStillStale {
				delete(cm.counters, key)
				removed++
			}
		}
	}
	remaining := len(cm.counters) // Read under write lock to prevent data race
	cm.mu.Unlock()

	if removed > 0 {
		cm.logger.Infof("Cleaned up %d stale counters (total remaining: %d)", removed, remaining)
	}
}

// Stop gracefully stops the cleanup goroutine
// Safe to call multiple times (uses sync.Once to prevent double-close panic)
func (cm *CounterManager) Stop() {
	cm.stopOnce.Do(func() {
		close(cm.stopCleanup)
		<-cm.cleanupDone
	})
}
