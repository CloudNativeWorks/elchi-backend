package xds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud"
	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
)

// parseDuplicateKeyError extracts a more user-friendly error message from MongoDB duplicate key errors
func parseDuplicateKeyError(err error, resourceName string) error {
	if err == nil {
		return nil
	}
	
	errStr := err.Error()
	if strings.Contains(errStr, "E11000 duplicate key error") {
		// Extract collection name from error message
		collection := "resource"
		if strings.Contains(errStr, "collection: elchi.") {
			parts := strings.Split(errStr, "collection: elchi.")
			if len(parts) > 1 {
				collectionPart := strings.Split(parts[1], " ")[0]
				collection = collectionPart
			}
		}
		
		return fmt.Errorf("A %s with the name \"%s\" already exists. Please choose a different name.", collection, resourceName)
	}
	
	return err
}

func (xds *AppHandler) SetResource(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	// Validate project access before creating resource
	if err := authorization.ValidateResourceProject(ctx, xds.Context.Client, requestDetails.User, resource); err != nil {
		return nil, fmt.Errorf("project access denied: %w", err)
	}

	general := resource.GetGeneral()
	err := resources.PrepareResource(resource, requestDetails, xds.Logger.Logger, xds.ResourceService)
	if err != nil {
		return nil, err
	}
	bootstrapID := ""
	resourceID := ""
	serviceID := ""
	adminPort := uint32(0)
	collection := xds.Context.Client.Collection(general.Collection)
	inserResult, err := collection.InsertOne(ctx, resource)
	if err != nil {
		if er := new(mongo.WriteException); errors.As(err, &er) && er.WriteErrors[0].Code == 11000 {
			return nil, parseDuplicateKeyError(err, general.Name)
		}
		return nil, err
	}

	if general.GType == models.Listener {
		bootstrapID, adminPort, err = xds.createBootstrap(ctx, general, requestDetails)
		if err != nil {
			return nil, err
		}

		if general.Managed {
			serviceID, err = xds.createService(ctx, general.Name, general.Project, general.Version, adminPort, general.Permissions)
			if err != nil {
				return nil, err
			}
		}
	}

	if oid, ok := inserResult.InsertedID.(primitive.ObjectID); ok {
		resource.SetID(oid)
		resourceID = oid.Hex()
	}

	data := map[string]any{"bootstrap_id": bootstrapID, "resource_id": resourceID, "service_id": serviceID}

	return map[string]any{"message": "Success", "data": data}, nil
}

func (xds *AppHandler) createService(ctx context.Context, serviceName string, project string, version string, adminPort uint32, listenerPermissions models.Permissions) (string, error) {
	var service models.Service
	collection := xds.Context.Client.Collection("services")
	service.Name = serviceName
	service.Project = project
	service.Version = version
	service.AdminPort = adminPort
	service.Clients = []models.ListenerClient{}
	
	// Service permissions inherit from the listener that created it
	service.Permissions = listenerPermissions

	inserResult, err := collection.InsertOne(ctx, service)
	if err != nil {
		if er := new(mongo.WriteException); errors.As(err, &er) && er.WriteErrors[0].Code == 11000 {
			return "", parseDuplicateKeyError(err, serviceName)
		}
		return "", err
	}

	if oid, ok := inserResult.InsertedID.(primitive.ObjectID); ok {
		return oid.Hex(), nil
	}

	return "", errors.New("inserted ID is not a valid ObjectID")
}

func (xds *AppHandler) createBootstrap(ctx context.Context, listenerGeneral models.General, requestDetails models.RequestDetails) (string, uint32, error) {
	collection := xds.Context.Client.Collection("bootstrap")
	bootstrap, err := crud.GetBootstrap(ctx, xds.Context.Client, listenerGeneral, xds.Context.Config)
	if err != nil {
		return "", 0, err
	}
	resource, err := DecodeFromMap(bootstrap)
	if err != nil {
		return "", 0, err
	}

	adminPort, err := resources.GetAdminPortFromBootstrap(resource.GetResource())
	if err != nil {
		return "", 0, err
	}

	err = resources.PrepareResource(resource, requestDetails, xds.Logger.Logger, xds.ResourceService)
	if err != nil {
		return "", 0, err
	}

	inserResult, err := collection.InsertOne(ctx, resource)
	if err != nil {
		if er := new(mongo.WriteException); errors.As(err, &er) && er.WriteErrors[0].Code == 11000 {
			return "", 0, parseDuplicateKeyError(err, listenerGeneral.Name)
		}
		return "", 0, err
	}

	if oid, ok := inserResult.InsertedID.(primitive.ObjectID); ok {
		return oid.Hex(), adminPort, nil
	}

	return "", 0, errors.New("inserted ID is not a valid ObjectID")
}

func DecodeFromMap(data map[string]any) (models.ResourceClass, error) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	var resource models.DBResource
	if err := json.Unmarshal(jsonData, &resource); err != nil {
		return nil, err
	}

	return &resource, nil
}
