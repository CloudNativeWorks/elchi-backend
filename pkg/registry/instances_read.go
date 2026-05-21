package registry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/registry/instances"
	"github.com/CloudNativeWorks/elchi-backend/registry/leader"
)

// GetRegistryInstances reads the registry HA topology straight from MongoDB:
// the per-instance heartbeat docs (registry_instances) joined with the
// singleton leader lock (registry_leader). Like GetRegistryDataFromSnapshot
// this bypasses the gRPC path entirely — there is no "ask a registry which
// registries exist" RPC, and even if there were it would suffer the same
// split-routing problem. Reading Mongo directly gives one consistent view.
//
// Leadership is derived from the lock doc (authoritative) rather than each
// instance's self-reported is_leader flag (which is only as fresh as its last
// heartbeat): an instance is the leader iff the lock's holder_id equals its ID
// AND the lease has not expired.
func (r *RegistryClient) GetRegistryInstances(ctx context.Context, db *mongo.Database) (map[string]any, error) {
	if db == nil {
		return nil, fmt.Errorf("mongo database not available")
	}

	insts, err := instances.List(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("list registry instances: %w", err)
	}

	// Read the leader lock. Missing doc = no leader currently elected
	// (cold cluster or mid-election) — not an error.
	var lock leader.Lock
	var leaderOut map[string]any
	leaderHolderID := ""
	lockErr := db.Collection(leader.CollectionName).
		FindOne(ctx, bson.M{"_id": leader.LockKey}).
		Decode(&lock)
	now := time.Now()
	switch {
	case lockErr == nil:
		expiresAt := lock.ExpiresAt.Time()
		leaseRemaining := time.Until(expiresAt)
		// A lock whose lease already expired is a stale holder — election
		// hasn't reclaimed it yet. Report it but mark expired so the UI
		// doesn't show a confidently-dead leader as active.
		leaseValid := leaseRemaining > 0
		if leaseValid {
			leaderHolderID = lock.HolderID
		}
		leaderOut = map[string]any{
			"holder_id":           lock.HolderID,
			"hostname":            lock.Hostname,
			"acquired_at":         lock.AcquiredAt.Time().UTC(),
			"expires_at":          expiresAt.UTC(),
			"lease_remaining_sec": int64(leaseRemaining.Seconds()),
			"lease_valid":         leaseValid,
		}
	case errors.Is(lockErr, mongo.ErrNoDocuments):
		leaderOut = nil
	default:
		return nil, fmt.Errorf("read registry leader lock: %w", lockErr)
	}

	out := make([]map[string]any, 0, len(insts))
	for _, in := range insts {
		out = append(out, map[string]any{
			"id":                in.ID,
			"hostname":          in.Hostname,
			"version":           in.Version,
			"grpc_addr":         in.GRPCAddr,
			"is_leader":         in.ID == leaderHolderID,
			"started_at":        in.StartedAt.UTC(),
			"last_seen":         in.LastSeen.UTC(),
			"uptime_sec":        int64(now.Sub(in.StartedAt).Seconds()),
			"last_seen_age_sec": int64(now.Sub(in.LastSeen).Seconds()),
		})
	}

	return map[string]any{
		"instances":      out,
		"instance_count": len(out),
		"leader":         leaderOut,
		"has_leader":     leaderHolderID != "",
		"timestamp":      now.UTC(),
	}, nil
}
