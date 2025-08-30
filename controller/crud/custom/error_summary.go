package custom

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// ErrorSummaryResponse represents the API response structure
type ErrorSummaryResponse struct {
	TotalError int             `json:"total_error"`
	Services   []ServiceStatus `json:"services"`
}

// ServiceStatus represents individual service error status
type ServiceStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Count  int    `json:"count"`
}

// GetErrorSummary returns error summary for all services in a project
// Only includes services that have active errors
func (custom *AppHandler) GetErrorSummary(ctx context.Context, _ models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	collection := custom.Context.Client.Collection("envoys")
	
	// MongoDB aggregation pipeline to get error summary
	pipeline := []bson.M{
		// Match services in the requested project that have active errors
		{
			"$match": bson.M{
				"project": requestDetails.Project,
				"enhanced_errors": bson.M{
					"$elemMatch": bson.M{
						"status": "active",
					},
				},
			},
		},
		// Add calculated fields
		{
			"$addFields": bson.M{
				// Count active errors
				"active_error_count": bson.M{
					"$size": bson.M{
						"$filter": bson.M{
							"input": "$enhanced_errors",
							"cond": bson.M{
								"$eq": []string{"$$this.status", "active"},
							},
						},
					},
				},
				// Determine status based on error severity
				"calculated_status": bson.M{
					"$switch": bson.M{
						"branches": []bson.M{
							{
								"case": bson.M{
									"$gt": []interface{}{
										bson.M{
											"$size": bson.M{
												"$filter": bson.M{
													"input": "$enhanced_errors",
													"cond": bson.M{
														"$and": []bson.M{
															{"$eq": []string{"$$this.status", "active"}},
															{"$eq": []string{"$$this.severity", "critical"}},
														},
													},
												},
											},
										},
										0,
									},
								},
								"then": "Critical",
							},
							{
								"case": bson.M{
									"$gt": []interface{}{
										bson.M{
											"$size": bson.M{
												"$filter": bson.M{
													"input": "$enhanced_errors",
													"cond": bson.M{
														"$and": []bson.M{
															{"$eq": []string{"$$this.status", "active"}},
															{"$eq": []string{"$$this.severity", "error"}},
														},
													},
												},
											},
										},
										0,
									},
								},
								"then": "Error",
							},
						},
						"default": "Warning",
					},
				},
			},
		},
		// Only include services with active errors
		{
			"$match": bson.M{
				"active_error_count": bson.M{
					"$gt": 0,
				},
			},
		},
		// Project only needed fields
		{
			"$project": bson.M{
				"name":               1,
				"calculated_status":  1,
				"active_error_count": 1,
			},
		},
		// Sort by name for consistency
		{
			"$sort": bson.M{
				"name": 1,
			},
		},
	}

	cursor, err := collection.Aggregate(ctx, pipeline)
	if err != nil {
		custom.Logger.Errorf("Failed to aggregate error summary: %v", err)
		return nil, err
	}
	defer cursor.Close(ctx)

	var results []struct {
		Name             string `bson:"name"`
		CalculatedStatus string `bson:"calculated_status"`
		ActiveErrorCount int    `bson:"active_error_count"`
	}

	if err = cursor.All(ctx, &results); err != nil {
		custom.Logger.Errorf("Failed to decode error summary results: %v", err)
		return nil, err
	}

	// Build response
	totalErrors := 0
	services := make([]ServiceStatus, 0, len(results))

	for _, result := range results {
		totalErrors += result.ActiveErrorCount
		services = append(services, ServiceStatus{
			Name:   result.Name,
			Status: result.CalculatedStatus,
			Count:  result.ActiveErrorCount,
		})
	}

	custom.Logger.Debugf("🔍 ERROR-SUMMARY: Found %d services with %d total active errors in project '%s'", 
		len(services), totalErrors, requestDetails.Project)

	return ErrorSummaryResponse{
		TotalError: totalErrors,
		Services:   services,
	}, nil
}