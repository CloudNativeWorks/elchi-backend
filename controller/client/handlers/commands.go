package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/controller/client/processor"
	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
	"go.mongodb.org/mongo-driver/bson"
)

// Custom error types for better error handling
type (
	ProcessorNotFoundError struct {
		CommandType string
	}
	
	ResponserNotFoundError struct {
		CommandType string
	}
	
	ClientFetchError struct {
		Operation string
		Cause     error
	}
)

func (e ProcessorNotFoundError) Error() string {
	return fmt.Sprintf("unsupported processor command type: %s", e.CommandType)
}

func (e ResponserNotFoundError) Error() string {
	return fmt.Sprintf("unsupported responser command type: %s", e.CommandType)
}

func (e ClientFetchError) Error() string {
	return fmt.Sprintf("failed to fetch clients for operation %s: %v", e.Operation, e.Cause)
}

// Constants
const (
	HTTPTimeout        = 30 * time.Second
	ControllerHTTPPort = "8099"
	DevModeEnvVar      = "DEV_MODE"
	
	// Headers
	HeaderContentType      = "Content-Type"
	HeaderForwardFrom      = "X-Forward-From"
	HeaderForwardClient    = "X-Forward-Client"
	HeaderForwardedRequest = "X-Forwarded-Request"
	HeaderToken            = "token"
	HeaderRefreshToken     = "refresh-token"
	
	// Values
	ContentTypeJSON = "application/json"
	ForwardTrue     = "true"
)

// handleForwardedResponse processes a forwarded response and adds it to results
func (h *Client) handleForwardedResponse(forwardedResp *ForwardedResponse, result *[]any) error {
	h.logger.Infof("Received forwarded response for client %s (%d bytes)", forwardedResp.ClientID, len(forwardedResp.RawJSON))
	
	// Parse the forwarded response - it should be an array of responses
	var forwardedResults []any
	if err := json.Unmarshal(forwardedResp.RawJSON, &forwardedResults); err != nil {
		h.logger.Errorf("Failed to parse forwarded response JSON for client %s: %v", forwardedResp.ClientID, err)
		return fmt.Errorf("failed to parse forwarded response for client %s: %v", forwardedResp.ClientID, err)
	}

	// Add all forwarded results to our main result array (flatten)
	h.logger.Infof("Flattening %d forwarded responses into main result for client %s", len(forwardedResults), forwardedResp.ClientID)
	*result = append(*result, forwardedResults...)
	return nil
}

// processLocalResponse processes a local response and adds it to results
func (h *Client) processLocalResponse(op models.OperationClass, response *pb.CommandResponse, clientID string, result *[]any) error {
	responser, exists := h.responser.GetResponser(op.GetType())
	if !exists {
		h.logger.Errorf("Unsupported responser command type: %s", op.GetType())
		return ResponserNotFoundError{CommandType: op.GetType()}
	}

	localResponse := responser.ValidateAndTransform(op, response)
	*result = append(*result, localResponse)
	
	h.logger.Infof("Added local response for client %s to result array", clientID)
	return nil
}

// validateAndPrepareCommand validates the operation and prepares processor
func (h *Client) validateAndPrepareCommand(op models.OperationClass) (processor.CommandProcessor, error) {
	processor, exists := h.cmdFactory.GetProcessor(op.GetType())
	if !exists {
		h.logger.Errorf("Unsupported processor command type: %s", op.GetType())
		return nil, ProcessorNotFoundError{CommandType: op.GetType()}
	}
	return processor, nil
}

// buildTargetURL builds the target URL for forwarding based on environment
func (h *Client) buildTargetURL(targetControllerID string) string {
	if os.Getenv(DevModeEnvVar) == ForwardTrue {
		// Development mode: use container hostname (bridge network)
		return fmt.Sprintf("http://%s:%s/api/op/clients", targetControllerID, ControllerHTTPPort)
	}
	// Production mode: use Kubernetes service DNS
	serviceName := helper.ToK8sServiceName(targetControllerID, h.Context.Config.ElchiNamespace)
	return fmt.Sprintf("http://%s:%s/api/op/clients", serviceName, ControllerHTTPPort)
}

