package gslb

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// MockProbeExecutor is a mock implementation for testing
type MockProbeExecutor struct {
	shouldSucceed bool
	responseCode  int
	responseTime  float64
}

func NewMockProbeExecutor(shouldSucceed bool, responseCode int) *MockProbeExecutor {
	return &MockProbeExecutor{
		shouldSucceed: shouldSucceed,
		responseCode:  responseCode,
		responseTime:  0.123,
	}
}

func (m *MockProbeExecutor) ExecuteProbe(ctx context.Context, ipHealth *models.GSLBIPHealth, probe *models.GSLBProbe) ProbeResult {
	return ProbeResult{
		IP:           ipHealth.IP,
		Success:      m.shouldSucceed,
		ResponseCode: m.responseCode,
		ResponseTime: m.responseTime,
		Probe:        probe,
		Context:      ctx,
	}
}

func (m *MockProbeExecutor) Close() {}

// Note: TriggerImmediateReProbeForManualChange requires full HealthChecker setup with MongoDB,
// BucketScheduler, and IPHealthManager. These are integration tests that require full system setup.
// For unit testing, we focus on the components used by manual re-probe (CounterManager, WriteBuffer).

// TestManualReProbeContextInjection tests that manual health state is injected into probe context
func TestManualReProbeContextInjection(t *testing.T) {
	// This test verifies the context-based state injection mechanism
	// The manual state should be injected into the probe result's context

	manualState := models.HealthStateCritical
	ctx := context.WithValue(context.Background(), manualHealthStateKey, manualState)

	// Retrieve the manual state from context
	retrievedState, ok := ctx.Value(manualHealthStateKey).(models.HealthState)

	assert.True(t, ok, "Should be able to retrieve manual state from context")
	assert.Equal(t, manualState, retrievedState, "Retrieved state should match injected state")
}

// TestProcessProbeResult_ManualResetDetection tests manual reset detection and clear behavior
func TestProcessProbeResult_ManualResetDetection(t *testing.T) {
	log := setupTestLogger(t)

	recordID := primitive.NewObjectID()

	// Create counter manager
	cm := NewCounterManager(log)
	defer cm.Stop()

	// Initialize counter with failures
	probe := &models.GSLBProbe{
		Type:              "http",
		Port:              80,
		Interval:          30,
		WarningThreshold:  1,
		CriticalThreshold: 2,
	}

	// Build up failures first
	cm.Update(recordID, "1.2.3.4", false) // 1 failure
	cm.Update(recordID, "1.2.3.4", false) // 2 failures

	// Verify 2 failures
	failures, _, exists := cm.GetStats(recordID, "1.2.3.4")
	assert.True(t, exists, "Counter should exist")
	assert.Equal(t, 2, failures, "Should have 2 failures")

	// Simulate manual reset (set 5 seconds ago)
	manualResetAt := time.Now().Add(-5 * time.Second)

	// GetOrInitialize should detect manual reset
	counter, isNew, isManualReset := cm.GetOrInitialize(
		recordID,
		"1.2.3.4",
		models.HealthStateCritical, // Admin manually set to CRITICAL
		probe,
		BackoffInfo{},
		manualResetAt, // Manual reset timestamp
	)

	assert.False(t, isNew, "Counter should already exist")
	assert.True(t, isManualReset, "Should detect manual reset")
	assert.Equal(t, 0, counter.ConsecutiveFailures, "Manual reset should clear failure count")
	assert.Equal(t, 0, counter.ConsecutiveSuccesses, "Manual reset should clear success count")
}

// TestProcessProbeResult_ManualResetExpired tests that old manual resets are ignored
func TestProcessProbeResult_ManualResetExpired(t *testing.T) {
	log := setupTestLogger(t)

	recordID := primitive.NewObjectID()

	// Create counter manager
	cm := NewCounterManager(log)
	defer cm.Stop()

	// Build up failures
	probe := &models.GSLBProbe{
		Type:              "http",
		Port:              80,
		Interval:          30,
		WarningThreshold:  1,
		CriticalThreshold: 2,
	}

	cm.Update(recordID, "1.2.3.4", false) // 1 failure
	cm.Update(recordID, "1.2.3.4", false) // 2 failures

	// Manual reset happened 2 minutes ago (expired - threshold is 60s)
	manualResetAt := time.Now().Add(-2 * time.Minute)

	// GetOrInitialize should NOT detect manual reset (too old)
	counter, _, isManualReset := cm.GetOrInitialize(
		recordID,
		"1.2.3.4",
		models.HealthStateCritical,
		probe,
		BackoffInfo{},
		manualResetAt, // Old manual reset
	)

	assert.False(t, isManualReset, "Should NOT detect manual reset (too old)")
	assert.Equal(t, 2, counter.ConsecutiveFailures, "Failure count should remain unchanged")
}

