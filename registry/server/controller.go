package server

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/registry/models"
	"github.com/CloudNativeWorks/elchi-backend/registry/service"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ControllerGRPCServer implements the gRPC controller routing service
type ControllerGRPCServer struct {
	bridge.UnimplementedControllerRoutingServiceServer
	controllerRoutingService *service.ControllerRoutingService
	extProcessorServer       *ExternalProcessorServer
	logger                   *logger.Logger
	appConfig                *config.AppConfig
}

// NewControllerGRPCServer creates a new gRPC server instance for controller routing
func NewControllerGRPCServer(controllerRoutingService *service.ControllerRoutingService, extProcessorServer *ExternalProcessorServer, logger *logger.Logger, appConfig *config.AppConfig) *ControllerGRPCServer {
	return &ControllerGRPCServer{
		controllerRoutingService: controllerRoutingService,
		extProcessorServer:       extProcessorServer,
		logger:                   logger,
		appConfig:                appConfig,
	}
}

// RegisterController handles controller registration
func (s *ControllerGRPCServer) RegisterController(ctx context.Context, req *bridge.RegisterControllerRequest) (*bridge.RegisterControllerResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request cannot be nil")
	}

	serviceName := helper.ToK8sServiceName(req.ControllerId, s.appConfig.ElchiNamespace)

	// Convert proto to internal model
	controllerInfo := &models.ControllerInfo{
		ID:          req.ControllerId,
		Version:     req.Version,
		HTTPAddress: fmt.Sprintf("%s:8099", serviceName),
		LastSeen:    time.Now(),
	}

	if err := s.controllerRoutingService.RegisterController(ctx, controllerInfo); err != nil {
		s.logger.Errorf("Failed to register controller %s: %v", req.ControllerId, err)
		return &bridge.RegisterControllerResponse{
			Success: false,
			Message: "failed: " + err.Error(),
		}, nil
	}

	s.logger.Infof("Controller registered successfully: %s version %s", req.ControllerId, req.Version)
	return &bridge.RegisterControllerResponse{
		Success: true,
		Message: "controller registered successfully",
	}, nil
}

// GetControllerCluster handles controller cluster routing requests
func (s *ControllerGRPCServer) GetControllerCluster(ctx context.Context, req *bridge.GetControllerClusterRequest) (*bridge.GetControllerClusterResponse, error) {
	if req == nil || req.ClientId == "" {
		return nil, status.Error(codes.InvalidArgument, "client ID cannot be empty")
	}

	// Log the search type
	if req.Version == "" {
		s.logger.Infof("Version-agnostic controller search for client: %s", req.ClientId)
	} else {
		s.logger.Infof("Version-specific controller search for client: %s version: %s", req.ClientId, req.Version)
	}

	controller, err := s.controllerRoutingService.GetControllerCluster(ctx, req.ClientId, req.Version)
	if err != nil {
		if req.Version == "" {
			s.logger.Errorf("Failed to find controller for client %s (version-agnostic): %v", req.ClientId, err)
		} else {
			s.logger.Errorf("Failed to find controller for client %s version %s: %v", req.ClientId, req.Version, err)
		}
		return &bridge.GetControllerClusterResponse{
			Found: false,
		}, nil
	}

	if req.Version == "" {
		s.logger.Infof("Controller routing decision (version-agnostic): %s -> %s", req.ClientId, controller.ID)
	} else {
		s.logger.Infof("Controller routing decision: %s:%s -> %s", req.ClientId, req.Version, controller.ID)
	}

	return &bridge.GetControllerClusterResponse{
		Found:        true,
		ControllerId: controller.ID,
	}, nil
}

// NotifyClientConnected handles client connection notifications
func (s *ControllerGRPCServer) NotifyClientConnected(ctx context.Context, req *bridge.NotifyClientConnectedRequest) (*bridge.NotifyClientConnectedResponse, error) {
	if req == nil || req.ControllerId == "" || req.ClientId == "" || req.Version == "" {
		return nil, status.Error(codes.InvalidArgument, "controller ID, client ID and version cannot be empty")
	}

	if err := s.controllerRoutingService.NotifyClientConnected(ctx, req.ControllerId, req.ClientId, req.Version, s.appConfig.ElchiNamespace); err != nil {
		s.logger.Errorf("Failed to notify client connected: %v", err)
		return &bridge.NotifyClientConnectedResponse{
			Success: false,
			Message: "failed: " + err.Error(),
		}, nil
	}

	// Note: Pending assignment will be cleared automatically when real mapping is found
	s.logger.Infof("Client connected notification processed: %s -> %s (version: %s)", req.ControllerId, req.ClientId, req.Version)

	return &bridge.NotifyClientConnectedResponse{
		Success: true,
		Message: "client connected notification processed",
	}, nil
}

