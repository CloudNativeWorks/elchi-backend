package server

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/registry/models"
	"github.com/CloudNativeWorks/elchi-backend/registry/service"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
	core "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/core/v3"
	ext "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Constants for header keys
const (
	HeaderNodeID         = "nodeid"
	HeaderClientID       = "clientid"
	HeaderVersion        = "envoy-version"
	HeaderTargetCluster  = "x-target-cluster"
	HeaderRoutingService = "x-routing-service"
	HeaderPath           = ":path"
	HeaderMethod         = ":method"
	HeaderContentType    = "content-type"
	HeaderGRPCAccept     = "accept"
)

// ExcludedPaths contains paths that should bypass routing
var ExcludedPaths = []string{
	"/healthz",
	"/ready",
	"/metrics",
	"/favicon.ico",
	"/api/v1/metrics",
	"/api/v1/health",
	"/opentelemetry.proto.collector.metrics.v1.MetricsService/Export",
}

// ControllerGRPCServer implements the gRPC controller routing service
type ControllerGRPCServer struct {
	pb.UnimplementedControllerRoutingServiceServer
	controllerRoutingService *service.ControllerRoutingService
	logger                   *logger.Logger
}

// NewControllerGRPCServer creates a new gRPC server instance for controller routing
func NewControllerGRPCServer(controllerRoutingService *service.ControllerRoutingService, logger *logger.Logger) *ControllerGRPCServer {
	return &ControllerGRPCServer{
		controllerRoutingService: controllerRoutingService,
		logger:                   logger,
	}
}

// RegisterController handles controller registration
func (s *ControllerGRPCServer) RegisterController(ctx context.Context, req *pb.RegisterControllerRequest) (*pb.RegisterControllerResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request cannot be nil")
	}

	serviceName := helper.ToK8sServiceName(req.ControllerId, "elchi-stack")

	// Convert proto to internal model
	controllerInfo := &models.ControllerInfo{
		ID:          req.ControllerId,
		Version:     req.Version,
		HttpAddress: fmt.Sprintf("%s:8099", serviceName),
		LastSeen:    time.Now(),
	}

	if err := s.controllerRoutingService.RegisterController(ctx, controllerInfo); err != nil {
		s.logger.Errorf("Failed to register controller %s: %v", req.ControllerId, err)
		return &pb.RegisterControllerResponse{
			Success: false,
			Message: "failed: " + err.Error(),
		}, nil
	}

	s.logger.Infof("Controller registered successfully: %s version %s", req.ControllerId, req.Version)
	return &pb.RegisterControllerResponse{
		Success: true,
		Message: "controller registered successfully",
	}, nil
}

// GetControllerCluster handles controller cluster routing requests
func (s *ControllerGRPCServer) GetControllerCluster(ctx context.Context, req *pb.GetControllerClusterRequest) (*pb.GetControllerClusterResponse, error) {
	if req == nil || req.ClientId == "" || req.Version == "" {
		return nil, status.Error(codes.InvalidArgument, "client ID and version cannot be empty")
	}

	controller, err := s.controllerRoutingService.GetControllerCluster(ctx, req.ClientId, req.Version)
	if err != nil {
		s.logger.Errorf("Failed to find controller for client %s version %s: %v", req.ClientId, req.Version, err)
		return &pb.GetControllerClusterResponse{
			Found: false,
		}, nil
	}

	s.logger.Infof("Controller routing decision: %s:%s -> %s", req.ClientId, req.Version, controller.ID)
	return &pb.GetControllerClusterResponse{
		Found:        true,
		ControllerId: controller.ID,
	}, nil
}

// NotifyClientConnected handles client connection notifications
func (s *ControllerGRPCServer) NotifyClientConnected(ctx context.Context, req *pb.NotifyClientConnectedRequest) (*pb.NotifyClientConnectedResponse, error) {
	if req == nil || req.ControllerId == "" || req.ClientId == "" || req.Version == "" {
		return nil, status.Error(codes.InvalidArgument, "controller ID, client ID and version cannot be empty")
	}

	if err := s.controllerRoutingService.NotifyClientConnected(ctx, req.ControllerId, req.ClientId, req.Version); err != nil {
		s.logger.Errorf("Failed to notify client connected: %v", err)
		return &pb.NotifyClientConnectedResponse{
			Success: false,
			Message: "failed: " + err.Error(),
		}, nil
	}

	s.logger.Infof("Client connected notification processed: %s -> %s (version: %s)", req.ControllerId, req.ClientId, req.Version)
	return &pb.NotifyClientConnectedResponse{
		Success: true,
		Message: "client connected notification processed",
	}, nil
}

