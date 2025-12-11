package models

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/pkg/security"
)

// PaginationRequest represents pagination and filtering parameters
type PaginationRequest struct {
	// Pagination
	Limit  int `json:"limit"`  // Items per page (default: 20, max: 100)
	Offset int `json:"offset"` // Page offset (default: 0)

	// Filtering
	Name    string `json:"name,omitempty"`    // Filter by general.name (contains, case-insensitive)
	Version string `json:"version,omitempty"` // Filter by general.version (contains, case-insensitive)

	// Sorting
	SortBy    string `json:"sort_by,omitempty"`    // Field to sort by (default: "name")
	SortOrder string `json:"sort_order,omitempty"` // Sort order: "asc" or "desc" (default: "asc")
}

// PaginationResponse represents the response with pagination metadata
type PaginationResponse struct {
	Data        interface{} `json:"data"`         // The actual data items
	Count       int         `json:"count"`        // Number of items in current page
	TotalCount  int64       `json:"total_count"`  // Total items matching filters
	TotalPages  int         `json:"total_pages"`  // Total number of pages
	Limit       int         `json:"limit"`        // Items per page
	Offset      int         `json:"offset"`       // Current offset
	CurrentPage int         `json:"current_page"` // Current page number (1-indexed)
	HasNext     bool        `json:"has_next"`     // Whether there's a next page
	HasPrev     bool        `json:"has_prev"`     // Whether there's a previous page
}

// ParsePaginationParams parses pagination parameters from Gin context
func ParsePaginationParams(c *gin.Context) *PaginationRequest {
	req := &PaginationRequest{
		Limit:     20,     // Default limit
		Offset:    0,      // Default offset
		SortBy:    "name", // Default sort field
		SortOrder: "asc",  // Default sort order
	}

	// Parse limit
	if limitParam := c.Query("limit"); limitParam != "" {
		if limit, err := strconv.Atoi(limitParam); err == nil && limit > 0 {
			if limit > 100 {
				limit = 100 // Max limit
			}
			req.Limit = limit
		}
	}

	// Parse offset
	if offsetParam := c.Query("offset"); offsetParam != "" {
		if offset, err := strconv.Atoi(offsetParam); err == nil && offset >= 0 {
			req.Offset = offset
		}
	}

	// Parse filters
	req.Name = c.Query("name")
	req.Version = c.Query("version")

	// Parse sorting
	if sortBy := c.Query("sort_by"); sortBy != "" {
		// Allow only specific fields for security
		allowedFields := []string{"name", "version", "created_at", "updated_at"}
		for _, field := range allowedFields {
			if sortBy == field {
				req.SortBy = sortBy
				break
			}
		}
	}

	if sortOrder := c.Query("sort_order"); sortOrder == "desc" || sortOrder == "asc" {
		req.SortOrder = sortOrder
	}

	return req
}

// BuildMongoFilter builds MongoDB filter based on pagination request
func (p *PaginationRequest) BuildMongoFilter(projectID string) bson.M {
	filter := bson.M{"general.project": projectID}

	// Add name filter (contains, case-insensitive) with sanitization
	if p.Name != "" {
		sanitizedName, valid := security.SanitizeSearchInput(p.Name)
		if valid && sanitizedName != "" {
			filter["general.name"] = bson.M{
				"$regex":   sanitizedName,
				"$options": "i", // case-insensitive
			}
		}
	}

	// Add version filter (contains, case-insensitive) with sanitization
	if p.Version != "" {
		sanitizedVersion, valid := security.SanitizeSearchInput(p.Version)
		if valid && sanitizedVersion != "" {
			filter["general.version"] = bson.M{
				"$regex":   sanitizedVersion,
				"$options": "i", // case-insensitive
			}
		}
	}

	return filter
}

// BuildMongoSort builds MongoDB sort options
func (p *PaginationRequest) BuildMongoSort() bson.D {
	sortValue := 1 // ascending
	if p.SortOrder == "desc" {
		sortValue = -1
	}

	// Map sort fields to MongoDB field names
	fieldMap := map[string]string{
		"name":       "general.name",
		"version":    "general.version",
		"created_at": "general.created_at",
		"updated_at": "general.updated_at",
	}

	mongoField := fieldMap[p.SortBy]
	if mongoField == "" {
		mongoField = "general.name" // fallback
	}

	return bson.D{primitive.E{Key: mongoField, Value: sortValue}}
}

// BuildFindOptions builds MongoDB find options with pagination and sorting
func (p *PaginationRequest) BuildFindOptions() *options.FindOptions {
	opts := options.Find()

	// Set sorting
	opts.SetSort(p.BuildMongoSort())

	// Set pagination
	opts.SetLimit(int64(p.Limit))
	opts.SetSkip(int64(p.Offset))

	return opts
}

// BuildResponse builds the final pagination response
func (p *PaginationRequest) BuildResponse(data interface{}, totalCount int64) *PaginationResponse {
	// Calculate pagination metadata
	currentPage := (p.Offset / p.Limit) + 1
	totalPages := int(totalCount) / p.Limit
	if int(totalCount)%p.Limit != 0 {
		totalPages++
	}

	// Get actual count from data
	count := 0
	switch v := data.(type) {
	case []interface{}:
		count = len(v)
	case []map[string]interface{}:
		count = len(v)
	case []bson.M:
		count = len(v)
	default:
		// Try to get length via reflection if needed
		count = 0
	}

	return &PaginationResponse{
		Data:        data,
		Count:       count,
		TotalCount:  totalCount,
		TotalPages:  totalPages,
		Limit:       p.Limit,
		Offset:      p.Offset,
		CurrentPage: currentPage,
		HasNext:     currentPage < totalPages,
		HasPrev:     currentPage > 1,
	}
}

// PaginationHelper provides common pagination functionality
type PaginationHelper struct {
	CollectionName string
}