// prepareForwardRequest creates and configures HTTP request for forwarding
func (h *Client) prepareForwardRequest(ctx context.Context, targetURL string, requestBody []byte, requestDetails models.RequestDetails, clientID string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", targetURL, bytes.NewBuffer(requestBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %v", err)
	}

	// Set headers
	req.Header.Set(HeaderContentType, ContentTypeJSON)
	req.Header.Set(HeaderForwardFrom, h.registryClient.GetControllerID())
	req.Header.Set(HeaderForwardClient, clientID)
	req.Header.Set(HeaderForwardedRequest, ForwardTrue) // Prevent infinite loops

	// Forward authentication tokens from original request
	if requestDetails.Token != "" {
		req.Header.Set(HeaderToken, requestDetails.Token)
		h.logger.Debugf("Forwarding authentication token for client %s", clientID)
	} else {
		h.logger.Warnf("No authentication token found in original request for client %s", clientID)
	}

	if requestDetails.RefreshToken != "" {
		req.Header.Set(HeaderRefreshToken, requestDetails.RefreshToken)
	}

	return req, nil
}

// executeForwardRequest executes HTTP request and returns response body
func (h *Client) executeForwardRequest(req *http.Request, targetURL string) ([]byte, error) {
	client := &http.Client{
		Timeout: HTTPTimeout,
	}

	resp, err := client.Do(req)
	if err != nil {
		h.logger.Errorf("HTTP forward request failed: %v", err)
		return nil, fmt.Errorf("failed to forward HTTP request to %s: %v", targetURL, err)
	}
	defer resp.Body.Close()

	h.logger.Infof("HTTP forward response received: Status=%d, ContentLength=%d", resp.StatusCode, resp.ContentLength)

	// Read response
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		h.logger.Errorf("Failed to read HTTP forward response: %v", err)
		return nil, fmt.Errorf("failed to read forward response: %v", err)
	}

	h.logger.Infof("HTTP forward response body size: %d bytes", len(responseBody))

	if resp.StatusCode != http.StatusOK {
		h.logger.Errorf("HTTP forward failed: Status=%d, Body=%s", resp.StatusCode, string(responseBody))
		return nil, fmt.Errorf("forward request failed with status %d: %s", resp.StatusCode, string(responseBody))
	}

	return responseBody, nil
}

func (h *Client) HandleSendCommand(ctx context.Context, op models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	// Check if this is a forwarded request to prevent infinite loops
	if requestDetails.IsForwarded {
		h.logger.Debugf("Processing forwarded request, using direct execution only")
		return h.executeDirectCommand(ctx, op, requestDetails)
	}

	// Validate and prepare command processor
	processor, err := h.validateAndPrepareCommand(op)
	if err != nil {
		return nil, err
	}

	// Get or fetch clients
	clients := op.GetClients()
	if len(clients) == 0 {
		clients, err = h.FetchClients(op)
		if err != nil {
			h.logger.Errorf("Failed to fetch clients: %v", err)
			return nil, ClientFetchError{Operation: op.GetType(), Cause: err}
		}
	}

	h.logger.Infof("Processing %d clients for command", len(clients))

	// Process each client
	result := []any{}
	for i, client := range clients {
		h.logger.Infof("Processing client %s (%d/%d)", client.ClientID, i+1, len(clients))
		
		// Check location and process accordingly
		response, err := h.sendCommandWithLocationCheck(ctx, requestDetails, client, op, processor)
		if err != nil {
			// Check if this is a forwarded response with raw JSON
			if forwardedResp, ok := err.(*ForwardedResponse); ok {
				if err := h.handleForwardedResponse(forwardedResp, &result); err != nil {
					return nil, err
				}
				continue
			}

			// Regular error
			h.logger.WithFields(logger.Fields{
				"client_id":          client.ClientID,
				"downstream_address": client.DownstreamAddress,
				"error":              err,
			}).Errorf("Command sending error")
			return nil, fmt.Errorf("command sending error for client %s: %v", client.ClientID, err)
		}

		// This is a local response - process normally
		if err := h.processLocalResponse(op, response, client.ClientID, &result); err != nil {
			return nil, err
		}
	}

	h.logger.Infof("Command processing completed. Returning %d total responses", len(result))
	return result, nil
}

