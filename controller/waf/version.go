package waf

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// WAFVersionsCollection is the MongoDB collection storing per-config snapshots.
const WAFVersionsCollection = "waf_versions"

// MaxVersionsPerConfig is the number of recent snapshots retained per config.
// Older snapshots are pruned by version_cleanup.go.
const MaxVersionsPerConfig = 50

// WAFConfigVersion is a point-in-time snapshot of a WAFConfig, captured on
// every successful Update.
type WAFConfigVersion struct {
	ID         primitive.ObjectID `bson:"_id,omitempty" json:"id,omitempty"`
	ConfigID   primitive.ObjectID `bson:"config_id" json:"config_id"`
	Version    int                `bson:"version" json:"version"`
	Data       WAFConfigData      `bson:"data" json:"data"`
	Name       string             `bson:"name" json:"name"`
	AuthorID   string             `bson:"author_id" json:"author_id"`
	AuthorName string             `bson:"author_name" json:"author_name"`
	Message    string             `bson:"message,omitempty" json:"message,omitempty"`
	CreatedAt  time.Time          `bson:"created_at" json:"created_at"`
}

// WAFVersionService handles snapshot CRUD against the waf_versions collection.
type WAFVersionService struct {
	dbContext *db.AppContext
	logger    *logger.Logger
}

// NewWAFVersionService creates a new version service.
func NewWAFVersionService(dbContext *db.AppContext, logger *logger.Logger) *WAFVersionService {
	return &WAFVersionService{dbContext: dbContext, logger: logger}
}

// RecordSnapshot inserts a new version snapshot when the post-update state
// differs from the most recent snapshot. No-op PUTs (e.g. user double-clicked
// Save, or FE re-emitted the same body) do NOT create new versions — that
// would pollute the history with duplicate entries.
//
// Best-effort: lookup or insert errors are logged at WARN and never
// propagated, so a snapshot failure cannot break the user's update.
func (s *WAFVersionService) RecordSnapshot(ctx context.Context, config *WAFConfig, authorID, authorName string) {
	if config == nil {
		return
	}
	latest, err := s.latestSnapshot(ctx, config.ID)
	if err != nil {
		s.logger.Warnf("WAF version: latestSnapshot lookup failed for config %s: %v", config.ID.Hex(), err)
		return
	}

	if latest != nil && snapshotMatches(latest, config) {
		// Nothing changed since the previous version. Skip silently.
		return
	}

	next := 1
	if latest != nil {
		next = latest.Version + 1
	}

	snapshot := WAFConfigVersion{
		ConfigID:   config.ID,
		Version:    next,
		Data:       config.Data,
		Name:       config.Name,
		AuthorID:   authorID,
		AuthorName: authorName,
		CreatedAt:  time.Now(),
	}
	if _, err := s.dbContext.Client.Collection(WAFVersionsCollection).InsertOne(ctx, snapshot); err != nil {
		s.logger.Warnf("WAF version: snapshot insert failed for config %s v%d: %v", config.ID.Hex(), next, err)
	}
}

// snapshotMatches reports whether the latest snapshot already represents the
// post-update state. We compare the user-meaningful fields: Name and Data.
// (CreatedAt, AuthorID, etc. always change and would defeat the dedup.)
func snapshotMatches(latest *WAFConfigVersion, config *WAFConfig) bool {
	if latest == nil || config == nil {
		return false
	}
	return latest.Name == config.Name && reflect.DeepEqual(latest.Data, config.Data)
}

// List returns the most recent N snapshots for a config, newest first.
func (s *WAFVersionService) List(ctx context.Context, configID primitive.ObjectID, limit int64) ([]WAFConfigVersion, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	cursor, err := s.dbContext.Client.Collection(WAFVersionsCollection).Find(
		ctx,
		bson.M{"config_id": configID},
		options.Find().
			SetSort(bson.D{{Key: "version", Value: -1}}).
			SetLimit(limit),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list WAF versions: %w", err)
	}
	defer cursor.Close(ctx)

	var versions []WAFConfigVersion
	if err := cursor.All(ctx, &versions); err != nil {
		return nil, fmt.Errorf("failed to decode WAF versions: %w", err)
	}
	return versions, nil
}

// Get returns a single snapshot by (configID, version).
func (s *WAFVersionService) Get(ctx context.Context, configID primitive.ObjectID, version int) (*WAFConfigVersion, error) {
	var snap WAFConfigVersion
	err := s.dbContext.Client.Collection(WAFVersionsCollection).FindOne(
		ctx,
		bson.M{"config_id": configID, "version": version},
	).Decode(&snap)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("version %d not found for config %s", version, configID.Hex())
		}
		return nil, fmt.Errorf("failed to load version: %w", err)
	}
	return &snap, nil
}

// latestSnapshot returns the most-recent snapshot for a config, or (nil, nil)
// if none exist. It is the single source of truth for both dedup decisions
// and the next-version counter inside RecordSnapshot.
func (s *WAFVersionService) latestSnapshot(ctx context.Context, configID primitive.ObjectID) (*WAFConfigVersion, error) {
	var latest WAFConfigVersion
	err := s.dbContext.Client.Collection(WAFVersionsCollection).FindOne(
		ctx,
		bson.M{"config_id": configID},
		options.FindOne().SetSort(bson.D{{Key: "version", Value: -1}}),
	).Decode(&latest)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, err
	}
	return &latest, nil
}
