package gslb

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

var ErrQueueClosed = errors.New("queue is closed")

// DynamicQueue is a thread-safe, dynamically-growing queue
// Unlike Go channels, it can expand capacity without breaking consumers
type DynamicQueue[T any] struct {
	// Ring buffer
	buffer   []T
	readPos  int
	writePos int
	count    int
	capacity int

	// Configuration
	initialCapacity int
	maxCapacity     int
	growthFactor    float64 // Multiply capacity by this when full

	// Synchronization
	mu       sync.Mutex
	notEmpty *sync.Cond // Signal when items available
	notFull  *sync.Cond // Signal when space available

	// Metrics
	enqueued   int64
	dequeued   int64
	dropped    int64
	expansions int64
	shrinks    int64
	peakSize   int64

	// Shrink tracking
	lowUsageSince time.Time // Time when usage dropped below shrink threshold

	// Lifecycle
	closed bool
}

// NewDynamicQueue creates a new dynamic queue
func NewDynamicQueue[T any](initialCapacity, maxCapacity int) *DynamicQueue[T] {
	if initialCapacity <= 0 {
		initialCapacity = 1000
	}
	if maxCapacity <= 0 || maxCapacity < initialCapacity {
		maxCapacity = initialCapacity * 10
	}

	dq := &DynamicQueue[T]{
		buffer:          make([]T, initialCapacity),
		capacity:        initialCapacity,
		initialCapacity: initialCapacity,
		maxCapacity:     maxCapacity,
		growthFactor:    2.0, // Double capacity on expansion
	}

	dq.notEmpty = sync.NewCond(&dq.mu)
	dq.notFull = sync.NewCond(&dq.mu)

	return dq
}

// Enqueue adds item to queue (blocks if at maxCapacity)
func (dq *DynamicQueue[T]) Enqueue(item T) error {
	dq.mu.Lock()
	defer dq.mu.Unlock()

	if dq.closed {
		return ErrQueueClosed
	}

	// Auto-expand if full (up to maxCapacity)
	if dq.count == dq.capacity {
		if dq.capacity < dq.maxCapacity {
			dq.expandLocked()
		} else {
			// At max capacity: block until space available
			for dq.count == dq.capacity && !dq.closed {
				dq.notFull.Wait()
			}

			if dq.closed {
				return ErrQueueClosed
			}
		}
	}

	// Write to buffer (ring buffer logic)
	dq.buffer[dq.writePos] = item
	dq.writePos = (dq.writePos + 1) % dq.capacity
	dq.count++

	// Update metrics
	atomic.AddInt64(&dq.enqueued, 1)
	if int64(dq.count) > atomic.LoadInt64(&dq.peakSize) {
		atomic.StoreInt64(&dq.peakSize, int64(dq.count))
	}

	// Signal consumers
	dq.notEmpty.Signal()

	return nil
}

// Dequeue removes item from queue (blocks if empty)
func (dq *DynamicQueue[T]) Dequeue() (T, error) {
	dq.mu.Lock()
	defer dq.mu.Unlock()

	// Wait for item or close
	for dq.count == 0 && !dq.closed {
		dq.notEmpty.Wait()
	}

	if dq.closed && dq.count == 0 {
		var zero T
		return zero, ErrQueueClosed
	}

	// Read from buffer
	item := dq.buffer[dq.readPos]

	// Clear reference (for GC)
	var zero T
	dq.buffer[dq.readPos] = zero

	dq.readPos = (dq.readPos + 1) % dq.capacity
	dq.count--

	// Update metrics
	atomic.AddInt64(&dq.dequeued, 1)

	// Check for shrink opportunity (if capacity > initial and usage < 25% for 5+ minutes)
	dq.checkShrinkLocked()

	// Signal producers
	dq.notFull.Signal()

	return item, nil
}

// checkShrinkLocked checks if queue should be shrunk (MUST hold lock)
// Shrinks when: capacity > initialCapacity AND usage < 25% for 5+ minutes
func (dq *DynamicQueue[T]) checkShrinkLocked() {
	// Don't shrink below initial capacity
	if dq.capacity <= dq.initialCapacity {
		dq.lowUsageSince = time.Time{} // Reset tracking
		return
	}

	// Calculate usage percentage
	usagePercent := float64(dq.count) / float64(dq.capacity)

	// Shrink threshold: 25% usage
	const shrinkThreshold = 0.25
	// Duration threshold: 5 minutes of low usage before shrinking
	const shrinkDelay = 5 * time.Minute

	if usagePercent < shrinkThreshold {
		// Track when low usage started
		if dq.lowUsageSince.IsZero() {
			dq.lowUsageSince = time.Now()
		}

		// Check if low usage has persisted long enough
		if time.Since(dq.lowUsageSince) >= shrinkDelay {
			dq.shrinkLocked()
			dq.lowUsageSince = time.Time{} // Reset after shrink
		}
	} else {
		// Usage is above threshold, reset tracking
		dq.lowUsageSince = time.Time{}
	}
}

