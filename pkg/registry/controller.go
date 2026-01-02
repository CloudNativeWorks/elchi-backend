package registry

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/version"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type RegistryClient struct {
	conn             *grpc.ClientConn
	controllerClient pb.ControllerRoutingServiceClient
	controllerID     string
	version          string
	grpcAddress      string
	registryAddr     string
	logger           *logger.Logger

	// Connection state tracking
	connectionState int32 // 0: disconnected, 1: connecting, 2: connected
	connectionMutex sync.RWMutex

	// Reconnect control
	reconnectEnabled bool
	reconnectMutex   sync.RWMutex
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup

	// Registration tracking
	isRegistered bool
	regMutex     sync.RWMutex
}

type Config struct {
	RegistryAddress string
	Version         string
}

// Connection states
const (
	ControllerStateDisconnected = iota
	ControllerStateConnecting
	ControllerStateConnected
)

// NewRegistryClient creates a new registry client with default config
func NewRegistryClient(registryAddress string, logger *logger.Logger, appConfig *config.AppConfig) (*RegistryClient, error) {
	return NewRegistryClientWithConfig(&Config{
		RegistryAddress: registryAddress,
		Version:         version.GetVersion(),
	}, logger, appConfig)
}

// NewRegistryClientWithConfig creates a new registry client with custom config
func NewRegistryClientWithConfig(config *Config, logger *logger.Logger, appConfig *config.AppConfig) (*RegistryClient, error) {
	// Auto-detect controller ID from hostname
	controllerID, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("failed to get hostname: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	client := &RegistryClient{
		controllerID:     controllerID,
		version:          config.Version,
		grpcAddress:      buildGRPCAddress(controllerID, appConfig.ElchiNamespace),
		registryAddr:     config.RegistryAddress,
		logger:           logger,
		connectionState:  ControllerStateDisconnected,
		reconnectEnabled: true,
		ctx:              ctx,
		cancel:           cancel,
		isRegistered:     false,
	}

	return client, nil
}

// buildGRPCAddress builds gRPC address for Kubernetes StatefulSet environment
func buildGRPCAddress(hostname string, namespace string) string {
	if ksp := os.Getenv("KUBERNETES_SERVICE_PORT"); ksp != "" {
		// Add headless service address to hostname
		return helper.ToK8sServiceName(hostname, namespace)
	}

	return fmt.Sprintf("%s:8099", hostname)
}

// Connect establishes gRPC connection to registry
func (r *RegistryClient) Connect() error {
	// Use shared gRPC dial options for consistency
	conn, err := grpc.NewClient(r.registryAddr, GetDefaultGRPCDialOptions()...)
	if err != nil {
		return fmt.Errorf("failed to create connection: %v", err)
	}

	r.conn = conn
	r.controllerClient = pb.NewControllerRoutingServiceClient(conn)

	r.setConnectionState(ControllerStateConnected)
	return nil
}

// Disconnect closes the gRPC connection
func (r *RegistryClient) Disconnect() error {
	if r.conn != nil {
		return r.conn.Close()
	}
	return nil
}

// Connection state management
func (r *RegistryClient) getConnectionState() int32 {
	r.connectionMutex.RLock()
	defer r.connectionMutex.RUnlock()
	return r.connectionState
}

func (r *RegistryClient) setConnectionState(state int32) bool {
	r.connectionMutex.Lock()
	defer r.connectionMutex.Unlock()

	// Prevent multiple connecting states
	if state == ControllerStateConnecting && r.connectionState == ControllerStateConnecting {
		return false
	}

	r.connectionState = state
	return true
}

// Reconnect control
func (r *RegistryClient) getReconnectEnabled() bool {
	r.reconnectMutex.RLock()
	defer r.reconnectMutex.RUnlock()
	return r.reconnectEnabled
}

