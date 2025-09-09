package custom

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/common"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

func (custom *AppHandler) GetResourceCounts(ctx context.Context, _ models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	results := make(map[string]int64)
	var collections []string

	for key := range db.Indices {
		collections = append(collections, key)
	}

	for _, collectionName := range collections {
		collection := custom.Context.Client.Collection(collectionName)
		filter := bson.M{"general.project": requestDetails.Project}
		filter = common.AddUserFilter(requestDetails, filter)

		count, err := collection.CountDocuments(ctx, filter)
		if err != nil {
			continue
		}

		results[collectionName] = count
	}

	return results, nil
}

func (custom *AppHandler) GetFilterCounts(ctx context.Context, _ models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	groupKey := "$general.canonical_name"
	if requestDetails.Category != "" {
		groupKey = "$general.category"
	}
	collection := custom.Context.Client.Collection(requestDetails.Collection)
	filter := bson.M{"general.project": requestDetails.Project}
	filter = common.AddUserFilter(requestDetails, filter)

	pipeline := mongo.Pipeline{
		{
			{Key: "$match", Value: filter},
		},
		{
			{Key: "$group", Value: bson.D{
				{Key: "_id", Value: groupKey},
				{Key: "count", Value: bson.D{
					{Key: "$sum", Value: 1},
				}},
			}},
		},
	}

	cursor, err := collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregation error: %w", err)
	}
	defer cursor.Close(ctx)

	results := make(map[string]int64)
	for cursor.Next(ctx) {
		var result struct {
			ID    string `bson:"_id"`
			Count int64  `bson:"count"`
		}
		if err := cursor.Decode(&result); err != nil {
			return nil, fmt.Errorf("decoding error: %w", err)
		}
		results[result.ID] = result.Count
	}

	return results, nil
}
