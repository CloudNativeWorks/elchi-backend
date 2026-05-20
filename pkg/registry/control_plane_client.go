package registry

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

type ControlPlaneRegistryClient struct {
	// connMu guards conn pointer mutation in Connect/Disconnect so a
	// concurrent reconnect cycle cannot double-close. The client stub is
	// published via atomic.Pointer so RPC callers never see a torn
	// interface value during reconnect (see controller.go for full
	// rationale of the same pattern).
	connMu       sync.Mutex
	conn         *grpc.ClientConn
	client       atomic.Pointer[bridge.EnvoyRoutingServiceClient]
	registryAddr string
	appConfig    *config.AppConfig // retained for TLS dial decisions on (re)connect
	logger       *logger.Logger
}

// getClient returns the current gRPC stub or nil if no connection has
// been established. Callers must nil-check before invoking RPCs.
func (r *ControlPlaneRegistryClient) getClient() bridge.EnvoyRoutingServiceClient {
	p := r.client.Load()
	if p == nil {
		return nil
	}
	return *p
}

type ControlPlaneConfig struct {
	RegistryAddress string
	ControlPlaneID  string
	Version         string
}

// NewControlPlaneConfig creates a new registry config with the control-plane
// identity resolved according to ResolveControlPlaneID rules (K8s hostname
// or "<host>-controlplane-<version>" outside Kubernetes; cfg.ControlPlaneID
// takes precedence when set).
func NewControlPlaneConfig(registryAddress, envoyVersion string, appConfig *config.AppConfig) *ControlPlaneConfig {
	return &ControlPlaneConfig{
		RegistryAddress: registryAddress,
		ControlPlaneID:  ResolveControlPlaneID(appConfig, envoyVersion),
		Version:         envoyVersion,
	}
}

// NewControlPlaneRegistryClient creates a new registry client for control-plane.
// appConfig is retained on the client so subsequent (re)connect attempts use
// the same transport credential decision (plaintext vs TLS).
func NewControlPlaneRegistryClient(cpConfig *ControlPlaneConfig, logger *logger.Logger, appConfig *config.AppConfig) (*ControlPlaneRegistryClient, error) {
	client := &ControlPlaneRegistryClient{
		registryAddr: cpConfig.RegistryAddress,
		appConfig:    appConfig,
		logger:       logger,
	}

	return client, nil
}

// Connect establishes gRPC connection to registry.
// connMu guards the conn pointer; the stub is published atomically so
// concurrent RPC callers never see a half-written interface value.
func (r *ControlPlaneRegistryClient) Connect() error {
	// Use shared gRPC dial options for consistency
	conn, err := grpc.NewClient(r.registryAddr, GetDefaultGRPCDialOptions(r.appConfig)...)
	if err != nil {
		return fmt.Errorf("failed to create connection: %w", err)
	}

	stub := bridge.NewEnvoyRoutingServiceClient(conn)
	r.connMu.Lock()
	r.conn = conn
	r.client.Store(&stub)
	r.connMu.Unlock()

	return nil
}

// Disconnect closes the gRPC connection.
// nil-check + nil-out so a second Disconnect is a no-op. Clears the
// atomic stub too so getClient() returns nil once disconnected.
func (r *ControlPlaneRegistryClient) Disconnect() error {
	r.connMu.Lock()
	defer r.connMu.Unlock()
	if r.conn == nil {
		return nil
	}
	err := r.conn.Close()
	r.conn = nil
	r.client.Store(nil)
	return err
}

// RegisterControlPlane registers this control-plane with registry
func (r *ControlPlaneRegistryClient) RegisterControlPlane(config *ControlPlaneConfig) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	request := &bridge.RegisterControlPlaneRequest{
		ControlPlaneId: config.ControlPlaneID,
		Version:        config.Version,
		Timestamp:      timestamppb.New(time.Now().UTC()),
	}

	client := r.getClient()
	if client == nil {
		return fmt.Errorf("control-plane registry client not connected")
	}
	response, err := client.RegisterControlPlane(ctx, request)
	if err != nil {
		return fmt.Errorf("failed to register control-plane: %w", err)
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

	request := &bridge.NotifySnapshotDeliveredRequest{
		ControlPlaneId: controlPlaneID,
		NodeId:         nodeID,
		Version:        version,
		Timestamp:      timestamppb.New(time.Now().UTC()),
	}

	client := r.getClient()
	if client == nil {
		return fmt.Errorf("control-plane registry client not connected")
	}
	response, err := client.NotifySnapshotDelivered(ctx, request)
	if err != nil {
		return fmt.Errorf("failed to notify snapshot delivery: %w", err)
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

// UpdateNodeList updates the list of connected nodes with version information
func (r *ControlPlaneRegistryClient) UpdateNodeList(controlPlaneID string, nodes []ControlPlaneNodeInfo, version string) error {
	r.logger.Infof("Updating node list for control-plane %s: %d nodes", controlPlaneID, len(nodes))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var pbNodes []*bridge.NodeInfo
	for _, node := range nodes {
		pbNodes = append(pbNodes, &bridge.NodeInfo{
			NodeId:   node.NodeID,
			Version:  node.Version,
			LastSeen: timestamppb.New(node.LastSeen),
		})
	}

	request := &bridge.UpdateNodeListRequest{
		ControlPlaneId: controlPlaneID,
		Nodes:          pbNodes,
		Timestamp:      timestamppb.New(time.Now().UTC()),
		Version:        version, // Add version for auto-registration
	}

	client := r.getClient()
	if client == nil {
		return fmt.Errorf("control-plane registry client not connected")
	}
	response, err := client.UpdateNodeList(ctx, request)
	if err != nil {
		return fmt.Errorf("failed to update node list: %w", err)
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

	request := &bridge.EnvoyHealthCheckRequest{
		Service: "control-plane",
	}

	client := r.getClient()
	if client == nil {
		return fmt.Errorf("control-plane registry client not connected")
	}
	response, err := client.HealthCheck(ctx, request)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}

	if !response.Healthy {
		return fmt.Errorf("registry unhealthy: %s", response.Message)
	}

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
func (r *ControlPlaneRegistryClient) SyncAllNodesWithRegistry(ctx context.Context, controlPlaneID string, getAllNodes func() []ControlPlaneNodeInfo, version string) error {
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
	if err := r.UpdateNodeList(controlPlaneID, nodes, version); err != nil {
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

	req := &bridge.DeleteControlPlaneRequest{
		ControlPlaneId: controlPlaneID,
	}

	client := r.getClient()
	if client == nil {
		return fmt.Errorf("control-plane registry client not connected")
	}
	resp, err := client.DeleteControlPlane(ctx, req)
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
