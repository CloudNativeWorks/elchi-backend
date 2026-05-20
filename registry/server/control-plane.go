package server

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/registry/models"
	"github.com/CloudNativeWorks/elchi-backend/registry/service"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ControlPlaneGRPCServer implements the gRPC control-plane routing service
type ControlPlaneGRPCServer struct {
	bridge.UnimplementedEnvoyRoutingServiceServer
	controlPlaneRoutingService *service.RoutingService
	extProcessorServer         *ExternalProcessorServer
	logger                     *logger.Logger
	leader                     LeaderChecker
}

// NewControlPlaneGRPCServer creates a new gRPC routing server instance.
// leader may be nil; when present, write/notify RPCs are rejected on standby instances.
func NewControlPlaneGRPCServer(controlPlaneRoutingService *service.RoutingService, extProcessorServer *ExternalProcessorServer, logger *logger.Logger, leader LeaderChecker) *ControlPlaneGRPCServer {
	return &ControlPlaneGRPCServer{
		controlPlaneRoutingService: controlPlaneRoutingService,
		extProcessorServer:         extProcessorServer,
		logger:                     logger,
		leader:                     leader,
	}
}

// rejectIfStandby returns a gRPC Unavailable error when this registry is not
// the active leader. Returns nil when there is no leader configured (single-node).
func (s *ControlPlaneGRPCServer) rejectIfStandby() error {
	if s.leader == nil || s.leader.IsLeader() {
		return nil
	}
	return status.Error(codes.Unavailable, "registry is not the active leader")
}

// RegisterControlPlane handles control plane registration
func (s *ControlPlaneGRPCServer) RegisterControlPlane(ctx context.Context, req *bridge.RegisterControlPlaneRequest) (*bridge.RegisterControlPlaneResponse, error) {
	if err := s.rejectIfStandby(); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request cannot be nil")
	}

	// Convert proto to internal model
	controlPlane := &models.ControlPlane{
		ID:       req.ControlPlaneId,
		Version:  req.Version,
		LastSeen: time.Now(),
	}

	if err := s.controlPlaneRoutingService.RegisterControlPlane(ctx, controlPlane); err != nil {
		s.logger.Errorf("Failed to register control plane %s: %v", req.ControlPlaneId, err)
		return &bridge.RegisterControlPlaneResponse{
			Success: false,
			Message: "failed: " + err.Error(),
		}, nil
	}

	s.logger.Infof("Control plane registered successfully: %s", req.ControlPlaneId)
	return &bridge.RegisterControlPlaneResponse{
		Success: true,
		Message: "control plane registered successfully",
	}, nil
}

// GetControlPlaneCluster handles routing requests from Envoy.
//
// Read-only: see controller.go GetControllerCluster for the standby-
// allow rationale. Snapshot-backed view is at most ~30s stale and that
// beats failing the lookup outright during the brief leader-transition
// window. State-mutating RPCs in this file still reject on standby.
func (s *ControlPlaneGRPCServer) GetControlPlaneCluster(ctx context.Context, req *bridge.GetControlPlaneClusterRequest) (*bridge.GetControlPlaneClusterResponse, error) {
	if req == nil || req.NodeId == "" || req.Version == "" {
		return nil, status.Error(codes.InvalidArgument, "node ID and version cannot be empty")
	}

	controlPlane, err := s.controlPlaneRoutingService.GetControlPlaneCluster(ctx, req.NodeId, req.Version)
	if err != nil {
		s.logger.Errorf("Failed to find control plane for node %s version %s: %v", req.NodeId, req.Version, err)
		return &bridge.GetControlPlaneClusterResponse{
			Found: false,
		}, nil
	}

	s.logger.Infof("Control-plane routing decision: %s:%s -> %s", req.NodeId, req.Version, controlPlane.ID)
	return &bridge.GetControlPlaneClusterResponse{
		Found:          true,
		ControlPlaneId: controlPlane.ID,
	}, nil
}

