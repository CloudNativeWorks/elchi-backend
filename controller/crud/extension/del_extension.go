package extension

import (
	"context"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/common"
	"github.com/CloudNativeWorks/elchi-backend/pkg/errstr"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models/downstreamfilters"
)

func (xds *AppHandler) DelExtension(ctx context.Context, _ models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	resourceType := requestDetails.Collection
	collection := xds.Context.Client.Collection(resourceType)
	filter, err := common.AddResourceIDFilter(requestDetails, buildFilter(requestDetails))
	if err != nil {
		return nil, errors.New("invalid id format")
	}

	isDefault, err := common.IsDefaultResource(ctx, xds.Context, requestDetails.Name, resourceType, requestDetails.Project)
	if err != nil {
		xds.Logger.Errorf("An error occurred while checking if the resource is default: %v", err)
	} else if isDefault {
		return nil, errors.New("this resource is a default resource and cannot be deleted")
	}

	downstreamFilterModel := downstreamfilters.DownstreamFilter{
		Name:    requestDetails.Name,
		Project: requestDetails.Project,
		Version: requestDetails.Version,
	}

	dependList := common.IsDeletable(ctx, xds.Context, requestDetails.GType, downstreamFilterModel)
	if len(dependList) > 0 {
		message := "Resource has dependencies: \n " + strings.Join(dependList, ", ")
		return nil, errors.New(message)
	}

	if err := checkDocumentExists(ctx, xds, collection, filter); err != nil {
		return nil, err
	}

	if err := deleteDocument(ctx, xds, collection, filter); err != nil {
		return nil, err
	}

	return gin.H{"message": "Success"}, nil
}

func buildFilter(requestDetails models.RequestDetails) bson.M {
	if requestDetails.User.IsOwner {
		return bson.M{"general.name": requestDetails.Name, "general.project": requestDetails.Project}
	}
	return bson.M{
		"general.name":    requestDetails.Name,
		"general.project": requestDetails.Project,
		"general.groups": bson.M{
			"$in": requestDetails.User.Groups,
		},
	}
}

func checkDocumentExists(ctx context.Context, _ *AppHandler, collection *mongo.Collection, filter bson.M) error {
	result := collection.FindOne(ctx, filter)
	if result.Err() != nil {
		if errors.Is(result.Err(), mongo.ErrNoDocuments) {
			return errstr.ErrNoDocumentsDelete
		}
		return errstr.ErrUnknownDBError
	}
	return nil
}

func deleteDocument(ctx context.Context, _ *AppHandler, collection *mongo.Collection, filter bson.M) error {
	res, err := collection.DeleteOne(ctx, filter)
	if err != nil {
		return errstr.ErrUnknownDBError
	}

	if res.DeletedCount == 0 {
		return errstr.ErrNoDocuments
	}

	return nil
}