func (r *RegistryClient) setReconnectEnabled(enabled bool) {
	r.reconnectMutex.Lock()
	defer r.reconnectMutex.Unlock()
	r.reconnectEnabled = enabled
}

// Registration state management
func (r *RegistryClient) isRegisteredState() bool {
	r.regMutex.RLock()
	defer r.regMutex.RUnlock()
	return r.isRegistered
}

func (r *RegistryClient) setRegistered(registered bool) {
	r.regMutex.Lock()
	defer r.regMutex.Unlock()
	r.isRegistered = registered
}

// RegisterController registers this controller with registry
func (r *RegistryClient) RegisterController() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := &pb.RegisterControllerRequest{
		ControllerId: r.controllerID,
		Version:      r.version,
		Timestamp:    timestamppb.New(time.Now()),
	}

	resp, err := r.controllerClient.RegisterController(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to register controller: %v", err)
	}

	if !resp.Success {
		return fmt.Errorf("controller registration failed: %s", resp.Message)
	}

	r.setRegistered(true)
	r.logger.Infof("Controller registered: ID=%s, Version=%s, Address=%s", r.controllerID, r.version, r.grpcAddress)
	return nil
}

// NotifyClientConnected notifies registry about client connection
func (r *RegistryClient) NotifyClientConnected(clientID string) error {
	// Check connection state
	if r.getConnectionState() != ControllerStateConnected {
		r.logger.Warnf("Not connected to registry, skipping client connected notification for: %s", clientID)
		return fmt.Errorf("not connected to registry")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req := &pb.NotifyClientConnectedRequest{
		ControllerId: r.controllerID,
		ClientId:     clientID,
		Version:      r.version,
		Timestamp:    timestamppb.New(time.Now()),
	}

	resp, err := r.controllerClient.NotifyClientConnected(ctx, req)
	if err != nil {
		// Mark as disconnected on error to trigger reconnection
		r.setConnectionState(ControllerStateDisconnected)
		return fmt.Errorf("failed to notify client connected: %v", err)
	}

	if !resp.Success {
		return fmt.Errorf("notify client connected failed: %s", resp.Message)
	}

	r.logger.Debugf("Client connected notification sent: %s -> %s (version: %s)", clientID, r.controllerID, r.version)
	return nil
}

// NotifyClientDisconnected notifies registry about client disconnection
func (r *RegistryClient) NotifyClientDisconnected(clientID string) error {
	// Check connection state
	if r.getConnectionState() != ControllerStateConnected {
		r.logger.Warnf("Not connected to registry, skipping client disconnected notification for: %s", clientID)
		return fmt.Errorf("not connected to registry")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req := &pb.NotifyClientDisconnectedRequest{
		ControllerId: r.controllerID,
		ClientId:     clientID,
		Version:      r.version,
		Timestamp:    timestamppb.New(time.Now()),
	}

	resp, err := r.controllerClient.NotifyClientDisconnected(ctx, req)
	if err != nil {
		// Mark as disconnected on error to trigger reconnection
		r.setConnectionState(ControllerStateDisconnected)
		return fmt.Errorf("failed to notify client disconnected: %v", err)
	}

	if !resp.Success {
		return fmt.Errorf("notify client disconnected failed: %s", resp.Message)
	}

	r.logger.Debugf("Client disconnected notification sent: %s -> %s (version: %s)", clientID, r.controllerID, r.version)
	return nil
}

// UpdateClientList reports all currently connected clients
func (r *RegistryClient) UpdateClientList(clientIDs []string) error {
	r.logger.Debugf("Updating client list for controller %s: %d clients", r.controllerID, len(clientIDs))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Convert to ClientInfo array
	var clients []*pb.ClientInfo
	for _, clientID := range clientIDs {
		clients = append(clients, &pb.ClientInfo{
			ClientId: clientID,
			Version:  r.version,
			LastSeen: timestamppb.New(time.Now()),
		})
	}

	req := &pb.UpdateClientListRequest{
		ControllerId: r.controllerID,
		Clients:      clients,
		Timestamp:    timestamppb.New(time.Now()),
	}

	resp, err := r.controllerClient.UpdateClientList(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to update client list: %v", err)
	}

	if !resp.Success {
		return fmt.Errorf("update client list failed: %s", resp.Message)
	}

	r.logger.Debugf("Successfully updated client list: %d clients processed", resp.UpdatedCount)
	return nil
}

// GetClientLocation finds which controller a client is connected to
func (r *RegistryClient) GetClientLocation(clientID string) (*pb.GetControllerClusterResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r.logger.Infof("Getting client location from registry: clientID=%s (searching all versions)", clientID)

	req := &pb.GetControllerClusterRequest{
		ClientId:  clientID,
		Version:   "", // Empty version to search across all versions
		Timestamp: timestamppb.New(time.Now()),
	}

	resp, err := r.controllerClient.GetControllerCluster(ctx, req)
	if err != nil {
		r.logger.Errorf("Registry GetControllerCluster failed: %v", err)
		return nil, fmt.Errorf("failed to get client location: %v", err)
	}

	r.logger.Infof("Registry response: Found=%v, ControllerId=%s", resp.Found, resp.ControllerId)

	if !resp.Found {
		return nil, fmt.Errorf("client location not found: %s (searched all versions)", clientID)
	}

	return resp, nil
}

// GetRegistryData returns all registry data via gRPC
func (r *RegistryClient) GetRegistryData(ctx context.Context) (map[string]interface{}, error) {
	if r.conn == nil {
		return nil, fmt.Errorf("registry connection not available")
	}

	// Get controller data from controller routing service
	controllerClient := pb.NewControllerRoutingServiceClient(r.conn)

	// Get control plane data from control plane routing service
	controlPlaneClient := pb.NewEnvoyRoutingServiceClient(r.conn)

	// Get all controller registry data
	controllerDataReq := &pb.GetAllControllerRegistryDataRequest{}
	controllerResp, err := controllerClient.GetAllRegistryData(ctx, controllerDataReq)
	if err != nil {
		r.logger.Errorf("Failed to get controller registry data: %v", err)
		return nil, fmt.Errorf("failed to get controller registry data: %v", err)
	}

	// Get all control plane registry data
	controlPlaneDataReq := &pb.GetAllRegistryDataRequest{}
	controlPlaneResp, err := controlPlaneClient.GetAllRegistryData(ctx, controlPlaneDataReq)
	if err != nil {
		r.logger.Errorf("Failed to get control plane registry data: %v", err)
		return nil, fmt.Errorf("failed to get control plane registry data: %v", err)
	}

	// Combine all data
	registryData := map[string]interface{}{
		"message": "Full registry data retrieved successfully",
		"status":  "connected",
		"controller_data": map[string]interface{}{
			"controllers":           controllerResp.Data.Controllers,
			"clients_by_controller": controllerResp.Data.ClientsByController,
		},
		"control_plane_data": map[string]interface{}{
			"control_planes":         controlPlaneResp.Data.ControlPlanes,
			"nodes_by_control_plane": controlPlaneResp.Data.NodesByControlPlane,
		},
		"client_info": map[string]interface{}{
			"controller_id": r.controllerID,
			"version":       r.version,
			"grpc_address":  r.grpcAddress,
		},
		"registry_address": r.registryAddr,
		"timestamp":        time.Now().UTC(),
	}

	r.logger.Infof("Retrieved full registry data: %d controllers, %d control planes",
		len(controllerResp.Data.Controllers),
		len(controlPlaneResp.Data.ControlPlanes))

	return registryData, nil
}

// GetControllerID returns the controller ID
func (r *RegistryClient) GetControllerID() string {
	r.logger.Debugf("Getting controller ID: %s", r.controllerID)
	return r.controllerID
}

// DeleteController deletes a controller from registry
func (r *RegistryClient) DeleteController(controllerID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := &pb.DeleteControllerRequest{
		ControllerId: controllerID,
	}

	resp, err := r.controllerClient.DeleteController(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to call DeleteController: %w", err)
	}

	if !resp.GetSuccess() {
		return fmt.Errorf("registry delete controller failed: %s", resp.GetMessage())
	}

	r.logger.Infof("Controller %s deleted from registry successfully", controllerID)
	return nil
}

// DeleteControlPlane deletes a control plane from registry
func (r *RegistryClient) DeleteControlPlane(controlPlaneID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Use control-plane routing service client
	controlPlaneClient := pb.NewEnvoyRoutingServiceClient(r.conn)

	req := &pb.DeleteControlPlaneRequest{
		ControlPlaneId: controlPlaneID,
	}

	resp, err := controlPlaneClient.DeleteControlPlane(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to call DeleteControlPlane: %w", err)
	}

	if !resp.GetSuccess() {
		return fmt.Errorf("registry delete control plane failed: %s", resp.GetMessage())
	}

	r.logger.Infof("Control plane %s deleted from registry successfully", controlPlaneID)
	return nil
}

// IsConnected checks if the gRPC connection is established
func (r *RegistryClient) IsConnected() bool {
	return r.conn != nil && r.controllerClient != nil
}

// StartHealthMonitor starts enhanced health monitoring with continuous reconnect capability
func (r *RegistryClient) StartHealthMonitor(getConnectedClients func() []string) {
	// Start continuous reconnect loop
	r.wg.Add(1)
	go r.continuousReconnectLoop()

	// Start client list update loop
	r.wg.Add(1)
	go r.clientListUpdateLoop(getConnectedClients)

	r.logger.Infof("✅ Enhanced health monitor started with continuous reconnect capability")
}

// continuousReconnectLoop provides continuous reconnection capability
func (r *RegistryClient) continuousReconnectLoop() {
	defer r.wg.Done()

	r.logger.Infof("🔄 Controller continuous reconnect loop started")
	ticker := time.NewTicker(15 * time.Second) // Check every 15 seconds
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			r.logger.Infof("🔄 Controller continuous reconnect loop terminated")
			return
		case <-ticker.C:
			if !r.getReconnectEnabled() {
				continue
			}

			state := r.getConnectionState()
			if state == ControllerStateDisconnected {
				r.logger.Infof("🔄 Detected disconnected state, attempting reconnection...")
				go r.attemptReconnection()
			}
		}
	}
}

