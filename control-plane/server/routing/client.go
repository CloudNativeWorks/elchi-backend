package routing

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
)

type RoutingClient struct {
	conn         *grpc.ClientConn
	client       pb.EnvoyRoutingServiceClient
	registryAddr string
	logger       *logger.Logger
}

type Config struct {
	RegistryAddress string
	ControlPlaneID  string
	Version         string
}

// NewConfig creates a new routing config with auto-detected values
func NewConfig(registryAddress, envoyVersion string) *Config {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}

	// Clean hostname (remove domain parts)
	hostname = strings.Split(hostname, ".")[0]

	return &Config{
		RegistryAddress: registryAddress,
		ControlPlaneID:  hostname,
		Version:         envoyVersion,
	}
}

// NewRoutingClient creates a new routing client
func NewRoutingClient(config *Config, logger *logger.Logger) (*RoutingClient, error) {
	client := &RoutingClient{
		registryAddr: config.RegistryAddress,
		logger:       logger,
	}

	return client, nil
}

// Connect establishes gRPC connection to registry
func (r *RoutingClient) Connect() error {
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
	r.client = pb.NewEnvoyRoutingServiceClient(conn)

	r.logger.Infof("Connected to registry at %s", r.registryAddr)
	return nil
}

// Disconnect closes the gRPC connection
func (r *RoutingClient) Disconnect() error {
	if r.conn != nil {
		return r.conn.Close()
	}
	return nil
}

// RegisterControlPlane registers this control-plane with registry
func (r *RoutingClient) RegisterControlPlane(config *Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	request := &pb.RegisterControlPlaneRequest{
		ControlPlaneId: config.ControlPlaneID,
		Version:        config.Version,
		Timestamp:      timestamppb.New(time.Now().UTC()),
	}

	response, err := r.client.RegisterControlPlane(ctx, request)
	if err != nil {
		return fmt.Errorf("failed to register control-plane: %v", err)
	}

	if !response.Success {
		return fmt.Errorf("registration failed: %s", response.Message)
	}

	r.logger.Infof("Successfully registered control-plane: %s", config.ControlPlaneID)
	return nil
}

// NotifySnapshotDelivered notifies registry about snapshot delivery
func (r *RoutingClient) NotifySnapshotDelivered(controlPlaneID, nodeID, version string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	request := &pb.NotifySnapshotDeliveredRequest{
		ControlPlaneId: controlPlaneID,
		NodeId:         nodeID,
		Version:        version,
		Timestamp:      timestamppb.New(time.Now().UTC()),
	}

	response, err := r.client.NotifySnapshotDelivered(ctx, request)
	if err != nil {
		return fmt.Errorf("failed to notify snapshot delivery: %v", err)
	}

	if !response.Success {
		return fmt.Errorf("notification failed: %s", response.Message)
	}

	r.logger.Debugf("Snapshot delivery notified for node: %s", nodeID)
	return nil
}

// UpdateNodeList updates the list of connected nodes
func (r *RoutingClient) UpdateNodeList(controlPlaneID string, nodes []NodeInfo) error {
	r.logger.Infof("Updating node list for control-plane %s: %d nodes", controlPlaneID, len(nodes))
	
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var pbNodes []*pb.NodeInfo
	for _, node := range nodes {
		pbNodes = append(pbNodes, &pb.NodeInfo{
			NodeId:   node.NodeID,
			Version:  node.Version,
			LastSeen: timestamppb.New(node.LastSeen),
		})
	}

	request := &pb.UpdateNodeListRequest{
		ControlPlaneId: controlPlaneID,
		Nodes:          pbNodes,
		Timestamp:      timestamppb.New(time.Now().UTC()),
	}

	response, err := r.client.UpdateNodeList(ctx, request)
	if err != nil {
		return fmt.Errorf("failed to update node list: %v", err)
	}

	if !response.Success {
		return fmt.Errorf("node list update failed: %s", response.Message)
	}

	r.logger.Infof("Successfully updated node list: %d nodes processed", response.UpdatedCount)
	return nil
}

