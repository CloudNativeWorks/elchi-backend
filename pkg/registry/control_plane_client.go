package registry

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
)

type ControlPlaneRegistryClient struct {
	conn         *grpc.ClientConn
	client       pb.EnvoyRoutingServiceClient
	registryAddr string
	logger       *logger.Logger
}

type ControlPlaneConfig struct {
	RegistryAddress string
	ControlPlaneID  string
	Version         string
}

// NewControlPlaneConfig creates a new registry config with auto-detected values
func NewControlPlaneConfig(registryAddress, envoyVersion string) *ControlPlaneConfig {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}

	// Clean hostname (remove domain parts)
	hostname = strings.Split(hostname, ".")[0]

	return &ControlPlaneConfig{
		RegistryAddress: registryAddress,
		ControlPlaneID:  hostname,
		Version:         envoyVersion,
	}
}

// NewControlPlaneRegistryClient creates a new registry client for control-plane
func NewControlPlaneRegistryClient(config *ControlPlaneConfig, logger *logger.Logger) (*ControlPlaneRegistryClient, error) {
	client := &ControlPlaneRegistryClient{
		registryAddr: config.RegistryAddress,
		logger:       logger,
	}

	return client, nil
}

// Connect establishes gRPC connection to registry
func (r *ControlPlaneRegistryClient) Connect() error {
	// Use shared gRPC dial options for consistency
	conn, err := grpc.NewClient(r.registryAddr, GetDefaultGRPCDialOptions()...)
	if err != nil {
		return fmt.Errorf("failed to create connection: %v", err)
	}

	r.conn = conn
	r.client = pb.NewEnvoyRoutingServiceClient(conn)

	return nil
}

// Disconnect closes the gRPC connection
func (r *ControlPlaneRegistryClient) Disconnect() error {
	if r.conn != nil {
		return r.conn.Close()
	}
	return nil
}

// RegisterControlPlane registers this control-plane with registry
func (r *ControlPlaneRegistryClient) RegisterControlPlane(config *ControlPlaneConfig) error {
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
func (r *ControlPlaneRegistryClient) NotifySnapshotDelivered(controlPlaneID, nodeID, version string) error {
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

// NotifyNodeDisconnected notifies registry about node disconnection
func (r *ControlPlaneRegistryClient) NotifyNodeDisconnected(ctx context.Context, controlPlaneID, nodeID, version string) error {
	r.logger.Infof("Node disconnected: %s from control-plane: %s (version: %s)", nodeID, controlPlaneID, version)
	
	// NOTE: We don't have a specific gRPC method for immediate node disconnection notification yet.
	// The disconnected node will be automatically cleaned up from registry during the next
	// periodic UpdateNodeList call (every 30 seconds) since it won't be included in the
	// list of connected nodes from the snapshot context.
	//
	// This approach is consistent with how the system handles:
	// 1. Envoy connections that drop without explicit disconnect
	// 2. Network partitions where nodes become unreachable
	// 3. Control-plane restarts where the node state is rebuilt
	//
	// For immediate cleanup, we could add a new gRPC method like:
	// rpc NotifyNodeDisconnected(NotifyNodeDisconnectedRequest) returns (NotifyNodeDisconnectedResponse);
	
	r.logger.Infof("Node %s will be cleaned up from registry during next periodic update", nodeID)
	return nil
}

// UpdateNodeList updates the list of connected nodes
func (r *ControlPlaneRegistryClient) UpdateNodeList(controlPlaneID string, nodes []ControlPlaneNodeInfo) error {
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
func (r *ControlPlaneRegistryClient) HealthCheck() error {
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
func (r *ControlPlaneRegistryClient) ConnectWithRetry(ctx context.Context) error {
	return RetryWithBackoff(ctx, "connect to registry", AggressiveRetryConfig(), r.logger, func() error {
		return r.Connect()
	})
}

// RegisterControlPlaneWithRetry registers control-plane with retry logic
func (r *ControlPlaneRegistryClient) RegisterControlPlaneWithRetry(ctx context.Context, config *ControlPlaneConfig) error {
	return RetryWithBackoff(ctx, "register control-plane", AggressiveRetryConfig(), r.logger, func() error {
		return r.RegisterControlPlane(config)
	})
}

// NotifySnapshotDeliveredWithRetry notifies registry about snapshot delivery with retry logic
func (r *ControlPlaneRegistryClient) NotifySnapshotDeliveredWithRetry(ctx context.Context, controlPlaneID, nodeID, version string) error {
	// Use a custom config with limited attempts for snapshot notifications
	config := RetryConfig{
		InitialBackoff: 1 * time.Second,
		MaxBackoff:     10 * time.Second,
		Multiplier:     1.5,
		MaxAttempts:    3, // Limited attempts for snapshot notifications
	}

	return RetryWithBackoff(ctx, fmt.Sprintf("notify snapshot delivery for node %s", nodeID), config, r.logger, func() error {
		return r.NotifySnapshotDelivered(controlPlaneID, nodeID, version)
	})
}

// SyncAllNodesWithRegistry syncs all existing nodes/snapshots with registry
func (r *ControlPlaneRegistryClient) SyncAllNodesWithRegistry(ctx context.Context, controlPlaneID string, getAllNodes func() []ControlPlaneNodeInfo) error {
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

// DeleteControlPlane deletes a control plane from registry
func (r *ControlPlaneRegistryClient) DeleteControlPlane(controlPlaneID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := &pb.DeleteControlPlaneRequest{
		ControlPlaneId: controlPlaneID,
	}

	resp, err := r.client.DeleteControlPlane(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to call DeleteControlPlane: %w", err)
	}

	if !resp.GetSuccess() {
		return fmt.Errorf("registry delete control plane failed: %s", resp.GetMessage())
	}

	r.logger.Infof("Control plane %s deleted from registry successfully", controlPlaneID)
	return nil
}

// ControlPlaneNodeInfo represents a connected node for control-plane
type ControlPlaneNodeInfo struct {
	NodeID   string
	Version  string
	LastSeen time.Time
} 