// NotifySnapshotDelivered handles snapshot delivery notifications
func (s *ControlPlaneGRPCServer) NotifySnapshotDelivered(ctx context.Context, req *bridge.NotifySnapshotDeliveredRequest) (*bridge.NotifySnapshotDeliveredResponse, error) {
	if err := s.rejectIfStandby(); err != nil {
		return nil, err
	}
	if req == nil || req.ControlPlaneId == "" || req.NodeId == "" || req.Version == "" {
		return nil, status.Error(codes.InvalidArgument, "control plane ID, node ID and version cannot be empty")
	}

	if err := s.controlPlaneRoutingService.NotifySnapshotDelivered(ctx, req.ControlPlaneId, req.NodeId, req.Version); err != nil {
		s.logger.Errorf("Failed to notify snapshot delivered: %v", err)
		return &bridge.NotifySnapshotDeliveredResponse{
			Success: false,
			Message: "failed: " + err.Error(),
		}, nil
	}

	// Clear pending assignment now that the snapshot is delivered
	s.extProcessorServer.ClearPendingNodeAssignment(req.NodeId, req.ControlPlaneId)

	s.logger.Infof("Snapshot delivered notification processed: %s -> %s", req.ControlPlaneId, req.NodeId)
	return &bridge.NotifySnapshotDeliveredResponse{
		Success: true,
		Message: "snapshot delivered notification processed",
	}, nil
}

// UpdateNodeList handles bulk node list updates
func (s *ControlPlaneGRPCServer) UpdateNodeList(ctx context.Context, req *bridge.UpdateNodeListRequest) (*bridge.UpdateNodeListResponse, error) {
	if err := s.rejectIfStandby(); err != nil {
		return nil, err
	}
	if req == nil || req.ControlPlaneId == "" {
		return nil, status.Error(codes.InvalidArgument, "control plane ID cannot be empty")
	}

	// Convert proto nodes to internal models
	var nodes []*models.NodeInfo
	for _, node := range req.Nodes {
		nodes = append(nodes, &models.NodeInfo{
			NodeID:   node.NodeId,
			Version:  node.Version,
			LastSeen: time.Now(),
		})
	}

	if err := s.controlPlaneRoutingService.UpdateNodeList(ctx, req.ControlPlaneId, nodes, req.Version); err != nil {
		s.logger.Errorf("Failed to update node list for control plane %s: %v", req.ControlPlaneId, err)
		return &bridge.UpdateNodeListResponse{
			Success:      false,
			Message:      "failed: " + err.Error(),
			UpdatedCount: 0,
		}, nil
	}

	s.logger.Infof("Node list updated for control plane %s: %d nodes", req.ControlPlaneId, len(nodes))

	nodeCount := len(nodes)
	var updatedCount int32
	if nodeCount > math.MaxInt32 {
		s.logger.Warnf("Node count %d exceeds int32 max, capping at MaxInt32", nodeCount)
		updatedCount = math.MaxInt32
	} else {
		updatedCount = int32(nodeCount) // #nosec G115 - overflow checked above
	}

	return &bridge.UpdateNodeListResponse{
		Success:      true,
		Message:      "node list updated successfully",
		UpdatedCount: updatedCount,
	}, nil
}

// HealthCheck handles health check requests
func (s *ControlPlaneGRPCServer) HealthCheck(ctx context.Context, req *bridge.EnvoyHealthCheckRequest) (*bridge.EnvoyHealthCheckResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request cannot be nil")
	}

	s.logger.Debugf("Health check request from: %s", req.Service)

	return &bridge.EnvoyHealthCheckResponse{
		Healthy:   true,
		Message:   "control-plane routing service is healthy",
		Timestamp: timestamppb.New(time.Now()),
	}, nil
}

