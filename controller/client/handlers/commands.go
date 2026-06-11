package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/controller/client/authorization"
	"github.com/CloudNativeWorks/elchi-backend/controller/client/processor"
	"github.com/CloudNativeWorks/elchi-backend/controller/client/services"
	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/registry"
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

// ClientProcessResult represents result of processing a single client
type ClientProcessResult struct {
	ClientID string
	Result   any
	Error    error
	Index    int
}

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
	// maxForwardTimeout bounds a forwarded command when the caller supplies no
	// context deadline (e.g. a UI-originated request). Context-deadline'd callers
	// (the parallel fan-out sizes its per-client ctx from the command type's real
	// timeout) are bounded by their own deadline instead — a fixed client-level
	// timeout here used to cut long commands (SHIELD 180s, ENVOY_VERSION 120s)
	// mid-flight while the remote pod kept executing them.
	maxForwardTimeout            = 4 * time.Minute
	defaultControllerForwardPort = uint(8099)
	DevModeEnvVar                = "DEV_MODE"

	// Headers
	HeaderContentType      = "Content-Type"
	HeaderForwardFrom      = "X-Forward-From"
	HeaderForwardClient    = "X-Forward-Client"
	HeaderForwardedRequest = "X-Forwarded-Request"
	HeaderAuthorization    = "Authorization"

	// Values
	ContentTypeJSON = "application/json"
	ForwardTrue     = "true"
)

// Shared HTTP client with connection pooling for better performance.
// No client-level Timeout: a forwarded command's duration is bounded by the
// per-request context (sized per command type by the fan-out path, or by
// maxForwardTimeout when the caller has no deadline — see executeForwardRequest).
// Connection ESTABLISHMENT stays tightly bounded via the dial/TLS timeouts below,
// so a dead pod fails fast while a long-running remote command is not cut off.
var sharedHTTPClient = &http.Client{
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second, // TCP connect bound (dead pod → fail fast)
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        100,              // Total idle connections
		MaxIdleConnsPerHost: 10,               // Idle connections per host
		IdleConnTimeout:     90 * time.Second, // How long idle connections stay open
		DisableCompression:  false,            // Enable gzip compression
		ForceAttemptHTTP2:   true,             // Use HTTP/2 when possible
	},
}