// attemptReconnection performs reconnection attempt
func (r *RegistryClient) attemptReconnection() {
	if !r.setConnectionState(ControllerStateConnecting) {
		// Already connecting
		return
	}

	r.logger.Infof("🔄 Starting controller reconnection attempt...")

	if err := r.ConnectAndRegister(); err != nil {
		r.logger.Errorf("🔄 Controller reconnection failed: %v", err)
		r.setConnectionState(ControllerStateDisconnected)
	}
}

// ConnectAndRegister performs connection and registration with extended timeout
func (r *RegistryClient) ConnectAndRegister() error {
	// Extended timeout for better reliability
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	r.logger.Infof("🔗 Attempting to connect controller to registry at %s...", r.registryAddr)

	// Disconnect first if needed
	if err := r.Disconnect(); err != nil {
		r.logger.Warnf("Error during disconnect: %v", err)
	}

	// Connect with retry - now includes real connectivity test
	if err := r.ConnectWithRetry(ctx); err != nil {
		r.logger.Errorf("❌ Controller registry connection failed: %v", err)
		return fmt.Errorf("failed to connect to registry: %v", err)
	}

	r.logger.Infof("✅ Controller registry connection established successfully")

	// Register with retry
	if err := r.RegisterControllerWithRetry(ctx); err != nil {
		r.logger.Errorf("❌ Controller registration failed: %v", err)
		return fmt.Errorf("failed to register controller: %v", err)
	}

	r.logger.Infof("✅ Controller registered successfully with registry")
	return nil
}