// HealthCheck performs health check with registry
func (r *RoutingClient) HealthCheck() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	request := &pb.HealthCheckRequest{
		Service: "control-plane",
	}

	response, err := r.client.HealthCheck(ctx, request)
	if err != nil {
		return fmt.Errorf("health check failed: %v", err)
	}

	if !response.Healthy {
		return fmt.Errorf("registry unhealthy: %s", response.Message)
	}

	r.logger.Debug("Health check passed")
	return nil
}

// ConnectWithRetry establishes gRPC connection to registry with retry logic
func (r *RoutingClient) ConnectWithRetry(ctx context.Context) error {
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

// RegisterControlPlaneWithRetry registers control-plane with retry logic
func (r *RoutingClient) RegisterControlPlaneWithRetry(ctx context.Context, config *Config) error {
	backoff := 1 * time.Second
	maxBackoff := 30 * time.Second
	attempt := 1

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := r.RegisterControlPlane(config)
		if err == nil {
			r.logger.Infof("Successfully registered control-plane after %d attempts", attempt)
			return nil
		}

		r.logger.Warnf("Failed to register control-plane (attempt %d): %v", attempt, err)
		
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

// NotifySnapshotDeliveredWithRetry notifies registry about snapshot delivery with retry logic
func (r *RoutingClient) NotifySnapshotDeliveredWithRetry(ctx context.Context, controlPlaneID, nodeID, version string) error {
	backoff := 1 * time.Second
	maxBackoff := 10 * time.Second
	attempt := 1
	maxAttempts := 3

	for attempt <= maxAttempts {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := r.NotifySnapshotDelivered(controlPlaneID, nodeID, version)
		if err == nil {
			if attempt > 1 {
				r.logger.Debugf("Successfully notified snapshot delivery for node %s after %d attempts", nodeID, attempt)
			}
			return nil
		}

		if attempt == maxAttempts {
			r.logger.Errorf("Failed to notify snapshot delivery for node %s after %d attempts: %v", nodeID, maxAttempts, err)
			return err
		}

		r.logger.Warnf("Failed to notify snapshot delivery for node %s (attempt %d): %v", nodeID, attempt, err)
		
		// Wait before retry
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		// Exponential backoff
		backoff = time.Duration(float64(backoff) * 1.5)
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
		attempt++
	}

	return fmt.Errorf("max attempts reached")
}

// SyncAllNodesWithRegistry syncs all existing nodes/snapshots with registry
func (r *RoutingClient) SyncAllNodesWithRegistry(ctx context.Context, controlPlaneID string, getAllNodes func() []NodeInfo) error {
	nodes := getAllNodes()
	if len(nodes) == 0 {
		r.logger.Infof("No nodes to sync with registry")
		return nil
	}

	r.logger.Infof("Starting sync of %d existing nodes with registry", len(nodes))
	
	// Log each node to be synced
	for i, node := range nodes {
		r.logger.Infof("Node %d/%d: %s (version: %s)", i+1, len(nodes), node.NodeID, node.Version)
	}

	// First, update the node list
	if err := r.UpdateNodeList(controlPlaneID, nodes); err != nil {
		r.logger.Errorf("Failed to update node list during sync: %v", err)
		return err
	}

	// Then, notify about each node's snapshot delivery
	successCount := 0
	for i, node := range nodes {
		r.logger.Infof("Syncing snapshot for node %d/%d: %s", i+1, len(nodes), node.NodeID)
		
		nodeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := r.NotifySnapshotDeliveredWithRetry(nodeCtx, controlPlaneID, node.NodeID, node.Version); err != nil {
			r.logger.Errorf("Failed to sync snapshot notification for node %s: %v", node.NodeID, err)
		} else {
			r.logger.Infof("Successfully synced snapshot for node: %s", node.NodeID)
			successCount++
		}
		cancel()
	}

	r.logger.Infof("Sync completed: %d/%d nodes successfully synced with registry", successCount, len(nodes))
	return nil
}

// NodeInfo represents a connected node
type NodeInfo struct {
	NodeID   string
	Version  string
	LastSeen time.Time
}