// handleForwardedResponse processes a forwarded response and adds it to results
func (h *Client) handleForwardedResponse(forwardedResp *ForwardedResponse, result *[]any) error {
	h.logger.Infof("Received forwarded response for client %s (%d bytes)", forwardedResp.ClientID, len(forwardedResp.RawJSON))

	// DEBUG: Log first part of raw JSON for inspection
	if len(forwardedResp.RawJSON) > 200 {
		h.logger.Debugf("Raw JSON preview: %s...", string(forwardedResp.RawJSON[:200]))
	} else {
		h.logger.Debugf("Raw JSON full: %s", string(forwardedResp.RawJSON))
	}

	// Parse the forwarded response - it should be an array of responses
	var forwardedResults []any
	if err := json.Unmarshal(forwardedResp.RawJSON, &forwardedResults); err != nil {
		h.logger.Errorf("Failed to parse forwarded response JSON for client %s: %v", forwardedResp.ClientID, err)
		h.logger.Errorf("Problematic JSON: %s", string(forwardedResp.RawJSON))
		return fmt.Errorf("failed to parse forwarded response for client %s: %w", forwardedResp.ClientID, err)
	}

	h.logger.Infof("Parsed %d forwarded results for client %s", len(forwardedResults), forwardedResp.ClientID)

	// DEBUG: Log what we're adding
	for i, item := range forwardedResults {
		h.logger.Debugf("Adding forwarded result %d: type=%T", i, item)
	}

	// Add all forwarded results to our main result array (flatten)
	h.logger.Infof("Flattening %d forwarded responses into main result for client %s", len(forwardedResults), forwardedResp.ClientID)

	// Store current length for verification
	beforeLen := len(*result)
	*result = append(*result, forwardedResults...)
	afterLen := len(*result)

	h.logger.Debugf("Result array: before=%d, added=%d, after=%d", beforeLen, len(forwardedResults), afterLen)

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

	// Add to result array
	*result = append(*result, localResponse)

	h.logger.Infof("Added local response for client %s (result count: %d)", clientID, len(*result))
	h.logger.Debugf("Local response type: %T", localResponse)

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

// processClientsInParallel processes multiple clients concurrently for better performance
func (h *Client) processClientsInParallel(ctx context.Context, clients []models.ServiceClients, op models.OperationClass, processor processor.CommandProcessor, requestDetails models.RequestDetails) ([]any, error) {
	if len(clients) == 0 {
		return []any{}, nil
	}

	// For single client, use sequential processing (no overhead)
	if len(clients) == 1 {
		return h.processClientSequential(ctx, clients, op, processor, requestDetails)
	}

	h.logger.Infof("Processing %d clients in parallel", len(clients))

	// Size the per-client context and the result-collector wait to the command
	// type's real response timeout (+margin for processing/forward overhead). The
	// old fixed 30s/60s caps abandoned long-running commands (SHIELD 180s,
	// ENVOY_VERSION 120s) mid-flight and reported them failed while the clients
	// were still executing them.
	cmdTimeout := services.CommandTimeout(op.GetTypeNum())
	perClientTimeout := cmdTimeout + 10*time.Second
	collectorWait := cmdTimeout + 20*time.Second

	// Use indexed results to maintain order
	results := make([]ClientProcessResult, len(clients))
	resultChan := make(chan ClientProcessResult, len(clients))

	// Worker pool size (max 5 concurrent forwards to avoid overwhelming)
	maxWorkers := min(5, len(clients))

	// Semaphore for limiting concurrent requests
	semaphore := make(chan struct{}, maxWorkers)

	// Start goroutines for each client
	for i, client := range clients {
		go func(index int, client models.ServiceClients) {
			// Acquire semaphore
			semaphore <- struct{}{}
			defer func() {
				<-semaphore
				// Recover from any panics to prevent goroutine hanging
				if r := recover(); r != nil {
					h.logger.Errorf("Panic in client processing goroutine for client %s: %v", client.ClientID, r)
					resultChan <- ClientProcessResult{
						ClientID: client.ClientID,
						Result:   nil,
						Error:    fmt.Errorf("panic in client processing: %v", r),
						Index:    index,
					}
				}
			}()

			// Create timeout context for this individual client processing
			clientCtx, clientCancel := context.WithTimeout(ctx, perClientTimeout)
			defer clientCancel()

			// Process single client with timeout protection AND per-client
			// serialization (the same safeguards as the sequential path) — two
			// concurrent sends to one client's gRPC stream are unsafe and can
			// reorder config pushes (e.g. a policy fan-out racing a
			// connect-triggered shield deploy).
			response, err := h.processClientWithSafeguards(clientCtx, requestDetails, client, op, processor)
			if err != nil {
				h.logger.Debugf("Client %s processing failed: %v", client.ClientID, err)
			}

			resultChan <- ClientProcessResult{
				ClientID: client.ClientID,
				Result:   response,
				Error:    err,
				Index:    index,
			}
		}(i, client)
	}

	// Collect results in order with timeout protection and duplicate detection
	collectedResults := 0
	collectedClientIDs := make(map[string]bool) // Track which clients we've already collected

	for collectedResults < len(clients) {
		select {
		case result := <-resultChan:
			// Debug log for all received results
			h.logger.Debugf("Received result from channel: ClientID='%s', Index=%d, HasError=%v",
				result.ClientID, result.Index, result.Error != nil)

			// Check for duplicate response from same client
			if collectedClientIDs[result.ClientID] {
				h.logger.Warnf("DUPLICATE RESPONSE detected from client %s (index %d) - ignoring and incrementing counter to prevent timeout",
					result.ClientID, result.Index)
				// Increment collectedResults even for duplicates to prevent timeout
				// Otherwise the loop waits forever for len(clients) results
				collectedResults++
				continue // Ignore duplicate response
			}

			// Mark client as collected and store result
			collectedClientIDs[result.ClientID] = true
			results[result.Index] = result // Store by index to maintain order
			collectedResults++
			h.logger.Debugf("Collected result %d/%d from client %s at index %d",
				collectedResults, len(clients), result.ClientID, result.Index)
		case <-time.After(collectorWait):
			h.logger.Errorf("Timeout waiting for results: collected %d/%d results", collectedResults, len(clients))
			// Create error results for missing clients
			for i := range len(clients) {
				if results[i].ClientID == "" { // Empty result means not received
					results[i] = ClientProcessResult{
						ClientID: clients[i].ClientID,
						Result:   nil,
						Error:    fmt.Errorf("timeout waiting for client processing result"),
						Index:    i,
					}
					h.logger.Errorf("Creating timeout error for client %s at index %d", clients[i].ClientID, i)
				}
			}
			// Exit the main loop after timeout
			collectedResults = len(clients)
		case <-ctx.Done():
			h.logger.Errorf("Context canceled while waiting for results: collected %d/%d results", collectedResults, len(clients))
			return nil, ctx.Err()
		}
	}

	// Process results in original order with partial success support
	finalResults := make([]any, 0, len(clients))
	var failedClients []string
	successCount := 0

	for i, result := range results {
		h.logger.Debugf("Processing result for client %s at index %d", result.ClientID, i)

		if result.Error != nil {
			// Handle forwarded response
			var forwardedResp *ForwardedResponse
			if errors.As(result.Error, &forwardedResp) {
				if err := h.handleForwardedResponse(forwardedResp, &finalResults); err != nil {
					h.logger.Errorf("Failed to handle forwarded response for client %s: %v", result.ClientID, err)
					failedClients = append(failedClients, result.ClientID)
					continue
				}
				successCount++
				continue
			}

			// PARTIAL SUCCESS: Log error but continue with other clients
			h.logger.WithFields(logger.Fields{
				"client_id": result.ClientID,
				"error":     result.Error,
				"index":     i,
			}).Warnf("Client %s failed in parallel processing, continuing with others", result.ClientID)

			failedClients = append(failedClients, result.ClientID)
			continue
		}

		// Process local response
		if response, ok := result.Result.(*pb.CommandResponse); ok {
			if err := h.processLocalResponse(op, response, result.ClientID, &finalResults); err != nil {
				h.logger.Errorf("Failed to process response for client %s: %v", result.ClientID, err)
				failedClients = append(failedClients, result.ClientID)
				continue
			}
			successCount++
		} else {
			h.logger.Errorf("Invalid response type from client %s", result.ClientID)
			failedClients = append(failedClients, result.ClientID)
			continue
		}
	}

	// PARTIAL SUCCESS REPORTING
	totalClients := len(clients)
	if successCount == 0 {
		// All clients failed
		return nil, fmt.Errorf("all %d clients failed in parallel processing: %v", totalClients, failedClients)
	}

	if len(failedClients) > 0 {
		// Some clients failed, log warning but return partial results
		h.logger.Warnf("PARTIAL SUCCESS (parallel): %d/%d clients succeeded, failed clients: %v",
			successCount, totalClients, failedClients)
	} else {
		// All clients succeeded
		h.logger.Infof("FULL SUCCESS (parallel): All %d clients processed successfully", totalClients)
	}

	h.logger.Infof("Parallel processing completed for %d clients, returning %d responses", len(clients), len(finalResults))
	return finalResults, nil
}

// processClientSequential processes clients sequentially with partial success support
func (h *Client) processClientSequential(ctx context.Context, clients []models.ServiceClients, op models.OperationClass, processor processor.CommandProcessor, requestDetails models.RequestDetails) ([]any, error) {
	result := []any{}
	failedClients := make(map[string]string) // client_id -> error_message
	successCount := 0

	for i, client := range clients {
		h.logger.Infof("Processing client %s (%d/%d)", client.ClientID, i+1, len(clients))

		// Process client with serialization and health check
		response, err := h.processClientWithSafeguards(ctx, requestDetails, client, op, processor)
		if err != nil {
			// Check if this is a forwarded response with raw JSON
			var forwardedResp *ForwardedResponse
			if errors.As(err, &forwardedResp) {
				if err := h.handleForwardedResponse(forwardedResp, &result); err != nil {
					h.logger.Errorf("Failed to handle forwarded response for client %s: %v", client.ClientID, err)
					failedClients[client.ClientID] = err.Error()
					continue
				}
				successCount++
				continue
			}

			// PARTIAL SUCCESS: Log error but continue with other clients
			h.logger.WithFields(logger.Fields{
				"client_id":          client.ClientID,
				"downstream_address": client.DownstreamAddress,
				"error":              err,
			}).Warnf("Client %s failed, continuing with others", client.ClientID)

			failedClients[client.ClientID] = err.Error()
			continue
		}

		// This is a local response - process normally
		if cmdResponse, ok := response.(*pb.CommandResponse); ok {
			if err := h.processLocalResponse(op, cmdResponse, client.ClientID, &result); err != nil {
				h.logger.Errorf("Failed to process response for client %s: %v", client.ClientID, err)
				failedClients[client.ClientID] = err.Error()
				continue
			}
			successCount++
		} else {
			h.logger.Errorf("Invalid response type from client %s", client.ClientID)
			failedClients[client.ClientID] = "invalid response type"
			continue
		}
	}

	// PARTIAL SUCCESS REPORTING
	totalClients := len(clients)
	if successCount == 0 {
		// All clients failed - build detailed error message
		errorDetails := make([]string, 0, len(failedClients))
		for clientID, errMsg := range failedClients {
			errorDetails = append(errorDetails, fmt.Sprintf("%s: %s", clientID, errMsg))
		}
		return nil, fmt.Errorf("all %d clients failed: %s", totalClients, strings.Join(errorDetails, "; "))
	}

	if len(failedClients) > 0 {
		// Some clients failed, log warning with details but return partial results
		errorDetails := make([]string, 0, len(failedClients))
		for clientID, errMsg := range failedClients {
			errorDetails = append(errorDetails, fmt.Sprintf("%s: %s", clientID, errMsg))
		}
		h.logger.Warnf("PARTIAL SUCCESS: %d/%d clients succeeded, failed clients: %s",
			successCount, totalClients, strings.Join(errorDetails, "; "))
	} else {
		// All clients succeeded
		h.logger.Infof("FULL SUCCESS: All %d clients processed successfully", totalClients)
	}

	return result, nil
}

// buildTargetURL builds the target URL for forwarding based on environment.
// The forward port is read from CONTROLLER_PORT (fleet-wide) and falls back to
// 8099 to preserve historical behaviour. K8s service-DNS suffixing is handled
// by registry.ResolveControllerHTTPAddress so bare-metal deployments resolve
// targetControllerID via /etc/hosts or real DNS instead.
func (h *Client) buildTargetURL(targetControllerID string, requestDetails models.RequestDetails) string {
	var baseURL string
	if os.Getenv(DevModeEnvVar) == ForwardTrue {
		// Development mode: bypass any DNS construction and use container hostname directly.
		port := h.Context.Config.ControllerPort
		if port == 0 {
			port = defaultControllerForwardPort
		}
		baseURL = fmt.Sprintf("http://%s:%d/api/op/clients", targetControllerID, port)
	} else {
		addr := registry.ResolveControllerHTTPAddress(targetControllerID, h.Context.Config.ControllerPort, h.Context.Config.ElchiNamespace)
		baseURL = fmt.Sprintf("http://%s/api/op/clients", addr)
	}

	// Add query parameters if they exist
	if requestDetails.Version != "" {
		baseURL += "?version=" + requestDetails.Version
	}

	return baseURL
}

// prepareForwardRequest creates and configures HTTP request for forwarding
func (h *Client) prepareForwardRequest(ctx context.Context, targetURL string, requestBody []byte, requestDetails models.RequestDetails, clientID string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewBuffer(requestBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Set headers
	req.Header.Set(HeaderContentType, ContentTypeJSON)
	req.Header.Set(HeaderForwardFrom, h.registryClient.GetControllerID())
	req.Header.Set(HeaderForwardClient, clientID)
	req.Header.Set(HeaderForwardedRequest, ForwardTrue) // Prevent infinite loops

	// Forward authentication tokens from original request using standard
	// Authorization header. Worker-originated commands (background jobs)
	// have no gin context, so requestDetails.Token is empty; in that case
	// mint a short-lived JWT impersonating the trigger user so the target
	// pod accepts the forward through the standard auth path.
	token := requestDetails.Token
	if token == "" {
		minted, err := helper.GenerateInternalForwardToken(requestDetails.User)
		if err != nil {
			return nil, fmt.Errorf("forward without authentication for client %s: %w", clientID, err)
		}
		token = minted
		h.logger.Debugf("Using internal forward token for client %s (user=%s)",
			clientID, requestDetails.User.UserID)
	} else {
		h.logger.Debugf("Forwarding authentication token for client %s", clientID)
	}
	req.Header.Set(HeaderAuthorization, "Bearer "+token)

	// Note: RefreshToken forwarding removed as it should only be used for /refresh endpoint

	return req, nil
}

// executeForwardRequest executes HTTP request and returns response body.
// The request duration is governed by the request context: callers with a
// deadline (the fan-out path sizes it per command type) keep it; an expired
// deadline fails fast; a caller WITHOUT a deadline gets maxForwardTimeout so a
// hung remote can never park the goroutine forever (sharedHTTPClient itself has
// no client-level timeout by design — see its definition).
func (h *Client) executeForwardRequest(req *http.Request, targetURL string) ([]byte, error) {
	if deadline, ok := req.Context().Deadline(); ok {
		timeLeft := time.Until(deadline)
		h.logger.Debugf("Context deadline check: %v remaining", timeLeft)
		if timeLeft <= 0 {
			return nil, fmt.Errorf("forward to %s aborted: caller context already expired", targetURL)
		}
	} else {
		boundedCtx, cancel := context.WithTimeout(req.Context(), maxForwardTimeout)
		defer cancel()
		req = req.WithContext(boundedCtx)
	}

	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		h.logger.Errorf("HTTP forward request failed: %v", err)
		return nil, fmt.Errorf("failed to forward HTTP request to %s: %w", targetURL, err)
	}
	defer resp.Body.Close()

	h.logger.Infof("HTTP forward response received: Status=%d, ContentLength=%d", resp.StatusCode, resp.ContentLength)

	// Read response
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		h.logger.Errorf("Failed to read HTTP forward response: %v", err)
		return nil, fmt.Errorf("failed to read forward response: %w", err)
	}

	h.logger.Infof("HTTP forward response body size: %d bytes", len(responseBody))

	if resp.StatusCode != http.StatusOK {
		h.logger.Errorf("HTTP forward failed: Status=%d, Body=%s", resp.StatusCode, string(responseBody))
		return nil, fmt.Errorf("forward request failed with status %d: %s", resp.StatusCode, string(responseBody))
	}

	return responseBody, nil
}

