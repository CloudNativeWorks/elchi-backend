package server

import (
	"context"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/registry/models"
	"github.com/CloudNativeWorks/elchi-backend/registry/service"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ControlPlaneGRPCServer implements the gRPC control-plane routing service
type ControlPlaneGRPCServer struct {
	pb.UnimplementedEnvoyRoutingServiceServer // Using legacy proto for now
	controlPlaneRoutingService                *service.RoutingService
	extProcessorServer                        *ExternalProcessorServer
	logger                                    *logger.Logger
}

// NewControlPlaneGRPCServer creates a new gRPC routing server instance
func NewControlPlaneGRPCServer(controlPlaneRoutingService *service.RoutingService, extProcessorServer *ExternalProcessorServer, logger *logger.Logger) *ControlPlaneGRPCServer {
	return &ControlPlaneGRPCServer{
		controlPlaneRoutingService: controlPlaneRoutingService,
		extProcessorServer:         extProcessorServer,
		logger:                     logger,
	}
}

// RegisterControlPlane handles control plane registration
func (s *ControlPlaneGRPCServer) RegisterControlPlane(ctx context.Context, req *pb.RegisterControlPlaneRequest) (*pb.RegisterControlPlaneResponse, error) {
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
		return &pb.RegisterControlPlaneResponse{
			Success: false,
			Message: "failed: " + err.Error(),
		}, nil
	}

	s.logger.Infof("Control plane registered successfully: %s", req.ControlPlaneId)
	return &pb.RegisterControlPlaneResponse{
		Success: true,
		Message: "control plane registered successfully",
	}, nil
}

// GetControlPlaneCluster handles routing requests from Envoy
func (s *ControlPlaneGRPCServer) GetControlPlaneCluster(ctx context.Context, req *pb.GetControlPlaneClusterRequest) (*pb.GetControlPlaneClusterResponse, error) {
	if req == nil || req.NodeId == "" || req.Version == "" {
		return nil, status.Error(codes.InvalidArgument, "node ID and version cannot be empty")
	}

	controlPlane, err := s.controlPlaneRoutingService.GetControlPlaneCluster(ctx, req.NodeId, req.Version)
	if err != nil {
		s.logger.Errorf("Failed to find control plane for node %s version %s: %v", req.NodeId, req.Version, err)
		return &pb.GetControlPlaneClusterResponse{
			Found: false,
		}, nil
	}

	s.logger.Infof("Control-plane routing decision: %s:%s -> %s", req.NodeId, req.Version, controlPlane.ID)
	return &pb.GetControlPlaneClusterResponse{
		Found:          true,
		ControlPlaneId: controlPlane.ID,
	}, nil
}

// NotifySnapshotDelivered handles snapshot delivery notifications
func (s *ControlPlaneGRPCServer) NotifySnapshotDelivered(ctx context.Context, req *pb.NotifySnapshotDeliveredRequest) (*pb.NotifySnapshotDeliveredResponse, error) {
	if req == nil || req.ControlPlaneId == "" || req.NodeId == "" || req.Version == "" {
		return nil, status.Error(codes.InvalidArgument, "control plane ID, node ID and version cannot be empty")
	}

	if err := s.controlPlaneRoutingService.NotifySnapshotDelivered(ctx, req.ControlPlaneId, req.NodeId, req.Version); err != nil {
		s.logger.Errorf("Failed to notify snapshot delivered: %v", err)
		return &pb.NotifySnapshotDeliveredResponse{
			Success: false,
			Message: "failed: " + err.Error(),
		}, nil
	}

	// Clear pending assignment now that the snapshot is delivered
	s.extProcessorServer.ClearPendingNodeAssignment(req.NodeId, req.ControlPlaneId)

	s.logger.Infof("Snapshot delivered notification processed: %s -> %s", req.ControlPlaneId, req.NodeId)
	return &pb.NotifySnapshotDeliveredResponse{
		Success: true,
		Message: "snapshot delivered notification processed",
	}, nil
}

