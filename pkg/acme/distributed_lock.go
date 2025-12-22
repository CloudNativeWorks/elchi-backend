package acme

import (
	"context"
	"fmt"
	"os"
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

// DistributedScheduler wraps RenewalScheduler with distributed locking
type DistributedScheduler struct {
	scheduler   *RenewalScheduler
	db          *mongo.Database
	instanceID  string
	hostname    string
	logger      *logger.Logger
	stopCh      chan struct{}
	isLeader    bool
	heartbeatCh chan struct{}
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
		scheduler:   scheduler,
		db:          db,
		instanceID:  instanceID,
		hostname:    hostname,
		logger:      logger,
		stopCh:      make(chan struct{}),
		isLeader:    false,
		heartbeatCh: make(chan struct{}),
	}
}

// Start begins the distributed scheduler with leader election
func (d *DistributedScheduler) Start(ctx context.Context) error {
	d.logger.Infof("Starting distributed scheduler (instance: %s)", d.instanceID)

	// Ensure indexes exist
	if err := d.ensureIndexes(ctx); err != nil {
		return fmt.Errorf("failed to create indexes: %w", err)
	}

	// Start leadership monitoring loop
	go d.leadershipLoop(ctx)

	return nil
}

// Stop gracefully stops the distributed scheduler
func (d *DistributedScheduler) Stop() error {
	d.logger.Infof("Stopping distributed scheduler (instance: %s)", d.instanceID)
	close(d.stopCh)

	// Release lock if we're the leader
	if d.isLeader {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.releaseLock(ctx)
	}

	// Stop the underlying scheduler if it's running
	if d.isLeader {
		return d.scheduler.Stop()
	}

	return nil
}

// leadershipLoop continuously tries to acquire or maintain leadership
func (d *DistributedScheduler) leadershipLoop(ctx context.Context) {
	ticker := time.NewTicker(LockCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.logger.Info("Leadership loop context cancelled")
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

			// Leadership state changed
			if acquired && !d.isLeader {
				// We just became leader
				d.becomeLeader(ctx)
			} else if !acquired && d.isLeader {
				// We lost leadership
				d.loseLeadership()
			}

		case <-d.heartbeatCh:
			// Heartbeat to maintain leadership
			if d.isLeader {
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

	if err == mongo.ErrNoDocuments {
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

// becomeLeader is called when this instance becomes the leader
func (d *DistributedScheduler) becomeLeader(ctx context.Context) {
	d.logger.Infof("🎖️  Became leader (instance: %s) - starting renewal scheduler", d.instanceID)
	d.isLeader = true

	// Start the underlying renewal scheduler
	if err := d.scheduler.Start(ctx); err != nil {
		d.logger.Errorf("Failed to start renewal scheduler: %v", err)
		d.isLeader = false
		return
	}

	// Start heartbeat goroutine
	go d.heartbeatLoop()
}

// loseLeadership is called when this instance loses leadership
func (d *DistributedScheduler) loseLeadership() {
	if !d.isLeader {
		return
	}

	d.logger.Warnf("⚠️  Lost leadership (instance: %s) - stopping renewal scheduler", d.instanceID)
	d.isLeader = false

	// Stop the underlying renewal scheduler
	if err := d.scheduler.Stop(); err != nil {
		d.logger.Errorf("Failed to stop renewal scheduler: %v", err)
	}
}

// heartbeatLoop sends regular heartbeat signals
func (d *DistributedScheduler) heartbeatLoop() {
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-d.stopCh:
			return
		case <-ticker.C:
			if !d.isLeader {
				return // Stop heartbeat if not leader anymore
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
