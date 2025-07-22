package registry

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
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
}

type Config struct {
	RegistryAddress string
	Version         string
}

// NewRegistryClient creates a new registry client with default config
func NewRegistryClient(registryAddress string, logger *logger.Logger) (*RegistryClient, error) {
	return NewRegistryClientWithConfig(&Config{
		RegistryAddress: registryAddress,
		Version:         "v1.0.0",
	}, logger)
}

// NewRegistryClientWithConfig creates a new registry client with custom config
func NewRegistryClientWithConfig(config *Config, logger *logger.Logger) (*RegistryClient, error) {
	// Auto-detect controller ID from hostname
	controllerID, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("failed to get hostname: %v", err)
	}

	client := &RegistryClient{
		controllerID: controllerID,
		version:      config.Version,
		grpcAddress:  buildGRPCAddress(controllerID),
		registryAddr: config.RegistryAddress,
		logger:       logger,
	}

	return client, nil
}

// buildGRPCAddress builds gRPC address for Kubernetes StatefulSet environment
func buildGRPCAddress(hostname string) string {
	if ksp := os.Getenv("KUBERNETES_SERVICE_PORT"); ksp != "" {
		// Add headless service address to hostname
		return helper.ToK8sServiceName(hostname, "elchi-stack")
	}

	return fmt.Sprintf("%s:8099", hostname)
}

// Connect establishes gRPC connection to registry
func (r *RegistryClient) Connect() error {
	conn, err := grpc.NewClient(r.registryAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDisableServiceConfig(),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: false,
		}),
		grpc.WithDefaultCallOptions(
			grpc.WaitForReady(true),
		),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay:  1.0 * time.Second,
				Multiplier: 1.6,
				Jitter:     0.2,
				MaxDelay:   10 * time.Second,
			},
		}),
	)
	if err != nil {
		return fmt.Errorf("failed to connect to registry: %v", err)
	}

	r.conn = conn
	r.controllerClient = pb.NewControllerRoutingServiceClient(conn)

	r.logger.Infof("Connected to registry at %s (Controller Routing API v2)", r.registryAddr)
	return nil
}

// Disconnect closes the gRPC connection
func (r *RegistryClient) Disconnect() error {
	if r.conn != nil {
		return r.conn.Close()
	}
	return nil
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

	r.logger.Infof("Controller registered: ID=%s, Version=%s, Address=%s", r.controllerID, r.version, r.grpcAddress)
	return nil
}

// NotifyClientConnected notifies registry about client connection
func (r *RegistryClient) NotifyClientConnected(clientID string) error {
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
	r.logger.Infof("Updating client list for controller %s: %d clients", r.controllerID, len(clientIDs))

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

	r.logger.Infof("Successfully updated client list: %d clients processed", resp.UpdatedCount)
	return nil
}

// GetClientLocation finds which controller a client is connected to
func (r *RegistryClient) GetClientLocation(clientID string) (*pb.GetControllerClusterResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := &pb.GetControllerClusterRequest{
		ClientId:  clientID,
		Version:   r.version,
		Timestamp: timestamppb.New(time.Now()),
	}

	resp, err := r.controllerClient.GetControllerCluster(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get client location: %v", err)
	}

	if !resp.Found {
		return nil, fmt.Errorf("client location not found: %s", clientID)
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
	return r.controllerID
}

// StartHealthMonitor starts periodic health monitoring for registry connection
func (r *RegistryClient) StartHealthMonitor(getConnectedClients func() []string) {
	interval := 30 * time.Second
	ticker := time.NewTicker(interval)

	go func() {
		defer ticker.Stop()
		for range ticker.C {
			// Get currently connected clients
			connectedClients := getConnectedClients()

			r.logger.Infof("Periodic client list update: %d connected clients", len(connectedClients))

			// Always send client list (even if empty) to indicate controller is alive
			if err := r.UpdateClientList(connectedClients); err != nil {
				r.logger.Errorf("Failed to update client list: %v", err)

				// Connection might be lost, try to reconnect and re-register
				r.logger.Infof("Attempting to reconnect and re-register due to health monitor failure")

				// Create context with timeout for reconnection
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)

				// Try to reconnect
				if err := r.Disconnect(); err != nil {
					r.logger.Warnf("Error disconnecting: %v", err)
				}

				if err := r.ConnectWithRetry(ctx); err != nil {
					r.logger.Errorf("Failed to reconnect to registry: %v", err)
					cancel()
					continue
				}

				// Try to re-register
				if err := r.RegisterControllerWithRetry(ctx); err != nil {
					r.logger.Errorf("Failed to re-register controller: %v", err)
				} else {
					r.logger.Infof("Successfully reconnected and re-registered controller")

					// Sync existing clients after reconnection
					r.logger.Infof("Starting client sync after reconnection...")
					if err := r.SyncAllClientsWithRegistry(ctx, getConnectedClients); err != nil {
						r.logger.Errorf("Failed to sync existing clients after reconnection: %v", err)
					} else {
						r.logger.Infof("Client sync completed successfully after reconnection")
					}
				}

				cancel()
			} else {
				r.logger.Debugf("Periodic client list update completed successfully")
			}
		}
	}()

	r.logger.Infof("Health monitor started - checking every %v (Controller Routing API v2)", interval)
}

// ConnectWithRetry establishes gRPC connection to registry with retry logic
func (r *RegistryClient) ConnectWithRetry(ctx context.Context) error {
	backoff := 1 * time.Second
	maxBackoff := 30 * time.Second
	attempt := 1

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := r.Connect()
		if err == nil {
			r.logger.Infof("Successfully connected to registry at %s after %d attempts", r.registryAddr, attempt)
			return nil
		}

		r.logger.Warnf("Failed to connect to registry (attempt %d): %v", attempt, err)

		// Wait before retry
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		// Exponential backoff with jitter
		backoff = time.Duration(float64(backoff) * 1.5)
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
		attempt++
	}
}

// RegisterControllerWithRetry registers controller with retry logic
func (r *RegistryClient) RegisterControllerWithRetry(ctx context.Context) error {
	backoff := 1 * time.Second
	maxBackoff := 30 * time.Second
	attempt := 1

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := r.RegisterController()
		if err == nil {
			r.logger.Infof("Successfully registered controller after %d attempts", attempt)
			return nil
		}

		r.logger.Warnf("Failed to register controller (attempt %d): %v", attempt, err)

		// Wait before retry
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		// Exponential backoff with jitter
		backoff = time.Duration(float64(backoff) * 1.5)
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
		attempt++
	}
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
