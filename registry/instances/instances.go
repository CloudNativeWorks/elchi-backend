// Package instances tracks the set of live elchi-registry instances so the
// controller can surface a registry-HA view (which instances exist, which one
// is the leader, lease/heartbeat timing).
//
// The leader-election lock (registry/leader) only records a SINGLE document —
// the current leader. Standbys never write anywhere, so "which registries are
// running" cannot be answered from the lock alone. This package fills that gap:
// every instance (leader or standby) upserts a heartbeat doc into the
// registry_instances collection on a fixed interval. A TTL index drops the doc
// shortly after an instance stops heartbeating, so the collection is
// self-cleaning without any cross-instance coordination.
package instances

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

const (
	// CollectionName holds one heartbeat doc per live registry instance.
	CollectionName = "registry_instances"

	// DefaultHeartbeatInterval is how often each instance refreshes its doc.
	// Matches the leader-election renewal cadence so the two stay in step.
	DefaultHeartbeatInterval = 10 * time.Second

	// StaleAfterSeconds is the TTL applied to last_seen. An instance that
	// stops heartbeating disappears from the collection ~this long after its
	// last write (plus MongoDB's TTL monitor granularity, ≤60s). 60s = 6
	// missed heartbeats — long enough to ride out a GC pause or transient
	// Mongo blip without flapping the instance in/out of the view.
	StaleAfterSeconds = 60
)

// Instance is the on-disk heartbeat document. The _id matches the
// registry_leader.holder_id so the controller can join the two by ID to mark
// the leader.
type Instance struct {
	ID        string             `bson:"_id"`
	Hostname  string             `bson:"hostname"`
	Version   string             `bson:"version"`
	GRPCAddr  string             `bson:"grpc_addr"`
	IsLeader  bool               `bson:"is_leader"`
	StartedAt time.Time          `bson:"started_at"`
	LastSeen  time.Time          `bson:"last_seen"`
}

// Registry writes this instance's heartbeat and reads the full set.
type Registry struct {
	db        *mongo.Database
	id        string
	hostname  string
	version   string
	grpcAddr  string
	startedAt time.Time
	isLeader  func() bool
	logger    *logger.Logger

	// stopped closes once the heartbeat goroutine has fully exited AND
	// removed its doc. Lets the shutdown sequence await graceful removal
	// deterministically. Buffered nil (chan) created in New.
	stopped chan struct{}
}

// New constructs a Registry. isLeader is typically election.IsLeader so the
// heartbeat reflects the current role without this package importing the
// election machinery.
func New(db *mongo.Database, id, hostname, version, grpcAddr string, isLeader func() bool, log *logger.Logger) *Registry {
	return &Registry{
		db:        db,
		id:        id,
		hostname:  hostname,
		version:   version,
		grpcAddr:  grpcAddr,
		startedAt: time.Now(),
		isLeader:  isLeader,
		logger:    log,
		stopped:   make(chan struct{}),
	}
}

// EnsureIndexes creates the TTL index on last_seen. Idempotent — safe to call
// on every startup. SetExpireAfterSeconds(StaleAfterSeconds) makes MongoDB
// drop a doc once last_seen is older than the TTL, so dead instances clean
// themselves up.
func (r *Registry) EnsureIndexes(ctx context.Context) error {
	idx := mongo.IndexModel{
		Keys:    bson.D{{Key: "last_seen", Value: 1}},
		Options: options.Index().SetExpireAfterSeconds(StaleAfterSeconds),
	}
	_, err := r.db.Collection(CollectionName).Indexes().CreateOne(ctx, idx)
	return err
}

// Start runs the heartbeat loop until ctx is cancelled. Non-blocking; spawns
// its own goroutine. Writes once immediately so the instance shows up without
// waiting a full interval.
//
// EnsureIndexes runs INSIDE the goroutine (not at the call site) on purpose:
// doing it synchronously before election.Run would let a slow Mongo delay
// leader election by up to the index-creation timeout. The TTL index is only
// needed for eventual cleanup, so it can lag the first heartbeat harmlessly.
//
// Graceful removal happens INSIDE this goroutine on ctx.Done (via remove),
// not from a separate caller goroutine. That guarantees the delete can never
// race with an in-flight heartbeat upsert — both run in this single goroutine,
// strictly ordered. The caller awaits Stopped() so the process doesn't exit
// before the delete commits (otherwise a heartbeat-then-exit would leave a
// ghost doc until the TTL).
func (r *Registry) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultHeartbeatInterval
	}
	go func() {
		defer close(r.stopped)

		idxCtx, idxCancel := context.WithTimeout(ctx, 10*time.Second)
		if err := r.EnsureIndexes(idxCtx); err != nil {
			r.logger.Warnf("registry instance: ensure indexes failed: %v", err)
		}
		idxCancel()

		if err := r.heartbeat(ctx); err != nil {
			r.logger.Warnf("registry instance: initial heartbeat failed: %v", err)
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				// Same-goroutine removal: no race with heartbeat above.
				r.remove()
				return
			case <-ticker.C:
				if err := r.heartbeat(ctx); err != nil {
					r.logger.Warnf("registry instance: heartbeat failed: %v", err)
				}
			}
		}
	}()
}

// Stopped returns a channel closed once the heartbeat goroutine has exited
// and removed its doc. The shutdown sequence selects on this (with a bounded
// timeout) so removal is awaited before the process exits.
func (r *Registry) Stopped() <-chan struct{} { return r.stopped }

// heartbeat upserts this instance's doc with a fresh last_seen + role.
func (r *Registry) heartbeat(ctx context.Context) error {
	now := time.Now()
	filter := bson.M{"_id": r.id}
	update := bson.M{
		"$set": bson.M{
			"hostname":  r.hostname,
			"version":   r.version,
			"grpc_addr": r.grpcAddr,
			"is_leader": r.isLeader(),
			"last_seen": now,
		},
		"$setOnInsert": bson.M{
			"started_at": r.startedAt,
		},
	}
	_, err := r.db.Collection(CollectionName).
		UpdateOne(ctx, filter, update, options.Update().SetUpsert(true))
	return err
}

// remove deletes this instance's doc on graceful shutdown so the instance
// disappears from the topology immediately instead of lingering until the TTL
// sweeps it (~60s). Best-effort: a failure just means the TTL handles cleanup
// later. Uses a short bounded context independent of the (already-cancelled)
// shutdown ctx. Called only from the heartbeat goroutine (on ctx.Done) so it
// never races with an in-flight heartbeat upsert.
func (r *Registry) remove() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := r.db.Collection(CollectionName).DeleteOne(ctx, bson.M{"_id": r.id}); err != nil {
		r.logger.Warnf("registry instance: failed to remove heartbeat on shutdown: %v", err)
	}
}

// List returns all live instances. Applies a defensive staleness filter
// (last_seen within StaleAfterSeconds) so callers don't see entries the TTL
// monitor hasn't swept yet (MongoDB runs it ~every 60s, so a dead doc can
// linger briefly past its TTL).
func List(ctx context.Context, db *mongo.Database) ([]Instance, error) {
	cutoff := time.Now().Add(-StaleAfterSeconds * time.Second)
	cur, err := db.Collection(CollectionName).Find(ctx, bson.M{
		"last_seen": bson.M{"$gte": cutoff},
	})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	out := make([]Instance, 0)
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}
