package xds

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/common"
	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/errstr"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
	"github.com/CloudNativeWorks/elchi-backend/pkg/validation"
)

// compareElchiDiscovery compares two ElchiDiscovery slices for equality
func compareElchiDiscovery(old, new []*models.ElchiDiscovery) bool {
	if len(old) != len(new) {
		return false
	}

	// Create map for comparison
	oldMap := make(map[string]*models.ElchiDiscovery)
	for _, disc := range old {
		key := fmt.Sprintf("%s:%d:%s", disc.ClusterName, disc.Port, disc.Protocol)
		oldMap[key] = disc
	}

	// Check if all new items exist in old with same values
	for _, disc := range new {
		key := fmt.Sprintf("%s:%d:%s", disc.ClusterName, disc.Port, disc.Protocol)
		if _, exists := oldMap[key]; !exists {
			return false
		}
	}

	return true
}

func (xds *AppHandler) UpdateResource(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	// Validate XDS resource name format
	if err := validation.ValidateXDSResource(resource); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	// Validate project access and modification permissions
	if err := authorization.CanModifyResource(ctx, xds.Context.Client, requestDetails.User, resource); err != nil {
		return nil, fmt.Errorf("modification denied: %w", err)
	}

	// Auto-validate resource based on GType if validation is requested
	if validationResult, err := validation.GlobalValidatorRegistry.ValidateByGType(resource, requestDetails, xds.Logger); err != nil {
		return validationResult, err
	}

	isDefault, err := common.IsDefaultResource(ctx, xds.Context, requestDetails.Name, requestDetails.Collection, requestDetails.Project)
	if err != nil {
		xds.Logger.Errorf("An error occurred while checking if the resource is default: %v", err)
	} else if isDefault {
		if requestDetails.User.Role != models.RoleOwner {
			return nil, errors.New("this resource is a default resource and cannot be changed")
		}
	}

	filter, err := common.AddResourceIDFilter(requestDetails, bson.M{"general.name": requestDetails.Name, "general.version": requestDetails.Version, "general.project": requestDetails.Project})
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

	// Get existing resource to check elchi_discovery changes
	var existingResource models.DBResource
	if err := result.Decode(&existingResource); err != nil {
		return nil, fmt.Errorf("failed to decode existing resource: %v", err)
	}

	// Check if elchi_discovery changed for endpoints
	needsPopulate := false
	general := resource.GetGeneral()

	if general.GType == models.Endpoint && len(general.ElchiDiscovery) > 0 {
		// Compare elchi_discovery configurations
		if !compareElchiDiscovery(existingResource.General.ElchiDiscovery, general.ElchiDiscovery) {
			needsPopulate = true
			xds.Logger.Infof("ElchiDiscovery changed for endpoint %s, will repopulate from discovery", general.Name)
		}
	}

	// Populate endpoint from discovery if needed
	if needsPopulate {
		if err := xds.populateEndpointFromDiscovery(ctx, resource); err != nil {
			xds.Logger.Errorf("Failed to populate endpoint from discovery: %v", err)
			// Continue even if population fails
		}
	}

	newResource := resource.GetResource()
	nodeid := fmt.Sprintf("%s::%s", requestDetails.Name, requestDetails.Project)

	if err := resources.ValidateResourceWithClient(context.Background(), resource.GetGeneral().GType, resource.GetGeneral().Version, nodeid, newResource, xds.ResourceService); err != nil {
		return nil, fmt.Errorf("%v", err)
	}

	// Version increment moved to control-plane GenerateSnapshot for centralized control
	// Keep existing version without manual increment

	resource.SetTypedConfig(resources.DecodeSetTypedConfigs(resource, xds.Logger))

	update := bson.M{
		"$set": bson.M{
			"resource.resource":        newResource,
			"resource.version":         resource.GetVersion(),
			"general.config_discovery": resource.GetConfigDiscovery(),
			"general.elchi_discovery":  resource.GetGeneral().ElchiDiscovery,
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

	// Handle download case - keep synchronous for immediate response
	if requestDetails.SaveOrPublish == "download" {
		return xds.handleDownloadRequest(ctx, requestDetails)
	}

	// For publish operations, use async processing
	if requestDetails.SaveOrPublish == "publish" {
		asyncService := NewAsyncXDSService(xds)
		return asyncService.ProcessAsyncUpdate(ctx, resource, requestDetails, project)
	}

	// Default case - return success for non-publish operations
	return gin.H{"message": "Success", "data": gin.H{"status": "completed"}}, nil
}

// handleDownloadRequest handles bootstrap download requests
func (xds *AppHandler) handleDownloadRequest(ctx context.Context, requestDetails models.RequestDetails) (gin.H, error) {
	if bootstrap, err := xds.DownloadBootstrap(ctx, requestDetails, models.ClientFields{}); err != nil {
		return gin.H{"message": "Error", "data": bootstrap}, err
	} else {
		return gin.H{"message": "Success", "data": bootstrap}, nil
	}
}
