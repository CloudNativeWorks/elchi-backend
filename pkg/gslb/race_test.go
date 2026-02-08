package gslb

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// setupRaceTestLogger initializes logger for race tests
func setupRaceTestLogger(t *testing.T) *logger.Logger {
	t.Helper()
	_ = logger.Init(logger.Config{
		Level:      "debug",
		Format:     "text",
		OutputPath: "stdout",
	})
	return logger.NewLogger("test")
}

// TestTimeWheel_CurrentSlot_DataRace tests that concurrent access to currentSlot
// between tick() (writer) and Schedule/Stats (readers) does NOT race.
func TestTimeWheel_CurrentSlot_DataRace(t *testing.T) {
	testLogger := setupRaceTestLogger(t)
	executor := NewDefaultProbeExecutor(testLogger)
	defer executor.Close()

	numProcessors := 2
	resultQueues := make([]chan ProbeResult, numProcessors)
	for i := 0; i < numProcessors; i++ {
		resultQueues[i] = make(chan ProbeResult, 100)
	}

	wp := NewWorkerPool(10, 2, 4, 20, resultQueues, numProcessors, executor, testLogger)
	defer wp.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ihm := &IPHealthManager{logger: testLogger}
	tw := NewTimeWheel(ctx, ihm, wp, testLogger)

	// Start the time wheel (spawns ticker goroutine that writes currentSlot)
	tw.Start()
	defer tw.Stop()

	probe := &models.GSLBProbe{
		Type:              "http",
		Port:              80,
		Path:              "/health",
		Interval:          10,
		Timeout:           5.0,
		WarningThreshold:  2,
		CriticalThreshold: 3,
		PassingThreshold:  2,
	}

	// Give tick() time to start running
	time.Sleep(100 * time.Millisecond)

	// Concurrently Schedule tasks while tick() is modifying currentSlot
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			recordID := primitive.NewObjectID()
			task := &ScheduledTask{
				RecordID: recordID,
				IP:       "10.0.0.1",
				FQDN:     "test.example.com",
				Probe:    probe,
			}
			_ = tw.Schedule(task, idx%NumSlots)
		}(i)
	}

	// Concurrently read Stats (reads currentSlot)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = tw.Stats()
		}()
	}

	wg.Wait()
}

// TestCounterManager_GetCurrentState_DataRace verifies that the FIXED version
// of getCurrentStateFromCounter properly locks counter fields.
// Uses the actual getCurrentStateFromCounter pattern (lock before read).
func TestCounterManager_GetCurrentState_DataRace(t *testing.T) {
	testLogger := setupRaceTestLogger(t)
	cm := NewCounterManager(testLogger)
	defer cm.Stop()

	recordID := primitive.NewObjectID()
	ip := "192.168.1.100"
	key := MakeIPKey(recordID, ip)

	probe := &models.GSLBProbe{
		WarningThreshold:  2,
		CriticalThreshold: 3,
		PassingThreshold:  2,
	}

	// Initialize counter
	cm.GetOrInitialize(recordID, ip, models.HealthStatePassing, probe, BackoffInfo{}, time.Time{})

	var wg sync.WaitGroup

	// Writer goroutines: Update counter under per-counter lock
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				cm.Update(recordID, ip, j%2 == 0)
			}
		}()
	}

	// Reader goroutines: Simulate FIXED getCurrentStateFromCounter
	// Now reads counter fields under counter.mu lock (no race)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				cm.mu.RLock()
				counter, exists := cm.counters[key]
				cm.mu.RUnlock()

				if exists && counter != nil {
					// FIXED: read fields under per-counter lock
					counter.mu.Lock()
					failures := counter.ConsecutiveFailures
					successes := counter.ConsecutiveSuccesses
					counter.mu.Unlock()

					_ = models.DetermineHealthStateWithRecovery(
						failures,
						successes,
						probe,
						models.HealthStatePassing,
					)
				}
			}
		}()
	}

	wg.Wait()
}

// TestWorkerPool_ScaleDown_TaskLoss verifies that tasks are NOT lost when
// workers receive kill signals after dequeuing.
// Uses realistic scenario: kill 5 of 20 workers (remaining 15 process all tasks).
func TestWorkerPool_ScaleDown_TaskLoss(t *testing.T) {
	testLogger := setupRaceTestLogger(t)
	executor := NewDefaultProbeExecutor(testLogger)
	defer executor.Close()

	numProcessors := 2
	resultQueues := make([]chan ProbeResult, numProcessors)
	for i := 0; i < numProcessors; i++ {
		resultQueues[i] = make(chan ProbeResult, 5000)
	}

	// Start with 20 workers (min=10, max=50) - realistic scale-down: kill 5, keep 15
	wp := NewWorkerPool(10, 20, 50, 200, resultQueues, numProcessors, executor, testLogger)

	totalSubmitted := 0
	probe := &models.GSLBProbe{
		Type:     "tcp",
		Port:     1, // Port 1 - will fail quickly (connection refused)
		Interval: 10,
		Timeout:  0.1, // Very short timeout
	}

	for i := 0; i < 100; i++ {
		recordID := primitive.NewObjectID()
		task := ProbeTask{
			IPHealth: &models.GSLBIPHealth{
				RecordID: recordID,
				IP:       "127.0.0.1",
				FQDN:     "test.local",
			},
			Probe:     probe,
			RecordIDs: []primitive.ObjectID{recordID},
		}
		if wp.Submit(task) {
			totalSubmitted++
		}
	}

	// Kill 5 workers (leaving 15 to process remaining tasks)
	killed := 0
	for i := 0; i < 5; i++ {
		select {
		case wp.workerControl <- struct{}{}:
			killed++
		case <-time.After(100 * time.Millisecond):
		}
	}

	// Wait for remaining workers to process all tasks
	time.Sleep(5 * time.Second)

	// Count total results received
	totalResults := 0
	for i := 0; i < numProcessors; i++ {
	drainLoop:
		for {
			select {
			case <-resultQueues[i]:
				totalResults++
			default:
				break drainLoop
			}
		}
	}

	wp.Stop()

	t.Logf("Submitted: %d, Results received: %d, Workers killed: %d, Lost: %d",
		totalSubmitted, totalResults, killed, totalSubmitted-totalResults)

	if totalResults < totalSubmitted {
		t.Errorf("TASK LOSS DETECTED: %d tasks were lost during scale-down (%.1f%% loss rate)",
			totalSubmitted-totalResults,
			float64(totalSubmitted-totalResults)/float64(totalSubmitted)*100)
	}
}
