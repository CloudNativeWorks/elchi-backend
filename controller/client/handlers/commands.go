package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/controller/client/processor"
	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
	"go.mongodb.org/mongo-driver/bson"
)

func (h *Client) HandleSendCommand(ctx context.Context, op models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	// Check if this is a forwarded request to prevent infinite loops
	if requestDetails.IsForwarded {
		h.logger.Debugf("Processing forwarded request, using direct execution only")
		return h.executeDirectCommand(ctx, op, requestDetails)
	}

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
		// *** NEW ROUTING LOGIC - CHECK LOCATION FIRST ***
		// First check where the client is located before doing any processing
		response, err := h.sendCommandWithLocationCheck(ctx, requestDetails, client, op, processor)
		if err != nil {
			h.logger.WithFields(logger.Fields{
				"client_id":          client.ClientID,
				"downstream_address": client.DownstreamAddress,
				"error":              err,
			}).Errorf("Command sending error")
			return nil, fmt.Errorf("command sending error: %v", err)
		}
		// *** END NEW ROUTING LOGIC ***

		responser, exists := h.responser.GetResponser(op.GetType())
		if !exists {
			h.logger.Errorf("Unsupported responser command type: %s", op.GetType())
			return nil, fmt.Errorf("unsupported responser command type: %s", op.GetType())
		}

		result = append(result, responser.ValidateAndTransform(op, response))
	}

	return result, nil
}

// executeDirectCommand executes command directly without routing (for forwarded requests)
func (h *Client) executeDirectCommand(_ context.Context, op models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	clients := op.GetClients()
	result := []any{}
	processor, exists := h.cmdFactory.GetProcessor(op.GetType())
	if !exists {
		return nil, fmt.Errorf("unsupported processor command type: %s", op.GetType())
	}

	if len(clients) == 0 {
		var err error
		clients, err = h.FetchClients(op)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch clients: %v", err)
		}
	}

	for _, client := range clients {
		processedPayload, err := processor.ValidateAndTransform(op, requestDetails, client)
		if err != nil {
			return nil, fmt.Errorf("command validation error: %v", err)
		}

		// Direct send only (no routing)
		response, err := h.tryDirectSend(client.ClientID, op.GetTypeNum(), op.GetSubTypeNum(), processedPayload)
		if err != nil {
			return nil, fmt.Errorf("client %s not found on this controller: %v", client.ClientID, err)
		}

		responser, exists := h.responser.GetResponser(op.GetType())
		if !exists {
			return nil, fmt.Errorf("unsupported responser command type: %s", op.GetType())
		}

		result = append(result, responser.ValidateAndTransform(op, response))
	}

	return result, nil
}

// sendCommandWithLocationCheck checks client location first, then processes accordingly
func (h *Client) sendCommandWithLocationCheck(ctx context.Context, requestDetails models.RequestDetails, client models.ServiceClients, op models.OperationClass, processor processor.CommandProcessor) (*pb.CommandResponse, error) {
	clientID := client.ClientID
	
	// Strategy 1: Check if client is connected locally first (connection check only)
	if h.Service.IsClientConnected(clientID) {
		// Client is local - do validate & transform then send
		h.logger.Debugf("Client %s is local, processing with validate & transform", clientID)
		
		processedPayload, err := processor.ValidateAndTransform(op, requestDetails, client)
		if err != nil {
			return nil, fmt.Errorf("command validation error for local client %s: %v", clientID, err)
		}
		
		return h.Service.SendCommand(clientID, op.GetTypeNum(), op.GetSubTypeNum(), processedPayload)
	}

	// Strategy 2: Client not local, use registry + forwarding (NO validate & transform)
	if h.registryClient == nil {
		h.logger.Errorf("Client %s not found locally and no registry client available", clientID)
		return nil, fmt.Errorf("client %s not found and no registry available", clientID)
	}

	h.logger.Infof("Client %s not local, forwarding without processing", clientID)

	// Get client location from registry
	clientLocation, err := h.registryClient.GetClientLocation(clientID)
	if err != nil {
		h.logger.Errorf("Failed to get client location from registry: %v", err)
		return nil, fmt.Errorf("failed to find client %s: %v", clientID, err)
	}

	// Get current controller ID
	currentControllerID := h.registryClient.GetControllerID()

	// If client is on this controller (somehow missed in direct check), try local processing
	if clientLocation.ControllerId == currentControllerID {
		h.logger.Debugf("Client %s is supposed to be local, processing locally", clientID)
		
		processedPayload, err := processor.ValidateAndTransform(op, requestDetails, client)
		if err != nil {
			return nil, fmt.Errorf("command validation error for supposedly local client %s: %v", clientID, err)
		}
		
		return h.Service.SendCommand(clientID, op.GetTypeNum(), op.GetSubTypeNum(), processedPayload)
	}

	// Client is on another controller, forward raw request (NO processing)
	h.logger.Infof("Forwarding raw request for client %s from %s to %s (no validate & transform)", clientID, currentControllerID, clientLocation.ControllerId)

	// Forward raw request via HTTP to target controller
	return h.forwardCommandViaHTTP(ctx, requestDetails, clientLocation.ControllerId, clientID, op.GetTypeNum(), op.GetSubTypeNum(), nil) // Pass nil for payload since we're forwarding raw
}

