package extension

import (
	"context"
	"errors"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/common"
	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/gin-gonic/gin"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// GetExtensionsWithPagination retrieves extensions with pagination support
func (extension *AppHandler) GetExtensionsWithPagination(c *gin.Context, requestDetails models.RequestDetails) (any, error) {
	ctx := c.Request.Context()
	paginationReq := models.ParsePaginationParams(c)
	
	// Build base filter with project and type
	baseFilter := bson.M{
		"general.type": requestDetails.Type, 
		"general.project": requestDetails.Project,
	}
	
	// Validate project access
	if requestDetails.Project != "" {
		if err := authorization.ValidateRequestProject(ctx, extension.Context.Client, requestDetails.User, requestDetails.Project); err != nil {
			return nil, fmt.Errorf("extension access denied: %w", err)
		}
	}
	
	// Add pagination filters (name, version)
	filter := paginationReq.BuildMongoFilter(requestDetails.Project)
	// Merge base filter with pagination filter
	for k, v := range baseFilter {
		filter[k] = v
	}
	
	// Apply security filters
	filterWithRestriction, err := common.AddSecureUserFilter(ctx, extension.Context.Client, requestDetails, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to apply security filters: %w", err)
	}
	
	collection := extension.Context.Client.Collection(requestDetails.Collection)
	
	// Get total count for pagination
	totalCount, err := collection.CountDocuments(ctx, filterWithRestriction)
	if err != nil {
		return nil, fmt.Errorf("failed to count documents: %w", err)
	}
	
	// Build find options with pagination and sorting
	opts := paginationReq.BuildFindOptions()
	opts.SetProjection(bson.M{"resource": 0})
	
	// Execute query with pagination
	cursor, err := collection.Find(ctx, filterWithRestriction, opts)
	if err != nil {
		return nil, fmt.Errorf("db find error: %w", err)
	}
	defer cursor.Close(ctx)
	
	var records []bson.M
	if err = cursor.All(ctx, &records); err != nil {
		return nil, fmt.Errorf("cursor all error: %w", err)
	}
	
	// Transform to generals format
	transformedRecords := common.TransformGenerals(records)
	
	// Build and return paginated response
	return paginationReq.BuildResponse(transformedRecords, totalCount), nil
}

func (extension *AppHandler) GetExtension(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	return getExtensionByFilter(ctx, resource, extension, requestDetails, bson.M{
		"general.name":           requestDetails.Name,
		"general.canonical_name": requestDetails.CanonicalName,
		"general.project":        requestDetails.Project,
		"general.version":        requestDetails.Version,
	})
}

func (extension *AppHandler) GetOtherExtension(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	fmt.Println("requestDetails.Version", requestDetails.Version)
	return getExtensionByFilter(ctx, resource, extension, requestDetails, bson.M{
		"general.name":    requestDetails.Name,
		"general.project": requestDetails.Project,
		"general.version": requestDetails.Version,
	})
}

func getExtensionByFilter(ctx context.Context, resource models.ResourceClass, extension *AppHandler, requestDetails models.RequestDetails, filter bson.M) (any, error) {
	// Validate project access
	if requestDetails.Project != "" {
		if err := authorization.ValidateRequestProject(ctx, extension.Context.Client, requestDetails.User, requestDetails.Project); err != nil {
			return nil, fmt.Errorf("extension access denied: %w", err)
		}
	}

	collection := extension.Context.Client.Collection(requestDetails.Collection)
	resourceIDFilter, err := common.AddResourceIDFilter(requestDetails, filter)
	if err != nil {
		return nil, fmt.Errorf("add resource id filter error: %w", err)
	}

	// Use secure filtering
	filterWithRestriction, err := common.AddSecureUserFilter(ctx, extension.Context.Client, requestDetails, resourceIDFilter)
	if err != nil {
		return nil, fmt.Errorf("failed to apply security filters: %w", err)
	}

	result := collection.FindOne(ctx, filterWithRestriction)

	if result.Err() != nil {
		if errors.Is(result.Err(), mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("resource not found - type: %s, name: %s, project: %s",
				requestDetails.Collection,
				requestDetails.Name,
				requestDetails.Project,
			)
		}
		return nil, fmt.Errorf("db find one error: %w", result.Err())
	}

	if err := result.Decode(resource); err != nil {
		return nil, fmt.Errorf("decode error: %w", err)
	}

	return resource, nil
}
