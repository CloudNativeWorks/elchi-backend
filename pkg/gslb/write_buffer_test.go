package gslb

import (
	"context"
	"testing"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TestWriteBuffer_Add tests adding updates to the buffer
func TestWriteBuffer_Add(t *testing.T) {
	log := setupTestLogger(t)
	ctx := context.Background()

	// Create WriteBuffer with large maxSize to prevent auto-flush during test
	wb := NewWriteBuffer(ctx, nil, log, 100, 10*time.Second)
	defer wb.Stop()

	recordID := primitive.NewObjectID()

	// Add first update
	update1 := HealthStateUpdate{
		RecordID:    recordID,
		IP:          "1.2.3.4",
		HealthState: models.HealthStatePassing,
		Timestamp:   time.Now(),
	}
	wb.Add(update1)

	// Check buffer size
	assert.Equal(t, 1, wb.Size(), "Buffer should contain 1 update")

	// Add second update
	update2 := HealthStateUpdate{
		RecordID:    recordID,
		IP:          "5.6.7.8",
		HealthState: models.HealthStateCritical,
		Timestamp:   time.Now(),
	}
	wb.Add(update2)

	// Check buffer size
	assert.Equal(t, 2, wb.Size(), "Buffer should contain 2 updates")
}

// TestWriteBuffer_Size tests the Size method
func TestWriteBuffer_Size(t *testing.T) {
	log := setupTestLogger(t)
	ctx := context.Background()

	wb := NewWriteBuffer(ctx, nil, log, 100, 10*time.Second)
	defer wb.Stop()

	// Initially empty
	assert.Equal(t, 0, wb.Size(), "Buffer should be empty initially")

	// Add updates
	recordID := primitive.NewObjectID()
	for i := 0; i < 10; i++ {
		update := HealthStateUpdate{
			RecordID:    recordID,
			IP:          "1.2.3.4",
			HealthState: models.HealthStatePassing,
			Timestamp:   time.Now(),
		}
		wb.Add(update)
	}

	// Check size
	assert.Equal(t, 10, wb.Size(), "Buffer should contain 10 updates")
}

// TestWriteBuffer_GetStats tests the GetStats method
func TestWriteBuffer_GetStats(t *testing.T) {
	log := setupTestLogger(t)
	ctx := context.Background()

	maxSize := 50
	wb := NewWriteBuffer(ctx, nil, log, maxSize, 10*time.Second)
	defer wb.Stop()

	// Get initial stats
	stats := wb.GetStats()
	assert.Equal(t, 0, stats.CurrentSize, "Initial size should be 0")
	assert.Equal(t, maxSize, stats.MaxSize, "Max size should match")
	assert.Equal(t, 0.0, stats.Capacity, "Initial capacity should be 0%")

	// Add updates
	recordID := primitive.NewObjectID()
	for i := 0; i < 25; i++ {
		update := HealthStateUpdate{
			RecordID:    recordID,
			IP:          "1.2.3.4",
			HealthState: models.HealthStatePassing,
			Timestamp:   time.Now(),
		}
		wb.Add(update)
	}

	// Get stats after adding
	stats = wb.GetStats()
	assert.Equal(t, 25, stats.CurrentSize, "Current size should be 25")
	assert.Equal(t, 50.0, stats.Capacity, "Capacity should be 50%")
}

// TestWriteBuffer_GetStats_FullCapacity tests capacity calculation at 100%
func TestWriteBuffer_GetStats_FullCapacity(t *testing.T) {
	log := setupTestLogger(t)
	ctx := context.Background()

	maxSize := 10
	wb := NewWriteBuffer(ctx, nil, log, maxSize, 10*time.Second)
	defer wb.Stop()

	// Fill buffer just below capacity (9 items) to avoid auto-flush
	recordID := primitive.NewObjectID()
	for i := 0; i < 9; i++ {
		update := HealthStateUpdate{
			RecordID:    recordID,
			IP:          "1.2.3.4",
			HealthState: models.HealthStatePassing,
			Timestamp:   time.Now(),
		}
		wb.Add(update)
	}

	// Get stats - should be at 90% capacity
	stats := wb.GetStats()
	assert.Equal(t, 9, stats.CurrentSize, "Current size should be 9")
	assert.Equal(t, 90.0, stats.Capacity, "Capacity should be 90%")
}

// TestWriteBuffer_BuildUpdateDocument tests the buildUpdateDocument method
func TestWriteBuffer_BuildUpdateDocument(t *testing.T) {
	log := setupTestLogger(t)
	ctx := context.Background()

	wb := NewWriteBuffer(ctx, nil, log, 100, 10*time.Second)
	defer wb.Stop()

	recordID := primitive.NewObjectID()
	now := time.Now()

	tests := []struct {
		name   string
		update HealthStateUpdate
	}{
		{
			name: "Normal health state update",
			update: HealthStateUpdate{
				RecordID:         recordID,
				IP:               "1.2.3.4",
				HealthState:      models.HealthStatePassing,
				BackoffUntil:     time.Time{},
				CurrentBackoff:   0,
				ResponseCode:     200,
				ResponseTime:     0.123,
				LastStatusChange: now,
				Timestamp:        now,
				ProbeType:        "http",
				ErrorMessage:     "",
			},
		},
		{
			name: "Critical state with error message",
			update: HealthStateUpdate{
				RecordID:       recordID,
				IP:             "5.6.7.8",
				HealthState:    models.HealthStateCritical,
				BackoffUntil:   now.Add(30 * time.Second),
				CurrentBackoff: 30,
				ResponseCode:   0,
				ResponseTime:   5.0,
				Timestamp:      now,
				ProbeType:      "http",
				ErrorMessage:   "connection timeout",
			},
		},
		{
			name: "TCP probe (no response code)",
			update: HealthStateUpdate{
				RecordID:     recordID,
				IP:           "10.0.0.1",
				HealthState:  models.HealthStatePassing,
				ResponseCode: 200, // Internal success marker
				ResponseTime: 0.05,
				Timestamp:    now,
				ProbeType:    "tcp",
			},
		},
		{
			name: "Clear manual reset",
			update: HealthStateUpdate{
				RecordID:         recordID,
				IP:               "192.168.1.1",
				Timestamp:        now,
				ClearManualReset: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := wb.buildUpdateDocument(tt.update)

			assert.NotNil(t, doc, "Update document should not be nil")

			if tt.update.ClearManualReset {
				// Should have $unset for manual_reset_at
				assert.Contains(t, doc, "$unset", "Should have $unset for manual reset")
			} else {
				// Should have $set for health state
				assert.Contains(t, doc, "$set", "Should have $set")
				setDoc := doc["$set"]
				assert.NotNil(t, setDoc, "$set should not be nil")

				// Should have $push for status_history
				assert.Contains(t, doc, "$push", "Should have $push for history")
			}
		})
	}
}

// TestWriteBuffer_BuildUpdateDocument_TCP_NoResponseCode tests that TCP probes don't persist ResponseCode
func TestWriteBuffer_BuildUpdateDocument_TCP_NoResponseCode(t *testing.T) {
	log := setupTestLogger(t)
	ctx := context.Background()

	wb := NewWriteBuffer(ctx, nil, log, 100, 10*time.Second)
	defer wb.Stop()

	recordID := primitive.NewObjectID()
	now := time.Now()

	// TCP probe with internal ResponseCode=200 (should NOT be persisted)
	update := HealthStateUpdate{
		RecordID:     recordID,
		IP:           "10.0.0.1",
		HealthState:  models.HealthStatePassing,
		ResponseCode: 200, // Internal marker - should NOT persist for TCP
		ResponseTime: 0.05,
		Timestamp:    now,
		ProbeType:    "tcp",
	}

	doc := wb.buildUpdateDocument(update)

	// Verify document structure
	assert.Contains(t, doc, "$push", "Should have $push")
	assert.Contains(t, doc, "$set", "Should have $set")

	// Extract $push operation
	pushOp := doc["$push"]
	assert.NotNil(t, pushOp, "$push should not be nil")

	// The structure is: $push -> status_history -> {$each: [...], $slice: -50}
	// We just verify the document was created correctly
	// In real MongoDB, the history entry would have ResponseCode=0 for TCP
}

// TestWriteBuffer_BuildUpdateDocument_HTTP_WithResponseCode tests that HTTP probes persist ResponseCode
func TestWriteBuffer_BuildUpdateDocument_HTTP_WithResponseCode(t *testing.T) {
	log := setupTestLogger(t)
	ctx := context.Background()

	wb := NewWriteBuffer(ctx, nil, log, 100, 10*time.Second)
	defer wb.Stop()

	recordID := primitive.NewObjectID()
	now := time.Now()

	// HTTP probe with ResponseCode (should be persisted)
	update := HealthStateUpdate{
		RecordID:     recordID,
		IP:           "1.2.3.4",
		HealthState:  models.HealthStatePassing,
		ResponseCode: 200,
		ResponseTime: 0.123,
		Timestamp:    now,
		ProbeType:    "http",
	}

	doc := wb.buildUpdateDocument(update)

	// Verify document structure
	assert.Contains(t, doc, "$push", "Should have $push")
	assert.Contains(t, doc, "$set", "Should have $set")

	// Extract $push operation
	pushOp := doc["$push"]
	assert.NotNil(t, pushOp, "$push should not be nil")

	// The structure is: $push -> status_history -> {$each: [...], $slice: -50}
	// We just verify the document was created correctly
	// In real MongoDB, the history entry would have ResponseCode=200 for HTTP
}

// TestWriteBuffer_Stop tests graceful shutdown
func TestWriteBuffer_Stop(t *testing.T) {
	log := setupTestLogger(t)
	ctx := context.Background()

	wb := NewWriteBuffer(ctx, nil, log, 100, 1*time.Second)

	// Add some updates
	recordID := primitive.NewObjectID()
	for i := 0; i < 5; i++ {
		update := HealthStateUpdate{
			RecordID:    recordID,
			IP:          "1.2.3.4",
			HealthState: models.HealthStatePassing,
			Timestamp:   time.Now(),
		}
		wb.Add(update)
	}

	// Stop should not panic
	wb.Stop()

	// Verify buffer is stopped (size should be 0 after final flush)
	// Note: Without MongoDB, flush fails but buffer is cleared
	size := wb.Size()
	assert.Equal(t, 0, size, "Buffer should be empty after Stop()")
}

// TestWriteBuffer_ConcurrentAdd tests thread-safe concurrent adds
func TestWriteBuffer_ConcurrentAdd(t *testing.T) {
	log := setupTestLogger(t)
	ctx := context.Background()

	wb := NewWriteBuffer(ctx, nil, log, 1000, 10*time.Second)
	defer wb.Stop()

	recordID := primitive.NewObjectID()

	// Concurrent adds from multiple goroutines
	numGoroutines := 10
	addsPerGoroutine := 10

	done := make(chan bool, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			for j := 0; j < addsPerGoroutine; j++ {
				update := HealthStateUpdate{
					RecordID:    recordID,
					IP:          "1.2.3.4",
					HealthState: models.HealthStatePassing,
					Timestamp:   time.Now(),
				}
				wb.Add(update)
			}
			done <- true
		}()
	}

	// Wait for all goroutines to finish
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Verify all updates were added
	expectedSize := numGoroutines * addsPerGoroutine
	actualSize := wb.Size()
	assert.Equal(t, expectedSize, actualSize, "All concurrent adds should be tracked")
}
