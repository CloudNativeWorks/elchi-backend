package custom

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/common"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/security"
)

type Record struct {
	Name          string `json:"name" bson:"name"`
	CanonicalName string `json:"canonical_name" bson:"canonical_name"`
	GType         string `json:"gtype" bson:"gtype"`
	Type          string `json:"type" bson:"type"`
	Category      string `json:"category" bson:"category"`
	Collection    string `json:"collection" bson:"collection"`
	Version       string `json:"version" bson:"version"`
}

func (custom *AppHandler) GetCustomResourceList(ctx context.Context, _ models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	collection := custom.Context.Client.Collection(requestDetails.Collection)

	opts := options.Find().SetProjection(bson.M{
		"general.name":           1,
		"general.canonical_name": 1,
		"general.gtype":          1,
		"general.type":           1,
		"general.category":       1,
		"general.version":        1,
	})

	filters := buildFilters(requestDetails)
	filters = common.AddUserFilter(requestDetails, filters)
	cursor, err := collection.Find(ctx, filters, opts)
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	defer cursor.Close(ctx)

	results, decodeErr := decodeResults(ctx, cursor, requestDetails.Collection, custom.Logger)
	if decodeErr != nil {
		return nil, decodeErr
	}

	return results, nil
}

func buildFilters(details models.RequestDetails) bson.M {
	filters := bson.M{}

	// Project is required - if no project specified, return empty result
	if details.Project == "" {
		// Return impossible filter to get empty result
		filters["_id"] = nil
		return filters
	}

	// Include all resources from the specified project (both regular and default resources)
	filters["general.project"] = details.Project

	// For metrics, include all versions. Otherwise, filter by specific version
	if details.ForMetrics != "true" {
		// Check if skip_version is requested
		skipVersion := false
		if details.Metadata != nil {
			if skip, ok := details.Metadata["skip_version"]; ok && skip == "yes" {
				skipVersion = true
			}
		}
		
		if !skipVersion && details.Version != "" {
			filters["general.version"] = details.Version
		}
	}

	if details.GType != "" {
		filters["general.gtype"] = details.GType
	}

	if details.Category != "" {
		filters["general.category"] = details.Category
	}

	if details.CanonicalName != "" {
		filters["general.canonical_name"] = details.CanonicalName
	}

	if details.Metadata != nil {
		if name, ok := details.Metadata["non_eds_cluster"]; ok && name == "true" {
			filters["resource.resource.type"] = bson.M{"$ne": "EDS"}
		}
	}

	return filters
}

func decodeResults(ctx context.Context, cursor *mongo.Cursor, collectionName string, logger *logger.Logger) ([]Record, error) {
	var results []Record

	for cursor.Next(ctx) {
		var doc struct {
			General struct {
				Name          string `bson:"name"`
				CanonicalName string `bson:"canonical_name"`
				GType         string `bson:"gtype"`
				Type          string `bson:"type"`
				Category      string `bson:"category"`
				Version       string `bson:"version"`
			} `bson:"general"`
		}

		if err := cursor.Decode(&doc); err != nil {
			logger.Debugf("Decode fail: %v", err)
			continue
		}

		results = append(results, Record{
			Name:          doc.General.Name,
			CanonicalName: doc.General.CanonicalName,
			GType:         doc.General.GType,
			Type:          doc.General.Type,
			Category:      doc.General.Category,
			Collection:    collectionName,
			Version:       doc.General.Version,
		})
	}

	if err := cursor.Err(); err != nil {
		logger.Debugf("Cursor error: %v", err)
		return nil, err
	}

	return results, nil
}

// GetCustomResourceListWithSearch returns filtered resource list based on search query
func (custom *AppHandler) GetCustomResourceListWithSearch(ctx context.Context, _ models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	collection := custom.Context.Client.Collection(requestDetails.Collection)

	opts := options.Find().SetProjection(bson.M{
		"general.name":           1,
		"general.canonical_name": 1,
		"general.gtype":          1,
		"general.type":           1,
		"general.category":       1,
		"general.version":        1,
	})

	// Build base filters
	filters := buildFilters(requestDetails)
	filters = common.AddUserFilter(requestDetails, filters)
	
	// Add search filter if search query is provided
	if requestDetails.Search != "" {
		// Use regex search with sanitized input (safer than text search for now)
		searchFilter := buildSearchFilter(requestDetails.Search)
		if len(searchFilter) > 0 {
			filters = bson.M{
				"$and": []bson.M{
					filters,
					{"$or": searchFilter},
				},
			}
		}
		custom.Logger.Debugf("Search query: '%s', Filter count: %d", requestDetails.Search, len(searchFilter))
	}
	// If no search query, use base filters only (this will return first N results)
	
	// Add limit to prevent too many results (default 100, max 1000)
	limit := int64(100)
	if limitStr := requestDetails.Metadata["limit"]; limitStr != "" {
		if l := parseIntSafe(limitStr); l > 0 && l <= 1000 {
			limit = int64(l)
		}
	}
	opts.SetLimit(limit)
	
	// Add sorting for consistent results
	opts.SetSort(bson.M{"general.name": 1})

	cursor, err := collection.Find(ctx, filters, opts)
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	defer cursor.Close(ctx)

	results, decodeErr := decodeResults(ctx, cursor, requestDetails.Collection, custom.Logger)
	if decodeErr != nil {
		return nil, decodeErr
	}

	return results, nil
}



// buildSearchFilter creates secure search filter (fallback for collections without text index)
func buildSearchFilter(searchQuery string) []bson.M {
	if searchQuery == "" {
		return nil
	}
	
	// Validate and sanitize input  
	sanitizedQuery, valid := security.SanitizeSearchInput(searchQuery)
	if !valid {
		return nil
	}
	
	// Use contains-style matching with escaped input for safety
	// This prevents ReDoS by escaping all regex special characters
	searchFilter := []bson.M{
		{"general.name": bson.M{
			"$regex":   sanitizedQuery,
			"$options": "i", // Case insensitive
		}},
		{"general.canonical_name": bson.M{
			"$regex":   sanitizedQuery, 
			"$options": "i",
		}},
	}
	
	return searchFilter
}

// parseIntSafe safely converts string to int, returns 0 on error
func parseIntSafe(s string) int {
	if s == "" {
		return 0
	}
	
	result := 0
	for _, char := range s {
		if char < '0' || char > '9' {
			return 0
		}
		result = result*10 + int(char-'0')
	}
	
	return result
}