// NotifyClientDisconnected handles client disconnection notifications
func (s *ControllerGRPCServer) NotifyClientDisconnected(ctx context.Context, req *bridge.NotifyClientDisconnectedRequest) (*bridge.NotifyClientDisconnectedResponse, error) {
	if req == nil || req.ControllerId == "" || req.ClientId == "" || req.Version == "" {
		return nil, status.Error(codes.InvalidArgument, "controller ID, client ID and version cannot be empty")
	}

	if err := s.controllerRoutingService.NotifyClientDisconnected(ctx, req.ControllerId, req.ClientId, req.Version); err != nil {
		s.logger.Errorf("Failed to notify client disconnected: %v", err)
		return &bridge.NotifyClientDisconnectedResponse{
			Success: false,
			Message: "failed: " + err.Error(),
		}, nil
	}

	s.logger.Infof("Client disconnected notification processed: %s -> %s (version: %s)", req.ControllerId, req.ClientId, req.Version)
	return &bridge.NotifyClientDisconnectedResponse{
		Success: true,
		Message: "client disconnected notification processed",
	}, nil
}

// UpdateClientList handles bulk client list updates
func (s *ControllerGRPCServer) UpdateClientList(ctx context.Context, req *bridge.UpdateClientListRequest) (*bridge.UpdateClientListResponse, error) {
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

	if err := s.controllerRoutingService.UpdateClientList(ctx, req.ControllerId, clients, s.appConfig.ElchiNamespace); err != nil {
		s.logger.Errorf("Failed to update client list for controller %s: %v", req.ControllerId, err)
		return &bridge.UpdateClientListResponse{
			Success:      false,
			Message:      "failed: " + err.Error(),
			UpdatedCount: 0,
		}, nil
	}

	s.logger.Infof("Client list updated for controller %s: %d clients", req.ControllerId, len(clients))

	// G115 fix: Safe conversion from int to int32 with overflow check
	clientCount := len(clients)
	var updatedCount int32
	if clientCount > math.MaxInt32 {
		// This is extremely unlikely (would need > 2 billion clients)
		s.logger.Warnf("Client count %d exceeds int32 max, capping at MaxInt32", clientCount)
		updatedCount = math.MaxInt32
	} else {
		updatedCount = int32(clientCount) // #nosec G115 - overflow checked above
	}

	return &bridge.UpdateClientListResponse{
		Success:      true,
		Message:      "client list updated successfully",
		UpdatedCount: updatedCount,
	}, nil
}

// HealthCheck handles health check requests
func (s *ControllerGRPCServer) HealthCheck(ctx context.Context, req *bridge.ControllerHealthCheckRequest) (*bridge.ControllerHealthCheckResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request cannot be nil")
	}

	s.logger.Debugf("Controller health check request from: %s", req.Service)

	return &bridge.ControllerHealthCheckResponse{
		Healthy:   true,
		Message:   "controller routing service is healthy",
		Timestamp: timestamppb.New(time.Now()),
	}, nil
}

// ListControllers handles list controllers requests
func (s *ControllerGRPCServer) ListControllers(ctx context.Context, req *bridge.ListControllersRequest) (*bridge.ListControllersResponse, error) {
	s.logger.Infof("Listing all controllers")

	controllers, err := s.controllerRoutingService.ListControllers(ctx)
	if err != nil {
		s.logger.Errorf("Failed to list controllers: %v", err)
		return nil, status.Errorf(codes.Internal, "failed to list controllers: %v", err)
	}

	var protoControllers []*bridge.ControllerInfo
	for _, ctrl := range controllers {
		protoControllers = append(protoControllers, &bridge.ControllerInfo{
			ControllerId: ctrl.ID,
			Version:      ctrl.Version,
			HttpAddress:  ctrl.HTTPAddress,
			LastSeen:     timestamppb.New(ctrl.LastSeen),
		})
	}

	s.logger.Infof("Found %d controllers", len(protoControllers))
	return &bridge.ListControllersResponse{
		Controllers: protoControllers,
	}, nil
}

// ListClientsByController handles list clients by controller requests
func (s *ControllerGRPCServer) ListClientsByController(ctx context.Context, req *bridge.ListClientsByControllerRequest) (*bridge.ListClientsByControllerResponse, error) {
	if req == nil || req.ControllerId == "" {
		return nil, status.Error(codes.InvalidArgument, "controller ID cannot be empty")
	}

	s.logger.Infof("Listing clients for controller: %s", req.ControllerId)

	clients, err := s.controllerRoutingService.ListClientsByController(ctx, req.ControllerId)
	if err != nil {
		s.logger.Errorf("Failed to get clients for controller %s: %v", req.ControllerId, err)
		return nil, status.Errorf(codes.Internal, "failed to get clients: %v", err)
	}

	var protoClients []*bridge.ClientInfo
	for _, client := range clients {
		protoClients = append(protoClients, &bridge.ClientInfo{
			ClientId: client.ClientID,
			Version:  client.Version,
			LastSeen: timestamppb.New(client.LastSeen),
		})
	}

	s.logger.Infof("Found %d clients for controller %s", len(protoClients), req.ControllerId)
	return &bridge.ListClientsByControllerResponse{
		Clients: protoClients,
	}, nil
}