// NotifyClientDisconnected handles client disconnection notifications
func (s *ControllerGRPCServer) NotifyClientDisconnected(ctx context.Context, req *pb.NotifyClientDisconnectedRequest) (*pb.NotifyClientDisconnectedResponse, error) {
	if req == nil || req.ControllerId == "" || req.ClientId == "" || req.Version == "" {
		return nil, status.Error(codes.InvalidArgument, "controller ID, client ID and version cannot be empty")
	}

	if err := s.controllerRoutingService.NotifyClientDisconnected(ctx, req.ControllerId, req.ClientId, req.Version); err != nil {
		s.logger.Errorf("Failed to notify client disconnected: %v", err)
		return &pb.NotifyClientDisconnectedResponse{
			Success: false,
			Message: "failed: " + err.Error(),
		}, nil
	}

	s.logger.Infof("Client disconnected notification processed: %s -> %s (version: %s)", req.ControllerId, req.ClientId, req.Version)
	return &pb.NotifyClientDisconnectedResponse{
		Success: true,
		Message: "client disconnected notification processed",
	}, nil
}

// UpdateClientList handles bulk client list updates
func (s *ControllerGRPCServer) UpdateClientList(ctx context.Context, req *pb.UpdateClientListRequest) (*pb.UpdateClientListResponse, error) {
	if req == nil || req.ControllerId == "" {
		return nil, status.Error(codes.InvalidArgument, "controller ID cannot be empty")
	}

	// Convert proto clients to internal models
	var clients []*models.ClientInfo
	for _, client := range req.Clients {
		clients = append(clients, &models.ClientInfo{
			ClientID: client.ClientId,
			Version:  client.Version,
			LastSeen: time.Now(),
		})
	}

	if err := s.controllerRoutingService.UpdateClientList(ctx, req.ControllerId, clients); err != nil {
		s.logger.Errorf("Failed to update client list for controller %s: %v", req.ControllerId, err)
		return &pb.UpdateClientListResponse{
			Success:      false,
			Message:      "failed: " + err.Error(),
			UpdatedCount: 0,
		}, nil
	}

	s.logger.Infof("Client list updated for controller %s: %d clients", req.ControllerId, len(clients))
	return &pb.UpdateClientListResponse{
		Success:      true,
		Message:      "client list updated successfully",
		UpdatedCount: int32(len(clients)),
	}, nil
}

// HealthCheck handles health check requests
func (s *ControllerGRPCServer) HealthCheck(ctx context.Context, req *pb.ControllerHealthCheckRequest) (*pb.ControllerHealthCheckResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request cannot be nil")
	}

	s.logger.Debugf("Controller health check request from: %s", req.Service)

	return &pb.ControllerHealthCheckResponse{
		Healthy:   true,
		Message:   "controller routing service is healthy",
		Timestamp: timestamppb.New(time.Now()),
	}, nil
}

// ListControllers handles list controllers requests
func (s *ControllerGRPCServer) ListControllers(ctx context.Context, req *pb.ListControllersRequest) (*pb.ListControllersResponse, error) {
	s.logger.Infof("Listing all controllers")

	controllers, err := s.controllerRoutingService.ListControllers(ctx)
	if err != nil {
		s.logger.Errorf("Failed to list controllers: %v", err)
		return nil, status.Errorf(codes.Internal, "failed to list controllers: %v", err)
	}

	var protoControllers []*pb.ControllerInfo
	for _, ctrl := range controllers {
		protoControllers = append(protoControllers, &pb.ControllerInfo{
			ControllerId: ctrl.ID,
			Version:      ctrl.Version,
			HttpAddress:  ctrl.HttpAddress,
			LastSeen:     timestamppb.New(ctrl.LastSeen),
		})
	}

	s.logger.Infof("Found %d controllers", len(protoControllers))
	return &pb.ListControllersResponse{
		Controllers: protoControllers,
	}, nil
}

