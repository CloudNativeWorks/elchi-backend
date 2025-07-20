package server

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/registry/models"
	"github.com/CloudNativeWorks/elchi-backend/registry/service"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
	core "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/core/v3"
	ext "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Constants for header keys
const (
	HeaderNodeID         = "nodeid"
	HeaderVersion        = "envoy-version"
	HeaderTargetCluster  = "x-target-cluster"
	HeaderRoutingService = "x-routing-service"
)

// RegistryGRPCServer implements the gRPC registry service
type RegistryGRPCServer struct {
	pb.UnimplementedControllerServiceServer
	registryService *service.RegistryService
	logger          *logger.Logger
}

// NewRegistryGRPCServer creates a new gRPC server instance
func NewRegistryGRPCServer(registryService *service.RegistryService, logger *logger.Logger) *RegistryGRPCServer {
	return &RegistryGRPCServer{
		registryService: registryService,
		logger:          logger,
	}
}

// RegisterController handles controller registration
func (s *RegistryGRPCServer) RegisterController(ctx context.Context, req *pb.ControllerInfo) (*pb.ControllerResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request cannot be nil")
	}

	// Convert proto to internal model
	controllerInfo := &models.ControllerInfo{
		ID:          req.ControllerId,
		GRPCAddress: req.GrpcAddress,
	}

	if err := s.registryService.RegisterController(ctx, controllerInfo); err != nil {
		s.logger.Errorf("Failed to register controller %s: %v", req.ControllerId, err)
		return &pb.ControllerResponse{
			Success: "failed: " + err.Error(),
		}, nil
	}

	s.logger.Infof("Controller registered successfully: %s", req.ControllerId)
	return &pb.ControllerResponse{
		Success: "controller registered successfully",
	}, nil
}

// GetClientLocation handles client location queries
func (s *RegistryGRPCServer) GetClientLocation(ctx context.Context, req *pb.ClientLocationRequest) (*pb.ClientLocationResponse, error) {
	if req == nil || req.ClientId == "" {
		return nil, status.Error(codes.InvalidArgument, "client ID cannot be empty")
	}

	location, err := s.registryService.GetClientLocation(ctx, req.ClientId)
	if err != nil {
		s.logger.Debugf("Client location not found: %s", req.ClientId)
		return &pb.ClientLocationResponse{
			Found: false,
		}, nil
	}

	// Get controller GRPC address
	controller, err := s.registryService.GetController(ctx, location.ControllerID)
	if err != nil {
		s.logger.Errorf("Controller not found for client %s: %v", req.ClientId, err)
		return &pb.ClientLocationResponse{
			Found: false,
		}, nil
	}

	return &pb.ClientLocationResponse{
		Found:          true,
		ControllerId:   location.ControllerID,
		ControllerFqdn: controller.GRPCAddress,
	}, nil
}

// SetClientLocation handles setting client location
func (s *RegistryGRPCServer) SetClientLocation(ctx context.Context, req *pb.SetClientLocationRequest) (*pb.SetClientLocationResponse, error) {
	if req == nil || req.ClientId == "" || req.ControllerId == "" {
		return nil, status.Error(codes.InvalidArgument, "client ID and controller ID cannot be empty")
	}

	if err := s.registryService.SetClientLocation(ctx, req.ClientId, req.ControllerId); err != nil {
		s.logger.Errorf("Failed to set client location for %s: %v", req.ClientId, err)
		return &pb.SetClientLocationResponse{
			Success: "failed: " + err.Error(),
		}, nil
	}

	s.logger.Infof("Client location set: %s -> %s", req.ClientId, req.ControllerId)
	return &pb.SetClientLocationResponse{
		Success: "client location set successfully",
	}, nil
}

