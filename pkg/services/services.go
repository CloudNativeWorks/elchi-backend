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
		return serviceClients.Clients
	}

	return serviceClients.Clients
}