// ListClientsByController handles list clients by controller requests
func (s *ControllerGRPCServer) ListClientsByController(ctx context.Context, req *pb.ListClientsByControllerRequest) (*pb.ListClientsByControllerResponse, error) {
	if req == nil || req.ControllerId == "" {
		return nil, status.Error(codes.InvalidArgument, "controller ID cannot be empty")
	}

	s.logger.Infof("Listing clients for controller: %s", req.ControllerId)

	clients, err := s.controllerRoutingService.ListClientsByController(ctx, req.ControllerId)
	if err != nil {
		s.logger.Errorf("Failed to get clients for controller %s: %v", req.ControllerId, err)
		return nil, status.Errorf(codes.Internal, "failed to get clients: %v", err)
	}

	var protoClients []*pb.ClientInfo
	for _, client := range clients {
		protoClients = append(protoClients, &pb.ClientInfo{
			ClientId: client.ClientID,
			Version:  client.Version,
			LastSeen: timestamppb.New(client.LastSeen),
		})
	}

	s.logger.Infof("Found %d clients for controller %s", len(protoClients), req.ControllerId)
	return &pb.ListClientsByControllerResponse{
		Clients: protoClients,
	}, nil
}

// GetAllRegistryData handles get all controller registry data requests
func (s *ControllerGRPCServer) GetAllRegistryData(ctx context.Context, req *pb.GetAllControllerRegistryDataRequest) (*pb.GetAllControllerRegistryDataResponse, error) {
	s.logger.Infof("Getting all controller registry data")

	data, err := s.controllerRoutingService.ListAllData(ctx)
	if err != nil {
		s.logger.Errorf("Failed to get all controller registry data: %v", err)
		return nil, status.Errorf(codes.Internal, "failed to get registry data: %v", err)
	}

	// Convert to proto
	var protoControllers []*pb.ControllerInfo
	for _, ctrl := range data.Controllers {
		protoControllers = append(protoControllers, &pb.ControllerInfo{
			ControllerId: ctrl.ID,
			Version:      ctrl.Version,
			HttpAddress:  ctrl.HttpAddress,
			LastSeen:     timestamppb.New(ctrl.LastSeen),
		})
	}

	clientsByController := make(map[string]*pb.ClientsData)
	for controllerID, clients := range data.ClientsByController {
		var protoClients []*pb.ClientInfo
		for _, client := range clients {
			protoClients = append(protoClients, &pb.ClientInfo{
				ClientId: client.ClientID,
				Version:  client.Version,
				LastSeen: timestamppb.New(client.LastSeen),
			})
		}
		clientsByController[controllerID] = &pb.ClientsData{
			Clients: protoClients,
		}
	}

	s.logger.Infof("Returning registry data: %d controllers, %d controller-client mappings", len(protoControllers), len(clientsByController))
	return &pb.GetAllControllerRegistryDataResponse{
		Data: &pb.ControllerRegistryData{
			Controllers:         protoControllers,
			ClientsByController: clientsByController,
		},
	}, nil
}

// ControlPlaneGRPCServer implements the gRPC control-plane routing service
type ControlPlaneGRPCServer struct {
	pb.UnimplementedEnvoyRoutingServiceServer // Using legacy proto for now
	controlPlaneRoutingService                *service.RoutingService
	logger                                    *logger.Logger
}

