package waf

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// WASMExtensionInfo contains information about a WASM extension using a WAF config
type WASMExtensionInfo struct {
	ID            string `bson:"_id"`
	Name          string `bson:"name"`
	CanonicalName string `bson:"canonical_name"`
	Version       string `bson:"version"`
	Collection    string `bson:"collection"`
	Project       string `bson:"project"`
	GType         string `bson:"gtype"`
}

// FindWASMExtensionsUsingWAF finds all HTTP WASM extensions that use the specified WAF config
// Searches in both "filters" and "extensions" collections
func FindWASMExtensionsUsingWAF(ctx context.Context, db *mongo.Database, wafName string, project string) ([]WASMExtensionInfo, error) {
	if wafName == "" {
		return nil, fmt.Errorf("wafName cannot be empty")
	}

	var results []WASMExtensionInfo

	// Search filter for HTTP WASM extensions using this WAF
	filter := bson.M{
		"general.gtype":   models.HTTPWasm.String(),
		"general.waf":     wafName,
		"general.project": project,
	}

	// Search in "filters" collection
	filtersResults, err := searchInCollection(ctx, db, "filters", filter)
	if err != nil {
		return nil, fmt.Errorf("error searching in filters collection: %w", err)
	}
	results = append(results, filtersResults...)

	// Search in "extensions" collection
	extensionsResults, err := searchInCollection(ctx, db, "extensions", filter)
	if err != nil {
		return nil, fmt.Errorf("error searching in extensions collection: %w", err)
	}
	results = append(results, extensionsResults...)

	return results, nil
}

// searchInCollection performs the actual MongoDB search in a specific collection
func searchInCollection(ctx context.Context, db *mongo.Database, collectionName string, filter bson.M) ([]WASMExtensionInfo, error) {
	collection := db.Collection(collectionName)

	// Projection to get only needed fields
	projection := bson.M{
		"_id":                   1,
		"general.name":          1,
		"general.canonical_name": 1,
		"general.version":       1,
		"general.collection":    1,
		"general.project":       1,
		"general.gtype":         1,
	}

	cursor, err := collection.Find(ctx, filter, options.Find().SetProjection(projection))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var results []WASMExtensionInfo
	for cursor.Next(ctx) {
		var doc struct {
			ID      string `bson:"_id"`
			General struct {
				Name          string `bson:"name"`
				CanonicalName string `bson:"canonical_name"`
				Version       string `bson:"version"`
				Collection    string `bson:"collection"`
				Project       string `bson:"project"`
				GType         string `bson:"gtype"`
			} `bson:"general"`
		}

		if err := cursor.Decode(&doc); err != nil {
			return nil, err
		}

		results = append(results, WASMExtensionInfo{
			ID:            doc.ID,
			Name:          doc.General.Name,
			CanonicalName: doc.General.CanonicalName,
			Version:       doc.General.Version,
			Collection:    collectionName, // Use actual collection name
			Project:       doc.General.Project,
			GType:         doc.General.GType,
		})
	}

	if err := cursor.Err(); err != nil {
		return nil, err
	}

	return results, nil
}
