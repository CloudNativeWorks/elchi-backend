package service

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/security"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type ServiceWithEnvoyStatus struct {
	*Service
	Status []string `json:"status"`
}

type ServiceWithStatus struct {
	*Service
	Status string `json:"status"`
}

func (s *AppHandler) ListServices(ctx context.Context, _ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	// Build base match filter
	matchFilter := bson.D{
		{Key: "project", Value: requestDetails.Project},
	}

	// P0-4 FIX: Add permission checks for non-Owner/non-Admin users
	// Services have permissions but they were never checked - security vulnerability!
	if !requestDetails.User.IsOwner && requestDetails.User.Role != models.RoleAdmin {
		// Build complete group list including base_group
		allGroups := append([]string{}, requestDetails.User.Groups...)
		if requestDetails.User.BaseGroup != "" && !slices.Contains(allGroups, requestDetails.User.BaseGroup) {
			allGroups = append(allGroups, requestDetails.User.BaseGroup)
		}

		// Add permission filter: user must be in permissions.groups OR permissions.users
		matchFilter = append(matchFilter, bson.E{
			Key: "$or",
			Value: bson.A{
				bson.D{{Key: "permissions.groups", Value: bson.D{{Key: "$in", Value: allGroups}}}},
				bson.D{{Key: "permissions.users", Value: requestDetails.User.UserID}},
			},
		})
	}

	// Add filter conditions based on query parameters
	if requestDetails.Metadata != nil {
		if nameVal, ok := requestDetails.Metadata["name"]; ok {
			if nameVal != "" {
				if sanitizedName, valid := security.SanitizeSearchInput(nameVal); valid && sanitizedName != "" {
					matchFilter = append(matchFilter, bson.E{
						Key:   "name",
						Value: bson.D{{Key: "$regex", Value: sanitizedName}, {Key: "$options", Value: "i"}},
					})
				}
			}
		}

		if versionVal, ok := requestDetails.Metadata["version"]; ok {
			if versionVal != "" {
				matchFilter = append(matchFilter, bson.E{Key: "version", Value: versionVal})
			}
		}

		if downstreamVal, ok := requestDetails.Metadata["downstream_address"]; ok {
			if downstreamVal != "" {
				if sanitizedDownstream, valid := security.SanitizeSearchInput(downstreamVal); valid && sanitizedDownstream != "" {
					matchFilter = append(matchFilter, bson.E{
						Key:   "clients.downstream_address",
						Value: bson.D{{Key: "$regex", Value: sanitizedDownstream}, {Key: "$options", Value: "i"}},
					})
				}
			}
		}
	}

	// Build aggregation pipeline with filters
	pipeline := bson.A{
		bson.D{{Key: "$match", Value: matchFilter}},
		bson.D{
			{Key: "$lookup", Value: bson.D{
				{Key: "from", Value: "envoys"},
				{Key: "let", Value: bson.D{
					{Key: "name", Value: "$name"},
					{Key: "project", Value: "$project"},
				}},
				{Key: "pipeline", Value: bson.A{
					bson.D{
						{Key: "$match", Value: bson.D{
							{Key: "$expr", Value: bson.D{
								{Key: "$and", Value: bson.A{
									bson.D{{Key: "$eq", Value: bson.A{"$name", "$$name"}}},
									bson.D{{Key: "$eq", Value: bson.A{"$project", "$$project"}}},
								}},
							}},
						}},
					},
					bson.D{
						{Key: "$project", Value: bson.D{
							{Key: "status", Value: 1},
							{Key: "_id", Value: 0},
						}},
					},
				}},
				{Key: "as", Value: "envoy_statuses"},
			}},
		},
		bson.D{
			{Key: "$addFields", Value: bson.D{
				{Key: "status", Value: bson.D{
					{Key: "$ifNull", Value: bson.A{
						bson.D{{Key: "$arrayElemAt", Value: bson.A{"$envoy_statuses.status", 0}}},
						"Not_Deployed",
					}},
				}},
			}},
		},
	}

	// Add status filter if provided
	if requestDetails.Metadata != nil {
		if statusVal, ok := requestDetails.Metadata["status"]; ok && statusVal != "" {
			pipeline = append(pipeline, bson.D{
				{Key: "$match", Value: bson.D{
					{Key: "status", Value: statusVal},
				}},
			})
		}
	}

	// Add sorting
	pipeline = append(pipeline, bson.D{{Key: "$sort", Value: bson.D{{Key: "name", Value: 1}}}})

	// Get pagination parameters
	page := 1
	limit := 10
	if requestDetails.Metadata != nil {
		if pageVal, ok := requestDetails.Metadata["page"]; ok {
			if p, err := strconv.Atoi(pageVal); err == nil && p > 0 {
				page = p
			}
		}
		if limitVal, ok := requestDetails.Metadata["limit"]; ok {
			if l, err := strconv.Atoi(limitVal); err == nil && l > 0 && l <= 100 {
				limit = l
			}
		}
	}

	// Add pagination to pipeline
	skip := (page - 1) * limit

	// Add facet for both data and total count
	pipeline = append(pipeline, bson.D{
		{Key: "$facet", Value: bson.D{
			{Key: "data", Value: bson.A{
				bson.D{{Key: "$skip", Value: skip}},
				bson.D{{Key: "$limit", Value: limit}},
			}},
			{Key: "total", Value: bson.A{
				bson.D{{Key: "$count", Value: "count"}},
			}},
		}},
	})

	cursor, err := s.Context.Client.Collection("services").Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate services: %v", err)
	}
	defer cursor.Close(ctx)

	var aggregateResult []bson.M
	if err := cursor.All(ctx, &aggregateResult); err != nil {
		return nil, fmt.Errorf("failed to decode aggregate result: %v", err)
	}

	if len(aggregateResult) == 0 {
		return map[string]any{
			"data":       []ServiceWithStatus{},
			"total":      0,
			"page":       page,
			"limit":      limit,
			"totalPages": 0,
		}, nil
	}

	result := aggregateResult[0]
	data, ok := result["data"].(bson.A)
	if !ok {
		return nil, fmt.Errorf("unexpected data format")
	}

	totalArray, ok := result["total"].(bson.A)
	total := 0
	if ok && len(totalArray) > 0 {
		if totalDoc, ok := totalArray[0].(bson.M); ok {
			if count, ok := totalDoc["count"].(int32); ok {
				total = int(count)
			}
		}
	}

	var services []ServiceWithStatus
	for _, item := range data {
		svc, ok := item.(bson.M)
		if !ok {
			continue
		}

		var service Service
		bsonBytes, _ := bson.Marshal(svc)
		_ = bson.Unmarshal(bsonBytes, &service)

		status, _ := svc["status"].(string)
		services = append(services, ServiceWithStatus{
			Service: &service,
			Status:  status,
		})
	}

	totalPages := (total + limit - 1) / limit

	return map[string]any{
		"data":       services,
		"total":      total,
		"page":       page,
		"limit":      limit,
		"totalPages": totalPages,
	}, nil
}

