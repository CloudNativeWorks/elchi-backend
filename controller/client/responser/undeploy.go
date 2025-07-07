package responser

import (
	"context"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
	"go.mongodb.org/mongo-driver/bson"
)

type UnDeployResponser struct {
	XDSHandler *xds.AppHandler
	Logger     *logger.Logger
}

func (p *UnDeployResponser) ValidateAndTransform(op models.OperationClass, response *pb.CommandResponse) any {
	if !p.validateResponse(response) {
		return response
	}

	result, ok := response.Result.(*pb.CommandResponse_Undeploy)
	if !ok {
		p.Logger.Errorf("undeploy response is not of type Undeploy")
		return response
	}

	clientID := response.Identity.ClientId
	projectName := op.GetCommandProject()
	serviceName := op.GetCommandName()
	downstreamAddress := result.Undeploy.DownstreamAddress
	clientName := response.Identity.ClientName

	if err := p.removeClientFromService(clientID, serviceName, projectName); err != nil {
		p.Logger.Errorf("Error while removing client from service: %v", err)
	} else {
		p.Logger.Infof("Client ID: %s, Service: %s successfully removed", clientID, serviceName)
	}

	if err := p.removeServiceFromEnvoys(serviceName, projectName, clientName, downstreamAddress); err != nil {
		p.Logger.Errorf("Error while removing service from envoys: %v", err)
	} else {
		p.Logger.Infof("Service: %s successfully removed from envoys", serviceName)
	}

	return response
}

func (p *UnDeployResponser) validateResponse(response *pb.CommandResponse) bool {
	if response == nil {
		p.Logger.Errorf("undeploy response is nil")
		return false
	}

	if response.Error != "" {
		p.Logger.Errorf("undeploy response error: %s", response.Error)
		return false
	}

	if !response.Success {
		p.Logger.Errorf("undeploy was not successful")
		return false
	}

	if response.Identity == nil || response.Identity.ClientId == "" {
		p.Logger.Errorf("client ID is empty in response identity")
		return false
	}

	return true
}

func (p *UnDeployResponser) removeClientFromService(clientID, serviceName, projectName string) error {
	servicesCollection := p.XDSHandler.Context.Client.Collection("services")

	var service struct {
		Name    string                  `bson:"name"`
		Clients []models.ServiceClients `bson:"clients"`
	}

	filter := bson.M{
		"name":    serviceName,
		"project": projectName,
	}

	if err := servicesCollection.FindOne(context.Background(), filter).Decode(&service); err != nil {
		return fmt.Errorf("service not found: %w", err)
	}

	update := bson.M{
		"$pull": bson.M{
			"clients": bson.M{
				"client_id": clientID,
			},
		},
	}

	result, err := servicesCollection.UpdateOne(context.Background(), filter, update)
	if err != nil {
		return fmt.Errorf("error while removing client from service: %w", err)
	}

	if result.ModifiedCount == 0 {
		return fmt.Errorf("client ID: %s, service not found", clientID)
	}

	return nil
}

func (p *UnDeployResponser) removeServiceFromEnvoys(serviceName, projectName, clientName, downstreamAddress string) error {
	envoysCollection := p.XDSHandler.Context.Client.Collection("envoys")
	var envoys models.Envoys

	filter := bson.M{
		"name":    serviceName,
		"project": projectName,
	}

	fmt.Println(serviceName, projectName, clientName, downstreamAddress)

	if err := envoysCollection.FindOne(context.Background(), filter).Decode(&envoys); err != nil {
		return fmt.Errorf("service not found: %w", err)
	}

	update := bson.M{
		"$pull": bson.M{
			"envoys": bson.M{
				"client_name":        clientName,
				"downstream_address": downstreamAddress,
			},
		},
	}

	result, err := envoysCollection.UpdateOne(context.Background(), filter, update)
	if err != nil {
		return fmt.Errorf("error while removing client from service: %w", err)
	}

	if result.ModifiedCount == 0 {
		return fmt.Errorf("client Name: %s, service not found", clientName)
	}

	return nil
}
