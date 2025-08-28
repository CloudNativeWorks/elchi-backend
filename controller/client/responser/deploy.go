package responser

import (
	"context"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/controller/client/services"
	"github.com/CloudNativeWorks/elchi-backend/controller/cloud/openstack"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
	"go.mongodb.org/mongo-driver/bson"
)

type DeployResponser struct {
	XDSHandler      *xds.AppHandler
	Logger          *logger.Logger
	Service         *services.ClientService
	OpenStackHandler *openstack.Handler
}

func (p *DeployResponser) ValidateAndTransform(op models.OperationClass, response *pb.CommandResponse) any {
	// Debug log raw response
	p.Logger.Debugf("Deploy Responser ENTRY - ClientID: %s, Success: %v, Error: '%s'", 
		response.Identity.ClientId, response.Success, response.Error)
	
	if !p.validateResponse(response) {
		return response
	}

	clientID := response.Identity.ClientId
	projectName := op.GetCommandProject()
	serviceName := op.GetCommandName()

	// Get deploy response details from proto
	result, ok := response.Result.(*pb.CommandResponse_Deploy)
	if !ok {
		p.Logger.Errorf("deploy response is not of type Deploy")
		return response
	}
	
	// Debug log the proto result before extracting fields
	if result.Deploy != nil {
		p.Logger.Debugf("Deploy Responser PROTO - Raw Deploy proto: DownstreamAddress='%s', InterfaceId='%s'", 
			result.Deploy.DownstreamAddress, result.Deploy.InterfaceId)
	} else {
		p.Logger.Errorf("Deploy Responser PROTO - result.Deploy is nil!")
	}

	downstreamAddress := result.Deploy.DownstreamAddress
	interfaceID := result.Deploy.InterfaceId

	// Debug logging for deploy response values
	p.Logger.Debugf("Deploy Response - ClientID: %s, DownstreamAddress: '%s', InterfaceID: '%s', ServiceName: %s", 
		clientID, downstreamAddress, interfaceID, serviceName)

	if err := p.addClientToService(clientID, downstreamAddress, serviceName, projectName, interfaceID); err != nil {
		p.Logger.Warnf("Error while adding client to service: %v", err)
	} else {
		p.Logger.Infof("Client ID: %s successfully added to service: %s", clientID, serviceName)
		
		// OpenStack integration for allowed address pairs
		if interfaceID != "" {
			if err := p.handleOpenStackIntegration(clientID, downstreamAddress, interfaceID, projectName); err != nil {
				p.Logger.Warnf("OpenStack integration failed for client %s: %v", clientID, err)
			}
		}
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

func (p *DeployResponser) addClientToService(clientID, downstreamAddress, serviceName, projectName string, interfaceID string) error {
	servicesCollection := p.XDSHandler.Context.Client.Collection("services")

	// Debug logging for service update
	p.Logger.Debugf("addClientToService - Adding client to service: ClientID=%s, DownstreamAddress='%s', InterfaceID='%s', ServiceName=%s, Project=%s", 
		clientID, downstreamAddress, interfaceID, serviceName, projectName)

	clientInfo := models.ServiceClients{
		ClientID:          clientID,
		DownstreamAddress: downstreamAddress,
		InterfaceID:       interfaceID,
	}

	var existingService struct {
		Clients []models.ServiceClients `bson:"clients"`
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

	// Debug log MongoDB update result
	p.Logger.Debugf("addClientToService - MongoDB update result: MatchedCount=%d, ModifiedCount=%d, UpsertedCount=%d", 
		result.MatchedCount, result.ModifiedCount, result.UpsertedCount)

	if result.MatchedCount == 0 {
		return fmt.Errorf("no service found with name: %s, project: %s", serviceName, projectName)
	}

	if result.ModifiedCount == 0 {
		return fmt.Errorf("service found but no modification occurred")
	}

	p.Logger.Infof("addClientToService - Successfully added client to service: ClientID=%s, Service=%s", clientID, serviceName)
	return nil
}

// handleOpenStackIntegration manages OpenStack allowed address pairs
func (p *DeployResponser) handleOpenStackIntegration(clientID, downstreamAddress, interfaceID, projectName string) error {
	// Get client information to check provider
	client, err := p.Service.GetClientByClientID(context.Background(), clientID)
	if err != nil {
		return fmt.Errorf("failed to get client info: %v", err)
	}

	// Only process OpenStack clients
	if client.Provider != "openstack" {
		p.Logger.Debugf("Client %s is not OpenStack provider, skipping integration", clientID)
		return nil
	}

	if p.OpenStackHandler == nil {
		p.Logger.Warnf("OpenStack handler not available for client %s", clientID)
		return nil
	}

	// Add allowed address pair using OpenStack handler
	p.Logger.Infof("OpenStack integration: Adding allowed address pair %s to interface %s for client %s", 
		downstreamAddress, interfaceID, clientID)

	if err := p.OpenStackHandler.AddAllowedAddressPair(context.Background(), interfaceID, downstreamAddress, projectName); err != nil {
		return fmt.Errorf("failed to add allowed address pair: %v", err)
	}

	p.Logger.Infof("Successfully added allowed address pair %s to interface %s", downstreamAddress, interfaceID)
	return nil
}
