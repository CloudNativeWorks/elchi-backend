package xds

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/common"
	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/errstr"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models/downstreamfilters"
)

func (xds *AppHandler) DelResource(ctx context.Context, _ models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	resourceType := requestDetails.Collection
	collection := xds.Context.Client.Collection(resourceType)

	// Validate project access before deletion
	if requestDetails.Project != "" {
		if err := authorization.ValidateRequestProject(ctx, xds.Context.Client, requestDetails.User, requestDetails.Project); err != nil {
			return nil, fmt.Errorf("delete access denied: %w", err)
		}
	}

	// Check delete permissions
	if requestDetails.User.Role == models.RoleViewer || requestDetails.User.Role == models.RoleEditor {
		return nil, fmt.Errorf("insufficient privileges: only owners and admins can delete resources")
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
		message := "Cannot delete resource. It is used by:\n" + strings.Join(dependList, "\n")
		return nil, errors.New(message)
	}

	filter, err := common.AddResourceIDFilter(requestDetails, buildFilter(requestDetails))
	if err != nil {
		return nil, errors.New("invalid id format")
	}

	if err := checkDocumentExists(ctx, collection, filter); err != nil {
		return nil, err
	}

	// For listeners, check managed flag and service deployment status before deletion
	var isManaged bool
	if resourceType == "listeners" {
		var listenerDoc models.DBResource
		if err := collection.FindOne(ctx, filter).Decode(&listenerDoc); err == nil {
			isManaged = listenerDoc.General.Managed
		}
		
		// Only check service deployments for managed listeners
		if isManaged {
			// Check if service has active client deployments
			if err := xds.checkServiceHasActiveClients(ctx, requestDetails); err != nil {
				return nil, err
			}
		}
	}

	if err := deleteDocument(ctx, collection, filter); err != nil {
		return nil, err
	}

	if resourceType == "listeners" {
		// Always delete bootstrap
		if err := xds.delBootstrap(ctx, filter); err != nil {
			return nil, err
		}

		// Only delete service and admin_port if listener was managed
		if isManaged {
			if err := xds.delService(ctx, requestDetails); err != nil {
				return nil, err
			}
			if err := xds.delAdminPort(ctx, requestDetails); err != nil {
				return nil, err
			}
		}
	}

	return gin.H{"message": "Success"}, nil
}

func (xds *AppHandler) delBootstrap(ctx context.Context, filter primitive.M) error {
	collection := xds.Context.Client.Collection("bootstrap")
	delete(filter, "_id")
	if err := checkDocumentExists(ctx, collection, filter); err != nil {
		return err
	}

	if err := deleteDocument(ctx, collection, filter); err != nil {
		return err
	}

	return nil
}

func (xds *AppHandler) delService(ctx context.Context, requestDetails models.RequestDetails) error {
	collection := xds.Context.Client.Collection("services")
	filter := bson.M{"name": requestDetails.Name, "project": requestDetails.Project, "version": requestDetails.Version}
	if err := checkDocumentExists(ctx, collection, filter); err != nil {
		return err
	}

	if err := deleteDocument(ctx, collection, filter); err != nil {
		return err
	}

	return nil
}

func (xds *AppHandler) delAdminPort(ctx context.Context, requestDetails models.RequestDetails) error {
	collection := xds.Context.Client.Collection("admin_ports")
	filter := bson.M{"name": requestDetails.Name, "project": requestDetails.Project, "version": requestDetails.Version}
	if err := checkDocumentExists(ctx, collection, filter); err != nil {
		return err
	}

	if err := deleteDocument(ctx, collection, filter); err != nil {
		return err
	}

	return nil
}

// checkServiceHasActiveClients checks if a service has active client deployments
func (xds *AppHandler) checkServiceHasActiveClients(ctx context.Context, requestDetails models.RequestDetails) error {
	serviceCollection := xds.Context.Client.Collection("services")
	serviceFilter := bson.M{
		"name":    requestDetails.Name,
		"project": requestDetails.Project,
		"version": requestDetails.Version,
	}
	
	var service models.Service
	err := serviceCollection.FindOne(ctx, serviceFilter).Decode(&service)
	if err != nil {
		// Service doesn't exist, no clients to check
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil
		}
		return fmt.Errorf("failed to check service status: %w", err)
	}
	
	// Check if service has active clients
	if len(service.Clients) > 0 {
		clientDetails := make([]string, 0, len(service.Clients))
		for _, client := range service.Clients {
			clientDetails = append(clientDetails, fmt.Sprintf("Client: %s (IP: %s)", 
				client.ClientID, client.DownstreamAddress))
		}
		
		return fmt.Errorf("cannot delete listener '%s': service has %d active deployment(s):\n%s", 
			requestDetails.Name, 
			len(service.Clients), 
			strings.Join(clientDetails, "\n"))
	}
	
	return nil
}

func buildFilter(requestDetails models.RequestDetails) bson.M {
	if requestDetails.User.IsOwner {
		return bson.M{"general.name": requestDetails.Name, "general.project": requestDetails.Project, "general.version": requestDetails.Version}
	}
	return bson.M{
		"general.name":    requestDetails.Name,
		"general.project": requestDetails.Project,
		"general.version": requestDetails.Version,
		"general.groups": bson.M{
			"$in": requestDetails.User.Groups,
		},
	}
}

func checkDocumentExists(ctx context.Context, collection *mongo.Collection, filter bson.M) error {
	result := collection.FindOne(ctx, filter)
	if result.Err() != nil {
		if errors.Is(result.Err(), mongo.ErrNoDocuments) {
			return errstr.ErrNoDocumentsDelete
		}
		return errstr.ErrUnknownDBError
	}
	return nil
}

func deleteDocument(ctx context.Context, collection *mongo.Collection, filter bson.M) error {
	res, err := collection.DeleteOne(ctx, filter)
	if err != nil {
		return errstr.ErrUnknownDBError
	}

	if res.DeletedCount == 0 {
		return errstr.ErrNoDocuments
	}

	return nil
}
