package registry

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type RegistryClient struct {
	conn         *grpc.ClientConn
	client       pb.ControllerServiceClient
	controllerID string
	grpcAddress  string
	registryAddr string
	logger       *logger.Logger
}

type Config struct {
	RegistryAddress string
	ControllerID    string 
	GRPCAddress     string
}

// NewRegistryClient creates a new registry client
func NewRegistryClient(config Config, logger *logger.Logger) (*RegistryClient, error) {
	// Auto-detect controller ID from hostname if not provided
	controllerID := config.ControllerID
	if controllerID == "" {
		hostname, err := os.Hostname()
		if err != nil {
			return nil, fmt.Errorf("failed to get hostname: %v", err)
		}
		controllerID = hostname
	}

	// Auto-detect gRPC address from hostname if not provided
	grpcAddress := config.GRPCAddress
	if grpcAddress == "" {
		hostname, err := os.Hostname()
		if err != nil {
			return nil, fmt.Errorf("failed to get hostname: %v", err)
		}
		
		// Kubernetes StatefulSet ortamında FQDN oluştur
		grpcAddress = buildGRPCAddress(hostname)
	}

	client := &RegistryClient{
		controllerID: controllerID,
		grpcAddress:  grpcAddress,
		registryAddr: config.RegistryAddress,
		logger:       logger,
	}

	return client, nil
}

// buildGRPCAddress builds gRPC address for Kubernetes StatefulSet environment
func buildGRPCAddress(hostname string) string {
	// Kubernetes environment variables'ını kontrol et
	if namespace := os.Getenv("POD_NAMESPACE"); namespace != "" {
		if serviceName := os.Getenv("CONTROLLER_SERVICE_NAME"); serviceName != "" {
			// Full FQDN: hostname.service.namespace.svc.cluster.local:8080
			return fmt.Sprintf("%s.%s.%s.svc.cluster.local:50051", hostname, serviceName, namespace)
		}
		// Service name yoksa sadece namespace ile: hostname.namespace.svc.cluster.local:8080
		return fmt.Sprintf("%s.%s.svc.cluster.local:50051", hostname, namespace)
	}
	
	// Local development ortamında hostname:port
	return fmt.Sprintf("%s:50051", hostname)
}

// Connect establishes gRPC connection to registry
func (r *RegistryClient) Connect() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, r.registryAddr, 
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return fmt.Errorf("failed to connect to registry: %v", err)
	}

	r.conn = conn
	r.client = pb.NewControllerServiceClient(conn)
	
	r.logger.Infof("Connected to registry at %s", r.registryAddr)
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

	req := &pb.ControllerInfo{
		ControllerId: r.controllerID,
		GrpcAddress:  r.grpcAddress,
	}

	resp, err := r.client.RegisterController(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to register controller: %v", err)
	}

	if resp.Success == "" || resp.Success == "false" {
		return fmt.Errorf("controller registration failed")
	}

	r.logger.Infof("Controller registered: ID=%s, Address=%s", r.controllerID, r.grpcAddress)
	return nil
}

// SetClientLocation notifies registry about client connection
func (r *RegistryClient) SetClientLocation(clientID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req := &pb.SetClientLocationRequest{
		ClientId:     clientID,
		ControllerId: r.controllerID,
	}

	resp, err := r.client.SetClientLocation(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to set client location: %v", err)
	}

	if resp.Success == "" || resp.Success == "false" {
		return fmt.Errorf("set client location failed")
	}

	r.logger.Debugf("Client location updated: %s -> %s", clientID, r.controllerID)
	return nil
}

// GetClientDetails gets the controller ID for a client
func (r *RegistryClient) GetClientDetails(clientID string) (*pb.ClientLocationResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req := &pb.ClientLocationRequest{
		ClientId: clientID,
	}

	resp, err := r.client.GetClientLocation(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to get client location: %v", err)
	}

	if !resp.Found {
		return nil, fmt.Errorf("client not found: %s", clientID)
	}

	return resp, nil
}

// RequestClientRefresh asks registry to refresh client locations (after registry restart)
func (r *RegistryClient) RequestClientRefresh() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := &pb.ClientRefreshRequest{
		ControllerId: r.controllerID,
	}

	resp, err := r.client.RequestClientRefresh(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to request client refresh: %v", err)
	}

	if resp.Success == "" || resp.Success == "false" {
		return fmt.Errorf("client refresh request failed")
	}

	r.logger.Infof("Client refresh requested successfully")
	return nil
}

// IsControllerRegistered checks if this controller is registered in registry
func (r *RegistryClient) IsControllerRegistered() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req := &pb.IsControllerRegisteredRequest{
		ControllerId: r.controllerID,
	}

	resp, err := r.client.IsControllerRegistered(ctx, req)
	if err != nil {
		return false, fmt.Errorf("failed to check controller registration: %v", err)
	}

	return resp.Registered, nil
}

// BulkSetClientLocations sets multiple client locations efficiently
func (r *RegistryClient) BulkSetClientLocations(clientIDs []string) error {
	if len(clientIDs) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req := &pb.BulkSetClientLocationsRequest{
		ControllerId: r.controllerID,
		ClientIds:    clientIDs,
	}

	resp, err := r.client.BulkSetClientLocations(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to bulk set client locations: %v", err)
	}

	if !resp.Success {
		return fmt.Errorf("bulk set client locations failed: %s", resp.Error)
	}

	r.logger.Infof("Bulk client locations updated: %d/%d clients", resp.UpdatedCount, len(clientIDs))
	return nil
}

// ReportConnectedClients reports all currently connected clients to registry
func (r *RegistryClient) ReportConnectedClients(clientIDs []string) error {
	// Use efficient bulk operation instead of one-by-one
	return r.BulkSetClientLocations(clientIDs)
}

// GetControllerID returns the controller ID
func (r *RegistryClient) GetControllerID() string {
	return r.controllerID
}

// GetGRPCAddress returns the gRPC address
func (r *RegistryClient) GetGRPCAddress() string {
	return r.grpcAddress
}

// StartHealthMonitor starts periodic health monitoring for registry connection
func (r *RegistryClient) StartHealthMonitor(getConnectedClients func() []string) {
	ticker := time.NewTicker(30 * time.Second)
	go func() {
		defer ticker.Stop()
		for range ticker.C {
			// Get currently connected clients
			connectedClients := getConnectedClients()
			
			// Only monitor if we have connected clients
			if len(connectedClients) == 0 {
				continue
			}

			// Check if controller is registered
			registered, err := r.IsControllerRegistered()
			if err != nil {
				r.logger.Errorf("Failed to check controller registration: %v", err)
				continue
			}

			if !registered {
				r.logger.Warnf("Controller not registered in registry, attempting re-registration...")
				
				// Re-register controller
				if err := r.RegisterController(); err != nil {
					r.logger.Errorf("Failed to re-register controller: %v", err)
					continue
				}

				// Report all connected clients
				if err := r.BulkSetClientLocations(connectedClients); err != nil {
					r.logger.Errorf("Failed to report connected clients after re-registration: %v", err)
				} else {
					r.logger.Infof("Successfully recovered: registered controller and reported %d clients", len(connectedClients))
				}
			}
		}
	}()
	
	r.logger.Infof("Health monitor started - checking every 30 seconds")
} 