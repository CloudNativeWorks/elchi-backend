package acme

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	// LockDuration is how long a lock is valid before it's considered stale
	LockDuration = 5 * time.Minute
	// HeartbeatInterval is how often the lock holder updates the lock
	HeartbeatInterval = 1 * time.Minute
	// LockCheckInterval is how often to check if we can acquire the lock
	LockCheckInterval = 30 * time.Second
)

// SchedulerLock represents a distributed lock in MongoDB
type SchedulerLock struct {
	ID         string             `bson:"_id"`
	InstanceID string             `bson:"instance_id"`
	Hostname   string             `bson:"hostname"`
	AcquiredAt primitive.DateTime `bson:"acquired_at"`
	ExpiresAt  primitive.DateTime `bson:"expires_at"`
	Heartbeat  primitive.DateTime `bson:"heartbeat"`
}

// DistributedScheduler wraps RenewalScheduler with distributed locking.
//
// Concurrency: isLeader is read by leadershipLoop, heartbeatLoop and Stop()
// while being mutated by becomeLeader / loseLeadership. We use atomic.Bool
// so all accesses are race-free without holding a mutex on the hot path.
// The CAS in becomeLeader/loseLeadership doubles as an idempotency guard so
// a re-entry (e.g. an immediate Start() acquire followed by a ticker tick)
// cannot spawn duplicate heartbeat goroutines or restart the inner
// RenewalScheduler twice.
type DistributedScheduler struct {
	scheduler   *RenewalScheduler
	db          *mongo.Database
	instanceID  string
	hostname    string
	logger      *logger.Logger
	stopCh      chan struct{}
	isLeader    atomic.Bool
	heartbeatCh chan struct{}
	stopOnce    sync.Once // Ensures Stop() is only executed once
}

// NewDistributedScheduler creates a new distributed scheduler with MongoDB locking
func NewDistributedScheduler(
	scheduler *RenewalScheduler,
	db *mongo.Database,
	logger *logger.Logger,
) *DistributedScheduler {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}

	// Generate unique instance ID
	instanceID := fmt.Sprintf("%s-%d", hostname, time.Now().Unix())

	return &DistributedScheduler{
		scheduler:  scheduler,
		db:         db,
		instanceID: instanceID,
		hostname:   hostname,
		logger:     logger,
		stopCh:     make(chan struct{}),
		// isLeader: atomic.Bool zero value is false; do not initialize.
		heartbeatCh: make(chan struct{}),
	}
}

// Start begins the distributed scheduler with leader election.
//
// The caller-supplied ctx is used only for the synchronous setup work
// (index creation + first lock attempt). All long-lived background work
// (leadership loop, heartbeat, inner RenewalScheduler) is given a
// process-lifetime context so it survives a request-scoped Start() ctx
// cancellation; without that the lock could be held in MongoDB for up
// to LockDuration after startup with no goroutine to renew or release
// it. Stop() remains the canonical way to terminate.
func (d *DistributedScheduler) Start(ctx context.Context) error {
	d.logger.Infof("Starting distributed scheduler (instance: %s)", d.instanceID)

	if err := d.ensureIndexes(ctx); err != nil {
		return fmt.Errorf("failed to create indexes: %w", err)
	}

	// Background context for the long-lived loops. Stop() closes stopCh
	// which is the actual shutdown signal these loops listen for.
	bgCtx := context.Background()

	// Attempt leadership immediately so the first renewal check fires within
	// seconds of startup rather than waiting LockCheckInterval (30s) for the
	// ticker. Critical for dev environments where the pod may restart before
	// the ticker ever fires; also reduces production cold-start latency.
	if acquired, err := d.tryAcquireLock(ctx); err != nil {
		d.logger.Warnf("Initial lock acquisition failed: %v (will retry via ticker)", err)
	} else if acquired {
		d.becomeLeader(bgCtx)
	}

	go d.leadershipLoop(bgCtx)

	return nil
}

