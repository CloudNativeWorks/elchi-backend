package gslb

import (
	"context"
	"fmt"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ShardRepository abstracts shard persistence operations
// This enables testing without MongoDB and centralizes retry logic
type ShardRepository interface {
	// FindAvailableShards returns shards available for acquisition
	// (expired lease, unowned, or owned by this controller)
	FindAvailableShards(ctx context.Context, controllerID string, limit int) ([]models.GSLBShard, error)

	// AcquireShardsWithRetry attempts to claim ownership of multiple shards atomically
	// Returns number of shards successfully acquired
	AcquireShardsWithRetry(ctx context.Context, shardIDs []int, controllerID string, leaseTTL int, maxRetries int) (int, error)

	// ReleaseShards releases specific shards owned by this controller
	ReleaseShardsWithIDs(ctx context.Context, shardIDs []int, controllerID string) error

	// ReleaseAllShards releases all shards owned by this controller
	ReleaseAllShards(ctx context.Context, controllerID string) error

	// RenewLeases extends lease expiry for specific shards owned by this controller
	RenewLeases(ctx context.Context, shardIDs []int, controllerID string, leaseTTL int) (int64, error)

	// CountActiveControllers returns number of controllers currently holding leases
	CountActiveControllers(ctx context.Context) (int, error)

	// VerifyShardsOwned checks which shards from the candidate list are actually owned
	VerifyShardsOwned(ctx context.Context, candidateShardIDs []int, controllerID string) ([]int, error)
}

// MongoShardRepository implements ShardRepository using MongoDB
type MongoShardRepository struct {
	collection *mongo.Collection
	logger     *logger.Logger
}

// NewMongoShardRepository creates a MongoDB-backed shard repository
func NewMongoShardRepository(db *mongo.Database, logger *logger.Logger) *MongoShardRepository {
	return &MongoShardRepository{
		collection: db.Collection("gslb_shards"),
		logger:     logger,
	}
}

// FindAvailableShards returns shards available for acquisition
func (r *MongoShardRepository) FindAvailableShards(ctx context.Context, controllerID string, limit int) ([]models.GSLBShard, error) {
	filter := bson.M{
		"$or": []bson.M{
			{"lease_expiry": bson.M{"$lt": time.Now()}},
			{"controller_id": ""},
			{"controller_id": controllerID}, // Include own shards for rebalancing
		},
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "lease_expiry", Value: 1}}).
		SetLimit(int64(limit))

	cursor, err := r.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to find available shards: %w", err)
	}
	defer cursor.Close(ctx)

	var shards []models.GSLBShard
	if err := cursor.All(ctx, &shards); err != nil {
		return nil, fmt.Errorf("failed to decode shards: %w", err)
	}

	return shards, nil
}

// AcquireShardsWithRetry attempts to claim ownership of multiple shards atomically
func (r *MongoShardRepository) AcquireShardsWithRetry(ctx context.Context, shardIDs []int, controllerID string, leaseTTL int, maxRetries int) (int, error) {
	leaseExpiry := time.Now().Add(time.Duration(leaseTTL) * time.Second)

	// Build bulk write operations
	var bulkOps []mongo.WriteModel
	for _, shardID := range shardIDs {
		updateFilter := bson.M{
			"shard_id": shardID,
			"$or": []bson.M{
				{"lease_expiry": bson.M{"$lt": time.Now()}},
				{"controller_id": ""},
			},
		}

		update := bson.M{
			"$set": bson.M{
				"controller_id":  controllerID,
				"lease_expiry":   leaseExpiry,
				"last_heartbeat": time.Now(),
			},
		}

		bulkOps = append(bulkOps, mongo.NewUpdateOneModel().
			SetFilter(updateFilter).
			SetUpdate(update))
	}

	if len(bulkOps) == 0 {
		return 0, nil
	}

	// Execute with retry
	var bulkResult *mongo.BulkWriteResult
	err := ExecuteWithExponentialBackoff(ctx, func(ctx context.Context) error {
		var bulkErr error
		bulkResult, bulkErr = r.collection.BulkWrite(ctx, bulkOps)
		return bulkErr
	}, maxRetries, r.logger, "bulk shard acquisition")

	if err != nil {
		return 0, err
	}

	return int(bulkResult.ModifiedCount), nil
}