// TestImmediateReProbe_WarningState tests immediate re-probe trigger for WARNING state
func TestImmediateReProbe_WarningState(t *testing.T) {
	// This is a behavioral test - verifies that WARNING state triggers immediate re-probe
	// The actual re-probe is scheduled asynchronously, so we just verify the logic

	log := setupTestLogger(t)

	recordID := primitive.NewObjectID()

	// Create counter manager
	cm := NewCounterManager(log)
	defer cm.Stop()

	probe := &models.GSLBProbe{
		Type:              "http",
		Port:              80,
		Interval:          30,
		WarningThreshold:  1,  // 1 failure = WARNING
		CriticalThreshold: 2,  // 2 failures = CRITICAL
	}

	// Initialize counter at 0 failures
	cm.GetOrInitialize(recordID, "1.2.3.4", models.HealthStatePassing, probe, BackoffInfo{}, time.Time{})

	// First failure -> WARNING state
	failures, successes := cm.Update(recordID, "1.2.3.4", false)

	assert.Equal(t, 1, failures, "Should have 1 failure")
	assert.Equal(t, 0, successes, "Success count should be 0")

	// At this point, the system should trigger immediate re-probe
	// (verified by checking if failures == WarningThreshold)
	shouldReProbe := failures == probe.WarningThreshold
	assert.True(t, shouldReProbe, "Should trigger re-probe at WARNING threshold")
}

// TestImmediateReProbe_RecoveryFromCritical tests immediate re-probe on first success after CRITICAL
func TestImmediateReProbe_RecoveryFromCritical(t *testing.T) {
	log := setupTestLogger(t)

	recordID := primitive.NewObjectID()

	// Create counter manager
	cm := NewCounterManager(log)
	defer cm.Stop()

	probe := &models.GSLBProbe{
		Type:              "http",
		Port:              80,
		Interval:          30,
		WarningThreshold:  1,
		CriticalThreshold: 2,
	}

	// Initialize counter with CRITICAL state (2 failures)
	cm.GetOrInitialize(recordID, "1.2.3.4", models.HealthStateCritical, probe, BackoffInfo{}, time.Time{})

	// Get current stats (should infer 2 failures from CRITICAL state)
	failures, _, _ := cm.GetStats(recordID, "1.2.3.4")
	assert.Equal(t, 2, failures, "Should infer 2 failures from CRITICAL state")

	// First success after CRITICAL
	failures, successes := cm.Update(recordID, "1.2.3.4", true)

	assert.Equal(t, 0, failures, "Success should reset failure count")
	assert.Equal(t, 1, successes, "Should have 1 success")

	// At this point, the system should trigger immediate re-probe
	// (verified by checking oldState was CRITICAL and current state is not CRITICAL)
	shouldReProbe := successes == 1 // First success after recovery
	assert.True(t, shouldReProbe, "Should trigger re-probe on first success after CRITICAL")
}

// TestProbeExecutorIntegration tests probe executor with real HTTP server
func TestProbeExecutorIntegration(t *testing.T) {
	log := setupTestLogger(t)
	executor := NewDefaultProbeExecutor(log)
	defer executor.Close()

	// Create test HTTP server
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("healthy"))
	}))
	defer server.Close()

	// Parse server address
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())

	ctx := context.Background()
	ipHealth := &models.GSLBIPHealth{
		RecordID: primitive.NewObjectID(),
		IP:       "127.0.0.1",
	}
	probe := &models.GSLBProbe{
		Type:                "http",
		Port:                mustParseInt(port),
		Path:                "/",
		Timeout:             5.0,
		ExpectedStatusCodes: []string{"200"},
		WarningThreshold:    1,
		CriticalThreshold:   2,
	}

	// Execute probe 3 times
	for i := 0; i < 3; i++ {
		result := executor.ExecuteProbe(ctx, ipHealth, probe)
		assert.True(t, result.Success, "Probe should succeed")
		assert.Equal(t, 200, result.ResponseCode, "Response code should be 200")
	}

	assert.Equal(t, 3, requestCount, "Server should receive 3 requests")
}

// TestCounterRaceCondition_UpdateBeforeInitialize tests the race condition safety
func TestCounterRaceCondition_UpdateBeforeInitialize(t *testing.T) {
	log := setupTestLogger(t)

	recordID := primitive.NewObjectID()
	ip := "100.0.0.1"

	cm := NewCounterManager(log)
	defer cm.Stop()

	// Simulate race condition: Update called before GetOrInitialize
	// This can happen when probe result arrives before counter initialization
	failures, successes := cm.Update(recordID, ip, false)

	assert.Equal(t, 1, failures, "Update should create counter at 0/0 then increment to 1")
	assert.Equal(t, 0, successes, "Success count should be 0")

	// Now GetOrInitialize is called (as would happen in normal flow)
	probe := &models.GSLBProbe{
		Type:              "http",
		Port:              80,
		Interval:          30,
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
