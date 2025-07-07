package service

import (
	"context"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// ServiceWithEnvoyStatus, bir service ve ona bağlı envoy statuslerini temsil eder.
type ServiceWithEnvoyStatus struct {
	*Service
	Status []string `json:"status"`
}

type ServiceWithStatus struct {
	*Service
	Status string `json:"status"`
}

func (s *AppHandler) ListServices(ctx context.Context, _ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	pipeline := bson.A{
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
					{Key: "$arrayElemAt", Value: bson.A{"$envoy_statuses.status", 0}},
				}},
			}},
		},
	}

	cursor, err := s.Context.Client.Collection("services").Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate services: %v", err)
	}
	defer cursor.Close(ctx)

	var result []ServiceWithStatus
	for cursor.Next(ctx) {
		var svc bson.M
		if err := cursor.Decode(&svc); err != nil {
			return nil, fmt.Errorf("failed to decode service: %v", err)
		}
		var service Service
		bsonBytes, _ := bson.Marshal(svc)
		_ = bson.Unmarshal(bsonBytes, &service)
		status, _ := svc["status"].(string)
		result = append(result, ServiceWithStatus{
			Service: &service,
			Status:  status,
		})
	}
	return result, nil
}

/* func (s *AppHandler) ListServicess(ctx context.Context, _ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	cursor, err := s.Context.Client.Collection("services").Find(context.Background(), bson.M{})
	if err != nil {
		return nil, fmt.Errorf("failed to get services: %v", err)
	}
	defer cursor.Close(context.Background())

	result := []*Service{}
	for cursor.Next(context.Background()) {
		var svc Service
		if err := cursor.Decode(&svc); err != nil {
			return nil, fmt.Errorf("failed to decode service: %v", err)
		}
		result = append(result, &svc)
	}
	return result, nil
} */

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
	cursor := s.Context.Client.Collection("services").FindOne(ctx, bson.M{"_id": objectID})
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
