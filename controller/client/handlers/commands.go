package handlers

import (
	"context"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/controller/client/services"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
)

func (h *Client) HandleSendCommand(ctx context.Context, op models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	var err error
	clients := op.GetClients()
	result := []any{}
	processor, exists := h.cmdFactory.GetProcessor(op.GetType())
	if !exists {
		h.logger.Errorf("Unsupported processor command type: %s", op.GetType())
		return nil, fmt.Errorf("unsupported processor command type: %s", op.GetType())
	}

	if len(clients) == 0 {
		clients, err = h.FetchClients(op)
		if err != nil {
			h.logger.Errorf("Failed to fetch clients: %v", err)
			return nil, fmt.Errorf("failed to fetch clients: %v", err)
		}
	}

	for _, client := range clients {
		processedPayload, err := processor.ValidateAndTransform(op, requestDetails, client)
		if err != nil {
			h.logger.Errorf("Command validation error: %v", err)
			return nil, fmt.Errorf("command validation error: %v", err)
		}

		// *** FORWARD LOGIC START ***
		response, err := h.sendCommandWithForward(client.ClientID, op.GetTypeNum(), op.GetSubTypeNum(), processedPayload)
		if err != nil {
			h.logger.WithFields(logger.Fields{
				"client_id":          client.ClientID,
				"downstream_address": client.DownstreamAddress,
				"error":              err,
			}).Errorf("Command sending error")
			return nil, fmt.Errorf("command sending error: %v", err)
		}
		// *** FORWARD LOGIC END ***

		responser, exists := h.responser.GetResponser(op.GetType())
		if !exists {
			h.logger.Errorf("Unsupported responser command type: %s", op.GetType())
			return nil, fmt.Errorf("unsupported responser command type: %s", op.GetType())
		}

		result = append(result, responser.ValidateAndTransform(op, response))
	}

	return result, nil
}

// sendCommandWithForward checks client location and forwards if necessary
func (h *Client) sendCommandWithForward(clientID string, cmdType pb.CommandType, subType pb.SubCommandType, payload any) (*pb.CommandResponse, error) {
	// If registry client is not available, send directly (fallback mode)
	if h.registryClient == nil {
		h.logger.Debugf("Registry client not available, sending command directly to client %s", clientID)
		return h.Service.SendCommand(clientID, cmdType, subType, payload)
	}

	// Check client location in registry
	clientDetails, err := h.registryClient.GetClientDetails(clientID)
	if err != nil {
		// If registry lookup fails, try direct send (client might be local but registry outdated)
		h.logger.Debugf("Registry lookup failed for client %s, trying direct send: %v", clientID, err)
		return h.Service.SendCommand(clientID, cmdType, subType, payload)
	}

	// Get current controller ID
	currentControllerID := h.registryClient.GetControllerID()

	// If client is on this controller, send directly
	if clientDetails.GetControllerId() == currentControllerID {
		h.logger.Debugf("Client %s is local on controller %s, sending directly", clientID, currentControllerID)
		return h.Service.SendCommand(clientID, cmdType, subType, payload)
	}

	// Client is on another controller, forward the command
	if h.forwardClient == nil {
		return nil, fmt.Errorf("client %s is on controller %s but forward client is not available", clientID, clientDetails.GetControllerId())
	}

	h.logger.Infof("Forwarding command for client %s from %s to %s", clientID, currentControllerID, clientDetails.GetControllerId())
	
	// Get target controller FQDN from registry response
	targetAddress := clientDetails.GetControllerFqdn()
	
	// Create command using helper (properly sets payload to correct field)
	identity := &pb.Identity{ClientId: clientID}
	command, err := services.NewCommandWithPayload(generateCommandID(), cmdType, subType, identity, payload)
	if err != nil {
		return nil, fmt.Errorf("failed to create command for forwarding: %v", err)
	}

	return h.forwardClient.ForwardCommand(targetAddress, command)
}

// generateCommandID generates a unique command ID
func generateCommandID() string {
	return uuid.New().String()
}

func (s *Client) FetchClients(op models.OperationClass) ([]models.ServiceClients, error) {
	collection := s.Context.Client.Collection("services")
	filter := bson.M{
		"name":    op.GetCommandName(),
		"project": op.GetCommandProject(),
	}

	var result struct {
		Clients []models.ServiceClients `bson:"clients"`
	}

	err := collection.FindOne(context.Background(), filter).Decode(&result)
	if err != nil {
		return nil, err
	}

	return result.Clients, nil
}
