package service

import (
	"context"
	"fmt"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/registry/models"
	"github.com/CloudNativeWorks/elchi-backend/registry/storage"
)

// RoutingService handles all routing operations
type RoutingService struct {
	storage storage.RoutingStorage
	logger  *logger.Logger
}

// NewRoutingService creates a new routing service
func NewRoutingService(storage storage.RoutingStorage, logger *logger.Logger) *RoutingService {
	return &RoutingService{
		storage: storage,
		logger:  logger,
	}
}

// RegisterControlPlane registers a new control plane
func (s *RoutingService) RegisterControlPlane(ctx context.Context, controlPlane *models.ControlPlane) error {
	s.logger.Infof("Registering control plane: %s version %s", controlPlane.ID, controlPlane.Version)

	if controlPlane.ID == "" {
		return fmt.Errorf("control plane ID cannot be empty")
	}

	if controlPlane.Version == "" {
		return fmt.Errorf("version cannot be empty")
	}

	return s.storage.RegisterControlPlane(ctx, controlPlane)
}

// GetControlPlaneCluster finds the appropriate control plane for a node
func (s *RoutingService) GetControlPlaneCluster(ctx context.Context, nodeID, version string) (*models.ControlPlane, error) {
	s.logger.Infof("Looking for control plane for node: %s version: %s", nodeID, version)

	if nodeID == "" {
		return nil, fmt.Errorf("node ID cannot be empty")
	}

	if version == "" {
		return nil, fmt.Errorf("version cannot be empty")
	}

	// Priority 1: Check if this nodeID is already mapped to a control plane
	mapping, err := s.storage.GetNodeMapping(ctx, nodeID, version)
	if err == nil {
		// Found existing mapping, return the control plane
		controlPlane, err := s.storage.GetControlPlane(ctx, mapping.ControlPlaneID)
		if err != nil {
			s.logger.Errorf("Control plane not found for mapping: %v", err)
			// Fall through to find new control plane
		} else {
			s.logger.Infof("Found existing mapping: %s", nodeID)
			return controlPlane, nil
		}
	}

	// Priority 2: Find available control plane with capacity for this version
	controlPlane, err := s.findControlPlaneForVersion(ctx, version)
	if err != nil {
		return nil, fmt.Errorf("no suitable control plane found for version %s: %w", version, err)
	}

	// Create mapping but don't add node to control plane's node list
	// The control plane will handle node registration separately
	mapping = &models.NodeMapping{
		NodeID:         nodeID,
		Version:        version,
		ControlPlaneID: controlPlane.ID,
		LastSeen:       time.Now(),
	}

	if err := s.storage.SetNodeMapping(ctx, mapping); err != nil {
		s.logger.Errorf("Failed to create node mapping: %v", err)
		// Still return the control plane even if mapping fails
	}

	s.logger.Infof("Created new mapping: %s -> %s (node registration will be handled separately)", nodeID, controlPlane.ID)
	return controlPlane, nil
}

// GetRandomControlPlaneForVersion returns a load-balanced control-plane for the given version
// Used by bridge calls that don't need specific node mappings, just any available control-plane
func (s *RoutingService) GetRandomControlPlaneForVersion(ctx context.Context, version string) (*models.ControlPlane, error) {
	s.logger.Debugf("Getting random control-plane for version: %s", version)

	if version == "" {
		return nil, fmt.Errorf("version cannot be empty")
	}

	// Use existing internal method for version-based selection
	controlPlane, err := s.findControlPlaneForVersion(ctx, version)
	if err != nil {
		return nil, fmt.Errorf("no control-plane available for version %s: %w", version, err)
	}

	s.logger.Debugf("Selected control-plane %s for version %s (load-balanced)", controlPlane.ID, version)
	return controlPlane, nil
}