// NewControlPlaneGRPCServer creates a new gRPC routing server instance
func NewControlPlaneGRPCServer(controlPlaneRoutingService *service.RoutingService, logger *logger.Logger) *ControlPlaneGRPCServer {
	return &ControlPlaneGRPCServer{
		controlPlaneRoutingService: controlPlaneRoutingService,
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

	if err := s.controlPlaneRoutingService.UpdateNodeList(ctx, req.ControlPlaneId, nodes); err != nil {
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

// GetAllRegistryData handles get all control plane registry data requests
func (s *ControlPlaneGRPCServer) GetAllRegistryData(ctx context.Context, req *pb.GetAllRegistryDataRequest) (*pb.GetAllRegistryDataResponse, error) {
	s.logger.Infof("Getting all control plane registry data")

	data, err := s.controlPlaneRoutingService.ListAllData(ctx)
	if err != nil {
		s.logger.Errorf("Failed to get all control plane registry data: %v", err)
		return nil, status.Errorf(codes.Internal, "failed to get registry data: %v", err)
	}

	// Convert to proto
	var protoControlPlanes []*pb.ControlPlaneInfo
	for _, cp := range data.ControlPlanes {
		protoControlPlanes = append(protoControlPlanes, &pb.ControlPlaneInfo{
			ControlPlaneId: cp.ID,
			Version:        cp.Version,
			LastSeen:       timestamppb.New(cp.LastSeen),
		})
	}

	nodesByControlPlane := make(map[string]*pb.NodesData)
	for controlPlaneID, nodes := range data.NodesByControlPlane {
		var protoNodes []*pb.NodeInfo
		for _, node := range nodes {
			protoNodes = append(protoNodes, &pb.NodeInfo{
				NodeId:   node.NodeID,
				Version:  node.Version,
				LastSeen: timestamppb.New(node.LastSeen),
			})
		}
		nodesByControlPlane[controlPlaneID] = &pb.NodesData{
			Nodes: protoNodes,
		}
	}

	s.logger.Infof("Returning registry data: %d control planes, %d control-plane-node mappings", len(protoControlPlanes), len(nodesByControlPlane))
	return &pb.GetAllRegistryDataResponse{
		Data: &pb.RegistryData{
			ControlPlanes:       protoControlPlanes,
			NodesByControlPlane: nodesByControlPlane,
		},
	}, nil
}

// ExternalProcessorServer implements Envoy's ext_proc protocol
type ExternalProcessorServer struct {
	ext.UnimplementedExternalProcessorServer
	controllerRoutingService   *service.ControllerRoutingService // For client routing
	controlPlaneRoutingService *service.RoutingService           // For control-plane routing
	logger                     *logger.Logger
	excludedPaths              map[string]bool // For O(1) lookup
}

// NewExternalProcessorServer creates a new external processor server
func NewExternalProcessorServer(controllerRoutingService *service.ControllerRoutingService, controlPlaneRoutingService *service.RoutingService, logger *logger.Logger) *ExternalProcessorServer {
	// Convert excluded paths slice to map for O(1) lookup
	excludedPaths := make(map[string]bool)
	for _, path := range ExcludedPaths {
		excludedPaths[path] = true
	}

	return &ExternalProcessorServer{
		controllerRoutingService:   controllerRoutingService,
		controlPlaneRoutingService: controlPlaneRoutingService,
		logger:                     logger,
		excludedPaths:              excludedPaths,
	}
}

// Process handles Envoy's ext_proc bidirectional stream
func (p *ExternalProcessorServer) Process(stream ext.ExternalProcessor_ProcessServer) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			if err.Error() == "EOF" {
				p.logger.Debugf("Stream closed normally (EOF)")
				return nil
			}
			p.logger.Warnf("Error receiving request: %v", err)
			return err
		}

		// Handle different types of processing requests
		switch req.Request.(type) {
		case *ext.ProcessingRequest_RequestHeaders:
			if err := p.handleRequestHeaders(stream, req.GetRequestHeaders()); err != nil {
				return err
			}
		case *ext.ProcessingRequest_RequestBody:
			if err := p.handleRequestBody(stream, req.GetRequestBody()); err != nil {
				return err
			}
		case *ext.ProcessingRequest_RequestTrailers:
			if err := p.handleRequestTrailers(stream, req.GetRequestTrailers()); err != nil {
				return err
			}
		case *ext.ProcessingRequest_ResponseHeaders:
			if err := p.handleResponseHeaders(stream, req.GetResponseHeaders()); err != nil {
				return err
			}
		case *ext.ProcessingRequest_ResponseBody:
			if err := p.handleResponseBody(stream, req.GetResponseBody()); err != nil {
				return err
			}
		case *ext.ProcessingRequest_ResponseTrailers:
			if err := p.handleResponseTrailers(stream, req.GetResponseTrailers()); err != nil {
				return err
			}
		default:
			p.logger.Errorf("Unknown request type: %T", req.Request)
			// Send continue response for unknown types
			if err := stream.Send(&ext.ProcessingResponse{
				Response: &ext.ProcessingResponse_RequestHeaders{
					RequestHeaders: &ext.HeadersResponse{
						Response: &ext.CommonResponse{
							Status: ext.CommonResponse_CONTINUE,
						},
					},
				},
			}); err != nil {
				return err
			}
		}
	}
}

