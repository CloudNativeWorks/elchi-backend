package extension

import (
	"context"
	"errors"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/common"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func (extension *AppHandler) GetExtensions(ctx context.Context, _ models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	var records []bson.M
	filter := bson.M{"general.type": requestDetails.Type, "general.project": requestDetails.Project}
	filterWithRestriction := common.AddUserFilter(requestDetails, filter)
	collection := extension.Context.Client.Collection(requestDetails.Collection)

	opts := options.Find().SetProjection(bson.M{"resource": 0})
	cursor, err := collection.Find(ctx, filterWithRestriction, opts)
	if err != nil {
		return nil, fmt.Errorf("db find error: %w", err)
	}
	defer cursor.Close(ctx)

	if err = cursor.All(ctx, &records); err != nil {
		return nil, fmt.Errorf("cursor all error: %w", err)
	}

	generals := common.TransformGenerals(records)
	return generals, nil
}

func (extension *AppHandler) GetExtension(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	return getExtensionByFilter(ctx, resource, extension, requestDetails, bson.M{
		"general.name":           requestDetails.Name,
		"general.canonical_name": requestDetails.CanonicalName,
		"general.project":        requestDetails.Project,
	})
}

func (extension *AppHandler) GetOtherExtension(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	fmt.Println("requestDetails.Version", requestDetails.Version)
	return getExtensionByFilter(ctx, resource, extension, requestDetails, bson.M{
		"general.name":    requestDetails.Name,
		"general.project": requestDetails.Project,
	})
}

func getExtensionByFilter(ctx context.Context, resource models.ResourceClass, extension *AppHandler, requestDetails models.RequestDetails, filter bson.M) (any, error) {
	collection := extension.Context.Client.Collection(requestDetails.Collection)
	resourceIDFilter, err := common.AddResourceIDFilter(requestDetails, filter)
	if err != nil {
		return nil, fmt.Errorf("add resource id filter error: %w", err)
	}

	filterWithRestriction := common.AddUserFilter(requestDetails, resourceIDFilter)
	result := collection.FindOne(ctx, filterWithRestriction)

	if result.Err() != nil {
		if errors.Is(result.Err(), mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("resource not found - type: %s, name: %s, project: %s",
				requestDetails.Collection,
				requestDetails.Name,
				requestDetails.Project,
			)
		}
		return nil, fmt.Errorf("db find one error: %w", result.Err())
	}

	if err := result.Decode(resource); err != nil {
		return nil, fmt.Errorf("decode error: %w", err)
	}

	return resource, nil
}