// Stop gracefully stops the distributed scheduler
func (d *DistributedScheduler) Stop() error {
	var stopErr error

	d.stopOnce.Do(func() {
		d.logger.Infof("Stopping distributed scheduler (instance: %s)", d.instanceID)
		close(d.stopCh)

		// Snapshot leadership state once so the release-lock and
		// scheduler-stop branches stay consistent even if leadershipLoop
		// flips the flag concurrently.
		wasLeader := d.isLeader.Load()

		if wasLeader {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = d.releaseLock(ctx)
		}

		if wasLeader {
			stopErr = d.scheduler.Stop()
		}
	})

	return stopErr
}

// leadershipLoop continuously tries to acquire or maintain leadership
func (d *DistributedScheduler) leadershipLoop(ctx context.Context) {
	ticker := time.NewTicker(LockCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.logger.Info("Leadership loop context canceled")
			return

		case <-d.stopCh:
			d.logger.Info("Leadership loop stop signal received")
			return

		case <-ticker.C:
			// Try to acquire or renew leadership
			acquired, err := d.tryAcquireLock(ctx)
			if err != nil {
				d.logger.Errorf("Failed to acquire lock: %v", err)
				continue
			}

			// Leadership state changed. becomeLeader/loseLeadership use a
			// CAS internally so calling them twice for the same state is
			// a no-op — that closes the window where Start()'s immediate
			// acquire and a ticker-driven acquire could both spawn the
			// inner scheduler / heartbeat goroutine.
			leader := d.isLeader.Load()
			if acquired && !leader {
				d.becomeLeader(ctx)
			} else if !acquired && leader {
				d.loseLeadership()
			}

		case <-d.heartbeatCh:
			if d.isLeader.Load() {
				if err := d.updateHeartbeat(ctx); err != nil {
					d.logger.Errorf("Failed to update heartbeat: %v", err)
					d.loseLeadership()
				}
			}
		}
	}
}

// tryAcquireLock attempts to acquire or renew the distributed lock
func (d *DistributedScheduler) tryAcquireLock(ctx context.Context) (bool, error) {
	collection := d.db.Collection("scheduler_locks")
	now := primitive.NewDateTimeFromTime(time.Now())
	expiresAt := primitive.NewDateTimeFromTime(time.Now().Add(LockDuration))

	lockDoc := bson.M{
		"_id":         "letsencrypt-renewal-scheduler",
		"instance_id": d.instanceID,
		"hostname":    d.hostname,
		"acquired_at": now,
		"expires_at":  expiresAt,
		"heartbeat":   now,
	}

	// Try to insert new lock
	_, err := collection.InsertOne(ctx, lockDoc)
	if err == nil {
		// Successfully acquired new lock
		d.logger.Infof("Acquired new lock (instance: %s)", d.instanceID)
		return true, nil
	}

	// Lock exists - check if we can take it
	if !mongo.IsDuplicateKeyError(err) {
		return false, err
	}

	// Try to update expired lock or renew our own lock
	filter := bson.M{
		"_id": "letsencrypt-renewal-scheduler",
		"$or": []bson.M{
			// Lock is expired
			{"expires_at": bson.M{"$lt": now}},
			// Lock is ours (renew)
			{"instance_id": d.instanceID},
		},
	}

	update := bson.M{
		"$set": bson.M{
			"instance_id": d.instanceID,
			"hostname":    d.hostname,
			"expires_at":  expiresAt,
			"heartbeat":   now,
		},
		"$setOnInsert": bson.M{
			"acquired_at": now,
		},
	}

	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)
	var result SchedulerLock
	err = collection.FindOneAndUpdate(ctx, filter, update, opts).Decode(&result)

	if errors.Is(err, mongo.ErrNoDocuments) {
		// Lock is held by another instance
		d.logger.Debugf("Lock held by another instance")
		return false, nil
	}

	if err != nil {
		return false, err
	}

	// Check if we successfully acquired/renewed the lock
	if result.InstanceID == d.instanceID {
		d.logger.Debugf("Renewed lock (instance: %s)", d.instanceID)
		return true, nil
	}

	return false, nil
}