// ReleaseShardsWithIDs releases specific shards owned by this controller
// Uses best-effort approach: continues releasing even if some shards fail
// Returns error only if ALL shards fail to release
func (r *MongoShardRepository) ReleaseShardsWithIDs(ctx context.Context, shardIDs []int, controllerID string) error {
	successCount := 0
	failedShards := []int{}

	for _, shardID := range shardIDs {
		filter := bson.M{
			"shard_id":      shardID,
			"controller_id": controllerID,
		}
		update := bson.M{
			"$set": bson.M{
				"controller_id":  "",
				"lease_expiry":   time.Now().Add(-1 * time.Second), // Expire immediately
				"last_heartbeat": time.Now(),
			},
		}
		if _, err := r.collection.UpdateOne(ctx, filter, update); err != nil {
			r.logger.Errorf("Failed to release shard %d: %v", shardID, err)
			failedShards = append(failedShards, shardID)
		} else {
			successCount++
		}
	}

	// Return error only if ALL shards failed
	if successCount == 0 && len(shardIDs) > 0 {
		return fmt.Errorf("failed to release all %d shards", len(shardIDs))
	}

	// Log partial failures (but don't error)
	if len(failedShards) > 0 {
		r.logger.Warnf("⚠️  Partial shard release failure: %d/%d succeeded (failed shards: %v)",
			successCount, len(shardIDs), failedShards)
	} else if successCount > 0 {
		r.logger.Debugf("✅ Released %d shards for controller %s", successCount, controllerID)
	}

	return nil
}

// ReleaseAllShards releases all shards owned by this controller
func (r *MongoShardRepository) ReleaseAllShards(ctx context.Context, controllerID string) error {
	filter := bson.M{"controller_id": controllerID}
	update := bson.M{
		"$set": bson.M{
			"controller_id":  "",
			"lease_expiry":   time.Now().Add(-1 * time.Second),
			"last_heartbeat": time.Now(),
		},
	}

	result, err := r.collection.UpdateMany(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to release shards: %w", err)
	}

	r.logger.Infof("Released %d shards for controller %s", result.ModifiedCount, controllerID)
	return nil
}

// RenewLeases extends lease expiry for specific shards owned by this controller
func (r *MongoShardRepository) RenewLeases(ctx context.Context, shardIDs []int, controllerID string, leaseTTL int) (int64, error) {
	leaseExpiry := time.Now().Add(time.Duration(leaseTTL) * time.Second)

	filter := bson.M{
		"shard_id":      bson.M{"$in": shardIDs},
		"controller_id": controllerID,
	}

	update := bson.M{
		"$set": bson.M{
			"lease_expiry":   leaseExpiry,
			"last_heartbeat": time.Now(),
		},
	}

	result, err := r.collection.UpdateMany(ctx, filter, update)
	if err != nil {
		return 0, fmt.Errorf("failed to renew leases: %w", err)
	}

	return result.ModifiedCount, nil
}

// CountActiveControllers returns number of controllers currently holding leases
func (r *MongoShardRepository) CountActiveControllers(ctx context.Context) (int, error) {
	filter := bson.M{
		"controller_id": bson.M{"$ne": ""},
		"lease_expiry":  bson.M{"$gte": time.Now()},
	}

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: filter}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$controller_id"},
		}}},
		{{Key: "$count", Value: "total"}},
	}

	cursor, err := r.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return 0, fmt.Errorf("failed to count controllers: %w", err)
	}
	defer cursor.Close(ctx)

	var result []struct {
		Total int `bson:"total"`
	}
	if err := cursor.All(ctx, &result); err != nil {
		return 0, fmt.Errorf("failed to decode result: %w", err)
	}

	if len(result) == 0 {
		return 0, nil
	}
	return result[0].Total, nil
}

// VerifyShardsOwned checks which shards from the candidate list are actually owned
func (r *MongoShardRepository) VerifyShardsOwned(ctx context.Context, candidateShardIDs []int, controllerID string) ([]int, error) {
	filter := bson.M{
		"shard_id":      bson.M{"$in": candidateShardIDs},
		"controller_id": controllerID,
	}

	cursor, err := r.collection.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to verify shards: %w", err)
	}
	defer cursor.Close(ctx)

	var ownedShardIDs []int
	for cursor.Next(ctx) {
		var shard models.GSLBShard
		if err := cursor.Decode(&shard); err != nil {
			continue
		}
		ownedShardIDs = append(ownedShardIDs, shard.ShardID)
	}

	return ownedShardIDs, nil
}
