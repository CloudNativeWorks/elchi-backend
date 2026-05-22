// Package inventory provides the drift-detection support for the API
// Inventory feature: point-in-time snapshots of the api_inventory
// surface plus the leader-elected scheduler that captures them.
//
// The live api_inventory collection is upsert-only (no history), so
// field-level change detection ("what changed in my API surface since
// yesterday") needs a baseline to diff against. WriteSnapshot captures
// the drift-relevant subset of each confirmed operation into
// api_inventory_snapshots; the drift handler diffs the live inventory
// against the most recent snapshot at-or-before a caller-supplied time.
package inventory

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
	// lockCollection is shared with the ACME renewal scheduler; the
	// distinct lockID keeps the two leader elections independent.
	lockCollection = "scheduler_locks"
	lockID         = "inventory-snapshot"

	snapshotCollection  = "api_inventory_snapshots"
	inventoryCollection = "api_inventory"

	// Lock cadence — mirrored from pkg/acme so this package stays
	// dependency-light (no import of the ACME scheduler just for three
	// constants). LockDuration must comfortably exceed heartbeatInterval
	// so a busy leader never lets its own lock expire.
	lockDuration      = 5 * time.Minute
	heartbeatInterval = 1 * time.Minute
	lockCheckInterval = 30 * time.Second

	// snapshotInterval is the automatic capture cadence. On leadership
	// acquisition a snapshot is also taken if the newest existing one is
	// older than snapshotStaleAfter — that establishes a baseline on a
	// fresh deployment and survives pod restarts without double-capturing
	// within the same day.
	snapshotInterval   = 24 * time.Hour
	snapshotStaleAfter = 23 * time.Hour

	// snapshotMaxRuntime caps a single $merge run so a pathological
	// inventory size can't pin the aggregation indefinitely.
	snapshotMaxRuntime = 10 * time.Minute
)

// schedulerLock is the on-disk shape of a lock document in
// scheduler_locks. Only the fields this package reads are modelled.
type schedulerLock struct {
	ID         string             `bson:"_id"`
	InstanceID string             `bson:"instance_id"`
	Hostname   string             `bson:"hostname"`
	AcquiredAt primitive.DateTime `bson:"acquired_at"`
	ExpiresAt  primitive.DateTime `bson:"expires_at"`
	Heartbeat  primitive.DateTime `bson:"heartbeat"`
}