// GetAllRegistryData handles get all controller registry data requests
func (s *ControllerGRPCServer) GetAllRegistryData(ctx context.Context, req *bridge.GetAllControllerRegistryDataRequest) (*bridge.GetAllControllerRegistryDataResponse, error) {
	s.logger.Infof("Getting all controller registry data")

	data, err := s.controllerRoutingService.ListAllData(ctx)
	if err != nil {
		s.logger.Errorf("Failed to get all controller registry data: %v", err)
		return nil, status.Errorf(codes.Internal, "failed to get registry data: %v", err)
	}

	// Convert to proto
	var protoControllers []*bridge.ControllerInfo
	for _, ctrl := range data.Controllers {
		protoControllers = append(protoControllers, &bridge.ControllerInfo{
			ControllerId: ctrl.ID,
			Version:      ctrl.Version,
			HttpAddress:  ctrl.HTTPAddress,
			LastSeen:     timestamppb.New(ctrl.LastSeen),
		})
	}

	clientsByController := make(map[string]*bridge.ClientsData)
	for controllerID, clients := range data.ClientsByController {
		var protoClients []*bridge.ClientInfo
		for _, client := range clients {
			protoClients = append(protoClients, &bridge.ClientInfo{
				ClientId: client.ClientID,
				Version:  client.Version,
				LastSeen: timestamppb.New(client.LastSeen),
			})
		}
		clientsByController[controllerID] = &bridge.ClientsData{
			Clients: protoClients,
		}
	}

	s.logger.Infof("Returning registry data: %d controllers, %d controller-client mappings", len(protoControllers), len(clientsByController))
	return &bridge.GetAllControllerRegistryDataResponse{
		Data: &bridge.ControllerRegistryData{
			Controllers:         protoControllers,
			ClientsByController: clientsByController,
		},
	}, nil
}

// DeleteController handles controller deletion requests
func (s *ControllerGRPCServer) DeleteController(ctx context.Context, req *bridge.DeleteControllerRequest) (*bridge.DeleteControllerResponse, error) {
	if req == nil || req.ControllerId == "" {
		return nil, status.Error(codes.InvalidArgument, "controller ID cannot be empty")
	}

	s.logger.Infof("Delete controller request: %s", req.ControllerId)

	if err := s.controllerRoutingService.DeleteController(ctx, req.ControllerId); err != nil {
		s.logger.Errorf("Failed to delete controller %s: %v", req.ControllerId, err)
		return &bridge.DeleteControllerResponse{
			Success: false,
			Message: "failed: " + err.Error(),
		}, nil
	}

	s.logger.Infof("Controller deleted successfully: %s", req.ControllerId)
	return &bridge.DeleteControllerResponse{
		Success: true,
		Message: "controller deleted successfully",
	}, nil
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

	// Atomic assignment with mutex
	p.assignmentMutex.Lock()
	defer p.assignmentMutex.Unlock()

	// Try to get existing mapping first (without pending check)
	controller, err := p.controllerRoutingService.GetControllerCluster(ctx, clientID, version)
	if err == nil && controller != nil {
		p.logger.Infof("Found existing controller mapping: %s -> %s", clientID, controller.ID)
		// Clear any pending assignment since we have a real mapping
		if _, exists := p.pendingClientAssignments[clientID]; exists {
			delete(p.pendingClientAssignments, clientID)
			p.logger.Infof("Cleared pending assignment (found real mapping): %s -> %s", clientID, controller.ID)
		}
		return controller.ID
	}

	// Check if there's a pending assignment
	if pendingController, exists := p.pendingClientAssignments[clientID]; exists {
		p.logger.Infof("Found pending controller assignment: %s -> %s", clientID, pendingController)
		return pendingController
	}

	// If no mapping exists or error occurred, get least loaded controller
	controller, err = p.controllerRoutingService.GetLeastLoadedController(ctx, version)
	if err != nil {
		p.logger.Errorf("Failed to get least loaded controller: %v", err)
		return ""
	}

	// Create pending assignment to prevent race conditions
	p.pendingClientAssignments[clientID] = controller.ID
	p.logger.Infof("Created pending assignment: %s -> %s (for new client)", clientID, controller.ID)

	// Set timeout to clear pending assignment if not confirmed
	go func() {
		time.Sleep(30 * time.Second) // 30 second timeout
		p.assignmentMutex.Lock()
		defer p.assignmentMutex.Unlock()

		if pendingController, exists := p.pendingClientAssignments[clientID]; exists && pendingController == controller.ID {
			delete(p.pendingClientAssignments, clientID)
			p.logger.Warnf("Cleared pending assignment due to timeout: %s -> %s", clientID, controller.ID)
		}
	}()

	p.logger.Infof("Selected least loaded controller: %s (for new client %s)", controller.ID, clientID)
	return controller.ID
}
