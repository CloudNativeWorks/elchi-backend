package gslb

import (
	"container/list"
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// Note: As of Go 1.20+, rand.Seed is deprecated and automatically seeded
// No explicit seeding is needed - the global random source is auto-seeded

const (
	NumSlots = 512 // 512 seconds = ~8.5 minutes max delay
)

// TimeWheel is a Linux kernel-style time wheel scheduler for GSLB health checks
// Each IP is scheduled independently based on its health state
type TimeWheel struct {
	// Ring buffer of slots
	slots       [NumSlots]*list.List
	currentSlot atomic.Int32 // Atomic to prevent data race between tick() writer and Schedule/Stats readers
	ticker      *time.Ticker

	// Task tracking
	tasks   map[string]*ScheduledTask
	tasksMu sync.RWMutex

	// In-flight task tracking (prevents race between execute and reschedule)
	// Key: "recordID::ip", Value: true while probe is in progress
	inFlight   map[string]bool
	inFlightMu sync.RWMutex

	// Dependencies
	ipHealthManager *IPHealthManager
	workerPool      *WorkerPool
	logger          *logger.Logger
	ctx             context.Context
	cancel          context.CancelFunc

	// Lifecycle
	doneCh   chan struct{}
	stopOnce sync.Once

	// Metrics
	scheduled   int64 // Total tasks scheduled
	executed    int64 // Total probes executed
	rescheduled int64 // Total reschedules
	currentLoad int64 // Current tasks in wheel
}

// ScheduledTask represents a health check scheduled in the time wheel
type ScheduledTask struct {
	RecordID primitive.ObjectID
	IP       string
	FQDN     string
	Probe    *models.GSLBProbe

	// Wheel positioning
	SlotIndex   int           // Which slot (0-511)
	ListElement *list.Element // For O(1) removal

	// Scheduling metadata
	NextProbeAt time.Time // When to probe (for debugging)
	ScheduledAt time.Time // When scheduled (for metrics)

	// Context flags
	IsReprobe        bool                // Prevent infinite loops
	IsWarningMonitor bool                // In WARNING monitoring loop
	ManualRecordID   *primitive.ObjectID // For manual state changes
	ManualState      *models.HealthState // Manual state if applicable
}

// NewTimeWheel creates a new time wheel scheduler
func NewTimeWheel(ctx context.Context, ipHealthManager *IPHealthManager,
	workerPool *WorkerPool, logger *logger.Logger,
) *TimeWheel {
	tw := &TimeWheel{
		tasks:           make(map[string]*ScheduledTask),
		inFlight:        make(map[string]bool),
		ipHealthManager: ipHealthManager,
		workerPool:      workerPool,
		logger:          logger,
		doneCh:          make(chan struct{}),
	}

	tw.ctx, tw.cancel = context.WithCancel(ctx)

	// Initialize all slots
	for i := 0; i < NumSlots; i++ {
		tw.slots[i] = list.New()
	}

	return tw
}

// Start begins the time wheel ticker
func (tw *TimeWheel) Start() {
	tw.ticker = time.NewTicker(1 * time.Second)

	go func() {
		defer close(tw.doneCh)
		defer tw.ticker.Stop()

		tw.logger.Infof("Time Wheel started (512 slots, 1s granularity)")

		for {
			select {
			case <-tw.ticker.C:
				tw.tick()
			case <-tw.ctx.Done():
				tw.logger.Infof("Time Wheel context canceled, stopping...")
				return
			}
		}
	}()
}

// tick advances the time wheel and executes tasks in current slot
func (tw *TimeWheel) tick() {
	// Log every 10 seconds for visibility
	slot := tw.currentSlot.Load()
	if slot%10 == 0 {
		stats := tw.Stats()
		tw.logger.Infof("Time Wheel tick: slot=%d, load=%d, scheduled=%d, executed=%d, rescheduled=%d",
			stats.CurrentSlot, stats.CurrentLoad, stats.Scheduled, stats.Executed, stats.Rescheduled)
	}

	// Execute tasks in current slot
	if err := tw.executeCurrentSlot(); err != nil {
		tw.logger.Errorf("Time wheel tick failed: %v", err)
	}

	tw.currentSlot.Store((slot + 1) % int32(NumSlots))
}

// Stop gracefully stops the time wheel
func (tw *TimeWheel) Stop() {
	tw.stopOnce.Do(func() {
		tw.logger.Infof("Stopping time wheel...")
		tw.cancel()
		<-tw.doneCh
		tw.logger.Infof("Time wheel stopped")
	})
}

// makeKey generates a unique key for a task
// Uses shared MakeIPKey helper for consistency across components
func (tw *TimeWheel) makeKey(recordID primitive.ObjectID, ip string) string {
	return MakeIPKey(recordID, ip)
}

// Stats returns current time wheel metrics
func (tw *TimeWheel) Stats() TimeWheelStats {
	return TimeWheelStats{
		CurrentSlot: int(tw.currentSlot.Load()),
		Scheduled:   atomic.LoadInt64(&tw.scheduled),
		Executed:    atomic.LoadInt64(&tw.executed),
		Rescheduled: atomic.LoadInt64(&tw.rescheduled),
		CurrentLoad: atomic.LoadInt64(&tw.currentLoad),
	}
}

// TimeWheelStats contains time wheel metrics
type TimeWheelStats struct {
	CurrentSlot int
	Scheduled   int64
	Executed    int64
	Rescheduled int64
	CurrentLoad int64
}

// Schedule adds a task to the time wheel at a specific delay
// O(1) operation - adds to specific slot's linked list
func (tw *TimeWheel) Schedule(task *ScheduledTask, delaySeconds int) error {
	if delaySeconds < 0 {
		delaySeconds = 0
	}
	if delaySeconds >= NumSlots {
		delaySeconds = NumSlots - 1 // Cap at max delay
	}

	key := tw.makeKey(task.RecordID, task.IP)

	tw.tasksMu.Lock()
	defer tw.tasksMu.Unlock()

	// Remove existing task if present (reschedule case)
	if existing, ok := tw.tasks[key]; ok {
		tw.removeTaskLocked(existing)
	}

	// Calculate target slot
	targetSlot := (int(tw.currentSlot.Load()) + delaySeconds) % NumSlots
	task.SlotIndex = targetSlot
	task.NextProbeAt = time.Now().Add(time.Duration(delaySeconds) * time.Second)
	task.ScheduledAt = time.Now()

	// Add to slot's linked list
	task.ListElement = tw.slots[targetSlot].PushBack(task)

	// Track in map
	tw.tasks[key] = task

	atomic.AddInt64(&tw.scheduled, 1)
	atomic.AddInt64(&tw.currentLoad, 1)

	return nil
}

// Reschedule moves an existing task to a new slot
// O(1) operation - removes from old slot and adds to new slot
// Uses single write lock to prevent race with ClearAll/Remove between read and schedule
func (tw *TimeWheel) Reschedule(recordID primitive.ObjectID, ip string, delaySeconds int) error {
	if delaySeconds < 0 {
		delaySeconds = 0
	}
	if delaySeconds >= NumSlots {
		delaySeconds = NumSlots - 1
	}

	key := tw.makeKey(recordID, ip)

	tw.tasksMu.Lock()
	defer tw.tasksMu.Unlock()

	task, exists := tw.tasks[key]
	if !exists {
		return fmt.Errorf("task not found: %s", key)
	}

	// Remove from old slot
	tw.removeTaskLocked(task)

	// Add to new slot
	targetSlot := (int(tw.currentSlot.Load()) + delaySeconds) % NumSlots
	task.SlotIndex = targetSlot
	task.NextProbeAt = time.Now().Add(time.Duration(delaySeconds) * time.Second)
	task.ScheduledAt = time.Now()
	task.ListElement = tw.slots[targetSlot].PushBack(task)
	tw.tasks[key] = task

	atomic.AddInt64(&tw.scheduled, 1)
	atomic.AddInt64(&tw.currentLoad, 1)
	atomic.AddInt64(&tw.rescheduled, 1)

	return nil
}

// RescheduleImmediate moves a task to the current slot (executes within 1 second)
// This is used for manual PASS operations
func (tw *TimeWheel) RescheduleImmediate(recordID primitive.ObjectID, ip string) error {
	return tw.Reschedule(recordID, ip, 0) // Schedule for next tick (1 second)
}

// Remove removes a task from the time wheel
// O(1) operation - removes from slot's linked list and tracking map
func (tw *TimeWheel) Remove(recordID primitive.ObjectID, ip string) error {
	key := tw.makeKey(recordID, ip)

	tw.tasksMu.Lock()
	defer tw.tasksMu.Unlock()

	task, exists := tw.tasks[key]
	if !exists {
		return nil // Already removed, OK
	}

	tw.removeTaskLocked(task)
	return nil
}

// removeTaskLocked removes a task (caller must hold tasksMu)
func (tw *TimeWheel) removeTaskLocked(task *ScheduledTask) {
	// Remove from slot's list
	tw.slots[task.SlotIndex].Remove(task.ListElement)

	// Remove from tracking map
	key := tw.makeKey(task.RecordID, task.IP)
	delete(tw.tasks, key)

	atomic.AddInt64(&tw.currentLoad, -1)
}

// ClearAll removes all tasks from the time wheel
// Used during shard rebalancing to prevent memory leaks from lost shards
func (tw *TimeWheel) ClearAll() {
	tw.tasksMu.Lock()
	defer tw.tasksMu.Unlock()

	// Clear all slots
	for i := 0; i < NumSlots; i++ {
		tw.slots[i].Init()
	}

	// Clear task map
	taskCount := len(tw.tasks)
	tw.tasks = make(map[string]*ScheduledTask)

	// Reset current load
	atomic.StoreInt64(&tw.currentLoad, 0)

	// Clear in-flight map to prevent stale entries after full reload
	tw.inFlightMu.Lock()
	tw.inFlight = make(map[string]bool)
	tw.inFlightMu.Unlock()

	tw.logger.Infof("Cleared %d tasks from time wheel", taskCount)
}

// executeCurrentSlot executes all tasks in the current slot
// This is called every second by the ticker
func (tw *TimeWheel) executeCurrentSlot() error {
	currentSlot := int(tw.currentSlot.Load())
	slot := tw.slots[currentSlot]

	// Get tasks from current slot under lock
	// Lock ordering: tasksMu first, then inFlightMu (consistent ordering prevents deadlocks)
	tw.tasksMu.Lock()
	var tasksToExecute []*ScheduledTask
	var keysToProcess []string

	for e := slot.Front(); e != nil; e = e.Next() {
		task := e.Value.(*ScheduledTask)
		tasksToExecute = append(tasksToExecute, task)
		keysToProcess = append(keysToProcess, tw.makeKey(task.RecordID, task.IP))
	}

	// Clear slot (tasks will be rescheduled after probe)
	slot.Init()

	// Remove tasks from tracking map (under tasksMu lock)
	for _, key := range keysToProcess {
		delete(tw.tasks, key)
		atomic.AddInt64(&tw.currentLoad, -1)
	}
	tw.tasksMu.Unlock()

	// Mark tasks as in-flight under separate lock (after releasing tasksMu)
	// This allows other Schedule() calls to proceed while we mark in-flight
	tw.inFlightMu.Lock()
	for _, key := range keysToProcess {
		tw.inFlight[key] = true
	}
	tw.inFlightMu.Unlock()

	if len(tasksToExecute) == 0 {
		return nil // Nothing to do
	}

	// Collect unique record IDs for batch fetch
	recordIDSet := make(map[primitive.ObjectID]struct{})
	for _, task := range tasksToExecute {
		recordIDSet[task.RecordID] = struct{}{}
	}

	recordIDs := make([]primitive.ObjectID, 0, len(recordIDSet))
	for recordID := range recordIDSet {
		recordIDs = append(recordIDs, recordID)
	}

	// Parallel batch fetch: IPs and fresh records from MongoDB
	// Both queries are independent and use the same recordIDs
	var ipsByRecord map[primitive.ObjectID][]models.GSLBIPHealth
	var freshRecords map[primitive.ObjectID]*models.GSLBRecord
	var ipFetchErr, recordFetchErr error

	var fetchWg sync.WaitGroup
	fetchWg.Add(2)

	go func() {
		defer fetchWg.Done()
		ctx, cancel := context.WithTimeout(tw.ctx, 5*time.Second)
		defer cancel()
		ipsByRecord, ipFetchErr = tw.ipHealthManager.GetIPsByRecordIDs(ctx, recordIDs)
	}()

	go func() {
		defer fetchWg.Done()
		// Batch fetch records to get fresh probe config from DB
		// This ensures probe config changes from other controllers are picked up immediately
		ctx2, cancel2 := context.WithTimeout(tw.ctx, 5*time.Second)
		defer cancel2()
		freshRecords, recordFetchErr = tw.ipHealthManager.GetRecordsByIDs(ctx2, recordIDs)
	}()

	fetchWg.Wait()

	if ipFetchErr != nil {
		tw.logger.Errorf("Failed to fetch IPs for slot %d: %v", currentSlot, ipFetchErr)
		// Clear all in-flight entries for these tasks (they won't get HandleProbeResult)
		tw.inFlightMu.Lock()
		for _, key := range keysToProcess {
			delete(tw.inFlight, key)
		}
		tw.inFlightMu.Unlock()
		// Reschedule tasks with jitter to prevent thundering herd
		// Each task gets random delay between 1-5 seconds
		for _, task := range tasksToExecute {
			jitteredDelay := 1 + rand.Intn(5) // #nosec G404 - Non-cryptographic randomness is fine for jitter delays
			if schedErr := tw.Schedule(task, jitteredDelay); schedErr != nil {
				tw.logger.Warnf("Failed to reschedule task for %s/%s: %v", task.RecordID.Hex(), task.IP, schedErr)
			}
		}
		return ipFetchErr
	}

	if recordFetchErr != nil {
		// Non-fatal: continue with cached probe config if record fetch fails
		tw.logger.Warnf("Failed to fetch fresh records for slot %d (using cached probe config): %v", currentSlot, recordFetchErr)
	}

	// Update probe config from DB for each task (detect config changes)
	configChangedTasks := make(map[string]bool) // key: recordID::ip
	if freshRecords != nil {
		for _, task := range tasksToExecute {
			if record, found := freshRecords[task.RecordID]; found && record.Probe != nil {
				if probeConfigChanged(task.Probe, record.Probe) {
					key := tw.makeKey(task.RecordID, task.IP)
					configChangedTasks[key] = true
					tw.logger.Infof("Probe config changed for %s (record: %s) - using fresh config from DB",
						task.IP, task.RecordID.Hex()[:8])
					task.Probe = record.Probe // Use fresh config from DB
				}
			}
		}
	}

	// Build secondary IP index for O(1) lookup (instead of O(n) linear search per task)
	ipIndex := make(map[primitive.ObjectID]map[string]*models.GSLBIPHealth, len(ipsByRecord))
	for recordID, ips := range ipsByRecord {
		ipMap := make(map[string]*models.GSLBIPHealth, len(ips))
		for i := range ips {
			ipMap[ips[i].IP] = &ips[i]
		}
		ipIndex[recordID] = ipMap
	}

	// Submit probe tasks to worker pool
	for _, task := range tasksToExecute {
		taskKey := tw.makeKey(task.RecordID, task.IP)

		ipMap, found := ipIndex[task.RecordID]
		if !found {
			tw.logger.Warnf("Record %s not found in batch fetch", task.RecordID.Hex())
			// Clean up in-flight entry for skipped task (prevents leak)
			tw.inFlightMu.Lock()
			delete(tw.inFlight, taskKey)
			tw.inFlightMu.Unlock()
			continue
		}

		// O(1) IP lookup via index
		ipHealth, ipFound := ipMap[task.IP]
		if !ipFound {
			tw.logger.Warnf("IP %s not found in record %s", task.IP, task.RecordID.Hex())
			// Clean up in-flight entry for skipped task (prevents leak)
			tw.inFlightMu.Lock()
			delete(tw.inFlight, taskKey)
			tw.inFlightMu.Unlock()
			continue
		}

		// Build probe task context
		// Cache IP health in context to avoid N+1 DB query in evaluateStatusChangeForRecord
		probeCtx := context.WithValue(context.Background(), cachedIPHealthKey, ipHealth)
		if task.IsReprobe {
			probeCtx = context.WithValue(probeCtx, isReprobeKey, true)
		}
		if task.IsWarningMonitor {
			probeCtx = context.WithValue(probeCtx, isWarningMonitorKey, true)
		}
		if task.ManualRecordID != nil {
			probeCtx = context.WithValue(probeCtx, manualRecordIDKey, *task.ManualRecordID)
			probeCtx = context.WithValue(probeCtx, manualHealthStateKey, *task.ManualState)
		}

		// Signal probe config change to HealthChecker for counter reset
		if configChangedTasks[taskKey] {
			probeCtx = context.WithValue(probeCtx, probeConfigChangedKey, true)
		}

		// Create probe task
		probeTask := ProbeTask{
			RecordIDs: []primitive.ObjectID{task.RecordID},
			IPHealth:  ipHealth,
			Probe:     task.Probe,
		}

		// Attach task to context
		probeCtx = context.WithValue(probeCtx, taskContextKey, probeTask)
		probeTask.Context = probeCtx

		// Submit to worker pool - check if successful
		if !tw.workerPool.Submit(probeTask) {
			tw.logger.Errorf("Failed to submit probe for %s (record: %s) - worker pool queue full or closed",
				task.IP, task.RecordID.Hex()[:8])
			// Clean up in-flight entry (task won't get HandleProbeResult)
			tw.inFlightMu.Lock()
			delete(tw.inFlight, taskKey)
			tw.inFlightMu.Unlock()

			// Reschedule task to retry in 1 second
			retryTask := &ScheduledTask{
				RecordID:         task.RecordID,
				IP:               task.IP,
				FQDN:             task.FQDN,
				Probe:            task.Probe,
				IsReprobe:        task.IsReprobe,
				IsWarningMonitor: task.IsWarningMonitor,
				ManualRecordID:   task.ManualRecordID,
				ManualState:      task.ManualState,
			}
			if err := tw.Schedule(retryTask, 1); err != nil {
				tw.logger.Warnf("Failed to schedule retry task for %s/%s: %v", task.RecordID.Hex(), task.IP, err)
			}
			continue
		}

		atomic.AddInt64(&tw.executed, 1)
	}
	return nil
}

// HandleProbeResult processes a probe result and reschedules the task
// This should be called by the result processor after processing the result
func (tw *TimeWheel) HandleProbeResult(recordID primitive.ObjectID, ip string, newState models.HealthState,
	success bool, consecutiveFailures int, probe *models.GSLBProbe, isWarningMonitor bool,
) error {
	key := tw.makeKey(recordID, ip)

	// Clear in-flight flag BEFORE scheduling
	// This prevents the race where executeCurrentSlot marks as in-flight
	// but HandleProbeResult hasn't cleared it yet
	tw.inFlightMu.Lock()
	delete(tw.inFlight, key)
	tw.inFlightMu.Unlock()

	var delaySeconds int

	switch newState {
	case models.HealthStateCritical:
		if !success {
			// Calculate graduated backoff using models package (interval-aware)
			backoff := models.CalculateGraduatedBackoff(newState, consecutiveFailures, probe.CriticalThreshold, probe.Interval)
			delaySeconds = int(backoff.Seconds())
		} else {
			// Unexpected: CRITICAL but success?
			delaySeconds = probe.Interval
		}

	case models.HealthStateRecovery:
		// RECOVERY state: Use half interval for faster recovery verification
		// This allows IP to recover quickly while still preventing flapping
		delaySeconds = probe.Interval / 2
		if delaySeconds < 1 {
			delaySeconds = 1
		}

	case models.HealthStateWarning:
		// WARNING state: Always use half interval for increased monitoring
		// This gives endpoint time to recover while detecting persistent failures faster
		delaySeconds = probe.Interval / 2
		if delaySeconds < 1 {
			delaySeconds = 1
		}

	case models.HealthStatePassing:
		// Back to normal interval
		delaySeconds = probe.Interval

	default:
		// Fallback
		delaySeconds = probe.Interval
	}

	// Reschedule task
	updatedTask := &ScheduledTask{
		RecordID:         recordID,
		IP:               ip,
		FQDN:             "", // Will be set from current task if rescheduling existing
		Probe:            probe,
		IsWarningMonitor: newState == models.HealthStateWarning && !success,
	}

	if err := tw.Schedule(updatedTask, delaySeconds); err != nil {
		tw.logger.Errorf("Failed to reschedule %s: %v", ip, err)
		return err
	}

	atomic.AddInt64(&tw.rescheduled, 1)
	return nil
}

// LoadRecords loads all GSLB records and their IPs into the time wheel at startup
func (tw *TimeWheel) LoadRecords(ctx context.Context, records []*models.GSLBRecord) error {
	tw.logger.Infof("Loading %d records into time wheel...", len(records))

	recordIDs := make([]primitive.ObjectID, len(records))
	recordMap := make(map[primitive.ObjectID]*models.GSLBRecord)

	for i, record := range records {
		recordIDs[i] = record.ID
		recordMap[record.ID] = record
	}

	// Batch fetch all IPs
	ipsByRecord, err := tw.ipHealthManager.GetIPsByRecordIDs(ctx, recordIDs)
	if err != nil {
		return fmt.Errorf("failed to load IPs: %w", err)
	}

	totalScheduled := 0
	for recordID, ips := range ipsByRecord {
		record := recordMap[recordID]

		for _, ip := range ips {
			// Calculate initial delay based on current state
			var delaySeconds int

			if !ip.BackoffUntil.IsZero() && time.Now().Before(ip.BackoffUntil) {
				// Still in backoff - schedule for when backoff expires
				remaining := time.Until(ip.BackoffUntil)
				delaySeconds = int(remaining.Seconds())
				if delaySeconds >= NumSlots {
					delaySeconds = NumSlots - 1 // Cap at max
				}
			} else {
				// No backoff or expired - randomize initial scheduling to avoid thundering herd
				// Use proper random source for better distribution (not time-based)
				delaySeconds = 1 + rand.Intn(record.Probe.Interval) // #nosec G404 - Non-cryptographic randomness is fine for scheduling jitter
			}

			task := &ScheduledTask{
				RecordID: recordID,
				IP:       ip.IP,
				FQDN:     record.FQDN,
				Probe:    record.Probe,
			}

			if err := tw.Schedule(task, delaySeconds); err != nil {
				tw.logger.Warnf("Failed to schedule %s: %v", ip.IP, err)
				continue
			}

			totalScheduled++
		}
	}

	tw.logger.Infof("Loaded %d tasks into time wheel", totalScheduled)
	return nil
}

// probeConfigChanged compares two probe configs to detect if the probe configuration
// was updated in MongoDB (e.g., port, path, interval changed from another controller)
func probeConfigChanged(old, current *models.GSLBProbe) bool {
	if old == nil || current == nil {
		return old != current
	}
	return old.Type != current.Type ||
		old.Port != current.Port ||
		old.Path != current.Path ||
		old.HostHeader != current.HostHeader ||
		old.Interval != current.Interval ||
		old.Timeout != current.Timeout ||
		old.WarningThreshold != current.WarningThreshold ||
		old.CriticalThreshold != current.CriticalThreshold ||
		old.PassingThreshold != current.PassingThreshold ||
		!boolPtrEqual(old.Enabled, current.Enabled) ||
		!boolPtrEqual(old.SkipSSLVerify, current.SkipSSLVerify) ||
		!boolPtrEqual(old.FollowRedirects, current.FollowRedirects)
}

// boolPtrEqual compares two *bool pointers for equality
func boolPtrEqual(a, b *bool) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
