package responser

import (
	"context"
	"fmt"
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/controller/client/services"
	"github.com/CloudNativeWorks/elchi-backend/controller/cloud/openstack"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
	"go.mongodb.org/mongo-driver/bson"
)

type DeployResponser struct {
	XDSHandler       *xds.AppHandler
	Logger           *logger.Logger
	Service          *services.ClientService
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
	ipMode := result.Deploy.IpMode
	version := result.Deploy.Version

	// Debug logging for deploy response values
	p.Logger.Debugf("Deploy Response - ClientID: %s, DownstreamAddress: '%s', InterfaceID: '%s', IPMode: '%s', Version: '%s', ServiceName: %s",
		clientID, downstreamAddress, interfaceID, ipMode, version, serviceName)

	// Step 1: Add client to service (critical step)
	// If service will be redeploy hits this
	if err := p.addClientToService(clientID, downstreamAddress, serviceName, projectName, version, interfaceID, ipMode); err != nil {
		p.Logger.Errorf("Failed to add client to service: %v", err)
		if strings.Contains(err.Error(), "already exists in service") {
			response.Success = true
		} else {
			response.Success = false
			response.Error = fmt.Sprintf("Client redeployed successfully but service registration failed: %v", err)
		}
		return response
	}

	p.Logger.Infof("Client ID: %s successfully added to service: %s", clientID, serviceName)

	// Step 2: OpenStack integration (if required)
	if interfaceID != "" && ipMode != "" {
		if err := p.handleOpenStackIntegration(clientID, downstreamAddress, interfaceID, projectName, ipMode); err != nil {
			p.Logger.Errorf("OpenStack integration failed for client %s: %v", clientID, err)
			response.Success = false
			response.Error = fmt.Sprintf("Client deployed and registered successfully but OpenStack IP configuration failed: %v", err)
			return response
		}
		p.Logger.Infof("OpenStack integration completed successfully for client %s", clientID)
	}

	// All steps successful
	p.Logger.Infof("Deploy completed successfully for client %s", clientID)
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

func (p *DeployResponser) addClientToService(clientID, downstreamAddress, serviceName, projectName, version, interfaceID, ipMode string) error {
	servicesCollection := p.XDSHandler.Context.Client.Collection("services")

	// Debug logging for service update
	p.Logger.Debugf("addClientToService - Adding client to service: ClientID=%s, DownstreamAddress='%s', InterfaceID='%s', IPMode='%s', Version='%s', ServiceName=%s, Project=%s",
		clientID, downstreamAddress, interfaceID, ipMode, version, serviceName, projectName)

	clientInfo := models.ServiceClients{
		ClientID:          clientID,
		DownstreamAddress: downstreamAddress,
		InterfaceID:       interfaceID,
		IPMode:            ipMode,
	}

	var existingService struct {
		Clients []models.ServiceClients `bson:"clients"`
	}

	filter := bson.M{
		"name":    serviceName,
		"project": projectName,
		"version": version,
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
		return fmt.Errorf("no service found with name: %s, project: %s, version: %s", serviceName, projectName, version)
	}

	if result.ModifiedCount == 0 {
		return fmt.Errorf("service found but no modification occurred")
	}

	p.Logger.Infof("addClientToService - Successfully added client to service: ClientID=%s, Service=%s", clientID, serviceName)
	return nil
}

// handleOpenStackIntegration manages OpenStack IP address operations (AAP or Fixed IP)
func (p *DeployResponser) handleOpenStackIntegration(clientID, downstreamAddress, interfaceID, projectName, ipMode string) error {
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

	// Handle IP address management based on mode
	switch ipMode {
	case "aap":
		// Add allowed address pair using OpenStack handler
		p.Logger.Infof("OpenStack integration: Adding allowed address pair %s to interface %s for client %s",
			downstreamAddress, interfaceID, clientID)

		if err := p.OpenStackHandler.AddAllowedAddressPair(context.Background(), interfaceID, downstreamAddress, projectName); err != nil {
			return fmt.Errorf("failed to add allowed address pair: %v", err)
		}

		p.Logger.Infof("Successfully added allowed address pair %s to interface %s", downstreamAddress, interfaceID)
		return nil

	case "fixed":
		// Add fixed IP using OpenStack handler
		// TODO: Need subnet ID for fixed IP - for now, we'll need to get it from the port's existing fixed IPs
		p.Logger.Infof("OpenStack integration: Adding fixed IP %s to interface %s for client %s",
			downstreamAddress, interfaceID, clientID)

		// We need to determine the subnet ID from the port's existing fixed IPs
		// For now, we'll use the first fixed IP's subnet ID as reference
		if err := p.OpenStackHandler.AddFixedIPWithAutoSubnet(context.Background(), interfaceID, downstreamAddress, projectName); err != nil {
			return fmt.Errorf("failed to add fixed IP: %v", err)
		}

		p.Logger.Infof("Successfully added fixed IP %s to interface %s", downstreamAddress, interfaceID)
		return nil

	default:
		return fmt.Errorf("invalid ip_mode: %s (must be 'aap' or 'fixed')", ipMode)
	}
}
