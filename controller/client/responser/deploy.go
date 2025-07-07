package responser

import (
	"context"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/controller/client/services"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
	"go.mongodb.org/mongo-driver/bson"
)

type DeployResponser struct {
	XDSHandler *xds.AppHandler
	Logger     *logger.Logger
	Service    *services.ClientService
}

func (p *DeployResponser) ValidateAndTransform(op models.OperationClass, response *pb.CommandResponse) any {
	if !p.validateResponse(response) {
		return response
	}

	clientID := response.Identity.ClientId
	projectName := op.GetCommandProject()
	serviceName := op.GetCommandName()
	downstreamAddress := op.GetExtend().DownstreamAddress

	if err := p.addClientToService(clientID, downstreamAddress, serviceName, projectName); err != nil {
		p.Logger.Warnf("Error while adding client to service: %v", err)
	} else {
		p.Logger.Infof("Client ID: %s successfully added to service: %s", clientID, serviceName)
	}

	return response
}

func (p *DeployResponser) validateResponse(response *pb.CommandResponse) bool {
	if response == nil {
		p.Logger.Errorf("deploy response is nil")
		return false
	}

	if response.Error != "" {
		p.Logger.Errorf("deploy response error: %s", response.Error)
		return false
	}

	if !response.Success {
		p.Logger.Errorf("deploy was not successful")
		return false
	}

	if response.Identity == nil || response.Identity.ClientId == "" {
		p.Logger.Errorf("client ID is empty in response identity")
		return false
	}

	return true
}

func (p *DeployResponser) addClientToService(clientID, downstreamAddress, serviceName, projectName string) error {
	servicesCollection := p.XDSHandler.Context.Client.Collection("services")

	clientInfo := models.ListenerClient{
		ClientID:          clientID,
		DownstreamAddress: downstreamAddress,
	}

	var existingService struct {
		Clients []models.ListenerClient `bson:"clients"`
	}

	filter := bson.M{
		"name":    serviceName,
		"project": projectName,
	}

	if err := servicesCollection.FindOne(context.Background(), filter).Decode(&existingService); err != nil {
		return fmt.Errorf("service not found: %w", err)
	}

	for _, client := range existingService.Clients {
		if client.ClientID == clientID && client.DownstreamAddress == downstreamAddress {
			return fmt.Errorf("client ID: %s with downstreamAddress: %s already exists in service", clientID, downstreamAddress)
		}
	}

	update := bson.M{
		"$push": bson.M{
			"clients": clientInfo,
		},
	}

	result, err := servicesCollection.UpdateOne(context.Background(), filter, update)
	if err != nil {
		return fmt.Errorf("error while updating service: %w", err)
	}

	if result.MatchedCount == 0 {
		return fmt.Errorf("no service found with name: %s, project: %s", serviceName, projectName)
	}

	if result.ModifiedCount == 0 {
		return fmt.Errorf("service found but no modification occurred")
	}

	return nil
}