func (h *Client) HandleSendCommand(ctx context.Context, op models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	// Generate unique request ID for debugging with cryptographically secure random component
	requestID := helper.GenerateSecureRequestID()

	h.logger.Debugf("Processing command %s for %d clients", op.GetType(), len(op.GetClients()))

	// Performance timing
	startTime := time.Now()
	defer func() {
		duration := time.Since(startTime)
		h.logger.WithFields(logger.Fields{
			"request_id": requestID,
			"duration":   duration,
		}).Debugf("=== HandleSendCommand END took %v ===", duration)
	}()

	// Check if this is a forwarded request to prevent infinite loops
	if requestDetails.IsForwarded {
		return h.executeDirectCommand(ctx, op, requestDetails)
	}

	// Check command authorization based on user role and command type
	commandType := op.GetType()
	subType := op.GetSubType()

	// Get service name and project from operation
	serviceName := op.GetCommandName()
	project := op.GetCommandProject()
	version := requestDetails.Version

	// Get path for PROXY commands (needed for readonly/write distinction)
	var commandPath string
	if commandType == "PROXY" {
		commandPath, _ = op.GetCommandPath()
	}

	// Check authorization
	if err := authorization.CheckCommandAuthorization(
		ctx,
		h.Context,
		requestDetails.User,
		commandType,
		subType,
		commandPath,
		serviceName,
		project,
		version,
	); err != nil {
		h.logger.Warnf("Command authorization failed for user %s (role: %s): %v",
			requestDetails.User.UserID, requestDetails.User.Role, err)
		return nil, err
	}

	h.logger.Debugf("Command authorization successful for user %s (role: %s) on command %s",
		requestDetails.User.UserID, requestDetails.User.Role, op.GetType())

	// Validate and prepare command processor
	processor, err := h.validateAndPrepareCommand(op)
	if err != nil {
		return nil, err
	}

	// Get or fetch clients
	clients := op.GetClients()

	if len(clients) == 0 {
		h.logger.Infof("No clients in payload, fetching from database...")
		fetchStart := time.Now()
		clients, err = h.FetchClients(op, requestDetails.Version)
		if err != nil {
			h.logger.Errorf("Failed to fetch clients: %v", err)
			return nil, ClientFetchError{Operation: op.GetType(), Cause: err}
		}
		h.logger.Infof("Client fetch took %v for %d clients", time.Since(fetchStart), len(clients))

		// Check if any clients were found after fetching from database - only for UPDATE_BOOTSTRAP
		if len(clients) == 0 && op.GetType() == "UPDATE_BOOTSTRAP" {
			return nil, fmt.Errorf("no clients found for service '%s' in project '%s' with version '%s'",
				op.GetCommandName(), op.GetCommandProject(), requestDetails.Version)
		}
	} else {
		h.logger.Infof("Using clients from payload, skipping database fetch")
	}

	h.logger.Infof("Processing %d clients for command", len(clients))

	// Use parallel processing for better performance
	processStart := time.Now()
	result, err := h.processClientsInParallel(ctx, clients, op, processor, requestDetails)
	if err != nil {
		return nil, err
	}

	processDuration := time.Since(processStart)
	if len(clients) > 0 {
		h.logger.Infof("Client processing took %v for %d clients (%v per client)",
			processDuration, len(clients), processDuration/time.Duration(len(clients)))
	} else {
		h.logger.Infof("Client processing took %v for %d clients (no clients found)",
			processDuration, len(clients))
	}

	h.logger.Infof("Command processing completed. Returning %d total responses", len(result))
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
		clients, err = h.FetchClients(op, requestDetails.Version)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch clients: %w", err)
		}

		// Check if any clients were found after fetching from database - only for UPDATE_BOOTSTRAP
		if len(clients) == 0 && op.GetType() == "UPDATE_BOOTSTRAP" {
			return nil, fmt.Errorf("no clients found for service '%s' in project '%s' with version '%s'",
				op.GetCommandName(), op.GetCommandProject(), requestDetails.Version)
		}
	}

	for _, client := range clients {
		processedPayload, err := processor.ValidateAndTransform(op, requestDetails, client)
		if err != nil {
			h.logger.Errorf("Validation failed for client %s: %v", client.ClientID, err)
			return nil, fmt.Errorf("command validation error for client %s: %w", client.ClientID, err)
		}

		// Direct send only (no routing) — under the per-client lock, so a
		// forwarded command can't interleave with this pod's own sends to the
		// same client stream (the local paths take the same lock). The closure
		// scopes a DEFERRED release: an explicit release after the call would
		// leak the mutex forever if the send panicked (gin recovery keeps the
		// process alive, and every later command to that client would then die
		// on the 15s lock timeout).
		response, err := func() (*pb.CommandResponse, error) {
			release, lerr := h.acquireClientLock(client.ClientID)
			if lerr != nil {
				return nil, lerr
			}
			defer release()
			return h.tryDirectSend(client.ClientID, op.GetTypeNum(), op.GetSubTypeNum(), processedPayload)
		}()
		if err != nil {
			h.logger.Errorf("Direct send failed for client %s: %v", client.ClientID, err)
			return nil, fmt.Errorf("client %s not found on this controller: %w", client.ClientID, err)
		}

		responser, exists := h.responser.GetResponser(op.GetType())
		if !exists {
			return nil, fmt.Errorf("unsupported responser command type: %s", op.GetType())
		}

		processedResponse := responser.ValidateAndTransform(op, response)
		result = append(result, processedResponse)
	}

	return result, nil
}