// UpdateNodeList handles bulk node list updates
func (s *ControlPlaneGRPCServer) UpdateNodeList(ctx context.Context, req *pb.UpdateNodeListRequest) (*pb.UpdateNodeListResponse, error) {
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
		return &pb.UpdateNodeListResponse{
			Success:      false,
			Message:      "failed: " + err.Error(),
			UpdatedCount: 0,
		}, nil
	}

	s.logger.Infof("Node list updated for control plane %s: %d nodes", req.ControlPlaneId, len(nodes))
	return &pb.UpdateNodeListResponse{
		Success:      true,
		Message:      "node list updated successfully",
		UpdatedCount: int32(len(nodes)),
	}, nil
}

// HealthCheck handles health check requests
func (s *ControlPlaneGRPCServer) HealthCheck(ctx context.Context, req *pb.HealthCheckRequest) (*pb.HealthCheckResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request cannot be nil")
	}

	s.logger.Debugf("Health check request from: %s", req.Service)

	return &pb.HealthCheckResponse{
		Healthy:   true,
		Message:   "control-plane routing service is healthy",
		Timestamp: timestamppb.New(time.Now()),
	}, nil
}

// ListControlPlanes handles list control planes requests
func (s *ControlPlaneGRPCServer) ListControlPlanes(ctx context.Context, req *pb.ListControlPlanesRequest) (*pb.ListControlPlanesResponse, error) {
	s.logger.Infof("Listing all control planes")

	controlPlanes, err := s.controlPlaneRoutingService.ListControlPlanes(ctx)
	if err != nil {
		s.logger.Errorf("Failed to list control planes: %v", err)
		return nil, status.Errorf(codes.Internal, "failed to list control planes: %v", err)
	}

	var protoControlPlanes []*pb.ControlPlaneInfo
	for _, cp := range controlPlanes {
		protoControlPlanes = append(protoControlPlanes, &pb.ControlPlaneInfo{
			ControlPlaneId: cp.ID,
			Version:        cp.Version,
			LastSeen:       timestamppb.New(cp.LastSeen),
		})
	}

	s.logger.Infof("Found %d control planes", len(protoControlPlanes))
	return &pb.ListControlPlanesResponse{
		ControlPlanes: protoControlPlanes,
	}, nil
}

// ListNodesByControlPlane handles list nodes by control plane requests
func (s *ControlPlaneGRPCServer) ListNodesByControlPlane(ctx context.Context, req *pb.ListNodesByControlPlaneRequest) (*pb.ListNodesByControlPlaneResponse, error) {
	if req == nil || req.ControlPlaneId == "" {
		return nil, status.Error(codes.InvalidArgument, "control plane ID cannot be empty")
	}

	s.logger.Infof("Listing nodes for control plane: %s", req.ControlPlaneId)

	nodes, err := s.controlPlaneRoutingService.ListNodesByControlPlane(ctx, req.ControlPlaneId)
	if err != nil {
		s.logger.Errorf("Failed to get nodes for control plane %s: %v", req.ControlPlaneId, err)
		return nil, status.Errorf(codes.Internal, "failed to get nodes: %v", err)
	}

	var protoNodes []*pb.NodeInfo
	for _, node := range nodes {
		protoNodes = append(protoNodes, &pb.NodeInfo{
			NodeId:   node.NodeID,
			Version:  node.Version,
			LastSeen: timestamppb.New(node.LastSeen),
		})
	}

	s.logger.Infof("Found %d nodes for control plane %s", len(protoNodes), req.ControlPlaneId)
	return &pb.ListNodesByControlPlaneResponse{
		Nodes: protoNodes,
	}, nil
}

// DeleteControlPlane handles control plane deletion requests
func (s *ControlPlaneGRPCServer) DeleteControlPlane(ctx context.Context, req *pb.DeleteControlPlaneRequest) (*pb.DeleteControlPlaneResponse, error) {
	if req == nil || req.ControlPlaneId == "" {
		return nil, status.Error(codes.InvalidArgument, "control plane ID cannot be empty")
	}

	s.logger.Infof("Delete control plane request: %s", req.ControlPlaneId)

	if err := s.controlPlaneRoutingService.DeleteControlPlane(ctx, req.ControlPlaneId); err != nil {
		s.logger.Errorf("Failed to delete control plane %s: %v", req.ControlPlaneId, err)
		return &pb.DeleteControlPlaneResponse{
			Success: false,
			Message: "failed: " + err.Error(),
		}, nil
	}

	s.logger.Infof("Control plane deleted successfully: %s", req.ControlPlaneId)
	return &pb.DeleteControlPlaneResponse{
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
