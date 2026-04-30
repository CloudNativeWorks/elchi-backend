// Package leader implements MongoDB-backed leader election for elchi-registry.
// Only one registry instance holds the lock at a time and serves traffic
// (gRPC health = SERVING). Standbys keep the lock under contention via TTL
// expiry: when the leader stops renewing, another instance takes over.
//
// The pattern is adapted from pkg/acme/distributed_lock.go.
package leader

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

const (
	// CollectionName is the MongoDB collection that stores the singleton lock document.
	CollectionName = "registry_leader"
	// LockKey is the fixed _id of the singleton lock document.
	LockKey = "registry_primary"

	// DefaultTTL is how long a lease is valid before another instance can claim it.
	DefaultTTL = 30 * time.Second
	// DefaultRenewInterval is how often the leader refreshes the lease.
	// Must be < TTL/2 to tolerate jitter.
	DefaultRenewInterval = 10 * time.Second
)

// Lock is the on-disk representation of the registry leader lease.
type Lock struct {
	ID         string             `bson:"_id"`
	HolderID   string             `bson:"holder_id"`
	Hostname   string             `bson:"hostname"`
	AcquiredAt primitive.DateTime `bson:"acquired_at"`
	ExpiresAt  primitive.DateTime `bson:"expires_at"`
}

// Election runs the leader-election loop for a single registry instance.
// Use IsLeader() for fast atomic reads on hot paths and OnLeadershipChange()
// to wire health-status flips and snapshot writer/reader switches.
type Election struct {
	db            *mongo.Database
	holderID      string
	hostname      string
	ttl           time.Duration
	renewInterval time.Duration
	logger        *logger.Logger

	leader   atomic.Bool
	stopOnce sync.Once
	stopCh   chan struct{}

	cbMu      sync.RWMutex
	callbacks []func(bool)
}

// New constructs an Election. ttl/renewInterval=0 fall back to defaults.
func New(db *mongo.Database, ttl, renewInterval time.Duration, log *logger.Logger) *Election {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if renewInterval <= 0 {
		renewInterval = DefaultRenewInterval
	}
	if renewInterval >= ttl {
		// Clamp to safe ratio.
		renewInterval = ttl / 3
	}
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	return &Election{
		db:            db,
		holderID:      fmt.Sprintf("%s-%s", hostname, uuid.NewString()[:8]),
		hostname:      hostname,
		ttl:           ttl,
		renewInterval: renewInterval,
		logger:        log,
		stopCh:        make(chan struct{}),
	}
}

// IsLeader returns true if this instance currently holds the lease.
// Safe and cheap to call from request handlers.
func (e *Election) IsLeader() bool { return e.leader.Load() }

// HolderID returns this instance's unique identifier (hostname-uuid8).
func (e *Election) HolderID() string { return e.holderID }

// OnLeadershipChange registers a callback fired when leadership flips.
// Callbacks must not block. They run in the election loop goroutine.
func (e *Election) OnLeadershipChange(fn func(isLeader bool)) {
	e.cbMu.Lock()
	e.callbacks = append(e.callbacks, fn)
	e.cbMu.Unlock()
}

// Run drives the acquire/renew loop until ctx is canceled or Stop() is called.
// Blocks; call from a goroutine.
func (e *Election) Run(ctx context.Context) {
	if err := e.ensureIndexes(ctx); err != nil {
		e.logger.Errorf("registry leader: failed to ensure indexes: %v", err)
	}

	// First attempt happens immediately so a fresh deployment becomes leader fast.
	e.tickOnce(ctx)

	ticker := time.NewTicker(e.renewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			e.releaseIfHeld(context.Background())
			return
		case <-e.stopCh:
			e.releaseIfHeld(context.Background())
			return
		case <-ticker.C:
			e.tickOnce(ctx)
		}
	}
}

// Stop gracefully terminates the election loop and releases the lock if held.
func (e *Election) Stop() {
	e.stopOnce.Do(func() { close(e.stopCh) })
}

func (e *Election) tickOnce(ctx context.Context) {
	acquired, err := e.tryAcquireOrRenew(ctx)
	if err != nil {
		e.logger.Errorf("registry leader: acquire/renew failed: %v", err)
		// On error, we cannot claim leadership; if we held it, drop.
		if e.leader.CompareAndSwap(true, false) {
			e.fireCallbacks(false)
		}
		return
	}
	if acquired {
		if e.leader.CompareAndSwap(false, true) {
			e.logger.Infof("registry leader: became LEADER (holder=%s)", e.holderID)
			e.fireCallbacks(true)
		}
		return
	}
	if e.leader.CompareAndSwap(true, false) {
		e.logger.Warnf("registry leader: lost leadership (holder=%s)", e.holderID)
		e.fireCallbacks(false)
	}
}

// tryAcquireOrRenew is the single atomic operation that drives election.
// Returns true if this instance holds the lock after the call.
func (e *Election) tryAcquireOrRenew(ctx context.Context) (bool, error) {
	col := e.db.Collection(CollectionName)
	now := time.Now()
	expires := now.Add(e.ttl)

	// Filter matches if (a) document missing/expired, OR (b) we are the current holder.
	filter := bson.M{
		"_id": LockKey,
		"$or": []bson.M{
			{"expires_at": bson.M{"$lt": primitive.NewDateTimeFromTime(now)}},
			{"holder_id": e.holderID},
		},
	}
	update := bson.M{
		"$set": bson.M{
			"holder_id":  e.holderID,
			"hostname":   e.hostname,
			"expires_at": primitive.NewDateTimeFromTime(expires),
		},
		"$setOnInsert": bson.M{
			"_id":         LockKey,
			"acquired_at": primitive.NewDateTimeFromTime(now),
		},
	}
	opts := options.FindOneAndUpdate().
		SetUpsert(true).
		SetReturnDocument(options.After)

	var got Lock
	err := col.FindOneAndUpdate(ctx, filter, update, opts).Decode(&got)
	if err != nil {
		// Another instance just upserted a non-expired doc — they're leader.
		if mongo.IsDuplicateKeyError(err) || errors.Is(err, mongo.ErrNoDocuments) {
			return false, nil
		}
		return false, err
	}
	return got.HolderID == e.holderID, nil
}

func (e *Election) releaseIfHeld(ctx context.Context) {
	if !e.leader.CompareAndSwap(true, false) {
		return
	}
	col := e.db.Collection(CollectionName)
	_, err := col.DeleteOne(ctx, bson.M{"_id": LockKey, "holder_id": e.holderID})
	if err != nil {
		e.logger.Warnf("registry leader: failed to release lock on shutdown: %v", err)
	} else {
		e.logger.Infof("registry leader: released lock (holder=%s)", e.holderID)
	}
	e.fireCallbacks(false)
}

func (e *Election) fireCallbacks(isLeader bool) {
	e.cbMu.RLock()
	cbs := make([]func(bool), len(e.callbacks))
	copy(cbs, e.callbacks)
	e.cbMu.RUnlock()
	for _, cb := range cbs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					e.logger.Errorf("registry leader: callback panicked: %v", r)
				}
			}()
			cb(isLeader)
		}()
	}
}

// ensureIndexes creates the TTL index on expires_at so abandoned leases
// are eventually cleaned up by MongoDB even without an active election loop.
func (e *Election) ensureIndexes(ctx context.Context) error {
	col := e.db.Collection(CollectionName)
	idx := mongo.IndexModel{
		Keys:    bson.D{{Key: "expires_at", Value: 1}},
		Options: options.Index().SetExpireAfterSeconds(0),
	}
	_, err := col.Indexes().CreateOne(ctx, idx)
	return err
}