// WriteSnapshot captures the drift-relevant surface of api_inventory
// into api_inventory_snapshots via a server-side $merge aggregation
// (the data never leaves MongoDB). projectID == "" snapshots every
// project; a non-empty value scopes the capture to one project (the
// manual-trigger path).
//
// Each operation becomes one snapshot document keyed by a compound _id
// (snapshot run id + the 8-field operation key). Re-running the same
// snapshot_id is therefore idempotent (whenMatched: keepExisting) while
// distinct runs always insert (whenNotMatched: insert). Returns the
// snapshot_id and the number of operation docs written.
//
// Intentionally omitted vs the live model (≈half the storage):
// clusters / routes / origins / consumers / sample_event_ids /
// content_types / latency_max_ms. status_dist is reduced to its key set
// (the observed status codes) since drift only needs "which codes
// appeared", not their counts.
func WriteSnapshot(ctx context.Context, database *mongo.Database, projectID string) (string, int64, error) {
	if database == nil {
		return "", 0, fmt.Errorf("nil mongo database")
	}
	snapshotAt := time.Now().UTC()
	snapshotID := snapshotAt.Format(time.RFC3339)

	match := bson.D{{Key: "confirmed", Value: true}}
	if projectID != "" {
		match = append(match, bson.E{Key: "project_id", Value: projectID})
	}

	pipeline := mongo.Pipeline{
		bson.D{{Key: "$match", Value: match}},
		bson.D{{Key: "$project", Value: bson.D{
			// Compound _id = snapshot run + operation key. Guarantees a
			// fresh document per run (snapshot_id differs each run) while
			// staying idempotent within a run. Literals are wrapped in
			// $literal so the projection engine never mistakes the
			// string for a field path or an include flag.
			{Key: "_id", Value: bson.D{
				{Key: "s", Value: bson.D{{Key: "$literal", Value: snapshotID}}},
				{Key: "p", Value: "$project_id"},
				{Key: "l", Value: "$listener_name"},
				{Key: "pr", Value: "$protocol"},
				{Key: "h", Value: "$host"},
				{Key: "m", Value: "$method"},
				{Key: "np", Value: "$normalized_path"},
				{Key: "gs", Value: "$grpc_service"},
				{Key: "gm", Value: "$grpc_method"},
			}},
			{Key: "snapshot_id", Value: bson.D{{Key: "$literal", Value: snapshotID}}},
			{Key: "snapshot_at", Value: bson.D{{Key: "$literal", Value: primitive.NewDateTimeFromTime(snapshotAt)}}},
			{Key: "project_id", Value: 1},
			{Key: "listener_name", Value: 1},
			{Key: "protocol", Value: 1},
			{Key: "host", Value: 1},
			{Key: "method", Value: 1},
			{Key: "normalized_path", Value: 1},
			{Key: "grpc_service", Value: 1},
			{Key: "grpc_method", Value: 1},
			{Key: "auth_observed", Value: 1},
			{Key: "noauth_observed", Value: 1},
			{Key: "auth_schemes", Value: 1},
			{Key: "pii_categories", Value: 1},
			{Key: "risk_flags", Value: 1},
			{Key: "max_risk_score", Value: 1},
			{Key: "max_posture_score", Value: 1},
			// status_dist (map code->count) reduced to its key set. $ifNull
			// guards docs missing status_dist so $objectToArray never errors.
			{Key: "status_codes", Value: bson.D{{Key: "$map", Value: bson.D{
				{Key: "input", Value: bson.D{{Key: "$objectToArray", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$status_dist", bson.D{}}}}}}},
				{Key: "in", Value: "$$this.k"},
			}}}},
			{Key: "last_seen", Value: 1},
			{Key: "seen_count", Value: 1},
			{Key: "confirmed", Value: 1},
		}}},
		bson.D{{Key: "$merge", Value: bson.D{
			{Key: "into", Value: snapshotCollection},
			{Key: "on", Value: "_id"},
			{Key: "whenMatched", Value: "keepExisting"},
			{Key: "whenNotMatched", Value: "insert"},
		}}},
	}

	aggCtx, cancel := context.WithTimeout(ctx, snapshotMaxRuntime)
	defer cancel()

	cur, err := database.Collection(inventoryCollection).Aggregate(aggCtx, pipeline)
	if err != nil {
		return "", 0, fmt.Errorf("snapshot aggregation failed: %w", err)
	}
	// $merge emits no result documents; just release the cursor.
	_ = cur.Close(aggCtx)

	// Count what landed for this run (cheap — backed by the
	// snap_snapshot_project index).
	countFilter := bson.M{"snapshot_id": snapshotID}
	if projectID != "" {
		countFilter["project_id"] = projectID
	}
	count, cerr := database.Collection(snapshotCollection).CountDocuments(ctx, countFilter)
	if cerr != nil {
		// The snapshot itself succeeded; the count is best-effort.
		return snapshotID, 0, nil
	}
	return snapshotID, count, nil
}

// SnapshotScheduler runs WriteSnapshot on a daily cadence, guarded by a
// MongoDB leader election so exactly one controller pod captures
// snapshots at a time. State is entirely database-driven (the lock
// document); there is no inter-pod messaging.
type SnapshotScheduler struct {
	db         *mongo.Database
	logger     *logger.Logger
	instanceID string
	hostname   string
	stopCh     chan struct{}
	isLeader   atomic.Bool
	stopOnce   sync.Once
}

// NewSnapshotScheduler constructs a scheduler bound to the controller's
// MongoDB database.
func NewSnapshotScheduler(database *mongo.Database, log *logger.Logger) *SnapshotScheduler {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	return &SnapshotScheduler{
		db:         database,
		logger:     log,
		instanceID: fmt.Sprintf("%s-%d", hostname, time.Now().Unix()),
		hostname:   hostname,
		stopCh:     make(chan struct{}),
	}
}

// Start launches the background leader-election + snapshot loop. The
// caller-supplied ctx is used only for first-tick work; the long-lived
// loop runs on a process-lifetime context and shuts down via Stop().
// First leadership (and the baseline snapshot, if stale) is established
// on the first leadership tick — a ≤30s delay that is irrelevant for a
// daily cadence.
func (s *SnapshotScheduler) Start(_ context.Context) error {
	s.logger.Infof("Starting inventory snapshot scheduler (instance: %s)", s.instanceID)
	go s.run(context.Background())
	return nil
}

// Stop signals the loop to exit and releases the lock if held.
func (s *SnapshotScheduler) Stop() {
	s.stopOnce.Do(func() {
		s.logger.Infof("Stopping inventory snapshot scheduler (instance: %s)", s.instanceID)
		close(s.stopCh)
		if s.isLeader.Load() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.releaseLock(ctx)
		}
	})
}

// run is the single-goroutine event loop: leadership maintenance,
// heartbeat, and the snapshot cadence on three independent tickers.
func (s *SnapshotScheduler) run(ctx context.Context) {
	leaderTicker := time.NewTicker(lockCheckInterval)
	heartbeatTicker := time.NewTicker(heartbeatInterval)
	snapshotTicker := time.NewTicker(snapshotInterval)
	defer leaderTicker.Stop()
	defer heartbeatTicker.Stop()
	defer snapshotTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return

		case <-leaderTicker.C:
			acquired, err := s.tryAcquireLock(ctx)
			if err != nil {
				s.logger.Errorf("Inventory snapshot: lock acquire failed: %v", err)
				continue
			}
			was := s.isLeader.Load()
			switch {
			case acquired && !was:
				s.isLeader.Store(true)
				s.logger.Infof("Inventory snapshot: became leader (instance: %s)", s.instanceID)
				// Establish a baseline immediately if none is recent.
				s.maybeSnapshot(ctx)
			case !acquired && was:
				s.isLeader.Store(false)
				s.logger.Warnf("Inventory snapshot: lost leadership (instance: %s)", s.instanceID)
			}

		case <-heartbeatTicker.C:
			if s.isLeader.Load() {
				if err := s.updateHeartbeat(ctx); err != nil {
					s.logger.Errorf("Inventory snapshot: heartbeat failed: %v", err)
					s.isLeader.Store(false)
				}
			}

		case <-snapshotTicker.C:
			if s.isLeader.Load() {
				s.runSnapshot(ctx)
			}
		}
	}
}

