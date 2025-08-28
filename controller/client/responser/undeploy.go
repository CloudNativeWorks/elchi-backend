package responser

import (
	"context"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/controller/client/services"
	"github.com/CloudNativeWorks/elchi-backend/controller/cloud/openstack"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
	"go.mongodb.org/mongo-driver/bson"
	"google.golang.org/grpc/metadata"
)

type UnDeployResponser struct {
	XDSHandler      *xds.AppHandler
	Logger          *logger.Logger
	Service         *services.ClientService
	OpenStackHandler *openstack.Handler
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

	// Get interface_id from database before removing client
	interfaceID, err := p.getInterfaceIDFromService(clientID, serviceName, projectName, downstreamAddress)
	if err != nil {
		p.Logger.Warnf("Could not get interface_id from service: %v", err)
	}

	if err := p.removeClientFromService(clientID, serviceName, projectName); err != nil {
		p.Logger.Errorf("Error while removing client from service: %v", err)
	} else {
		p.Logger.Infof("Client ID: %s, Service: %s successfully removed", clientID, serviceName)
		
		// OpenStack integration for allowed address pair removal
		if interfaceID != "" {
			if err := p.handleOpenStackIntegration(clientID, downstreamAddress, interfaceID, projectName); err != nil {
				p.Logger.Warnf("OpenStack integration failed for client %s: %v", clientID, err)
			}
		}
	}

	if err := p.notifyControlPlaneUndeploy(serviceName, projectName, downstreamAddress); err != nil {
		p.Logger.Errorf("Error while notifying control-plane about undeploy: %v", err)
	} else {
		p.Logger.Infof("Control-plane notified about undeploy: %s", serviceName)
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

// getInterfaceIDFromService retrieves the interface_id for a specific client from service
func (p *UnDeployResponser) getInterfaceIDFromService(clientID, serviceName, projectName, downstreamAddress string) (string, error) {
	servicesCollection := p.XDSHandler.Context.Client.Collection("services")

	var service struct {
		Clients []models.ServiceClients `bson:"clients"`
	}

	filter := bson.M{
		"name":    serviceName,
		"project": projectName,
	}

	if err := servicesCollection.FindOne(context.Background(), filter).Decode(&service); err != nil {
		return "", fmt.Errorf("service not found: %w", err)
	}

	// Find the matching client to get interface_id
	for _, client := range service.Clients {
		if client.ClientID == clientID && client.DownstreamAddress == downstreamAddress {
			p.Logger.Debugf("Found interface_id: %s for client %s", client.InterfaceID, clientID)
			return client.InterfaceID, nil
		}
	}

	return "", fmt.Errorf("client not found in service: %s", clientID)
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

func (p *UnDeployResponser) notifyControlPlaneUndeploy(serviceName, projectName, downstreamAddress string) error {
	nodeID := fmt.Sprintf("%s::%s::%s", serviceName, projectName, downstreamAddress)

	p.Logger.Debugf("notifyControlPlaneUndeploy called - nodeID=%s, serviceName=%s, project=%s, downstream=%s\n",
		nodeID, serviceName, projectName, downstreamAddress)

	version := ""
	collection := p.XDSHandler.Context.Client.Collection("listeners")
	filter := bson.M{
		"general.name":    serviceName,
		"general.project": projectName,
	}

	var result bson.M
	err := collection.FindOne(context.Background(), filter).Decode(&result)
	if err == nil {
		if general, ok := result["general"].(bson.M); ok {
			if v, ok := general["version"].(string); ok && v != "" {
				version = v
				p.Logger.Infof("Found version from DB: %s\n", version)
			}
		}
	} else {
		p.Logger.Errorf("Could not find version in DB, using fallback: %s (error: %v)\n", version, err)
	}

	p.Logger.Debugf("Using version: %s\n", version)

	request := &bridge.UndeployRequest{
		NodeID:            nodeID,
		Project:           projectName,
		ServiceName:       serviceName,
		DownstreamAddress: downstreamAddress,
	}

	md := metadata.Pairs("nodeid", nodeID, "envoy-version", version)
	ctxOut := metadata.NewOutgoingContext(context.Background(), md)

	_, err = (*p.XDSHandler.PokeService).NotifyUndeploy(ctxOut, request)

	if err != nil {
		p.Logger.Errorf("NotifyUndeploy RPC failed: %v\n", err)
		return err
	}

	return nil
}

// handleOpenStackIntegration manages OpenStack allowed address pair removal
func (p *UnDeployResponser) handleOpenStackIntegration(clientID, downstreamAddress, interfaceID, projectName string) error {
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

	// Remove allowed address pair using OpenStack handler
	p.Logger.Infof("OpenStack integration: Removing allowed address pair %s from interface %s for client %s", 
		downstreamAddress, interfaceID, clientID)

	if err := p.OpenStackHandler.RemoveAllowedAddressPair(context.Background(), interfaceID, downstreamAddress, projectName); err != nil {
		return fmt.Errorf("failed to remove allowed address pair: %v", err)
	}

	p.Logger.Infof("Successfully removed allowed address pair %s from interface %s", downstreamAddress, interfaceID)
	return nil
}