// NotifySnapshotDelivered updates node mapping after snapshot delivery
func (s *RoutingService) NotifySnapshotDelivered(ctx context.Context, controlPlaneID, nodeID, version string) error {
	s.logger.Infof("Snapshot delivered notification: %s -> %s (version: %s)", controlPlaneID, nodeID, version)

	if controlPlaneID == "" {
		return fmt.Errorf("control plane ID cannot be empty")
	}

	if nodeID == "" {
		return fmt.Errorf("node ID cannot be empty")
	}

	if version == "" {
		return fmt.Errorf("version cannot be empty")
	}

	// Check if control plane exists
	_, err := s.storage.GetControlPlane(ctx, controlPlaneID)
	if err != nil {
		// Control plane not found, try to register it
		s.logger.Warnf("Control plane %s not found during snapshot notification, attempting to register it", controlPlaneID)

		// Create and register control plane
		controlPlane := &models.ControlPlane{
			ID:       controlPlaneID,
			Version:  version,
			LastSeen: time.Now(),
		}

		if err := s.storage.RegisterControlPlane(ctx, controlPlane); err != nil {
			return fmt.Errorf("failed to register control plane %s: %w", controlPlaneID, err)
		}

		s.logger.Infof("Successfully registered control plane %s with version %s", controlPlaneID, version)
	}

	// Update or create node mapping
	mapping := &models.NodeMapping{
		NodeID:         nodeID,
		Version:        version,
		ControlPlaneID: controlPlaneID,
		LastSeen:       time.Now(),
	}

	return s.storage.SetNodeMapping(ctx, mapping)
}

// UpdateNodeList updates the list of nodes for a control plane with version information for auto-registration
func (s *RoutingService) UpdateNodeList(ctx context.Context, controlPlaneID string, nodes []*models.NodeInfo, version string) error {
	s.logger.Infof("Updating node list for control plane %s: %d nodes", controlPlaneID, len(nodes))

	if controlPlaneID == "" {
		return fmt.Errorf("control plane ID cannot be empty")
	}

	// Check if control plane exists
	_, err := s.storage.GetControlPlane(ctx, controlPlaneID)
	if err != nil {
		// Control plane not found, try to register it
		s.logger.Warnf("Control plane %s not found, attempting to register it", controlPlaneID)

		// Use version from request parameter for auto-registration
		if version == "" {
			// Extract from nodes as fallback
			if len(nodes) > 0 {
				version = nodes[0].Version
			}
		}

		if version == "" {
			s.logger.Warnf("Cannot register control plane %s without version information", controlPlaneID)
			return nil // Don't fail, just skip the update
		}

		// Create and register control plane
		controlPlane := &models.ControlPlane{
			ID:       controlPlaneID,
			Version:  version,
			LastSeen: time.Now(),
		}

		if err := s.storage.RegisterControlPlane(ctx, controlPlane); err != nil {
			return fmt.Errorf("failed to register control plane %s: %w", controlPlaneID, err)
		}

		s.logger.Infof("Successfully registered control plane %s with version %s", controlPlaneID, version)
	} else {
		// Control plane exists, update its last seen
		if err := s.storage.UpdateControlPlaneLastSeen(ctx, controlPlaneID); err != nil {
			s.logger.Errorf("Failed to update control plane last seen: %v", err)
		}
	}

	return s.storage.UpdateNodeList(ctx, controlPlaneID, nodes)
}

// findControlPlaneForVersion finds the best control plane for a given version
func (s *RoutingService) findControlPlaneForVersion(ctx context.Context, version string) (*models.ControlPlane, error) {
	// Only exact version match - no prefix or major version matching
	controlPlanes, err := s.storage.ListControlPlanesByVersion(ctx, version)
	if err != nil {
		return nil, fmt.Errorf("failed to list control planes for version %s: %w", version, err)
	}

	if len(controlPlanes) == 0 {
		return nil, fmt.Errorf("no control plane found for exact version %s", version)
	}

	// Load balance among exact matches
	return s.selectControlPlane(ctx, controlPlanes), nil
}

