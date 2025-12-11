package extension

import (
	"fmt"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/common"
	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/errstr"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/gin-gonic/gin"
)

// ListExtensionsWithPagination retrieves extensions with pagination support
func (extension *AppHandler) ListExtensionsWithPagination(c *gin.Context, requestDetails models.RequestDetails) (any, error) {
	ctx := c.Request.Context()
	paginationReq := models.ParsePaginationParams(c)

	// Build base filter with canonical name and project
	baseFilter := bson.M{
		"general.canonical_name": requestDetails.CanonicalName,
		"general.project":        requestDetails.Project,
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

	// Apply user restrictions with secure filter
	filterWithRestriction, err := common.AddSecureUserFilter(ctx, extension.Context.Client, requestDetails, filter)
	if err != nil {
		extension.Logger.Errorf("Error adding secure user filter: %v", err)
		return nil, fmt.Errorf("authorization error: %w", err)
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
		return nil, errstr.ErrUnknownDBError
	}
	defer cursor.Close(ctx)

	var records []bson.M
	if err = cursor.All(ctx, &records); err != nil {
		return nil, errstr.ErrUnknownDBError
	}

	// Transform to generals format
	transformedRecords := common.TransformGenerals(records)

	// Build and return paginated response
	return paginationReq.BuildResponse(transformedRecords, totalCount), nil
}
