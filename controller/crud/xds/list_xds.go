package xds

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/common"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

type Field struct {
	Name string
	Type string
}

type ResourceSchema map[string][]Field

func (xds *AppHandler) ListResource(ctx context.Context, _ models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	filter := bson.M{}
	collection := xds.Context.Client.Collection(requestDetails.Collection)
	opts := options.Find().SetProjection(bson.M{"resource": 0})

	if requestDetails.GType != "" {
		filter["general.gtype"] = requestDetails.GType.String()
	}

	// Use secure filtering that properly handles project access for all roles
	filterWithRestriction, err := common.AddSecureUserFilter(ctx, xds.Context.Client, requestDetails, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to apply security filters: %w", err)
	}

	cursor, err := collection.Find(ctx, filterWithRestriction, opts)
	if err != nil {
		return nil, fmt.Errorf("could not find records: %w", err)
	}

	var records []bson.M
	if err = cursor.All(ctx, &records); err != nil {
		return nil, fmt.Errorf("could not decode records: %w", err)
	}

	return common.TransformGenerals(records), nil
}