// handleRequestHeaders processes request headers and adds routing information
func (p *ExternalProcessorServer) handleRequestHeaders(stream ext.ExternalProcessor_ProcessServer, headers *ext.HttpHeaders) error {
	if headers == nil || headers.Headers == nil {
		return stream.Send(p.createContinueResponse())
	}

	// First check path for early exclude
	var path string
	for _, header := range headers.Headers.Headers {
		if header != nil && header.Key == ":path" {
			path = string(header.RawValue)
			break
		}
	}

	// Early return for excluded paths
	if path != "" {
		// First check exact path match
		if p.excludedPaths[path] {
			return stream.Send(p.createContinueResponse())
		}

		// Then check if it's a gRPC service path that should be excluded
		// gRPC path format: /package.service/method
		parts := strings.Split(path, "/")
		if len(parts) > 2 {
			servicePath := strings.Join(parts[1:len(parts)-1], "/") // Get everything except the method
			if p.excludedPaths[servicePath] {
				return stream.Send(p.createContinueResponse())
			}
		}
	}

	// Check if this is a gRPC request
	contentType := p.getHeaderFromMap(headers.Headers, HeaderContentType)
	method := p.getHeaderFromMap(headers.Headers, HeaderMethod)

	isGRPC := strings.HasPrefix(contentType, "application/grpc")

	// If not gRPC, continue without processing
	if !isGRPC {
		p.logger.Infof("Non-gRPC request received: %s %s - continuing without processing", method, path)
		return stream.Send(p.createContinueResponse())
	}

	// For gRPC requests, proceed with routing
	p.logger.Infof("=== Processing gRPC Request ===")
	p.logger.Infof("Path: %s", path)
	p.logger.Infof("Method: %s", method)
	p.logger.Infof("Content-Type: %s", contentType)

	// Extract routing information from headers
	nodeID := p.getHeaderFromMap(headers.Headers, HeaderNodeID)
	clientID := p.getHeaderFromMap(headers.Headers, HeaderClientID)
	version := p.getHeaderFromMap(headers.Headers, HeaderVersion)

	// Also check for alternative header names
	if nodeID == "" {
		nodeID = p.getHeaderFromMap(headers.Headers, "nodeid")
	}
	if clientID == "" {
		clientID = p.getHeaderFromMap(headers.Headers, "client-id")
	}
	if version == "" {
		version = p.getHeaderFromMap(headers.Headers, "envoy-version")
	}

	p.logger.Infof("NodeID: %s", nodeID)
	p.logger.Infof("ClientID: %s", clientID)
	p.logger.Infof("Version: %s", version)

	// Determine routing type and get target cluster
	var targetCluster string
	if clientID != "" {
		// Controller routing (client requests)
		targetCluster = p.resolveControllerCluster(version, clientID)
		p.logger.Infof("Controller routing: %s:%s -> %s", clientID, version, targetCluster)
	} else if nodeID != "" && version != "" {
		// Control-plane routing (envoy node requests)
		targetCluster = p.resolveControlPlaneCluster(version, nodeID)
		p.logger.Infof("Control-plane routing: %s:%s -> %s", nodeID, version, targetCluster)
	}

	// Only proceed with mutation if we found a target cluster
	if targetCluster != "" {
		p.logger.Infof("=== Adding Routing Header ===")
		p.logger.Infof("Original headers:")
		for _, h := range headers.Headers.Headers {
			if h != nil {
				p.logger.Infof("  %s: %s", h.Key, string(h.RawValue))
			}
		}

		// Create the response with only the new header
		response := &ext.ProcessingResponse{
			Response: &ext.ProcessingResponse_RequestHeaders{
				RequestHeaders: &ext.HeadersResponse{
					Response: &ext.CommonResponse{
						Status:          ext.CommonResponse_CONTINUE,
						ClearRouteCache: true,
						HeaderMutation: &ext.HeaderMutation{
							SetHeaders: []*core.HeaderValueOption{
								{
									Header: &core.HeaderValue{
										Key:      HeaderTargetCluster,
										RawValue: []byte(targetCluster), // Use RawValue instead of Value
									},
									// Don't append, just set
									Append: &wrapperspb.BoolValue{Value: false},
								},
							},
						},
					},
				},
			},
		}

		p.logger.Infof("=== Adding Routing Header ===")
		p.logger.Infof("Status: CONTINUE")
		p.logger.Infof("Adding Header: %s = %s (as RawValue)", HeaderTargetCluster, targetCluster)
		p.logger.Infof("Header Mutation: %+v", response.GetResponse().(*ext.ProcessingResponse_RequestHeaders).RequestHeaders.Response.HeaderMutation)
		p.logger.Infof("============================")

		// Send the response
		err := stream.Send(response)
		if err != nil {
			p.logger.Errorf("Failed to send response: %v", err)
			return err
		}

		p.logger.Infof("Response sent successfully")
		return nil
	}

	p.logger.Warnf("No routing decision found for gRPC request, continuing without routing")
	return stream.Send(p.createContinueResponse())
}