// executeDirectCommand executes command directly without routing (for forwarded requests)
func (h *Client) executeDirectCommand(_ context.Context, op models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	h.logger.Infof("=== DIRECT EXECUTION START ===")
	h.logger.Infof("Is Forwarded Request: %v", requestDetails.IsForwarded)
	h.logger.Infof("Original Body Size: %d bytes", len(requestDetails.OriginalBody))
	
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

	h.logger.Infof("Direct execution for %d clients (forwarded request)", len(clients))

	for i, client := range clients {
		h.logger.Infof("Processing client %s (%d/%d) - direct execution", client.ClientID, i+1, len(clients))
		
		processedPayload, err := processor.ValidateAndTransform(op, requestDetails, client)
		if err != nil {
			h.logger.Errorf("Validation failed for client %s: %v", client.ClientID, err)
			return nil, fmt.Errorf("command validation error for client %s: %v", client.ClientID, err)
		}

		// Direct send only (no routing)
		response, err := h.tryDirectSend(client.ClientID, op.GetTypeNum(), op.GetSubTypeNum(), processedPayload)
		if err != nil {
			h.logger.Errorf("Direct send failed for client %s: %v", client.ClientID, err)
			return nil, fmt.Errorf("client %s not found on this controller: %v", client.ClientID, err)
		}

		responser, exists := h.responser.GetResponser(op.GetType())
		if !exists {
			return nil, fmt.Errorf("unsupported responser command type: %s", op.GetType())
		}

		processedResponse := responser.ValidateAndTransform(op, response)
		result = append(result, processedResponse)
		
		h.logger.Infof("Successfully processed client %s via direct execution", client.ClientID)
	}

	h.logger.Infof("Direct execution completed for %d clients, returning %d responses", len(clients), len(result))
	h.logger.Infof("=== DIRECT EXECUTION END ===")
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
	h.logger.Infof("Client %s not local, checking registry availability", clientID)

	if h.registryClient == nil {
		h.logger.Errorf("Client %s not found locally and no registry client available (nil)", clientID)
		return nil, fmt.Errorf("client %s not found and no registry available", clientID)
	}

	h.logger.Debugf("Registry client object exists for client %s, checking connection...", clientID)

	if !h.registryClient.IsConnected() {
		h.logger.Errorf("Client %s not found locally and registry client not connected yet (waiting for connection)", clientID)
		return nil, fmt.Errorf("client %s not found and registry not connected", clientID)
	}

	h.logger.Infof("Registry client available and connected for client %s", clientID)

	// Get client location from registry
	h.logger.Infof("Requesting client location from registry for client: %s", clientID)
	clientLocation, err := h.registryClient.GetClientLocation(clientID)
	if err != nil {
		h.logger.Errorf("Failed to get client location from registry for client %s: %v", clientID, err)
		return nil, fmt.Errorf("failed to find client %s: %v", clientID, err)
	}

	h.logger.Infof("Registry returned location for client %s: controller=%s, found=%v", clientID, clientLocation.ControllerId, clientLocation.Found)

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
	h.logger.Infof("=== FORWARD DEBUG ===")
	h.logger.Infof("Client: %s", clientID)
	h.logger.Infof("From Controller: %s", currentControllerID)
	h.logger.Infof("To Controller: %s", clientLocation.ControllerId)
	h.logger.Infof("Original Body Size: %d bytes", len(requestDetails.OriginalBody))
	h.logger.Infof("=====================")

	return h.forwardCommandViaHTTP(ctx, requestDetails, clientLocation.ControllerId, clientID, op.GetTypeNum(), op.GetSubTypeNum(), nil) // Pass nil for payload since we're forwarding raw
}

// ForwardedResponse is a special error type that contains the raw forwarded response
type ForwardedResponse struct {
	RawJSON    []byte
	StatusCode int
	ClientID   string
}

func (e *ForwardedResponse) Error() string {
	return fmt.Sprintf("forwarded response for client %s (%d bytes)", e.ClientID, len(e.RawJSON))
}

// forwardCommandViaHTTP forwards command to another controller via HTTP with authentication
func (h *Client) forwardCommandViaHTTP(ctx context.Context, requestDetails models.RequestDetails, targetControllerID, clientID string, cmdType pb.CommandType, subCmdType pb.SubCommandType, _ any) (*pb.CommandResponse, error) {
	// Build target URL
	targetURL := h.buildTargetURL(targetControllerID)

	h.logger.Infof("=== HTTP FORWARD ===")
	h.logger.Infof("Target Controller ID: %s", targetControllerID)
	h.logger.Infof("Target URL: %s", targetURL)
	h.logger.Infof("Client ID: %s", clientID)
	h.logger.Infof("Has Original Body: %v", len(requestDetails.OriginalBody) > 0)
	h.logger.Infof("Has Token: %v", requestDetails.Token != "")
	h.logger.Infof("Is Forwarded: %v", requestDetails.IsForwarded)
	h.logger.Infof("===================")

	// Filter original request body to only include the specific client
	var requestBody []byte
	if len(requestDetails.OriginalBody) > 0 {
		h.logger.Infof("About to filter request body for client %s...", clientID)
		filteredBody, err := h.filterRequestBodyForClient(requestDetails.OriginalBody, clientID)
		if err != nil {
			h.logger.Errorf("Failed to filter request body for client %s: %v", clientID, err)
			return nil, fmt.Errorf("failed to filter request body for client %s: %v", clientID, err)
		}
		requestBody = filteredBody
		h.logger.Infof("Successfully filtered request body for client %s: original=%d bytes, filtered=%d bytes", clientID, len(requestDetails.OriginalBody), len(requestBody))
	} else {
		h.logger.Warnf("No original request body found for client %s", clientID)
	}

	// Prepare HTTP request
	req, err := h.prepareForwardRequest(ctx, targetURL, requestBody, requestDetails, clientID)
	if err != nil {
		return nil, err
	}

	// Execute request and get response
	responseBody, err := h.executeForwardRequest(req, targetURL)
	if err != nil {
		return nil, err
	}

	// Return the raw response as a special error that can be handled by caller
	h.logger.Infof("Command successfully forwarded via HTTP to %s for client %s", targetURL, clientID)
	h.logger.Infof("Returning raw forwarded response (%d bytes)", len(responseBody))

	return nil, &ForwardedResponse{
		RawJSON:    responseBody,
		StatusCode: http.StatusOK,
		ClientID:   clientID,
	}
}