// RequestClientRefresh asks a controller to refresh its client list
func (s *RegistryGRPCServer) RequestClientRefresh(ctx context.Context, req *pb.ClientRefreshRequest) (*pb.ClientRefreshResponse, error) {
	if req == nil || req.ControllerId == "" {
		return nil, status.Error(codes.InvalidArgument, "controller ID cannot be empty")
	}

	// Verify controller exists
	controller, err := s.registryService.GetController(ctx, req.ControllerId)
	if err != nil {
		s.logger.Errorf("Controller not found for refresh request: %s", req.ControllerId)
		return &pb.ClientRefreshResponse{
			Success:     "failed: controller not found",
			ClientCount: 0,
		}, nil
	}

	// TODO: This would actually call the controller to refresh its clients
	// For now, just log and return success
	s.logger.Infof("Client refresh requested for controller %s at %s", controller.ID, controller.GRPCAddress)

	return &pb.ClientRefreshResponse{
		Success:     "refresh request sent",
		ClientCount: 0, // Will be updated when controller responds
	}, nil
}

// IsControllerRegistered checks if a controller is registered
func (s *RegistryGRPCServer) IsControllerRegistered(ctx context.Context, req *pb.IsControllerRegisteredRequest) (*pb.IsControllerRegisteredResponse, error) {
	if req == nil || req.ControllerId == "" {
		return nil, status.Error(codes.InvalidArgument, "controller ID cannot be empty")
	}

	// Check if controller exists
	_, err := s.registryService.GetController(ctx, req.ControllerId)
	if err != nil {
		s.logger.Debugf("Controller registration check failed: %s", req.ControllerId)
		return &pb.IsControllerRegisteredResponse{
			Registered: false,
		}, nil
	}

	return &pb.IsControllerRegisteredResponse{
		Registered: true,
	}, nil
}

// BulkSetClientLocations sets multiple client locations efficiently
func (s *RegistryGRPCServer) BulkSetClientLocations(ctx context.Context, req *pb.BulkSetClientLocationsRequest) (*pb.BulkSetClientLocationsResponse, error) {
	if req == nil || req.ControllerId == "" {
		return nil, status.Error(codes.InvalidArgument, "controller ID cannot be empty")
	}

	if len(req.ClientIds) == 0 {
		return &pb.BulkSetClientLocationsResponse{
			Success:      true,
			Error:        "",
			UpdatedCount: 0,
		}, nil
	}

	// Verify controller exists
	_, err := s.registryService.GetController(ctx, req.ControllerId)
	if err != nil {
		s.logger.Errorf("Controller not found for bulk client update: %s", req.ControllerId)
		return &pb.BulkSetClientLocationsResponse{
			Success:      false,
			Error:        "controller not found",
			UpdatedCount: 0,
		}, nil
	}

	// Set each client location
	successCount := int32(0)
	for _, clientID := range req.ClientIds {
		if err := s.registryService.SetClientLocation(ctx, clientID, req.ControllerId); err != nil {
			s.logger.Errorf("Failed to set location for client %s: %v", clientID, err)
			// Continue with other clients, don't fail completely
		} else {
			successCount++
		}
	}

	s.logger.Infof("Bulk client location update: %d/%d clients updated for controller %s",
		successCount, len(req.ClientIds), req.ControllerId)

	return &pb.BulkSetClientLocationsResponse{
		Success:      true,
		Error:        "",
		UpdatedCount: successCount,
	}, nil
}

// RoutingGRPCServer implements the gRPC routing service
type RoutingGRPCServer struct {
	pb.UnimplementedEnvoyRoutingServiceServer
	routingService *service.RoutingService
	logger         *logger.Logger
}

// NewRoutingGRPCServer creates a new gRPC routing server instance
func NewRoutingGRPCServer(routingService *service.RoutingService, logger *logger.Logger) *RoutingGRPCServer {
	return &RoutingGRPCServer{
		routingService: routingService,
		logger:         logger,
	}
}

