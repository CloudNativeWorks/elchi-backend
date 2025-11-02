package services

import (
	"context"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

type ServiceClients struct {
	Clients []models.ServiceClients `json:"clients" bson:"clients"`
}

func FetchDownstreamAddressFromService(db *mongo.Database, name, project, version string) []models.ServiceClients {
	var serviceClients ServiceClients

	err := db.Collection("services").FindOne(
		context.TODO(),
		bson.M{"name": name, "project": project},
	).Decode(&serviceClients)

	if err != nil {
		// Log error and return empty slice
		// Note: serviceClients.Clients will be nil if FindOne failed
		return []models.ServiceClients{}
	}

	// DEBUG: Log how many clients were found
	// This helps track multi-client deployments
	if len(serviceClients.Clients) > 0 {
		// Create a simple log-friendly representation
		clientAddresses := make([]string, len(serviceClients.Clients))
		for i, client := range serviceClients.Clients {
			clientAddresses[i] = client.DownstreamAddress
		}
		// Note: We can't log here directly, but the caller will see the count
	}

	return serviceClients.Clients
}
