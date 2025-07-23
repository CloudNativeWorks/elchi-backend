package service

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/registry/models"
	"github.com/CloudNativeWorks/elchi-backend/registry/storage"
)

// ControllerRoutingService handles all controller routing operations
type ControllerRoutingService struct {
	storage storage.Storage
	logger  *logger.Logger
}

// NewControllerRoutingService creates a new controller routing service
func NewControllerRoutingService(storage storage.Storage, logger *logger.Logger) *ControllerRoutingService {
	return &ControllerRoutingService{
		storage: storage,
		logger:  logger,
	}
}

// RegisterController registers a new controller
func (s *ControllerRoutingService) RegisterController(ctx context.Context, controller *models.ControllerInfo) error {
	s.logger.Infof("Registering controller: %s version %s", controller.ID, controller.Version)

	if controller.ID == "" {
		return fmt.Errorf("controller ID cannot be empty")
	}

	if controller.Version == "" {
		return fmt.Errorf("version cannot be empty")
	}

	// Ensure GRPCAddress is set
	if controller.HttpAddress == "" {
		return fmt.Errorf("gRPC address cannot be empty")
	}

	// Store the controller with its actual gRPC address
	return s.storage.RegisterController(ctx, controller)
}

// GetControllerCluster finds the appropriate controller for a client (version-agnostic search)
func (s *ControllerRoutingService) GetControllerCluster(ctx context.Context, clientID, version string) (*models.ControllerInfo, error) {
	s.logger.Infof("Looking for controller for client: %s (searching all versions)", clientID)

	if clientID == "" {
		return nil, fmt.Errorf("client ID cannot be empty")
	}

	// Search for client across all versions
	return s.findClientInAnyVersion(ctx, clientID)
}

// findClientInAnyVersion searches for a client across all versions
func (s *ControllerRoutingService) findClientInAnyVersion(ctx context.Context, clientID string) (*models.ControllerInfo, error) {
	// Get all controllers to find which versions exist
	controllers, err := s.storage.ListControllers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list controllers: %w", err)
	}

	// Extract unique versions
	versionsMap := make(map[string]bool)
	for _, ctrl := range controllers {
		versionsMap[ctrl.Version] = true
	}

	// Search in each version
	for version := range versionsMap {
		s.logger.Debugf("Searching for client %s in version %s", clientID, version)

		mapping, err := s.storage.GetClientMapping(ctx, clientID, version)
		if err == nil {
			// Found mapping, get controller
			controller, err := s.storage.GetController(ctx, mapping.ControllerID)
			if err == nil {
				s.logger.Infof("Found client %s in version %s -> controller %s", clientID, version, controller.ID)
				return controller, nil
			}
		}
	}

	return nil, fmt.Errorf("client %s not found in any version", clientID)
}

// NotifyClientConnected updates client mapping after client connection
func (s *ControllerRoutingService) NotifyClientConnected(ctx context.Context, controllerID, clientID, version string) error {
	s.logger.Infof("Client connected notification: %s -> %s (version: %s)", controllerID, clientID, version)

	if controllerID == "" {
		return fmt.Errorf("controller ID cannot be empty")
	}

	if clientID == "" {
		return fmt.Errorf("client ID cannot be empty")
	}

	if version == "" {
		return fmt.Errorf("version cannot be empty")
	}

	// Check if controller exists
	_, err := s.storage.GetController(ctx, controllerID)
	if err != nil {
		// Controller not found, try to register it
		s.logger.Warnf("Controller %s not found during client notification, attempting to register it", controllerID)

		serviceName := helper.ToK8sServiceName(controllerID, "elchi-stack")
		// Create and register controller
		controller := &models.ControllerInfo{
			ID:          controllerID,
			Version:     version,
			HttpAddress: fmt.Sprintf("%s:8099", serviceName), // Default GRPC address
			LastSeen:    time.Now(),
		}

		if err := s.storage.RegisterController(ctx, controller); err != nil {
			return fmt.Errorf("failed to register controller %s: %w", controllerID, err)
		}

		s.logger.Infof("Successfully registered controller %s with version %s", controllerID, version)
	}

	// Update or create client mapping
	mapping := &models.ClientMapping{
		ClientID:     clientID,
		Version:      version,
		ControllerID: controllerID,
		LastSeen:     time.Now(),
	}

	return s.storage.SetClientMapping(ctx, mapping)
}