// sendCommandWithLocationCheck checks client location first, then processes accordingly
func (h *Client) sendCommandWithLocationCheck(ctx context.Context, requestDetails models.RequestDetails, client models.ServiceClients, op models.OperationClass, processor processor.CommandProcessor) (*pb.CommandResponse, error) {
	clientID := client.ClientID

	// Try local client first
	if h.Service.IsClientConnected(clientID) {
		processedPayload, err := processor.ValidateAndTransform(op, requestDetails, client)
		if err != nil {
			return nil, fmt.Errorf("command validation error for client %s: %w", clientID, err)
		}

		return h.Service.SendCommand(clientID, op.GetTypeNum(), op.GetSubTypeNum(), processedPayload)
	}

	// Strategy 2: Client not local, use registry + forwarding
	if h.registryClient == nil {
		return nil, fmt.Errorf("client %s not found and no registry available", clientID)
	}

	if !h.registryClient.IsConnected() {
		return nil, fmt.Errorf("client %s not found and registry not connected", clientID)
	}

	// Create timeout context for registry lookup
	registryCtx, registryCancel := context.WithTimeout(ctx, 10*time.Second)
	defer registryCancel()

	// Use a channel to make the registry call interruptible
	type registryResult struct {
		location *bridge.GetControllerClusterResponse
		err      error
	}

	resultChan := make(chan registryResult, 1)
	go func() {
		location, err := h.registryClient.GetClientLocation(clientID)
		resultChan <- registryResult{location: location, err: err}
	}()

	var clientLocation *bridge.GetControllerClusterResponse
	var err error

	select {
	case result := <-resultChan:
		clientLocation = result.location
		err = result.err
	case <-registryCtx.Done():
		h.logger.Errorf("Registry lookup timeout for client %s after 10s", clientID)
		return nil, fmt.Errorf("registry lookup timeout for client %s", clientID)
	}

	if err != nil {
		h.logger.Errorf("Failed to get client location from registry for client %s: %v", clientID, err)
		return nil, fmt.Errorf("failed to find client %s: %w", clientID, err)
	}

	h.logger.Infof("Registry returned location for client %s: controller=%s, found=%v", clientID, clientLocation.ControllerId, clientLocation.Found)

	// Get current controller ID
	currentControllerID := h.registryClient.GetControllerID()

	// If client is on this controller (somehow missed in direct check), try local processing
	if clientLocation.ControllerId == currentControllerID {
		h.logger.Debugf("Client %s is supposed to be local, processing locally", clientID)

		processedPayload, err := processor.ValidateAndTransform(op, requestDetails, client)
		if err != nil {
			return nil, fmt.Errorf("command validation error for supposedly local client %s: %w", clientID, err)
		}

		return h.Service.SendCommand(clientID, op.GetTypeNum(), op.GetSubTypeNum(), processedPayload)
	}

	// Client is on another controller, forward raw request (NO processing)
	h.logger.Infof("Forwarding raw request for client %s from %s to %s (no validate & transform)", clientID, currentControllerID, clientLocation.ControllerId)

	// Forward raw request via HTTP to target controller
	h.logger.Debugf("=== FORWARD DEBUG ===")
	h.logger.Debugf("Client: %s", clientID)
	h.logger.Debugf("From Controller: %s", currentControllerID)
	h.logger.Debugf("To Controller: %s", clientLocation.ControllerId)
	h.logger.Debugf("Original Body Size: %d bytes", len(requestDetails.OriginalBody))
	h.logger.Debugf("=====================")

	return h.forwardCommandViaHTTP(ctx, requestDetails, clientLocation.ControllerId, clientID, op.GetTypeNum(), op.GetSubTypeNum(), op, &client) // Pass op so a body can be synthesised when OriginalBody is empty (worker-originated commands)
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

// forwardCommandViaHTTP forwards command to another controller via HTTP with authentication.
// Note: This function always returns nil for *pb.CommandResponse because the raw HTTP response
// is returned via ForwardedResponse error type to preserve the original JSON without protobuf conversion.
// The caller should check for ForwardedResponse using errors.As() to handle the forwarded response.
//
//nolint:unparam // response is intentionally nil; raw JSON is returned via ForwardedResponse error
func (h *Client) forwardCommandViaHTTP(ctx context.Context, requestDetails models.RequestDetails, targetControllerID, clientID string, _ pb.CommandType, _ pb.SubCommandType, op models.OperationClass, clientInfo *models.ServiceClients) (*pb.CommandResponse, error) {
	// Build target URL
	targetURL := h.buildTargetURL(targetControllerID, requestDetails)

	h.logger.Debugf("=== HTTP FORWARD ===")
	h.logger.Debugf("Target Controller ID: %s", targetControllerID)
	h.logger.Debugf("Target URL: %s", targetURL)
	h.logger.Debugf("Client ID: %s", clientID)
	h.logger.Debugf("Has Original Body: %v", len(requestDetails.OriginalBody) > 0)
	h.logger.Debugf("Has Token: %v", requestDetails.Token != "")
	h.logger.Debugf("Is Forwarded: %v", requestDetails.IsForwarded)
	h.logger.Debugf("===================")

	// Filter original request body to only include the specific client.
	// User-triggered commands carry the original POST body in
	// requestDetails.OriginalBody. Worker-originated commands don't have a
	// gin context, so OriginalBody is empty — in that case synthesise the
	// body from the in-memory op so the target pod can decode it normally.
	originalBody := requestDetails.OriginalBody
	if len(originalBody) == 0 {
		if op == nil {
			return nil, fmt.Errorf("no original body and no operation available for client %s", clientID)
		}
		synthesised, err := json.Marshal(op)
		if err != nil {
			return nil, fmt.Errorf("synthesise forward body for client %s: %w", clientID, err)
		}
		originalBody = synthesised
		h.logger.Debugf("Synthesised forward body for client %s from in-memory op (%d bytes)", clientID, len(originalBody))
	}

	h.logger.Debugf("About to filter request body for client %s...", clientID)
	requestBody, err := h.filterRequestBodyForClient(originalBody, clientID, clientInfo)
	if err != nil {
		h.logger.Errorf("Failed to filter request body for client %s: %v", clientID, err)
		return nil, fmt.Errorf("failed to filter request body for client %s: %w", clientID, err)
	}
	h.logger.Debugf("Successfully filtered request body for client %s: original=%d bytes, filtered=%d bytes", clientID, len(originalBody), len(requestBody))

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
	h.logger.Debugf("Command successfully forwarded via HTTP to %s for client %s", targetURL, clientID)
	h.logger.Debugf("Returning raw forwarded response (%d bytes)", len(responseBody))

	return nil, &ForwardedResponse{
		RawJSON:    responseBody,
		StatusCode: http.StatusOK,
		ClientID:   clientID,
	}
}

// filterRequestBodyForClient filters the original request body to only include the specified client
// It preserves downstream_address from clientInfo if available
func (h *Client) filterRequestBodyForClient(originalBody []byte, targetClientID string, clientInfo *models.ServiceClients) ([]byte, error) {
	h.logger.Debugf("=== FILTER REQUEST BODY ===")
	h.logger.Debugf("Target Client ID: %s", targetClientID)
	h.logger.Debugf("Original Body Size: %d bytes", len(originalBody))

	// Parse as generic JSON to avoid type conversion issues
	var jsonData map[string]any
	if err := json.Unmarshal(originalBody, &jsonData); err != nil {
		h.logger.Errorf("Failed to parse original request body as JSON: %v", err)
		return nil, fmt.Errorf("failed to parse original request body as JSON: %w", err)
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
		clientsArray, ok := clients.([]any)
		if !ok {
			h.logger.Errorf("Clients field is not an array")
			return nil, fmt.Errorf("clients field is not an array")
		}

		if len(clientsArray) == 0 {
			shouldAddTargetClient = true
		}
	}

	if shouldAddTargetClient {
		h.logger.Debugf("Adding target client info for forwarding")

		// Create new client entry
		newClient := map[string]any{
			"client_id": targetClientID,
		}

		// Use downstream_address from clientInfo if available
		if clientInfo != nil && clientInfo.DownstreamAddress != "" {
			newClient["downstream_address"] = clientInfo.DownstreamAddress
			h.logger.Debugf("Using downstream_address from client info: %s", clientInfo.DownstreamAddress)
		} else {
			newClient["downstream_address"] = ""
			h.logger.Debugf("No downstream_address available in client info, using empty string")
		}

		// Add the target client to the JSON
		jsonData["clients"] = []map[string]any{newClient}

		// Marshal back to JSON
		updatedBody, err := json.Marshal(jsonData)
		if err != nil {
			h.logger.Errorf("Failed to marshal updated request body: %v", err)
			return nil, fmt.Errorf("failed to marshal updated request body: %w", err)
		}

		h.logger.Debugf("Created filtered body with target client: %d bytes", len(updatedBody))
		return updatedBody, nil
	}

	// If clients array exists, filter it efficiently
	clientsArray := clients.([]any)
	h.logger.Debugf("Original request contains %d clients", len(clientsArray))

	// Pre-allocate slice for better performance
	filteredClients := make([]map[string]any, 0, 1) // Usually just one client

	// Filter clients to only include the target client
	for _, client := range clientsArray {
		clientMap, ok := client.(map[string]any)
		if !ok {
			h.logger.Errorf("Unexpected client format in original request body")
			return nil, fmt.Errorf("unexpected client format in original request body")
		}

		if clientMap["client_id"] == targetClientID {
			filteredClients = append(filteredClients, clientMap)
			h.logger.Debugf("Including client %s in filtered request", clientMap["client_id"])
			break // Found target client, no need to continue
		} else {
			h.logger.Debugf("Excluding client %s from filtered request", clientMap["client_id"])
		}
	}

	if len(filteredClients) == 0 {
		h.logger.Errorf("Target client %s not found in original request", targetClientID)
		return nil, fmt.Errorf("target client %s not found in original request", targetClientID)
	}

	// Create new operations struct with filtered clients
	jsonData["clients"] = filteredClients

	h.logger.Debugf("Filtered request contains %d clients (target: %s)", len(filteredClients), targetClientID)

	// Marshal back to JSON
	filteredBody, err := json.Marshal(jsonData)
	if err != nil {
		h.logger.Errorf("Failed to marshal filtered request: %v", err)
		return nil, fmt.Errorf("failed to marshal filtered request: %w", err)
	}

	h.logger.Debugf("Filtered Body Size: %d bytes", len(filteredBody))
	h.logger.Debugf("==============================")
	return filteredBody, nil
}

// tryDirectSend attempts to send command directly to local client
func (h *Client) tryDirectSend(clientID string, cmdType pb.CommandType, subType pb.SubCommandType, payload any) (*pb.CommandResponse, error) {
	if h.Service.IsClientConnected(clientID) {
		return h.Service.SendCommand(clientID, cmdType, subType, payload)
	}
	return nil, fmt.Errorf("client not connected to this controller")
}

func (h *Client) FetchClients(op models.OperationClass, version string) ([]models.ServiceClients, error) {
	collection := h.Context.Client.Collection("services")
	filter := bson.M{
		"name":    op.GetCommandName(),
		"project": op.GetCommandProject(),
		"version": version,
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

// acquireClientLock serializes command sends to one client: concurrent stream.Send
// calls on the same gRPC server stream are unsafe, and unserialized sends also
// reorder config pushes. Returns a release func on success; errors out after a
// bounded wait so a stuck holder can't park callers forever (the eventual
// acquisition is then released by a watcher goroutine, never leaked).
func (h *Client) acquireClientLock(clientID string) (func(), error) {
	mutex := h.getClientMutex(clientID)

	// Try to acquire lock with timeout to prevent deadlocks
	mutexCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	locked := false
	done := make(chan struct{})
	go func() {
		mutex.Lock()
		locked = true
		close(done)
	}()

	select {
	case <-done:
		// Successfully acquired lock
		return mutex.Unlock, nil
	case <-mutexCtx.Done():
		// Timeout occurred
		// CRITICAL: If goroutine eventually acquires lock after timeout, we must unlock it
		// Otherwise mutex stays locked forever and client is stuck
		go func() {
			<-done // Wait for goroutine to finish (it will eventually acquire lock)
			if locked {
				mutex.Unlock() // Unlock the orphaned lock
				h.logger.Warnf("🔓 Released orphaned mutex for client %s after timeout", clientID)
			}
		}()
		return nil, fmt.Errorf("mutex timeout for %s", clientID)
	}
}

// processClientWithSafeguards processes a single client with serialization and health checks
func (h *Client) processClientWithSafeguards(ctx context.Context, requestDetails models.RequestDetails, client models.ServiceClients, op models.OperationClass, processor processor.CommandProcessor) (any, error) {
	// Client-level serialization to prevent "SendHeader called multiple times"
	release, err := h.acquireClientLock(client.ClientID)
	if err != nil {
		return nil, err
	}
	defer release()

	// Send command with existing logic (handles both local and remote)
	return h.sendCommandWithLocationCheck(ctx, requestDetails, client, op, processor)
}