// ListControlPlanes handles list control planes requests
func (s *ControlPlaneGRPCServer) ListControlPlanes(ctx context.Context, req *bridge.ListControlPlanesRequest) (*bridge.ListControlPlanesResponse, error) {
	s.logger.Infof("Listing all control planes")

	controlPlanes, err := s.controlPlaneRoutingService.ListControlPlanes(ctx)
	if err != nil {
		s.logger.Errorf("Failed to list control planes: %v", err)
		return nil, status.Errorf(codes.Internal, "failed to list control planes: %v", err)
	}

	var protoControlPlanes []*bridge.ControlPlaneInfo
	for _, cp := range controlPlanes {
		protoControlPlanes = append(protoControlPlanes, &bridge.ControlPlaneInfo{
			ControlPlaneId: cp.ID,
			Version:        cp.Version,
			LastSeen:       timestamppb.New(cp.LastSeen),
		})
	}

	s.logger.Infof("Found %d control planes", len(protoControlPlanes))
	return &bridge.ListControlPlanesResponse{
		ControlPlanes: protoControlPlanes,
	}, nil
}

// ListNodesByControlPlane handles list nodes by control plane requests
func (s *ControlPlaneGRPCServer) ListNodesByControlPlane(ctx context.Context, req *bridge.ListNodesByControlPlaneRequest) (*bridge.ListNodesByControlPlaneResponse, error) {
	if req == nil || req.ControlPlaneId == "" {
		return nil, status.Error(codes.InvalidArgument, "control plane ID cannot be empty")
	}

	s.logger.Infof("Listing nodes for control plane: %s", req.ControlPlaneId)

	nodes, err := s.controlPlaneRoutingService.ListNodesByControlPlane(ctx, req.ControlPlaneId)
	if err != nil {
		s.logger.Errorf("Failed to get nodes for control plane %s: %v", req.ControlPlaneId, err)
		return nil, status.Errorf(codes.Internal, "failed to get nodes: %v", err)
	}

	var protoNodes []*bridge.NodeInfo
	for _, node := range nodes {
		protoNodes = append(protoNodes, &bridge.NodeInfo{
			NodeId:   node.NodeID,
			Version:  node.Version,
			LastSeen: timestamppb.New(node.LastSeen),
		})
	}

	s.logger.Infof("Found %d nodes for control plane %s", len(protoNodes), req.ControlPlaneId)
	return &bridge.ListNodesByControlPlaneResponse{
		Nodes: protoNodes,
	}, nil
}

// DeleteControlPlane handles control plane deletion requests
func (s *ControlPlaneGRPCServer) DeleteControlPlane(ctx context.Context, req *bridge.DeleteControlPlaneRequest) (*bridge.DeleteControlPlaneResponse, error) {
	if err := s.rejectIfStandby(); err != nil {
		return nil, err
	}
	if req == nil || req.ControlPlaneId == "" {
		return nil, status.Error(codes.InvalidArgument, "control plane ID cannot be empty")
	}

	s.logger.Infof("Delete control plane request: %s", req.ControlPlaneId)

	if err := s.controlPlaneRoutingService.DeleteControlPlane(ctx, req.ControlPlaneId); err != nil {
		s.logger.Errorf("Failed to delete control plane %s: %v", req.ControlPlaneId, err)
		return &bridge.DeleteControlPlaneResponse{
			Success: false,
			Message: "failed: " + err.Error(),
		}, nil
	}

	s.logger.Infof("Control plane deleted successfully: %s", req.ControlPlaneId)
	return &bridge.DeleteControlPlaneResponse{
		Success: true,
		Message: "control plane deleted successfully",
	}, nil
}