// clientListUpdateLoop handles periodic client list updates
func (r *RegistryClient) clientListUpdateLoop(getConnectedClients func() []string) {
	defer r.wg.Done()

	r.logger.Infof("🔍 Controller client list update loop started")
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			r.logger.Infof("🔍 Controller client list update loop terminated")
			return
		case <-ticker.C:
			// Check if we're connected and registered
			if r.getConnectionState() != ControllerStateConnected {
				continue
			}

			if !r.isRegisteredState() {
				r.logger.Debugf("Controller not registered yet - skipping client list update")
				continue
			}

			// Get currently connected clients
			connectedClients := getConnectedClients()
			r.logger.Debugf("Periodic client list update: %d connected clients", len(connectedClients))

			// Always send client list (even if empty) to indicate controller is alive
			if err := r.UpdateClientList(connectedClients); err != nil {
				r.logger.Errorf("Failed to update client list: %v", err)
				r.handleConnectionFailure("client list update")
			} else {
				r.logger.Debugf("✅ Client list update completed successfully: %d clients", len(connectedClients))
			}
		}
	}
}

// handleConnectionFailure handles connection failures by marking disconnected
func (r *RegistryClient) handleConnectionFailure(operation string) {
	r.logger.Warnf("Connection failure detected during %s - marking as disconnected", operation)

	// Reset states
	r.setConnectionState(ControllerStateDisconnected)
	r.setRegistered(false)

	// The continuous reconnect loop will handle reconnection
}

