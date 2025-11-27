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

	// P1-2 FIX: Check delete permissions based on role
	// Viewer cannot delete anything
	if requestDetails.User.Role == models.RoleViewer {
		return nil, fmt.Errorf("insufficient privileges: viewers cannot delete resources")
	}

	// Check if resource is default will be done after fetching the document

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

	// Get resource document for additional checks (managed flag, default check, permissions)
	var resourceDoc models.DBResource
	var isManaged bool
	if err := collection.FindOne(ctx, filter).Decode(&resourceDoc); err == nil {
		// Check if resource is default (for all resource types)
		if common.IsDefaultXDSResource(&resourceDoc) {
			return nil, errors.New("this resource is a default resource and cannot be deleted")
		}

		// P1-2 FIX: Editor can only delete resources they have permission for
		if requestDetails.User.Role == models.RoleEditor {
			if err := checkResourcePermission(requestDetails.User, &resourceDoc); err != nil {
				return nil, fmt.Errorf("insufficient privileges: you don't have permission to delete this resource")
			}
		}
		// Note: Owner and Admin can delete any resource (already checked via buildFilter and project access)

		// For listeners, check managed flag
		if resourceType == "listeners" {
			isManaged = resourceDoc.General.Managed
		}
	} else {
		return nil, err
	}
		
	// Only check service deployments for managed listeners
	if isManaged {
		// Check if service has active client deployments
		if err := xds.checkServiceHasActiveClients(ctx, requestDetails); err != nil {
			xds.Logger.Errorf("❌ BLOCKING DELETE: %v", err)
			return nil, err
		}
		xds.Logger.Infof("✅ Managed listener delete allowed: no active clients found")
	}

	if err := deleteDocument(ctx, collection, filter); err != nil {
		return nil, err
	}

	if resourceType == "listeners" {
		// Always delete bootstrap
		xds.Logger.Debugf("Attempting to delete bootstrap for listener: %s", requestDetails.Name)
		if err := xds.delBootstrap(ctx, filter); err != nil {
			xds.Logger.Errorf("Failed to delete bootstrap: %v", err)
			return nil, err
		}
		xds.Logger.Infof("✅ Successfully deleted bootstrap for listener: %s", requestDetails.Name)

		// Only delete service and admin_port if listener was managed
		if isManaged {
			xds.Logger.Debugf("Attempting to delete service for managed listener: %s", requestDetails.Name)
			if err := xds.delService(ctx, requestDetails); err != nil {
				xds.Logger.Errorf("Failed to delete service: %v", err)
				return nil, err
			}
			xds.Logger.Infof("✅ Successfully deleted service for listener: %s", requestDetails.Name)

			xds.Logger.Debugf("Attempting to delete admin_port for managed listener: %s", requestDetails.Name)
			if err := xds.delAdminPort(ctx, requestDetails); err != nil {
				xds.Logger.Errorf("Failed to delete admin_port: %v", err)
				return nil, err
			}
			xds.Logger.Infof("✅ Successfully deleted admin_port for listener: %s", requestDetails.Name)
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
	// Owner and Admin: No group filtering - they can access all resources in their project
	if requestDetails.User.IsOwner || requestDetails.User.Role == models.RoleAdmin {
		return bson.M{
			"general.name":    requestDetails.Name,
			"general.project": requestDetails.Project,
			"general.version": requestDetails.Version,
		}
	}

	// Editor/Viewer: Group filtering required
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

// checkResourcePermission checks if user has permission to access the resource
// P1-2 FIX: Helper function for Editor permission check
func checkResourcePermission(user models.UserDetails, resource *models.DBResource) error {
	// If no permissions set at all, deny access
	if len(resource.General.Permissions.Users) == 0 && len(resource.General.Permissions.Groups) == 0 {
		return fmt.Errorf("resource has no permissions set")
	}

	// Check if user is in permissions.users[]
	for _, userID := range resource.General.Permissions.Users {
		if userID == user.UserID {
			return nil
		}
	}

	// Build complete group list including base_group
	allGroups := append([]string{}, user.Groups...)
	if user.BaseGroup != "" {
		found := false
		for _, g := range allGroups {
			if g == user.BaseGroup {
				found = true
				break
			}
		}
		if !found {
			allGroups = append(allGroups, user.BaseGroup)
		}
	}

	// Check if any of user's groups is in permissions.groups[]
	for _, groupID := range resource.General.Permissions.Groups {
		for _, userGroupID := range allGroups {
			if groupID == userGroupID {
				return nil
			}
		}
	}

	return fmt.Errorf("user has no permission for this resource")
}