// selectControlPlane selects a control plane with the least number of nodes (load balancing)
func (s *RoutingService) selectControlPlane(ctx context.Context, controlPlanes []*models.ControlPlane) *models.ControlPlane {
	if len(controlPlanes) == 0 {
		return nil
	}

	if len(controlPlanes) == 1 {
		return controlPlanes[0]
	}

	// Find control plane with minimum node count for better load balancing
	var selectedControlPlane *models.ControlPlane
	minNodeCount := int(^uint(0) >> 1) // Max int value

	s.logger.Infof("Selecting best control plane from %d candidates based on node count", len(controlPlanes))

	for _, controlPlane := range controlPlanes {
		// Get nodes for this control plane
		nodes, err := s.storage.GetNodesByControlPlane(ctx, controlPlane.ID)
		if err != nil {
			s.logger.Warnf("Failed to get nodes for control plane %s: %v, skipping", controlPlane.ID, err)
			continue
		}

		nodeCount := len(nodes)
		s.logger.Debugf("Control plane %s has %d nodes", controlPlane.ID, nodeCount)

		if nodeCount < minNodeCount {
			minNodeCount = nodeCount
			selectedControlPlane = controlPlane
			s.logger.Debugf("New best candidate: %s with %d nodes", controlPlane.ID, nodeCount)
		}
	}

	if selectedControlPlane == nil {
		// Fallback to first control plane if we couldn't get node counts
		s.logger.Warnf("Could not determine node counts, falling back to first control plane")
		return controlPlanes[0]
	}

	s.logger.Infof("Selected control plane %s with %d nodes (least loaded)", selectedControlPlane.ID, minNodeCount)
	return selectedControlPlane
}

// CleanupStaleData removes stale control planes and node mappings with logging
func (s *RoutingService) CleanupStaleData(ctx context.Context, maxAge time.Duration) error {
	s.logger.Infof("Starting cleanup of stale data (max age: %v)", maxAge)

	// Get current data for logging
	controlPlanes, err := s.storage.ListControlPlanes(ctx)
	if err != nil {
		return fmt.Errorf("failed to list control planes for cleanup: %w", err)
	}

	// Log current state before cleanup
	s.logger.Debugf("Current state before cleanup: %d control planes", len(controlPlanes))

	// Perform cleanup
	if err := s.storage.CleanupStaleData(ctx, maxAge); err != nil {
		return fmt.Errorf("failed to cleanup stale data: %w", err)
	}

	// Get data after cleanup for comparison
	controlPlanesAfter, err := s.storage.ListControlPlanes(ctx)
	if err != nil {
		s.logger.Errorf("Failed to list control planes after cleanup: %v", err)
		return nil
	}

	cleanedCount := len(controlPlanes) - len(controlPlanesAfter)
	if cleanedCount > 0 {
		s.logger.Infof("Cleanup completed: %d control planes removed", cleanedCount)
	} else {
		s.logger.Debug("No stale data found during cleanup")
	}

	return nil
}

// ListAllData returns all control plane and node data for reporting
func (s *RoutingService) ListAllData(ctx context.Context) (*models.RegistryData, error) {
	s.logger.Infof("Listing all registry data (control planes and nodes)")

	// Get all control planes
	controlPlanes, err := s.storage.ListControlPlanes(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list control planes: %w", err)
	}

	// Get node mappings for each control plane
	nodesByControlPlane := make(map[string][]*models.NodeInfo)
	for _, cp := range controlPlanes {
		nodes, err := s.storage.GetNodesByControlPlane(ctx, cp.ID)
		if err != nil {
			s.logger.Warnf("Failed to get nodes for control plane %s: %v", cp.ID, err)
			continue
		}
		nodesByControlPlane[cp.ID] = nodes
	}

	data := &models.RegistryData{
		ControlPlanes:       controlPlanes,
		NodesByControlPlane: nodesByControlPlane,
	}

	s.logger.Infof("Retrieved %d control planes with total node data", len(controlPlanes))
	return data, nil
}