// filterRequestBodyForClient filters the original request body to only include the specified client
func (h *Client) filterRequestBodyForClient(originalBody []byte, targetClientID string) ([]byte, error) {
	h.logger.Infof("=== FILTER REQUEST BODY ===")
	h.logger.Infof("Target Client ID: %s", targetClientID)
	h.logger.Infof("Original Body Size: %d bytes", len(originalBody))
	
	// Parse as generic JSON to avoid type conversion issues
	var jsonData map[string]interface{}
	if err := json.Unmarshal(originalBody, &jsonData); err != nil {
		h.logger.Errorf("Failed to parse original request body as JSON: %v", err)
		return nil, fmt.Errorf("failed to parse original request body as JSON: %v", err)
	}

	// Check if clients array exists
	clients, exists := jsonData["clients"]
	
	// Handle cases where we need to add target client:
	// 1. Field doesn't exist
	// 2. Field is nil
	// 3. Field is empty array
	shouldAddTargetClient := false
	
	if !exists || clients == nil {
		shouldAddTargetClient = true
	} else {
		// Check if it's an empty array
		clientsArray, ok := clients.([]interface{})
		if !ok {
			h.logger.Errorf("Clients field is not an array")
			return nil, fmt.Errorf("clients field is not an array")
		}
		
		if len(clientsArray) == 0 {
			shouldAddTargetClient = true
		}
	}
	
	if shouldAddTargetClient {
		h.logger.Infof("Adding target client info for forwarding")
		
		// Add the target client to the JSON
		jsonData["clients"] = []map[string]interface{}{
			{
				"client_id":          targetClientID,
				"downstream_address": "", // Target controller will resolve this
			},
		}
		
		// Marshal back to JSON
		updatedBody, err := json.Marshal(jsonData)
		if err != nil {
			h.logger.Errorf("Failed to marshal updated request body: %v", err)
			return nil, fmt.Errorf("failed to marshal updated request body: %v", err)
		}
		
		h.logger.Infof("Created filtered body with target client: %d bytes", len(updatedBody))
		return updatedBody, nil
	}
	
	// If clients array exists, filter it
	clientsArray, ok := clients.([]interface{})
	if !ok {
		h.logger.Errorf("Clients field is not an array")
		return nil, fmt.Errorf("clients field is not an array")
	}
	
	h.logger.Infof("Original request contains %d clients", len(clientsArray))
	
	// Filter clients to only include the target client
	var filteredClients []map[string]interface{}
	for _, client := range clientsArray {
		clientMap, ok := client.(map[string]interface{})
		if !ok {
			h.logger.Errorf("Unexpected client format in original request body")
			return nil, fmt.Errorf("unexpected client format in original request body")
		}

		if clientMap["client_id"] == targetClientID {
			filteredClients = append(filteredClients, clientMap)
			h.logger.Infof("✓ Including client %s in filtered request", clientMap["client_id"])
		} else {
			h.logger.Infof("✗ Excluding client %s from filtered request", clientMap["client_id"])
		}
	}

	if len(filteredClients) == 0 {
		h.logger.Errorf("Target client %s not found in original request", targetClientID)
		return nil, fmt.Errorf("target client %s not found in original request", targetClientID)
	}

	// Create new operations struct with filtered clients
	filteredOp := jsonData
	filteredOp["clients"] = filteredClients

	h.logger.Infof("Filtered request contains %d clients (target: %s)", len(filteredClients), targetClientID)

	// Marshal back to JSON
	filteredBody, err := json.Marshal(filteredOp)
	if err != nil {
		h.logger.Errorf("Failed to marshal filtered request: %v", err)
		return nil, fmt.Errorf("failed to marshal filtered request: %v", err)
	}

	h.logger.Infof("Filtered Body Size: %d bytes", len(filteredBody))
	h.logger.Infof("==============================")
	return filteredBody, nil
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