// Stop stops the health monitor gracefully
func (r *RegistryClient) Stop() {
	r.setReconnectEnabled(false)
	r.cancel()
	r.wg.Wait()
	if err := r.Disconnect(); err != nil {
		r.logger.Errorf("Failed to disconnect from registry: %v", err)
	}
	r.logger.Infof("Controller registry client stopped")
}

// ConnectWithRetry establishes gRPC connection to registry with retry logic
func (r *RegistryClient) ConnectWithRetry(ctx context.Context) error {
	return RetryWithBackoff(ctx, "connect to registry", AggressiveRetryConfig(), r.logger, func() error {
		return r.Connect()
	})
}

// RegisterControllerWithRetry registers controller with retry logic
func (r *RegistryClient) RegisterControllerWithRetry(ctx context.Context) error {
	return RetryWithBackoff(ctx, "register controller", AggressiveRetryConfig(), r.logger, func() error {
		return r.RegisterController()
	})
}

// SyncAllClientsWithRegistry syncs all existing clients with registry
func (r *RegistryClient) SyncAllClientsWithRegistry(ctx context.Context, getAllClients func() []string) error {
	clients := getAllClients()
	if len(clients) == 0 {
		r.logger.Infof("No clients to sync with registry")
		return nil
	}

	r.logger.Infof("Starting sync of %d existing clients with registry", len(clients))

	// Log each client to be synced
	for i, clientID := range clients {
		r.logger.Infof("Client %d/%d: %s (version: %s)", i+1, len(clients), clientID, r.version)
	}

	// First, update the client list
	if err := r.UpdateClientList(clients); err != nil {
		r.logger.Errorf("Failed to update client list during sync: %v", err)
		return err
	}

	// Then, notify about each client connection
	successCount := 0
	for i, clientID := range clients {
		r.logger.Infof("Syncing client %d/%d: %s", i+1, len(clients), clientID)

		if err := r.NotifyClientConnected(clientID); err != nil {
			r.logger.Errorf("Failed to sync client notification for client %s: %v", clientID, err)
		} else {
			r.logger.Infof("Successfully synced client: %s", clientID)
			successCount++
		}
	}

	r.logger.Infof("Sync completed: %d/%d clients successfully synced with registry", successCount, len(clients))
	return nil
}
