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
	"github.com/CloudNativeWorks/elchi-backend/pkg/security/secretredact"
)

// SecretsCollection is the MongoDB collection name that triggers TLS
// secret redaction on read.
const SecretsCollection = "secrets"

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

	// For the secrets collection, mask sensitive `inline_string` /
	// `inline_bytes` leaves before returning to the management API. The
	// data plane (control-plane → Envoy via xDS) is unaffected — that
	// path reads MongoDB directly without going through this handler.
	if requestDetails.Collection == SecretsCollection {
		masked, err := secretredact.MaskJSON(resource)
		if err != nil {
			return nil, fmt.Errorf("failed to mask secret resource: %w", err)
		}
		return masked, nil
	}

	return resource, nil
}