// NotifyClientDisconnected removes client mapping when client disconnects
func (s *ControllerRoutingService) NotifyClientDisconnected(ctx context.Context, controllerID, clientID, version string) error {
	s.logger.Infof("Client disconnected notification: %s -> %s (version: %s)", controllerID, clientID, version)

	if controllerID == "" {
		return fmt.Errorf("controller ID cannot be empty")
	}

	if clientID == "" {
		return fmt.Errorf("client ID cannot be empty")
	}

	if version == "" {
		return fmt.Errorf("version cannot be empty")
	}

	// Remove client mapping from registry
	if err := s.storage.RemoveClientMapping(ctx, clientID, version); err != nil {
		s.logger.Errorf("Failed to remove client mapping for %s:%s: %v", clientID, version, err)
		return fmt.Errorf("failed to remove client mapping: %w", err)
	}

	s.logger.Infof("Client mapping removed from registry: %s:%s", clientID, version)
	return nil
}

// UpdateClientList updates the list of clients for a controller
func (s *ControllerRoutingService) UpdateClientList(ctx context.Context, controllerID string, clients []*models.ClientInfo) error {
	s.logger.Infof("Updating client list for controller %s: %d clients", controllerID, len(clients))

	if controllerID == "" {
		return fmt.Errorf("controller ID cannot be empty")
	}

	// Check if controller exists
	_, err := s.storage.GetController(ctx, controllerID)
	if err != nil {
		// Controller not found, try to register it
		s.logger.Warnf("Controller %s not found, attempting to register it", controllerID)

		// Extract version from clients (assuming all clients have same version)
		var version string
		if len(clients) > 0 {
			version = clients[0].Version
		} else {
			return fmt.Errorf("cannot register controller without version information")
		}

		serviceName := helper.ToK8sServiceName(controllerID, "elchi-stack")

		// Create and register controller
		controller := &models.ControllerInfo{
			ID:          controllerID,
			Version:     version,
			HttpAddress: fmt.Sprintf("%s:8099", serviceName),
			LastSeen:    time.Now(),
		}

		if err := s.storage.RegisterController(ctx, controller); err != nil {
			return fmt.Errorf("failed to register controller %s: %w", controllerID, err)
		}

		s.logger.Infof("Successfully registered controller %s with version %s", controllerID, version)
	} else {
		// Controller exists, update its last seen
		if err := s.storage.UpdateControllerLastSeen(ctx, controllerID); err != nil {
			s.logger.Errorf("Failed to update controller last seen: %v", err)
		}
	}

	return s.storage.UpdateClientList(ctx, controllerID, clients)
}

// CleanupStaleData removes stale controllers and client mappings with logging
func (s *ControllerRoutingService) CleanupStaleData(ctx context.Context, maxAge time.Duration) error {
	s.logger.Infof("Starting cleanup of stale controller data (max age: %v)", maxAge)

	// Get current data for logging
	controllers, err := s.storage.ListControllers(ctx)
	if err != nil {
		return fmt.Errorf("failed to list controllers for cleanup: %w", err)
	}

	// Log current state before cleanup
	s.logger.Debugf("Current state before cleanup: %d controllers", len(controllers))

	// Perform cleanup
	if err := s.storage.CleanupStaleData(ctx, maxAge); err != nil {
		return fmt.Errorf("failed to cleanup stale data: %w", err)
	}

	// Get data after cleanup for comparison
	controllersAfter, err := s.storage.ListControllers(ctx)
	if err != nil {
		s.logger.Errorf("Failed to list controllers after cleanup: %v", err)
		return nil
	}

	cleanedCount := len(controllers) - len(controllersAfter)
	if cleanedCount > 0 {
		s.logger.Infof("Cleanup completed: %d controllers removed", cleanedCount)
	} else {
		s.logger.Debug("No stale data found during cleanup")
	}

	return nil
}

// ListAllData returns all controller and client data for reporting
func (s *ControllerRoutingService) ListAllData(ctx context.Context) (*models.ControllerRegistryData, error) {
	s.logger.Infof("Listing all controller registry data (controllers and clients)")

	// Get all controllers
	controllers, err := s.storage.ListControllers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list controllers: %w", err)
	}

	// Get client mappings for each controller
	var clientsByController = make(map[string][]*models.ClientInfo)
	for _, ctrl := range controllers {
		clients, err := s.storage.GetClientsByController(ctx, ctrl.ID)
		if err != nil {
			s.logger.Warnf("Failed to get clients for controller %s: %v", ctrl.ID, err)
			continue
		}
		clientsByController[ctrl.ID] = clients
	}

	data := &models.ControllerRegistryData{
		Controllers:         controllers,
		ClientsByController: clientsByController,
	}

	s.logger.Infof("Retrieved %d controllers with total client data", len(controllers))
	return data, nil
}

