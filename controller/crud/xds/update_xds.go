package xds

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/common"
	"github.com/CloudNativeWorks/elchi-backend/pkg/errstr"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
)

func (xds *AppHandler) UpdateResource(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	isDefault, err := common.IsDefaultResource(ctx, xds.Context, requestDetails.Name, requestDetails.Collection, requestDetails.Project)
	if err != nil {
		xds.Logger.Errorf("An error occurred while checking if the resource is default: %v", err)
	} else if isDefault {
		if requestDetails.User.Role != models.RoleOwner {
			return nil, errors.New("this resource is a default resource and cannot be changed")
		}
	}

	filter, err := common.AddResourceIDFilter(requestDetails, bson.M{"general.name": requestDetails.Name})
	if err != nil {
		return nil, errors.New("invalid id format")
	}

	filterWithRestriction := common.AddUserFilter(requestDetails, filter)
	result := xds.Context.Client.Collection(requestDetails.Collection).FindOne(ctx, filterWithRestriction)

	if result.Err() != nil {
		if errors.Is(result.Err(), mongo.ErrNoDocuments) {
			return nil, errstr.ErrNoDocumentsUpdate
		}
		return nil, errstr.ErrUnknownDBError
	}

	newResource := resource.GetResource()
	version, _ := strconv.Atoi(resource.GetVersion().(string))
	nodeid := fmt.Sprintf("%s::%s", requestDetails.Name, requestDetails.Project)

	if err := resources.ValidateResourceWithClient(context.Background(), resource.GetGeneral().GType, resource.GetGeneral().Version, nodeid, newResource, xds.ResourceService); err != nil {
		return nil, fmt.Errorf("%v", err)
	}

	resource.SetVersion(strconv.Itoa(version + 1))
	resource.SetTypedConfig(resources.DecodeSetTypedConfigs(resource, xds.Logger.Logger))

	update := bson.M{
		"$set": bson.M{
			"resource.resource":        newResource,
			"resource.version":         resource.GetVersion(),
			"general.config_discovery": resource.GetConfigDiscovery(),
			"general.updated_at":       primitive.NewDateTimeFromTime(time.Now()),
			"general.typed_config":     resource.GetTypedConfig(),
		},
	}

	collection := xds.Context.Client.Collection(requestDetails.Collection)
	_, err = collection.UpdateOne(ctx, filterWithRestriction, update)
	if err != nil {
		return nil, err
	}

	project := resource.GetGeneral().Project
	changedResources := crud.HandleResourceChange(ctx, resource, requestDetails, xds.Context, project, xds.PokeService)

	if requestDetails.SaveOrPublish == "download" {
		if bootstrap, err := xds.DownloadBootstrap(ctx, requestDetails, models.ClientFields{}); err != nil {
			return gin.H{"message": "Error", "data": bootstrap}, err
		} else {
			return gin.H{"message": "Success", "data": bootstrap}, nil
		}
	}

	return gin.H{"message": "Success", "data": changedResources}, nil
}