func (s *AppHandler) GetService(ctx context.Context, _ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	if requestDetails.FromClient == "true" {
		return s.GetServicesByClientID(ctx, nil, requestDetails)
	}
	return s.GetSingleService(ctx, nil, requestDetails)
}

func (s *AppHandler) GetSingleService(ctx context.Context, _ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	objectID, err := primitive.ObjectIDFromHex(requestDetails.ServiceID)
	if err != nil {
		return nil, fmt.Errorf("invalid service id: %v", err)
	}
	filter := bson.M{"_id": objectID, "project": requestDetails.Project}

	// P0-4 FIX: Add permission check for non-Owner/non-Admin users
	if !requestDetails.User.IsOwner && requestDetails.User.Role != models.RoleAdmin {
		allGroups := append([]string{}, requestDetails.User.Groups...)
		if requestDetails.User.BaseGroup != "" {
			found := false
			for _, g := range allGroups {
				if g == requestDetails.User.BaseGroup {
					found = true
					break
				}
			}
			if !found {
				allGroups = append(allGroups, requestDetails.User.BaseGroup)
			}
		}

		filter["$or"] = []bson.M{
			{"permissions.groups": bson.M{"$in": allGroups}},
			{"permissions.users": requestDetails.User.UserID},
		}
	}

	cursor := s.Context.Client.Collection("services").FindOne(ctx, filter)
	var service Service
	if err := cursor.Decode(&service); err != nil {
		return nil, fmt.Errorf("failed to decode service: %v", err)
	}

	cursor = s.Context.Client.Collection("envoys").FindOne(ctx, bson.M{"name": service.Name, "project": service.Project})
	var envoy models.Envoys
	err = cursor.Decode(&envoy)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			envoy = models.Envoys{}
		} else {
			return nil, fmt.Errorf("failed to decode envoy: %v", err)
		}
	}

	result := map[string]any{
		"service": service,
		"envoys":  envoy,
	}

	return result, nil
}

func (s *AppHandler) GetServicesByClientID(ctx context.Context, _ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	filter := bson.M{"clients.client_id": requestDetails.ClientID}

	// Add project filter if provided
	if requestDetails.Project != "" {
		filter["project"] = requestDetails.Project
	}

	cursor, err := s.Context.Client.Collection("services").Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to get services: %v", err)
	}
	defer cursor.Close(ctx)

	var result []*Service
	for cursor.Next(ctx) {
		var svc Service
		if err := cursor.Decode(&svc); err != nil {
			return nil, fmt.Errorf("failed to decode service: %v", err)
		}
		result = append(result, &svc)
	}

	return result, nil
}

func (s *AppHandler) GetEnvoyDetails(ctx context.Context, _ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	objectID, err := primitive.ObjectIDFromHex(requestDetails.ServiceID)
	if err != nil {
		return nil, fmt.Errorf("invalid service id: %v", err)
	}
	cursor := s.Context.Client.Collection("services").FindOne(ctx, bson.M{"_id": objectID})
	var service Service
	if err := cursor.Decode(&service); err != nil {
		return nil, fmt.Errorf("failed to decode service: %v", err)
	}

	return &service, nil
}
