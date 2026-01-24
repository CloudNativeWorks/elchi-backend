package storage

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/registry/models"
)

// InMemoryRoutingStorage implements RoutingStorage interface using in-memory maps
type InMemoryRoutingStorage struct {
	controlPlanes map[string]*models.ControlPlane
	nodeMappings  map[string]*models.NodeMapping // key: nodeID:version
	mu            sync.RWMutex
}

// NewInMemoryRoutingStorage creates a new in-memory routing storage instance
func NewInMemoryRoutingStorage() *InMemoryRoutingStorage {
	return &InMemoryRoutingStorage{
		controlPlanes: make(map[string]*models.ControlPlane),
		nodeMappings:  make(map[string]*models.NodeMapping),
		mu:            sync.RWMutex{},
	}
}

// RegisterControlPlane registers a control plane in the storage
func (s *InMemoryRoutingStorage) RegisterControlPlane(ctx context.Context, controlPlane *models.ControlPlane) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Copy to avoid external modifications
	controlPlaneCopy := *controlPlane
	controlPlaneCopy.LastSeen = time.Now()
	s.controlPlanes[controlPlane.ID] = &controlPlaneCopy

	return nil
}

func (s *InMemoryRoutingStorage) GetControlPlane(ctx context.Context, controlPlaneID string) (*models.ControlPlane, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	controlPlane, exists := s.controlPlanes[controlPlaneID]
	if !exists {
		return nil, fmt.Errorf("control plane not found: %s", controlPlaneID)
	}

	// Return a copy to avoid external modifications
	controlPlaneCopy := *controlPlane
	return &controlPlaneCopy, nil
}

func (s *InMemoryRoutingStorage) ListControlPlanes(ctx context.Context) ([]*models.ControlPlane, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	controlPlanes := make([]*models.ControlPlane, 0, len(s.controlPlanes))
	for _, cp := range s.controlPlanes {
		cpCopy := *cp
		controlPlanes = append(controlPlanes, &cpCopy)
	}

	return controlPlanes, nil
}

func (s *InMemoryRoutingStorage) ListControlPlanesByVersion(ctx context.Context, version string) ([]*models.ControlPlane, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var controlPlanes []*models.ControlPlane
	for _, cp := range s.controlPlanes {
		if cp.Version == version {
			cpCopy := *cp
			controlPlanes = append(controlPlanes, &cpCopy)
		}
	}

	return controlPlanes, nil
}

func (s *InMemoryRoutingStorage) UpdateControlPlaneLastSeen(ctx context.Context, controlPlaneID string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	controlPlane, exists := s.controlPlanes[controlPlaneID]
	if !exists {
		return fmt.Errorf("control plane not found: %s", controlPlaneID)
	}

	controlPlane.LastSeen = time.Now()
	return nil
}

// SetNodeMapping sets a node mapping in the storage
func (s *InMemoryRoutingStorage) SetNodeMapping(ctx context.Context, mapping *models.NodeMapping) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Verify control plane exists
	if _, exists := s.controlPlanes[mapping.ControlPlaneID]; !exists {
		return fmt.Errorf("control plane not found: %s", mapping.ControlPlaneID)
	}

	key := fmt.Sprintf("%s:%s", mapping.NodeID, mapping.Version)

	// Upsert: Update existing mapping or create new one
	mappingCopy := *mapping
	mappingCopy.LastSeen = time.Now()
	s.nodeMappings[key] = &mappingCopy

	return nil
}

func (s *InMemoryRoutingStorage) GetNodeMapping(ctx context.Context, nodeID, version string) (*models.NodeMapping, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	key := fmt.Sprintf("%s:%s", nodeID, version)
	mapping, exists := s.nodeMappings[key]
	if !exists {
		return nil, fmt.Errorf("node mapping not found: %s", key)
	}

	// Return a copy to avoid external modifications
	mappingCopy := *mapping
	return &mappingCopy, nil
}

func (s *InMemoryRoutingStorage) UpdateNodeList(ctx context.Context, controlPlaneID string, nodes []*models.NodeInfo) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Verify control plane exists and update its last_seen
	controlPlane, exists := s.controlPlanes[controlPlaneID]
	if !exists {
		return fmt.Errorf("control plane not found: %s", controlPlaneID)
	}
	controlPlane.LastSeen = time.Now()

	// CRITICAL FIX: First, remove all existing mappings for this control-plane
	// This ensures that disconnected nodes are properly cleaned up
	keysToDelete := make([]string, 0)
	for key, mapping := range s.nodeMappings {
		if mapping.ControlPlaneID == controlPlaneID {
			keysToDelete = append(keysToDelete, key)
		}
	}

	// Delete old mappings
	for _, key := range keysToDelete {
		delete(s.nodeMappings, key)
	}

	now := time.Now()

	// Then, add new node mappings (only for currently connected nodes)
	for _, node := range nodes {
		key := fmt.Sprintf("%s:%s", node.NodeID, node.Version)

		// Create new mapping
		mapping := &models.NodeMapping{
			NodeID:         node.NodeID,
			Version:        node.Version,
			ControlPlaneID: controlPlaneID,
			LastSeen:       now,
		}

		s.nodeMappings[key] = mapping
	}

	return nil
}

func (s *InMemoryRoutingStorage) GetNodesByControlPlane(ctx context.Context, controlPlaneID string) ([]*models.NodeInfo, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var nodes []*models.NodeInfo
	for _, mapping := range s.nodeMappings {
		if mapping.ControlPlaneID == controlPlaneID {
			node := &models.NodeInfo{
				NodeID:   mapping.NodeID,
				Version:  mapping.Version,
				LastSeen: mapping.LastSeen,
			}
			nodes = append(nodes, node)
		}
	}

	return nodes, nil
}

// DeleteControlPlane deletes a control plane and its associated nodes from storage
func (s *InMemoryRoutingStorage) DeleteControlPlane(ctx context.Context, controlPlaneID string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Delete the control plane
	delete(s.controlPlanes, controlPlaneID)

	// Delete all node mappings associated with this control plane
	for key, mapping := range s.nodeMappings {
		if mapping.ControlPlaneID == controlPlaneID {
			delete(s.nodeMappings, key)
		}
	}

	return nil
}

// CleanupStaleData removes stale control planes and node mappings
// This should be called periodically (e.g., every 5 minutes) for long-running services
func (s *InMemoryRoutingStorage) CleanupStaleData(ctx context.Context, maxAge time.Duration) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	cleanedControlPlanes := 0
	cleanedNodeMappings := 0

	// Cleanup stale control planes
	for id, cp := range s.controlPlanes {
		if cp.LastSeen.Before(cutoff) {
			delete(s.controlPlanes, id)
			cleanedControlPlanes++
		}
	}

	// Cleanup stale node mappings
	for key, mapping := range s.nodeMappings {
		if mapping.LastSeen.Before(cutoff) {
			delete(s.nodeMappings, key)
			cleanedNodeMappings++
		}
	}

	return nil
}
