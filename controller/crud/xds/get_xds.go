package xds

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/common"
	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/errstr"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

func (xds *AppHandler) GetResource(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	collection := xds.Context.Client.Collection(requestDetails.Collection)

	// Validate project access first
	if requestDetails.Project != "" {
		if err := authorization.ValidateRequestProject(ctx, xds.Context.Client, requestDetails.User, requestDetails.Project); err != nil {
			return nil, fmt.Errorf("get access denied: %w", err)
		}
	}

	filter, err := common.AddResourceIDFilter(requestDetails, bson.M{})
	if err != nil {
		return nil, errors.New("invalid id format")
	}

	// Use secure filtering that doesn't bypass Admin users
	filterWithRestriction, err := common.AddSecureUserFilter(ctx, xds.Context.Client, requestDetails, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to apply security filters: %w", err)
	}

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
