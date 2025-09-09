package extension

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/common"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async/job"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
	"github.com/CloudNativeWorks/elchi-backend/pkg/validation"
)

func (extension *AppHandler) UpdateFilters(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	filter, err := common.AddResourceIDFilter(requestDetails, bson.M{"general.canonical_name": requestDetails.CanonicalName, "general.project": requestDetails.Project, "general.version": requestDetails.Version})
	if err != nil {
		return nil, errors.New("invalid id format")
	}
	return updateResource(ctx, extension, resource, requestDetails, filter)
}

func (extension *AppHandler) UpdateExtensions(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	filter, err := common.AddResourceIDFilter(requestDetails, bson.M{"general.project": requestDetails.Project})
	if err != nil {
		return nil, errors.New("invalid id format")
	}
	return updateResource(ctx, extension, resource, requestDetails, filter)
}

func updateResource(ctx context.Context, extension *AppHandler, resource models.ResourceClass, requestDetails models.RequestDetails, filter bson.M) (any, error) {
	// Validate Extension resource name format
	if err := validation.ValidateExtensionResource(resource); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	// Auto-validate resource based on GType if validation is requested
	if validationResult, err := validation.GlobalValidatorRegistry.ValidateByGType(resource, requestDetails, extension.Logger); err != nil {
		return validationResult, err
	}

	isDefault, err := common.IsDefaultResource(ctx, extension.Context, requestDetails.Name, requestDetails.Collection, requestDetails.Project)
	if err != nil {
		extension.Logger.Errorf("An error occurred while checking if the resource is default: %v", err)
	} else if isDefault {
		if requestDetails.User.Role != models.RoleOwner {
			return nil, errors.New("this resource is a default resource and cannot be changed")
		}
	}

	filterWithRestriction := common.AddUserFilter(requestDetails, filter)
	// Version increment moved to control-plane GenerateSnapshot for centralized control
	// Keep existing version without manual increment
	newResource := resource.GetResource()
	nodeid := fmt.Sprintf("%s::%s", requestDetails.Name, requestDetails.Project)

	if err := resources.ValidateResourceWithClient(
		context.Background(),
		resource.GetGeneral().GType,
		resource.GetGeneral().Version,
		nodeid,
		newResource,
		extension.ResourceService,
	); err != nil {
		return nil, fmt.Errorf("%v", err)
	}

	resource.SetTypedConfig(resources.DecodeSetTypedConfigs(resource, extension.Logger))

	update := bson.M{
		"$set": bson.M{
			"resource.resource":        newResource,
			"resource.version":         resource.GetVersion(),
			"general.config_discovery": resource.GetConfigDiscovery(),
			"general.updated_at":       primitive.NewDateTimeFromTime(time.Now()),
			"general.typed_config":     resource.GetTypedConfig(),
		},
	}

	collection := extension.Context.Client.Collection(requestDetails.Collection)
	_, err = collection.UpdateOne(ctx, filterWithRestriction, update)
	if err != nil {
		return nil, fmt.Errorf("update failed: %w", err)
	}

	project := resource.GetGeneral().Project
	
	// For extension updates, use async processing with fast response pattern if publish operation
	if requestDetails.SaveOrPublish == "publish" {
		// Initialize async job system
		asyncSystem, err := async.NewAsyncJobSystem(&async.Config{
			MongoDB:     extension.Context.Client,
			DBContext:   extension.Context,
			WorkerCount: 3,
			BatchSize:   5,
		})
		if err != nil {
			extension.Logger.Errorf("Failed to initialize async system: %v", err)
			// Fallback to synchronous processing
			changedResources := crud.HandleResourceChange(ctx, resource, requestDetails, extension.Context, project, extension.PokeService)
			return gin.H{"message": "Success", "data": changedResources}, nil
		}
		
		// Set poke service for async system
		if err := asyncSystem.SetPokeService(extension.PokeService); err != nil {
			extension.Logger.Errorf("Failed to set poke service: %v", err)
			// Fallback to synchronous processing
			changedResources := crud.HandleResourceChange(ctx, resource, requestDetails, extension.Context, project, extension.PokeService)
			return gin.H{"message": "Success", "data": changedResources}, nil
		}
		
		// Create preliminary job immediately (fast response)
		jobReq := &async.CreateJobRequest{
			Type:   job.JobTypeSnapshotUpdate,
			Status: job.JobStatusAnalyzing, // Will be set automatically by CreatePreliminaryJob
			Metadata: &job.JobMetadata{
				SourceResource: &job.SourceResource{
					Type:       resource.GetGeneral().GType.String(),
					Name:       requestDetails.Name,
					Collection: requestDetails.Collection,
					Action:     job.ActionType("UPDATE"),
					ProjectID:  project,
					Version:    requestDetails.Version,
				},
				TriggerUser: &job.TriggerUser{
					ID:       requestDetails.User.UserID,
					Username: requestDetails.User.UserName,
					Role:     string(requestDetails.User.Role),
				},
				AffectedListeners: []string{}, // Will be populated by background analysis
				TotalAffected:     0,          // Will be updated after analysis
				AnalysisDuration:  0,          // Will be set after analysis completes
			},
		}
		
		preliminaryJob, err := asyncSystem.CreatePreliminaryJob(ctx, jobReq)
		if err != nil {
			extension.Logger.Errorf("Failed to create preliminary job: %v", err)
			// Fallback to synchronous processing
			changedResources := crud.HandleResourceChange(ctx, resource, requestDetails, extension.Context, project, extension.PokeService)
			return gin.H{"message": "Success", "data": changedResources}, nil
		}
		
		// Start background dependency analysis
		asyncSystem.PerformBackgroundAnalysis(preliminaryJob.ID.Hex(), resource, requestDetails, project)
		
		extension.Logger.Infof("Created preliminary job %s for %s extension %s, analyzing dependencies in background",
			preliminaryJob.JobID, resource.GetGeneral().GType, requestDetails.Name)
		
		// Return fast response with task ID
		return gin.H{
			"message": "Success",
			"data": gin.H{
				"task_id": preliminaryJob.JobID,
				"status":  "analyzing",
				"message": "Extension saved successfully, dependency analysis in progress",
			},
		}, nil
	}
	
	// Default case - fallback to synchronous processing for non-publish operations
	changedResources := crud.HandleResourceChange(ctx, resource, requestDetails, extension.Context, project, extension.PokeService)
	return gin.H{"message": "Success", "data": changedResources}, nil
}