// shrinkLocked reduces the buffer capacity by half (MUST hold lock)
func (dq *DynamicQueue[T]) shrinkLocked() {
	// Calculate new capacity (halve it, but don't go below initial)
	newCapacity := dq.capacity / 2
	if newCapacity < dq.initialCapacity {
		newCapacity = dq.initialCapacity
	}

	// Don't shrink if new capacity is same or larger
	if newCapacity >= dq.capacity {
		return
	}

	// Don't shrink if current items won't fit
	if dq.count > newCapacity {
		return
	}

	// Create new smaller buffer
	newBuffer := make([]T, newCapacity)

	// Copy existing items (handle ring buffer wrap-around)
	if dq.count > 0 {
		if dq.readPos < dq.writePos {
			// Simple case: no wrap
			copy(newBuffer, dq.buffer[dq.readPos:dq.writePos])
		} else {
			// Wrap-around case
			n := copy(newBuffer, dq.buffer[dq.readPos:])
			copy(newBuffer[n:], dq.buffer[:dq.writePos])
		}
	}

	// Update state
	oldCapacity := dq.capacity
	dq.buffer = newBuffer
	dq.readPos = 0
	dq.writePos = dq.count
	dq.capacity = newCapacity

	atomic.AddInt64(&dq.shrinks, 1)

	// Log shrink (can't use logger here, but stats will show it)
	_ = oldCapacity // Suppress unused variable warning
}

// expandLocked grows the buffer capacity (MUST hold lock)
func (dq *DynamicQueue[T]) expandLocked() {
	newCapacity := int(float64(dq.capacity) * dq.growthFactor)
	if newCapacity > dq.maxCapacity {
		newCapacity = dq.maxCapacity
	}

	if newCapacity <= dq.capacity {
		return // Already at max
	}

	// Create new buffer
	newBuffer := make([]T, newCapacity)

	// Copy existing items (handle ring buffer wrap-around)
	if dq.readPos < dq.writePos {
		// Simple case: no wrap
		copy(newBuffer, dq.buffer[dq.readPos:dq.writePos])
	} else if dq.readPos > dq.writePos {
		// Wrap-around case
		n := copy(newBuffer, dq.buffer[dq.readPos:])
		copy(newBuffer[n:], dq.buffer[:dq.writePos])
	}

	// Update state
	dq.buffer = newBuffer
	dq.readPos = 0
	dq.writePos = dq.count
	dq.capacity = newCapacity

	atomic.AddInt64(&dq.expansions, 1)
}

// Close signals queue shutdown
func (dq *DynamicQueue[T]) Close() {
	dq.mu.Lock()
	defer dq.mu.Unlock()

	if !dq.closed {
		dq.closed = true
		dq.notEmpty.Broadcast()
		dq.notFull.Broadcast()
	}
}

// Stats returns queue metrics
func (dq *DynamicQueue[T]) Stats() DynamicQueueStats {
	dq.mu.Lock()
	currentSize := dq.count
	currentCapacity := dq.capacity
	dq.mu.Unlock()

	return DynamicQueueStats{
		CurrentSize:     currentSize,
		CurrentCapacity: currentCapacity,
		MaxCapacity:     dq.maxCapacity,
		Enqueued:        atomic.LoadInt64(&dq.enqueued),
		Dequeued:        atomic.LoadInt64(&dq.dequeued),
		Dropped:         atomic.LoadInt64(&dq.dropped),
		Expansions:      atomic.LoadInt64(&dq.expansions),
		Shrinks:         atomic.LoadInt64(&dq.shrinks),
		PeakSize:        atomic.LoadInt64(&dq.peakSize),
	}
}

// DynamicQueueStats contains queue metrics
type DynamicQueueStats struct {
	CurrentSize     int
	CurrentCapacity int
	MaxCapacity     int
	Enqueued        int64
	Dequeued        int64
	Dropped         int64
	Expansions      int64
	Shrinks         int64
	PeakSize        int64
}