// createContinueResponse creates a simple continue response without modifications
func (p *ExternalProcessorServer) createContinueResponse() *ext.ProcessingResponse {
	return &ext.ProcessingResponse{
		Response: &ext.ProcessingResponse_RequestHeaders{
			RequestHeaders: &ext.HeadersResponse{
				Response: &ext.CommonResponse{
					Status: ext.CommonResponse_CONTINUE,
				},
			},
		},
	}
}

// handleRequestBody processes request body (if needed)
func (p *ExternalProcessorServer) handleRequestBody(stream ext.ExternalProcessor_ProcessServer, body *ext.HttpBody) error {
	p.logger.Debugf("Processing request body (length: %d)", len(body.Body))

	// For now, just continue without modification
	response := &ext.ProcessingResponse{
		Response: &ext.ProcessingResponse_RequestBody{
			RequestBody: &ext.BodyResponse{
				Response: &ext.CommonResponse{
					Status: ext.CommonResponse_CONTINUE,
				},
			},
		},
	}

	return stream.Send(response)
}

// handleRequestTrailers processes request trailers
func (p *ExternalProcessorServer) handleRequestTrailers(stream ext.ExternalProcessor_ProcessServer, _ *ext.HttpTrailers) error {
	p.logger.Debugf("Processing request trailers")

	response := &ext.ProcessingResponse{
		Response: &ext.ProcessingResponse_RequestTrailers{
			RequestTrailers: &ext.TrailersResponse{
				HeaderMutation: &ext.HeaderMutation{},
			},
		},
	}

	return stream.Send(response)
}

// handleResponseHeaders processes response headers
func (p *ExternalProcessorServer) handleResponseHeaders(stream ext.ExternalProcessor_ProcessServer, _ *ext.HttpHeaders) error {
	// Just continue without any processing since response_header_mode is SKIP
	return stream.Send(&ext.ProcessingResponse{
		Response: &ext.ProcessingResponse_ResponseHeaders{
			ResponseHeaders: &ext.HeadersResponse{
				Response: &ext.CommonResponse{
					Status: ext.CommonResponse_CONTINUE,
				},
			},
		},
	})
}

// handleResponseBody processes response body
func (p *ExternalProcessorServer) handleResponseBody(stream ext.ExternalProcessor_ProcessServer, body *ext.HttpBody) error {
	p.logger.Debugf("Processing response body (length: %d)", len(body.Body))

	response := &ext.ProcessingResponse{
		Response: &ext.ProcessingResponse_ResponseBody{
			ResponseBody: &ext.BodyResponse{
				Response: &ext.CommonResponse{
					Status: ext.CommonResponse_CONTINUE,
				},
			},
		},
	}

	return stream.Send(response)
}

