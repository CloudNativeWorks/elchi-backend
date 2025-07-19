package service

import (
	"context"
	"fmt"
	"math/rand"
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

// UpdateNodeList updates the list of nodes for a control plane
func (s *RoutingService) UpdateNodeList(ctx context.Context, controlPlaneID string, nodes []*models.NodeInfo) error {
	s.logger.Infof("Updating node list for control plane %s: %d nodes", controlPlaneID, len(nodes))

	if controlPlaneID == "" {
		return fmt.Errorf("control plane ID cannot be empty")
	}

	// Check if control plane exists
	_, err := s.storage.GetControlPlane(ctx, controlPlaneID)
	if err != nil {
		// Control plane not found, try to register it
		s.logger.Warnf("Control plane %s not found, attempting to register it", controlPlaneID)
		
		// Extract version from nodes (assuming all nodes have same version)
		var version string
		if len(nodes) > 0 {
			version = nodes[0].Version
		} else {
			return fmt.Errorf("cannot register control plane without version information")
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
	return s.selectControlPlane(controlPlanes), nil
}

// selectControlPlane selects a control plane using simple round-robin
func (s *RoutingService) selectControlPlane(controlPlanes []*models.ControlPlane) *models.ControlPlane {
	if len(controlPlanes) == 0 {
		return nil
	}

	if len(controlPlanes) == 1 {
		return controlPlanes[0]
	}

	// Simple random selection for now
	// In production, you might want weighted round-robin based on load
	rand.Seed(time.Now().UnixNano())
	return controlPlanes[rand.Intn(len(controlPlanes))]
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