// DeleteControlPlane deletes a control plane and its associated nodes
func (s *RoutingService) DeleteControlPlane(ctx context.Context, controlPlaneID string) error {
	s.logger.Infof("Deleting control plane: %s", controlPlaneID)

	if controlPlaneID == "" {
		return fmt.Errorf("control plane ID cannot be empty")
	}

	// Check if control plane exists
	_, err := s.storage.GetControlPlane(ctx, controlPlaneID)
	if err != nil {
		return fmt.Errorf("control plane %s not found: %w", controlPlaneID, err)
	}

	// Delete control plane and all its associated nodes
	if err := s.storage.DeleteControlPlane(ctx, controlPlaneID); err != nil {
		return fmt.Errorf("failed to delete control plane %s: %w", controlPlaneID, err)
	}

	s.logger.Infof("Successfully deleted control plane %s and its associated nodes", controlPlaneID)
	return nil
}

// ListControlPlanes returns all registered control planes
func (s *RoutingService) ListControlPlanes(ctx context.Context) ([]*models.ControlPlane, error) {
	s.logger.Infof("Listing all control planes")

	controlPlanes, err := s.storage.ListControlPlanes(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list control planes: %w", err)
	}

	s.logger.Infof("Found %d control planes", len(controlPlanes))
	return controlPlanes, nil
}

// ListNodesByControlPlane returns all nodes for a specific control plane
func (s *RoutingService) ListNodesByControlPlane(ctx context.Context, controlPlaneID string) ([]*models.NodeInfo, error) {
	s.logger.Infof("Listing nodes for control plane: %s", controlPlaneID)

	if controlPlaneID == "" {
		return nil, fmt.Errorf("control plane ID cannot be empty")
	}

	nodes, err := s.storage.GetNodesByControlPlane(ctx, controlPlaneID)
	if err != nil {
		return nil, fmt.Errorf("failed to get nodes for control plane %s: %w", controlPlaneID, err)
	}

	s.logger.Infof("Found %d nodes for control plane %s", len(nodes), controlPlaneID)
	return nodes, nil
}

// GetLeastLoadedControlPlane returns the control plane with the least number of nodes
func (s *RoutingService) GetLeastLoadedControlPlane(ctx context.Context, version string) (*models.ControlPlane, error) {
	s.logger.Infof("Finding least loaded control plane for version: %s", version)

	// Get all control planes
	controlPlanes, err := s.storage.ListControlPlanes(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list control planes: %w", err)
	}

	if len(controlPlanes) == 0 {
		return nil, fmt.Errorf("no control planes available")
	}

	// Filter by version if specified
	var filteredControlPlanes []*models.ControlPlane
	if version != "" {
		for _, controlPlane := range controlPlanes {
			if controlPlane.Version == version {
				filteredControlPlanes = append(filteredControlPlanes, controlPlane)
			}
		}

		if len(filteredControlPlanes) == 0 {
			return nil, fmt.Errorf("no control planes available for version %s", version)
		}

		s.logger.Infof("Filtered to %d control planes for version %s", len(filteredControlPlanes), version)
	} else {
		filteredControlPlanes = controlPlanes
		s.logger.Infof("Using all %d control planes (no version filter)", len(filteredControlPlanes))
	}

	// Use our improved selectControlPlane function for load balancing
	selectedControlPlane := s.selectControlPlane(ctx, filteredControlPlanes)
	if selectedControlPlane == nil {
		if version != "" {
			return nil, fmt.Errorf("no suitable control planes available for version %s", version)
		}
		return nil, fmt.Errorf("no suitable control planes available")
	}

	s.logger.Infof("GetLeastLoadedControlPlane selected: %s for version: %s", selectedControlPlane.ID, version)
	return selectedControlPlane, nil
}