// handleResponseTrailers processes response trailers
func (p *ExternalProcessorServer) handleResponseTrailers(stream ext.ExternalProcessor_ProcessServer, _ *ext.HttpTrailers) error {
	p.logger.Debugf("Processing response trailers")

	response := &ext.ProcessingResponse{
		Response: &ext.ProcessingResponse_ResponseTrailers{
			ResponseTrailers: &ext.TrailersResponse{
				HeaderMutation: &ext.HeaderMutation{},
			},
		},
	}

	return stream.Send(response)
}

// getHeaderFromMap extracts header value from Envoy HeaderMap
func (p *ExternalProcessorServer) getHeaderFromMap(headers *core.HeaderMap, key string) string {
	if headers == nil || headers.Headers == nil {
		return ""
	}

	// Envoy ext_proc'da header'lar raw_value field'ında geliyor
	for _, header := range headers.Headers {
		if header != nil && strings.EqualFold(header.Key, key) {
			return string(header.RawValue)
		}
	}

	return ""
}

// resolveControllerCluster determines the target controller cluster based on client ID and version
func (p *ExternalProcessorServer) resolveControllerCluster(version, clientID string) string {
	p.logger.Debugf("Resolving controller cluster for ClientID: %s, Version: %s", clientID, version)

	if clientID == "" {
		p.logger.Errorf("ClientID cannot be empty")
		return ""
	}

	// Use controller routing service to find appropriate controller
	ctx := context.Background()

	// First try to get existing mapping
	controller, err := p.controllerRoutingService.GetControllerCluster(ctx, clientID, version)
	if err == nil && controller != nil {
		p.logger.Infof("Found existing controller mapping: %s -> %s", clientID, controller.ID)
		return controller.ID
	}

	// If no mapping exists or error occurred, get least loaded controller
	controller, err = p.controllerRoutingService.GetLeastLoadedController(ctx, version)
	if err != nil {
		p.logger.Errorf("Failed to get least loaded controller: %v", err)
		return ""
	}

	p.logger.Infof("Selected least loaded controller: %s (for new client %s)", controller.ID, clientID)
	return controller.ID
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

	// First try to get existing mapping
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

	p.logger.Infof("Selected least loaded control-plane: %s (for new node %s)", controlPlane.ID, nodeID)
	return controlPlane.ID
}

// StartGRPCServer starts the gRPC server
func StartGRPCServer(address string, controllerRoutingService *service.ControllerRoutingService, controlPlaneRoutingService *service.RoutingService, logger *logger.Logger) error {
	lis, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("failed to listen on address %s: %w", address, err)
	}

	grpcServer := grpc.NewServer(
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle:     5 * time.Minute,
			MaxConnectionAge:      10 * time.Minute,
			MaxConnectionAgeGrace: 30 * time.Second,
			Time:                  60 * time.Second,
			Timeout:               30 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             20 * time.Second, // Client can ping at most every 20 seconds
			PermitWithoutStream: false,            // Don't allow pings without streams
		}),
	)

	// Register controller routing service
	controllerGRPCServer := NewControllerGRPCServer(controllerRoutingService, logger)
	pb.RegisterControllerRoutingServiceServer(grpcServer, controllerGRPCServer)

	// Register control-plane routing service
	controlPlaneGRPCServer := NewControlPlaneGRPCServer(controlPlaneRoutingService, logger)
	pb.RegisterEnvoyRoutingServiceServer(grpcServer, controlPlaneGRPCServer)

	// Register external processor service (for Envoy ext_proc)
	extProcessorServer := NewExternalProcessorServer(controllerRoutingService, controlPlaneRoutingService, logger)
	ext.RegisterExternalProcessorServer(grpcServer, extProcessorServer)

	logger.Infof("gRPC server starting on address %s (Controller + Control-Plane + ExternalProcessor services)", address)

	if err := grpcServer.Serve(lis); err != nil {
		return fmt.Errorf("failed to serve gRPC: %w", err)
	}

	return nil
}
