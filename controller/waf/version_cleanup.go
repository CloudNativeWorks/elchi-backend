package waf

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// VersionPruneInterval is how often the pruner runs.
const VersionPruneInterval = 24 * time.Hour

// StartVersionPruner runs a background loop that, every VersionPruneInterval,
// deletes WAFConfigVersion docs older than the MaxVersionsPerConfig newest
// per config_id. The loop exits when ctx is cancelled.
func (s *WAFVersionService) StartVersionPruner(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(VersionPruneInterval)
		defer ticker.Stop()
		// Run once immediately so cleanup happens shortly after restart, then
		// on the regular cadence.
		s.pruneOnce(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.pruneOnce(ctx)
			}
		}
	}()
}

// pruneOnce performs a single sweep: for every distinct config_id, keep the
// newest MaxVersionsPerConfig snapshots and delete the rest.
func (s *WAFVersionService) pruneOnce(ctx context.Context) {
	coll := s.dbContext.Client.Collection(WAFVersionsCollection)

	// Step 1: enumerate distinct config_ids.
	configIDs, err := coll.Distinct(ctx, "config_id", bson.M{})
	if err != nil {
		s.logger.Warnf("WAF version pruner: distinct lookup failed: %v", err)
		return
	}

	totalDeleted := int64(0)
	for _, raw := range configIDs {
		objID, ok := raw.(primitive.ObjectID)
		if !ok {
			continue
		}
		deleted, err := s.pruneConfig(ctx, objID)
		if err != nil {
			s.logger.Warnf("WAF version pruner: prune failed for config %s: %v", objID.Hex(), err)
			continue
		}
		totalDeleted += deleted
	}
	if totalDeleted > 0 {
		s.logger.Infof("WAF version pruner: deleted %d snapshots across %d configs", totalDeleted, len(configIDs))
	}
}

// pruneConfig keeps the newest MaxVersionsPerConfig snapshots for one config
// and deletes the rest. Returns the number of deleted documents.
func (s *WAFVersionService) pruneConfig(ctx context.Context, configID primitive.ObjectID) (int64, error) {
	coll := s.dbContext.Client.Collection(WAFVersionsCollection)

	cursor, err := coll.Find(
		ctx,
		bson.M{"config_id": configID},
		options.Find().
			SetSort(bson.D{{Key: "version", Value: -1}}).
			SetSkip(int64(MaxVersionsPerConfig)).
			SetProjection(bson.M{"_id": 1}),
	)
	if err != nil {
		return 0, err
	}
	defer cursor.Close(ctx)

	var staleIDs []primitive.ObjectID
	for cursor.Next(ctx) {
		var doc struct {
			ID primitive.ObjectID `bson:"_id"`
		}
		if err := cursor.Decode(&doc); err != nil {
			return 0, err
		}
		staleIDs = append(staleIDs, doc.ID)
	}
	if err := cursor.Err(); err != nil {
		return 0, err
	}
	if len(staleIDs) == 0 {
		return 0, nil
	}
	res, err := coll.DeleteMany(ctx, bson.M{"_id": bson.M{"$in": staleIDs}})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}
