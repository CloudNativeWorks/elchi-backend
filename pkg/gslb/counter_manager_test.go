package gslb

import (
	"testing"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// setupTestLogger initializes logger for tests
func setupTestLogger(t *testing.T) *logger.Logger {
	t.Helper()
	// Initialize global logger (required before NewLogger)
	_ = logger.Init(logger.Config{
		Level:      "debug",
		Format:     "text",
		OutputPath: "stdout",
	})
	return logger.NewLogger("test")
}

// TestCounterManager_Update tests the Update method
func TestCounterManager_Update(t *testing.T) {
	log := setupTestLogger(t)
	cm := NewCounterManager(log)
	defer cm.Stop()

	recordID := primitive.NewObjectID()
	ip := "1.2.3.4"

	// Test consecutive failures
	failures, successes := cm.Update(recordID, ip, false)
	assert.Equal(t, 1, failures, "First failure should count 1")
	assert.Equal(t, 0, successes, "Success count should reset to 0")

	failures, successes = cm.Update(recordID, ip, false)
	assert.Equal(t, 2, failures, "Second failure should count 2")
	assert.Equal(t, 0, successes, "Success count should remain 0")

	failures, successes = cm.Update(recordID, ip, false)
	assert.Equal(t, 3, failures, "Third failure should count 3")
	assert.Equal(t, 0, successes, "Success count should remain 0")

	// Test success resets failures
	failures, successes = cm.Update(recordID, ip, true)
	assert.Equal(t, 0, failures, "Success should reset failure count to 0")
	assert.Equal(t, 1, successes, "First success should count 1")

	// Test consecutive successes
	failures, successes = cm.Update(recordID, ip, true)
	assert.Equal(t, 0, failures, "Failure count should remain 0")
	assert.Equal(t, 2, successes, "Second success should count 2")
}

// TestCounterManager_GetOrInitialize tests counter initialization
func TestCounterManager_GetOrInitialize(t *testing.T) {
	log := setupTestLogger(t)
	cm := NewCounterManager(log)
	defer cm.Stop()

	recordID := primitive.NewObjectID()
	ip := "5.6.7.8"

	probe := &models.GSLBProbe{
		WarningThreshold:  1,
		CriticalThreshold: 2,
	}

	// Test 1: Initialize with PASSING state
	counter, isNew, isManualReset := cm.GetOrInitialize(
		recordID,
		ip,
		models.HealthStatePassing,
		probe,
		BackoffInfo{},
		time.Time{}, // No manual reset
	)

	assert.True(t, isNew, "Counter should be newly created")
	assert.False(t, isManualReset, "Should not be manual reset")
	assert.Equal(t, 0, counter.ConsecutiveFailures, "PASSING should initialize at 0 failures")

	// Test 2: Get existing counter (not newly created)
	counter2, isNew2, _ := cm.GetOrInitialize(
		recordID,
		ip,
		models.HealthStatePassing,
		probe,
		BackoffInfo{},
		time.Time{},
	)

	assert.False(t, isNew2, "Counter should already exist")
	assert.Equal(t, counter, counter2, "Should return same counter instance")
}

// TestCounterManager_GetOrInitialize_CriticalState tests initialization from CRITICAL state
func TestCounterManager_GetOrInitialize_CriticalState(t *testing.T) {
	log := setupTestLogger(t)
	cm := NewCounterManager(log)
	defer cm.Stop()

	recordID := primitive.NewObjectID()
	ip := "10.0.0.1"

	probe := &models.GSLBProbe{
		WarningThreshold:  1,
		CriticalThreshold: 2,
	}

	// Initialize with CRITICAL state (e.g., after controller restart)
	counter, isNew, isManualReset := cm.GetOrInitialize(
		recordID,
		ip,
		models.HealthStateCritical,
		probe,
		BackoffInfo{},
		time.Time{}, // No manual reset
	)

	assert.True(t, isNew, "Counter should be newly created")
	assert.False(t, isManualReset, "Should not be manual reset")
	assert.Equal(t, 2, counter.ConsecutiveFailures, "CRITICAL should infer 2 failures (critical threshold)")
}

// TestCounterManager_ManualReset tests manual reset detection
func TestCounterManager_ManualReset(t *testing.T) {
	log := setupTestLogger(t)
	cm := NewCounterManager(log)
	defer cm.Stop()

	recordID := primitive.NewObjectID()
	ip := "192.168.1.1"

	probe := &models.GSLBProbe{
		WarningThreshold:  1,
		CriticalThreshold: 2,
	}

	// Build up failures
	cm.Update(recordID, ip, false)
	cm.Update(recordID, ip, false)

	// Verify 2 failures
	failures, _, exists := cm.GetStats(recordID, ip)
	assert.True(t, exists, "Counter should exist")
	assert.Equal(t, 2, failures, "Should have 2 failures")

	// Simulate manual reset (admin changed state 5 seconds ago)
	manualResetAt := time.Now().Add(-5 * time.Second)

	counter, isNew, isManualReset := cm.GetOrInitialize(
		recordID,
		ip,
		models.HealthStateCritical, // Admin set to CRITICAL
		probe,
		BackoffInfo{},
		manualResetAt, // Manual reset timestamp
	)

	assert.False(t, isNew, "Counter already exists")
	assert.True(t, isManualReset, "Should detect manual reset")
	// When admin sets state to CRITICAL, counter should match critical threshold
	// This ensures the IP stays in CRITICAL state on next failed probe
	assert.Equal(t, probe.CriticalThreshold, counter.ConsecutiveFailures, "Manual reset to CRITICAL should set failures to critical threshold")
	assert.Equal(t, 0, counter.ConsecutiveSuccesses, "Manual reset should clear success count")

	// Test manual reset to PASSING (should clear counter)
	manualResetAt2 := time.Now().Add(-3 * time.Second)
	counter2, _, isManualReset2 := cm.GetOrInitialize(
		recordID,
		ip,
		models.HealthStatePassing, // Admin set to PASSING
		probe,
		BackoffInfo{},
		manualResetAt2,
	)

	assert.True(t, isManualReset2, "Should detect manual reset to PASSING")
	assert.Equal(t, 0, counter2.ConsecutiveFailures, "Manual reset to PASSING should clear failures")
}

// TestCounterManager_ManualResetExpired tests that old manual resets are ignored
func TestCounterManager_ManualResetExpired(t *testing.T) {
	log := setupTestLogger(t)
	cm := NewCounterManager(log)
	defer cm.Stop()

	recordID := primitive.NewObjectID()
	ip := "172.16.0.1"

	probe := &models.GSLBProbe{
		WarningThreshold:  1,
		CriticalThreshold: 2,
	}

	// Build up failures
	cm.Update(recordID, ip, false)
	cm.Update(recordID, ip, false)

	// Manual reset happened 2 minutes ago (expired - threshold is 60s)
	manualResetAt := time.Now().Add(-2 * time.Minute)

	counter, _, isManualReset := cm.GetOrInitialize(
		recordID,
		ip,
		models.HealthStateCritical,
		probe,
		BackoffInfo{},
		manualResetAt, // Old manual reset
	)

	assert.False(t, isManualReset, "Should NOT detect manual reset (too old)")
	assert.Equal(t, 2, counter.ConsecutiveFailures, "Failure count should remain unchanged")
}

// TestCounterManager_Reset tests the Reset method
func TestCounterManager_Reset(t *testing.T) {
	log := setupTestLogger(t)
	cm := NewCounterManager(log)
	defer cm.Stop()

	recordID := primitive.NewObjectID()
	ip := "10.1.1.1"

	// Build up failures
	cm.Update(recordID, ip, false)
	cm.Update(recordID, ip, false)
	cm.Update(recordID, ip, false)

	// Verify 3 failures
	failures, successes, exists := cm.GetStats(recordID, ip)
	assert.True(t, exists, "Counter should exist")
	assert.Equal(t, 3, failures, "Should have 3 failures")
	assert.Equal(t, 0, successes, "Should have 0 successes")

	// Reset counter
	cm.Reset(recordID, ip)

	// Verify reset
	failures, successes, exists = cm.GetStats(recordID, ip)
	assert.True(t, exists, "Counter should still exist")
	assert.Equal(t, 0, failures, "Failures should be reset to 0")
	assert.Equal(t, 0, successes, "Successes should be reset to 0")
}

// TestCounterManager_GetStats tests the GetStats method
func TestCounterManager_GetStats(t *testing.T) {
	log := setupTestLogger(t)
	cm := NewCounterManager(log)
	defer cm.Stop()

	recordID := primitive.NewObjectID()
	ip := "8.8.8.8"

	// Non-existent counter
	_, _, exists := cm.GetStats(recordID, ip)
	assert.False(t, exists, "Counter should not exist yet")

	// Create counter
	cm.Update(recordID, ip, false)
	cm.Update(recordID, ip, false)

	// Get stats
	failures, successes, exists := cm.GetStats(recordID, ip)
	assert.True(t, exists, "Counter should exist")
	assert.Equal(t, 2, failures, "Should have 2 failures")
	assert.Equal(t, 0, successes, "Should have 0 successes")
}

// TestCounterManager_MultipleIPs tests handling multiple IPs independently
func TestCounterManager_MultipleIPs(t *testing.T) {
	log := setupTestLogger(t)
	cm := NewCounterManager(log)
	defer cm.Stop()

	recordID := primitive.NewObjectID()
	ip1 := "1.1.1.1"
	ip2 := "2.2.2.2"

	// Update IP1 with failures
	cm.Update(recordID, ip1, false)
	cm.Update(recordID, ip1, false)

	// Update IP2 with successes
	cm.Update(recordID, ip2, true)
	cm.Update(recordID, ip2, true)
	cm.Update(recordID, ip2, true)

	// Verify IP1
	failures1, successes1, exists1 := cm.GetStats(recordID, ip1)
	assert.True(t, exists1, "IP1 counter should exist")
	assert.Equal(t, 2, failures1, "IP1 should have 2 failures")
	assert.Equal(t, 0, successes1, "IP1 should have 0 successes")

	// Verify IP2
	failures2, successes2, exists2 := cm.GetStats(recordID, ip2)
	assert.True(t, exists2, "IP2 counter should exist")
	assert.Equal(t, 0, failures2, "IP2 should have 0 failures")
	assert.Equal(t, 3, successes2, "IP2 should have 3 successes")
}

// TestCounterManager_RaceCondition tests Update-before-Initialize race condition handling
func TestCounterManager_RaceCondition(t *testing.T) {
	log := setupTestLogger(t)
	cm := NewCounterManager(log)
	defer cm.Stop()

	recordID := primitive.NewObjectID()
	ip := "100.0.0.1"

	// Simulate race condition: Update called before GetOrInitialize
	// This can happen when probe result arrives before counter initialization
	failures, successes := cm.Update(recordID, ip, false)
	assert.Equal(t, 1, failures, "Update should create counter at 0/0 then increment to 1")
	assert.Equal(t, 0, successes, "Success count should be 0")

	// Now GetOrInitialize is called
	probe := &models.GSLBProbe{
		WarningThreshold:  1,
		CriticalThreshold: 2,
	}

	counter, isNew, _ := cm.GetOrInitialize(
		recordID,
		ip,
		models.HealthStatePassing,
		probe,
		BackoffInfo{},
		time.Time{},
	)

	// GetOrInitialize should find existing counter (created by Update)
	assert.False(t, isNew, "Counter should already exist (created by Update)")
	assert.Equal(t, 1, counter.ConsecutiveFailures, "Failure count should be preserved")
	assert.Equal(t, 0, counter.ConsecutiveSuccesses, "Success count should be 0")
}
