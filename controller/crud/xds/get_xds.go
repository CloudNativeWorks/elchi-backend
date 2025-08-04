package xds

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/common"
	"github.com/CloudNativeWorks/elchi-backend/pkg/errstr"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

func (xds *AppHandler) GetResource(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	collection := xds.Context.Client.Collection(requestDetails.Collection)

	filter, err := common.AddResourceIDFilter(requestDetails, bson.M{})
	if err != nil {
		return nil, errors.New("invalid id format")
	}

	filterWithRestriction := common.AddUserFilter(requestDetails, filter)
	result := collection.FindOne(ctx, filterWithRestriction)

	if result.Err() != nil {
		if errors.Is(result.Err(), mongo.ErrNoDocuments) {
			return nil, errors.New("not found: (" + requestDetails.Name + ")")
		}
		return nil, errstr.ErrUnknownDBError
	}

	err = result.Decode(resource)
	if err != nil {
		return nil, err
	}

	return resource, nil
}