// RegisterControlPlane handles control plane registration
func (s *RoutingGRPCServer) RegisterControlPlane(ctx context.Context, req *pb.RegisterControlPlaneRequest) (*pb.RegisterControlPlaneResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request cannot be nil")
	}

	// Convert proto to internal model
	controlPlane := &models.ControlPlane{
		ID:       req.ControlPlaneId,
		Version:  req.Version,
		LastSeen: time.Now(),
	}

	if err := s.routingService.RegisterControlPlane(ctx, controlPlane); err != nil {
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
func (s *RoutingGRPCServer) GetControlPlaneCluster(ctx context.Context, req *pb.GetControlPlaneClusterRequest) (*pb.GetControlPlaneClusterResponse, error) {
	if req == nil || req.NodeId == "" || req.Version == "" {
		return nil, status.Error(codes.InvalidArgument, "node ID and version cannot be empty")
	}

	controlPlane, err := s.routingService.GetControlPlaneCluster(ctx, req.NodeId, req.Version)
	if err != nil {
		s.logger.Errorf("Failed to find control plane for node %s version %s: %v", req.NodeId, req.Version, err)
		return &pb.GetControlPlaneClusterResponse{
			Found: false,
		}, nil
	}

	s.logger.Infof("Routing decision: %s:%s -> %s", req.NodeId, req.Version, controlPlane.ID)
	return &pb.GetControlPlaneClusterResponse{
		Found:          true,
		ControlPlaneId: controlPlane.ID,
	}, nil
}

// NotifySnapshotDelivered handles snapshot delivery notifications
func (s *RoutingGRPCServer) NotifySnapshotDelivered(ctx context.Context, req *pb.NotifySnapshotDeliveredRequest) (*pb.NotifySnapshotDeliveredResponse, error) {
	if req == nil || req.ControlPlaneId == "" || req.NodeId == "" || req.Version == "" {
		return nil, status.Error(codes.InvalidArgument, "control plane ID, node ID and version cannot be empty")
	}

	if err := s.routingService.NotifySnapshotDelivered(ctx, req.ControlPlaneId, req.NodeId, req.Version); err != nil {
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
func (s *RoutingGRPCServer) UpdateNodeList(ctx context.Context, req *pb.UpdateNodeListRequest) (*pb.UpdateNodeListResponse, error) {
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

	if err := s.routingService.UpdateNodeList(ctx, req.ControlPlaneId, nodes); err != nil {
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
func (s *RoutingGRPCServer) HealthCheck(ctx context.Context, req *pb.HealthCheckRequest) (*pb.HealthCheckResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request cannot be nil")
	}

	s.logger.Debugf("Health check request from: %s", req.Service)

	return &pb.HealthCheckResponse{
		Healthy:   true,
		Message:   "routing service is healthy",
		Timestamp: timestamppb.New(time.Now()),
	}, nil
}

// ExternalProcessorServer implements Envoy's ext_proc protocol
type ExternalProcessorServer struct {
	ext.UnimplementedExternalProcessorServer
	routingService *service.RoutingService
	logger         *logger.Logger
}

// NewExternalProcessorServer creates a new external processor server
func NewExternalProcessorServer(routingService *service.RoutingService, logger *logger.Logger) *ExternalProcessorServer {
	return &ExternalProcessorServer{
		routingService: routingService,
		logger:         logger,
	}
}

// Process handles Envoy's ext_proc bidirectional stream
func (p *ExternalProcessorServer) Process(stream ext.ExternalProcessor_ProcessServer) error {
	p.logger.Infof("Started ext_proc stream")
	defer p.logger.Infof("Ended ext_proc stream")

	for {
		req, err := stream.Recv()
		if err != nil {
			p.logger.Errorf("Error receiving request: %v", err)
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
	p.logger.Debugf("Processing request headers")

	// Extract node information from ADS initial_metadata headers
	nodeID := p.getHeaderFromMap(headers.Headers, HeaderNodeID)
	version := p.getHeaderFromMap(headers.Headers, HeaderVersion)

	// Also check for alternative header names that might be used
	if nodeID == "" {
		nodeID = p.getHeaderFromMap(headers.Headers, "x-node-id")
	}
	if version == "" {
		version = p.getHeaderFromMap(headers.Headers, "x-envoy-version")
	}

	p.logger.Infof("Request from NodeID: %s, Version: %s", nodeID, version)

	// Get routing decision
	targetCluster := p.resolveCluster(version, nodeID)

	// Create response with routing information
	response := &ext.ProcessingResponse{
		Response: &ext.ProcessingResponse_RequestHeaders{
			RequestHeaders: &ext.HeadersResponse{
				Response: &ext.CommonResponse{
					Status: ext.CommonResponse_CONTINUE,
				},
			},
		},
	}

	// Only add routing headers if we found a target cluster
	if targetCluster != "" {
		response.GetRequestHeaders().Response.HeaderMutation = &ext.HeaderMutation{
			SetHeaders: []*core.HeaderValueOption{
				{
					Header: &core.HeaderValue{
						Key:   HeaderTargetCluster,
						Value: targetCluster,
					},
					Append: &wrapperspb.BoolValue{Value: false},
				},
				{
					Header: &core.HeaderValue{
						Key:   HeaderRoutingService,
						Value: "elchi-registry",
					},
					Append: &wrapperspb.BoolValue{Value: false},
				},
			},
		}
		p.logger.Infof("Sending routing response: cluster=%s for node=%s version=%s", targetCluster, nodeID, version)
	} else {
		p.logger.Errorf("No routing decision found for node=%s version=%s, continuing without routing", nodeID, version)
	}

	return stream.Send(response)
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
	p.logger.Debugf("Processing response headers")

	// Add response processing metadata
	response := &ext.ProcessingResponse{
		Response: &ext.ProcessingResponse_ResponseHeaders{
			ResponseHeaders: &ext.HeadersResponse{
				Response: &ext.CommonResponse{
					Status: ext.CommonResponse_CONTINUE,
					HeaderMutation: &ext.HeaderMutation{
						SetHeaders: []*core.HeaderValueOption{
							{
								Header: &core.HeaderValue{
									Key:   "x-processed-by",
									Value: "elchi-registry-ext-proc",
								},
								Append: &wrapperspb.BoolValue{Value: false},
							},
						},
					},
				},
			},
		},
	}

	return stream.Send(response)
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
	if headers == nil {
		return ""
	}

	p.logger.Debugf("Looking for header key: %s", key)
	for _, h := range headers.Headers {
		p.logger.Debugf("Found header: %s = %s", h.Key, h.Value)
		if strings.EqualFold(h.Key, key) {
			p.logger.Debugf("Matched header: %s = %s", h.Key, h.Value)
			return h.Value
		}
	}

	p.logger.Debugf("Header key '%s' not found", key)
	return ""
}

// resolveCluster determines the target cluster based on version and nodeID
func (p *ExternalProcessorServer) resolveCluster(version, nodeID string) string {
	p.logger.Debugf("Resolving cluster for NodeID: %s, Version: %s", nodeID, version)

	if version == "" || nodeID == "" {
		p.logger.Errorf("Missing version or nodeID: version=%s, nodeID=%s", version, nodeID)
		return ""
	}

	// Use routing service to find appropriate control plane
	// Priority: 1. Control plane that already has this nodeID
	//          2. Available control plane with exact version match
	ctx := context.Background()
	controlPlane, err := p.routingService.GetControlPlaneCluster(ctx, nodeID, version)
	if err != nil {
		p.logger.Errorf("Failed to resolve cluster for %s:%s: %v", nodeID, version, err)
		// Return empty string to indicate no routing decision
		// Envoy will handle this as no routing change
		return ""
	}

	p.logger.Infof("Routing decision: %s:%s -> %s", nodeID, version, controlPlane.ID)
	return controlPlane.ID
}

// StartGRPCServer starts the gRPC server
func StartGRPCServer(address string, registryService *service.RegistryService, routingService *service.RoutingService, logger *logger.Logger) error {
	lis, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("failed to listen on address %s: %w", address, err)
	}

	grpcServer := grpc.NewServer()

	// Register registry service
	registryGRPCServer := NewRegistryGRPCServer(registryService, logger)
	pb.RegisterControllerServiceServer(grpcServer, registryGRPCServer)

	// Register routing service
	routingGRPCServer := NewRoutingGRPCServer(routingService, logger)
	pb.RegisterEnvoyRoutingServiceServer(grpcServer, routingGRPCServer)

	// Register external processor service (for Envoy ext_proc)
	extProcessorServer := NewExternalProcessorServer(routingService, logger)
	ext.RegisterExternalProcessorServer(grpcServer, extProcessorServer)

	logger.Infof("gRPC server starting on address %s (Registry + Routing + ExternalProcessor services)", address)

	if err := grpcServer.Serve(lis); err != nil {
		return fmt.Errorf("failed to serve gRPC: %w", err)
	}

	return nil
}
