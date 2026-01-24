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

func (extension *AppHandler) DelExtension(ctx context.Context, _ models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	// P1-2 FIX: Check delete permissions based on role
	// Viewer cannot delete anything
	if requestDetails.User.Role == models.RoleViewer {
		return nil, errors.New("insufficient privileges: viewers cannot delete resources")
	}

	resourceType := requestDetails.Collection
	collection := extension.Context.Client.Collection(resourceType)
	filter, err := common.AddResourceIDFilter(requestDetails, buildFilter(requestDetails))
	if err != nil {
		return nil, errors.New("invalid id format")
	}

	// Get resource to check if it's default and for permission check
	var resourceDoc models.DBResource
	if err := collection.FindOne(ctx, filter).Decode(&resourceDoc); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, errors.New("resource not found")
		}
		return nil, err
	}

	if common.IsDefaultXDSResource(&resourceDoc) {
		return nil, errors.New("this resource is a default resource and cannot be deleted")
	}

	// P1-2 FIX: Editor can only delete resources they have permission for
	if requestDetails.User.Role == models.RoleEditor {
		if err := checkResourcePermission(requestDetails.User, &resourceDoc); err != nil {
			return nil, errors.New("insufficient privileges: you don't have permission to delete this resource")
		}
	}
	// Note: Owner and Admin can delete any resource (already checked via buildFilter and project access)

	downstreamFilterModel := downstreamfilters.DownstreamFilter{
		Name:    requestDetails.Name,
		Project: requestDetails.Project,
		Version: requestDetails.Version,
	}

	dependList := common.IsDeletable(ctx, extension.Context, requestDetails.GType, downstreamFilterModel)
	if len(dependList) > 0 {
		message := "Cannot delete resource. It is used by:\n" + strings.Join(dependList, "\n")
		return nil, errors.New(message)
	}

	if err := checkDocumentExists(ctx, extension, collection, filter); err != nil {
		return nil, err
	}

	if err := deleteDocument(ctx, extension, collection, filter); err != nil {
		return nil, err
	}

	return gin.H{"message": "Success"}, nil
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

// checkResourcePermission checks if user has permission to access the resource
// P1-2 FIX: Helper function for Editor permission check
func checkResourcePermission(user models.UserDetails, resource *models.DBResource) error {
	// If no permissions set at all, deny access
	if len(resource.General.Permissions.Users) == 0 && len(resource.General.Permissions.Groups) == 0 {
		return errors.New("resource has no permissions set")
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

	return errors.New("user has no permission for this resource")
}
