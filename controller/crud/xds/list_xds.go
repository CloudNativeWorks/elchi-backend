package xds

import (
	"fmt"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/common"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

type Field struct {
	Name string
	Type string
}

type ResourceSchema map[string][]Field

// ListResourceWithPagination lists resources with pagination, filtering, and sorting
func (xds *AppHandler) ListResourceWithPagination(c *gin.Context, requestDetails models.RequestDetails) (any, error) {
	ctx := c.Request.Context()

	// Parse pagination parameters
	paginationReq := models.ParsePaginationParams(c)

	collection := xds.Context.Client.Collection(requestDetails.Collection)

	// Build base filter
	baseFilter := bson.M{}
	if requestDetails.GType != "" {
		baseFilter["general.gtype"] = requestDetails.GType.String()
	}

	// Add pagination filters (name, version)
	paginationFilter := paginationReq.BuildMongoFilter(requestDetails.Project)

	// Merge filters
	finalFilter := bson.M{}
	for k, v := range baseFilter {
		finalFilter[k] = v
	}
	for k, v := range paginationFilter {
		finalFilter[k] = v
	}

	// Use secure filtering that properly handles project access for all roles
	filterWithRestriction, err := common.AddSecureUserFilter(ctx, xds.Context.Client, requestDetails, finalFilter)
	if err != nil {
		return nil, fmt.Errorf("failed to apply security filters: %w", err)
	}

	// Get total count for pagination
	totalCount, err := collection.CountDocuments(ctx, filterWithRestriction)
	if err != nil {
		return nil, fmt.Errorf("could not count records: %w", err)
	}

	// Build find options with pagination and sorting
	opts := paginationReq.BuildFindOptions()
	opts.SetProjection(bson.M{"resource": 0}) // Exclude heavy resource data

	// Execute query
	cursor, err := collection.Find(ctx, filterWithRestriction, opts)
	if err != nil {
		return nil, fmt.Errorf("could not find records: %w", err)
	}

	var records []bson.M
	if err = cursor.All(ctx, &records); err != nil {
		return nil, fmt.Errorf("could not decode records: %w", err)
	}

	// Transform records
	transformedRecords := common.TransformGenerals(records)

	// Build and return paginated response
	return paginationReq.BuildResponse(transformedRecords, totalCount), nil
}