// updateHeartbeat updates the heartbeat timestamp to prove we're still alive
func (d *DistributedScheduler) updateHeartbeat(ctx context.Context) error {
	collection := d.db.Collection("scheduler_locks")
	now := primitive.NewDateTimeFromTime(time.Now())
	expiresAt := primitive.NewDateTimeFromTime(time.Now().Add(LockDuration))

	filter := bson.M{
		"_id":         "letsencrypt-renewal-scheduler",
		"instance_id": d.instanceID,
	}

	update := bson.M{
		"$set": bson.M{
			"heartbeat":  now,
			"expires_at": expiresAt,
		},
	}

	result, err := collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}

	if result.MatchedCount == 0 {
		return fmt.Errorf("lost lock ownership")
	}

	d.logger.Debugf("Updated heartbeat (instance: %s)", d.instanceID)
	return nil
}

// releaseLock explicitly releases the lock
func (d *DistributedScheduler) releaseLock(ctx context.Context) error {
	collection := d.db.Collection("scheduler_locks")
	filter := bson.M{
		"_id":         "letsencrypt-renewal-scheduler",
		"instance_id": d.instanceID,
	}

	result, err := collection.DeleteOne(ctx, filter)
	if err != nil {
		return err
	}

	if result.DeletedCount > 0 {
		d.logger.Infof("Released lock (instance: %s)", d.instanceID)
	}

	return nil
}

// becomeLeader is called when this instance becomes the leader.
// Idempotent: a CAS guard ensures the inner RenewalScheduler and the
// heartbeat goroutine are spawned at most once per leadership episode.
func (d *DistributedScheduler) becomeLeader(ctx context.Context) {
	if !d.isLeader.CompareAndSwap(false, true) {
		// Already leader (re-entry from a concurrent acquire path); no-op.
		return
	}
	d.logger.Infof("Became leader (instance: %s) - starting renewal scheduler", d.instanceID)

	// Start the underlying renewal scheduler. If it fails, roll back the
	// leader flag so a future tick can retry cleanly.
	if err := d.scheduler.Start(ctx); err != nil {
		d.logger.Errorf("Failed to start renewal scheduler: %v", err)
		d.isLeader.Store(false)
		return
	}

	// Start heartbeat goroutine.
	go d.heartbeatLoop()
}

// loseLeadership is called when this instance loses leadership.
// Idempotent: CAS guards a duplicate scheduler.Stop() call from a racing
// caller.
func (d *DistributedScheduler) loseLeadership() {
	if !d.isLeader.CompareAndSwap(true, false) {
		return
	}

	d.logger.Warnf("Lost leadership (instance: %s) - stopping renewal scheduler", d.instanceID)

	if err := d.scheduler.Stop(); err != nil {
		d.logger.Errorf("Failed to stop renewal scheduler: %v", err)
	}
}

// heartbeatLoop sends regular heartbeat signals while we hold leadership.
// Exits as soon as the leader flag flips so we don't outlive a
// loseLeadership transition.
func (d *DistributedScheduler) heartbeatLoop() {
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-d.stopCh:
			return
		case <-ticker.C:
			if !d.isLeader.Load() {
				return
			}
			select {
			case d.heartbeatCh <- struct{}{}:
			default:
				// Channel full, skip this heartbeat
			}
		}
	}
}

// ensureIndexes creates necessary MongoDB indexes
func (d *DistributedScheduler) ensureIndexes(ctx context.Context) error {
	collection := d.db.Collection("scheduler_locks")

	// Index on expires_at for efficient stale lock detection
	indexModel := mongo.IndexModel{
		Keys: bson.D{{Key: "expires_at", Value: 1}},
	}

	_, err := collection.Indexes().CreateOne(ctx, indexModel)
	return err
}