// forwardCommandViaHTTP forwards command to another controller via HTTP with authentication
func (h *Client) forwardCommandViaHTTP(ctx context.Context, requestDetails models.RequestDetails, targetControllerID, clientID string, _ pb.CommandType, _ pb.SubCommandType, _ any) (*pb.CommandResponse, error) {
	// Use helper to get full Kubernetes service DNS name
	serviceName := helper.ToK8sServiceName(targetControllerID, "elchi-stack")
	// Create target HTTP URL
	targetURL := fmt.Sprintf("http://%s:8099/api/op/clients", serviceName)

	h.logger.Infof("Forwarding command to: %s", targetURL)

	// Use original request body for forwarding
	var requestBody []byte
	if len(requestDetails.OriginalBody) > 0 {
		requestBody = requestDetails.OriginalBody
		h.logger.Debugf("Forwarding original request body (%d bytes) for client %s", len(requestBody), clientID)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", targetURL, bytes.NewBuffer(requestBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forward-From", h.registryClient.GetControllerID())
	req.Header.Set("X-Forward-Client", clientID)
	req.Header.Set("X-Forwarded-Request", "true") // Prevent infinite loops

	// Forward authentication tokens from original request
	if requestDetails.Token != "" {
		req.Header.Set("token", requestDetails.Token)
		h.logger.Debugf("Forwarding authentication token for client %s", clientID)
	} else {
		h.logger.Warnf("No authentication token found in original request for client %s", clientID)
	}

	if requestDetails.RefreshToken != "" {
		req.Header.Set("refresh-token", requestDetails.RefreshToken)
	}

	// Send HTTP request with timeout
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to forward HTTP request to %s: %v", targetURL, err)
	}
	defer resp.Body.Close()

	// Read response
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read forward response: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("forward request failed with status %d: %s", resp.StatusCode, string(responseBody))
	}

	// Parse response as CommandResponse
	var commandResponse pb.CommandResponse
	if err := json.Unmarshal(responseBody, &commandResponse); err != nil {
		return nil, fmt.Errorf("failed to parse forward response: %v", err)
	}

	h.logger.Infof("Command successfully forwarded via HTTP to %s for client %s", targetURL, clientID)
	return &commandResponse, nil
}

// tryDirectSend attempts to send command directly to local client
func (h *Client) tryDirectSend(clientID string, cmdType pb.CommandType, subType pb.SubCommandType, payload any) (*pb.CommandResponse, error) {
	// Check if client is connected to this controller
	if h.Service.IsClientConnected(clientID) {
		return h.Service.SendCommand(clientID, cmdType, subType, payload)
	}
	return nil, fmt.Errorf("client not connected to this controller")
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