// resolveControlPlaneCluster determines the target control-plane cluster based on node ID and version
func (p *ExternalProcessorServer) resolveControlPlaneCluster(version, nodeID string) string {
	p.logger.Debugf("Resolving control-plane cluster for NodeID: %s, Version: %s", nodeID, version)

	if nodeID == "" {
		p.logger.Errorf("NodeID cannot be empty")
		return ""
	}

	// Use control-plane routing service to find appropriate control plane
	ctx := context.Background()

	// Atomic assignment with mutex
	p.assignmentMutex.Lock()
	defer p.assignmentMutex.Unlock()

	// Check if there's a pending assignment first
	if pendingControlPlane, exists := p.pendingNodeAssignments[nodeID]; exists {
		p.logger.Infof("Found pending control-plane assignment: %s -> %s", nodeID, pendingControlPlane)
		return pendingControlPlane
	}

	// Try to get existing mapping
	controlPlane, err := p.controlPlaneRoutingService.GetControlPlaneCluster(ctx, nodeID, version)
	if err == nil && controlPlane != nil {
		p.logger.Infof("Found existing control-plane mapping: %s -> %s", nodeID, controlPlane.ID)
		return controlPlane.ID
	}

	// If no mapping exists or error occurred, get least loaded control-plane
	controlPlane, err = p.controlPlaneRoutingService.GetLeastLoadedControlPlane(ctx, version)
	if err != nil {
		p.logger.Errorf("Failed to get least loaded control-plane: %v", err)
		return ""
	}

	// Create pending assignment to prevent race conditions
	p.pendingNodeAssignments[nodeID] = controlPlane.ID
	p.logger.Infof("Created pending assignment: %s -> %s (for new node)", nodeID, controlPlane.ID)

	// Set timeout to clear pending assignment if not confirmed
	go func() {
		time.Sleep(30 * time.Second) // 30 second timeout
		p.assignmentMutex.Lock()
		defer p.assignmentMutex.Unlock()

		if pendingControlPlane, exists := p.pendingNodeAssignments[nodeID]; exists && pendingControlPlane == controlPlane.ID {
			delete(p.pendingNodeAssignments, nodeID)
			p.logger.Warnf("Cleared pending assignment due to timeout: %s -> %s", nodeID, controlPlane.ID)
		}
	}()

	p.logger.Infof("Selected least loaded control-plane: %s (for new node %s)", controlPlane.ID, nodeID)
	return controlPlane.ID
}

// resolveBridgeControlPlaneCluster determines the target control-plane cluster for bridge calls
func (p *ExternalProcessorServer) resolveBridgeControlPlaneCluster(ctx context.Context, version, nodeID, path string) string {
	p.logger.Debugf("Resolving bridge control-plane cluster for NodeID: %s, Version: %s, Path: %s", nodeID, version, path)

	// Check if this requires existing node mapping (Poke or GetNodeSnapshot)
	requiresMapping := strings.HasSuffix(path, "/Poke") || strings.Contains(path, "GetNodeSnapshot")

	if requiresMapping {
		// For Poke and GetNodeSnapshot requests, check if node is already mapped to a control-plane
		// This ensures the request goes to the control-plane where the node's snapshot exists
		if controlPlane, err := p.controlPlaneRoutingService.GetControlPlaneCluster(ctx, nodeID, version); err == nil && controlPlane != nil {
			requestType := "Poke"
			if strings.Contains(path, "GetNodeSnapshot") {
				requestType = "GetNodeSnapshot"
			}
			p.logger.Infof("Bridge routing (%s - using existing mapping): %s:%s -> %s", requestType, nodeID, version, controlPlane.ID)
			return controlPlane.ID
		}

		// If no mapping exists, get least loaded control-plane for this version
		if controlPlane, err := p.controlPlaneRoutingService.GetLeastLoadedControlPlane(ctx, version); err == nil {
			requestType := "Poke"
			if strings.Contains(path, "GetNodeSnapshot") {
				requestType = "GetNodeSnapshot"
			}
			p.logger.Infof("Bridge routing (%s - no mapping, using least loaded): %s:%s -> %s", requestType, nodeID, version, controlPlane.ID)
			return controlPlane.ID
		}

		requestType := "Poke"
		if strings.Contains(path, "GetNodeSnapshot") {
			requestType = "GetNodeSnapshot"
		}
		p.logger.Errorf("Bridge routing (%s): No control-plane available for %s:%s", requestType, nodeID, version)
		return ""
	}

	// For ValidateResource and other bridge requests, just get any control-plane for this version
	// No need to check node mappings as validation can be done by any control-plane with the same version
	if controlPlane, err := p.controlPlaneRoutingService.GetLeastLoadedControlPlane(ctx, version); err == nil {
		p.logger.Infof("Bridge routing (ValidateResource - random for version): %s:%s -> %s", nodeID, version, controlPlane.ID)
		return controlPlane.ID
	}

	p.logger.Errorf("Bridge routing (ValidateResource): No control-plane available for version %s", version)
	return ""
}