// maybeSnapshot captures a snapshot only if the newest existing one is
// stale (or none exists). Keeps restarts from double-capturing within a
// day while still guaranteeing a baseline on a fresh deployment.
func (s *SnapshotScheduler) maybeSnapshot(ctx context.Context) {
	opts := options.FindOne().
		SetSort(bson.D{{Key: "snapshot_at", Value: -1}}).
		SetProjection(bson.D{{Key: "snapshot_at", Value: 1}})
	var doc struct {
		SnapshotAt time.Time `bson:"snapshot_at"`
	}
	err := s.db.Collection(snapshotCollection).FindOne(ctx, bson.M{}, opts).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			s.logger.Infof("Inventory snapshot: no baseline found, taking initial snapshot")
			s.runSnapshot(ctx)
			return
		}
		s.logger.Errorf("Inventory snapshot: baseline check failed: %v", err)
		return
	}
	if age := time.Since(doc.SnapshotAt); age >= snapshotStaleAfter {
		s.logger.Infof("Inventory snapshot: last snapshot %v ago, refreshing", age.Round(time.Minute))
		s.runSnapshot(ctx)
	}
}

// runSnapshot performs an all-projects capture and logs the outcome.
func (s *SnapshotScheduler) runSnapshot(ctx context.Context) {
	snapshotID, count, err := WriteSnapshot(ctx, s.db, "")
	if err != nil {
		s.logger.Errorf("Inventory snapshot run failed: %v", err)
		return
	}
	s.logger.Infof("Inventory snapshot complete: id=%s ops=%d", snapshotID, count)
}

// tryAcquireLock acquires or renews the distributed lock. Mirrors the
// ACME scheduler's logic against the same scheduler_locks collection
// with a distinct _id.
func (s *SnapshotScheduler) tryAcquireLock(ctx context.Context) (bool, error) {
	collection := s.db.Collection(lockCollection)
	now := primitive.NewDateTimeFromTime(time.Now())
	expiresAt := primitive.NewDateTimeFromTime(time.Now().Add(lockDuration))

	lockDoc := bson.M{
		"_id":         lockID,
		"instance_id": s.instanceID,
		"hostname":    s.hostname,
		"acquired_at": now,
		"expires_at":  expiresAt,
		"heartbeat":   now,
	}

	// Fast path: insert a brand-new lock.
	if _, err := collection.InsertOne(ctx, lockDoc); err == nil {
		s.logger.Infof("Inventory snapshot: acquired new lock (instance: %s)", s.instanceID)
		return true, nil
	} else if !mongo.IsDuplicateKeyError(err) {
		return false, err
	}

	// Lock exists — take it over if expired, or renew if it's ours.
	filter := bson.M{
		"_id": lockID,
		"$or": []bson.M{
			{"expires_at": bson.M{"$lt": now}},
			{"instance_id": s.instanceID},
		},
	}
	update := bson.M{
		"$set": bson.M{
			"instance_id": s.instanceID,
			"hostname":    s.hostname,
			"expires_at":  expiresAt,
			"heartbeat":   now,
		},
		"$setOnInsert": bson.M{"acquired_at": now},
	}
	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)
	var result schedulerLock
	err := collection.FindOneAndUpdate(ctx, filter, update, opts).Decode(&result)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil // held by another live instance
	}
	if err != nil {
		return false, err
	}
	return result.InstanceID == s.instanceID, nil
}

// updateHeartbeat refreshes the lock's expiry while we hold leadership.
func (s *SnapshotScheduler) updateHeartbeat(ctx context.Context) error {
	collection := s.db.Collection(lockCollection)
	now := primitive.NewDateTimeFromTime(time.Now())
	expiresAt := primitive.NewDateTimeFromTime(time.Now().Add(lockDuration))

	result, err := collection.UpdateOne(ctx,
		bson.M{"_id": lockID, "instance_id": s.instanceID},
		bson.M{"$set": bson.M{"heartbeat": now, "expires_at": expiresAt}},
	)
	if err != nil {
		return err
	}
	if result.MatchedCount == 0 {
		return fmt.Errorf("lost lock ownership")
	}
	return nil
}

// releaseLock deletes the lock document if we still own it.
func (s *SnapshotScheduler) releaseLock(ctx context.Context) error {
	_, err := s.db.Collection(lockCollection).DeleteOne(ctx,
		bson.M{"_id": lockID, "instance_id": s.instanceID},
	)
	return err
}