// ListControllers returns all registered controllers
func (s *ControllerRoutingService) ListControllers(ctx context.Context) ([]*models.ControllerInfo, error) {
	s.logger.Infof("Listing all controllers")

	controllers, err := s.storage.ListControllers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list controllers: %w", err)
	}

	s.logger.Infof("Found %d controllers", len(controllers))
	return controllers, nil
}

// ListClientsByController returns all clients for a specific controller
func (s *ControllerRoutingService) ListClientsByController(ctx context.Context, controllerID string) ([]*models.ClientInfo, error) {
	s.logger.Infof("Listing clients for controller: %s", controllerID)

	if controllerID == "" {
		return nil, fmt.Errorf("controller ID cannot be empty")
	}

	clients, err := s.storage.GetClientsByController(ctx, controllerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get clients for controller %s: %w", controllerID, err)
	}

	s.logger.Infof("Found %d clients for controller %s", len(clients), controllerID)
	return clients, nil
}

// Legacy compatibility methods
// GetController returns controller info by ID
func (s *ControllerRoutingService) GetController(ctx context.Context, controllerID string) (*models.ControllerInfo, error) {
	return s.storage.GetController(ctx, controllerID)
}

// SetClientLocation sets client location (legacy method for backward compatibility)
func (s *ControllerRoutingService) SetClientLocation(ctx context.Context, clientID, controllerID string) error {
	s.logger.Infof("Setting client location (legacy): %s -> %s", clientID, controllerID)

	if clientID == "" {
		return fmt.Errorf("client ID cannot be empty")
	}

	if controllerID == "" {
		return fmt.Errorf("controller ID cannot be empty")
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	location := &models.ClientLocation{
		ClientID:     clientID,
		ControllerID: controllerID,
		LastSeen:     time.Now(),
	}

	return s.storage.SetClientLocation(ctx, location)
}

// GetClientLocation finds which controller a client is connected to
func (s *ControllerRoutingService) GetClientLocation(ctx context.Context, clientID, version string) (*models.ControllerInfo, error) {
	s.logger.Infof("Looking up location for client: %s version: %s", clientID, version)

	if clientID == "" {
		return nil, fmt.Errorf("client ID cannot be empty")
	}

	if version == "" {
		return nil, fmt.Errorf("version cannot be empty")
	}

	// Get client mapping from storage
	mapping, err := s.storage.GetClientMapping(ctx, clientID, version)
	if err != nil {
		s.logger.Debugf("Client mapping not found: %s:%s - %v", clientID, version, err)
		return nil, fmt.Errorf("client location not found: %s", clientID)
	}

	// Get controller info
	controller, err := s.storage.GetController(ctx, mapping.ControllerID)
	if err != nil {
		s.logger.Errorf("Controller not found for client %s: %v", clientID, err)
		return nil, fmt.Errorf("controller not found for client %s", clientID)
	}

	s.logger.Infof("Client location found: %s:%s -> %s", clientID, version, controller.ID)
	return controller, nil
}

// GetLeastLoadedController returns the controller with the least number of clients
func (s *ControllerRoutingService) GetLeastLoadedController(ctx context.Context, version string) (*models.ControllerInfo, error) {
	s.logger.Infof("Finding least loaded controller for version: %s", version)

	// Get all controllers
	controllers, err := s.storage.ListControllers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list controllers: %w", err)
	}

	if len(controllers) == 0 {
		return nil, fmt.Errorf("no controllers available")
	}

	// Find controller with least clients
	var selectedController *models.ControllerInfo
	minClients := math.MaxInt32

	for _, controller := range controllers {
		// Skip controllers with different version if version is specified
		if version != "" && controller.Version != version {
			continue
		}

		clients, err := s.storage.GetClientsByController(ctx, controller.ID)
		if err != nil {
			s.logger.Warnf("Failed to get clients for controller %s: %v", controller.ID, err)
			continue
		}

		if len(clients) < minClients {
			minClients = len(clients)
			selectedController = controller
		}
	}

	if selectedController == nil {
		if version != "" {
			return nil, fmt.Errorf("no controllers available for version %s", version)
		}
		return nil, fmt.Errorf("no controllers available")
	}

	s.logger.Infof("Selected controller %s with %d clients", selectedController.ID, minClients)
	return selectedController, nil
}
