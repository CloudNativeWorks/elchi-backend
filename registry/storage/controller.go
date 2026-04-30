// Package storage provides in-memory storage implementations for the registry service,
// including controller and control-plane data management with thread-safe operations.
package storage

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/registry/models"
)

// InMemoryStorage implements Storage interface using in-memory maps
type InMemoryStorage struct {
	controllers    map[string]*models.ControllerInfo
	clientMappings map[string]*models.ClientMapping // key: clientID:version
	mu             sync.RWMutex
}

// NewInMemoryStorage creates a new in-memory storage instance
func NewInMemoryStorage() *InMemoryStorage {
	return &InMemoryStorage{
		controllers:    make(map[string]*models.ControllerInfo),
		clientMappings: make(map[string]*models.ClientMapping),
		mu:             sync.RWMutex{},
	}
}

// RegisterController registers a controller in the storage
func (s *InMemoryStorage) RegisterController(ctx context.Context, controller *models.ControllerInfo) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Copy to avoid external modifications
	controllerCopy := *controller
	controllerCopy.LastSeen = time.Now()
	s.controllers[controller.ID] = &controllerCopy

	return nil
}

func (s *InMemoryStorage) GetController(ctx context.Context, controllerID string) (*models.ControllerInfo, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	controller, exists := s.controllers[controllerID]
	if !exists {
		return nil, fmt.Errorf("controller not found: %s", controllerID)
	}

	// Return a copy to avoid external modifications
	controllerCopy := *controller
	return &controllerCopy, nil
}

func (s *InMemoryStorage) ListControllers(ctx context.Context) ([]*models.ControllerInfo, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	controllers := make([]*models.ControllerInfo, 0, len(s.controllers))
	for _, ctrl := range s.controllers {
		ctrlCopy := *ctrl
		controllers = append(controllers, &ctrlCopy)
	}

	return controllers, nil
}

func (s *InMemoryStorage) ListControllersByVersion(ctx context.Context, version string) ([]*models.ControllerInfo, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var controllers []*models.ControllerInfo
	for _, ctrl := range s.controllers {
		if ctrl.Version == version {
			ctrlCopy := *ctrl
			controllers = append(controllers, &ctrlCopy)
		}
	}

	return controllers, nil
}

func (s *InMemoryStorage) UpdateControllerLastSeen(ctx context.Context, controllerID string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	controller, exists := s.controllers[controllerID]
	if !exists {
		return fmt.Errorf("controller not found: %s", controllerID)
	}

	controller.LastSeen = time.Now()
	return nil
}

// SetClientMapping sets a client mapping in the storage
func (s *InMemoryStorage) SetClientMapping(ctx context.Context, mapping *models.ClientMapping) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Verify controller exists
	if _, exists := s.controllers[mapping.ControllerID]; !exists {
		return fmt.Errorf("controller not found: %s", mapping.ControllerID)
	}

	key := fmt.Sprintf("%s:%s", mapping.ClientID, mapping.Version)

	// Upsert: Update existing mapping or create new one
	mappingCopy := *mapping
	mappingCopy.LastSeen = time.Now()
	s.clientMappings[key] = &mappingCopy

	return nil
}

func (s *InMemoryStorage) GetClientMapping(ctx context.Context, clientID, version string) (*models.ClientMapping, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	key := fmt.Sprintf("%s:%s", clientID, version)
	mapping, exists := s.clientMappings[key]
	if !exists {
		return nil, fmt.Errorf("client mapping not found: %s", key)
	}

	// Return a copy to avoid external modifications
	mappingCopy := *mapping
	return &mappingCopy, nil
}

func (s *InMemoryStorage) RemoveClientMapping(ctx context.Context, clientID, version string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := fmt.Sprintf("%s:%s", clientID, version)

	if _, exists := s.clientMappings[key]; !exists {
		// Not an error if mapping doesn't exist - idempotent operation
		return nil
	}

	delete(s.clientMappings, key)
	return nil
}

func (s *InMemoryStorage) UpdateClientList(ctx context.Context, controllerID string, clients []*models.ClientInfo) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Verify controller exists and update its last_seen
	controller, exists := s.controllers[controllerID]
	if !exists {
		return fmt.Errorf("controller not found: %s", controllerID)
	}
	controller.LastSeen = time.Now()

	keysToDelete := make([]string, 0)
	for key, mapping := range s.clientMappings {
		if mapping.ControllerID == controllerID {
			keysToDelete = append(keysToDelete, key)
		}
	}

	// Delete old mappings
	for _, key := range keysToDelete {
		delete(s.clientMappings, key)
	}

	now := time.Now()

	// Then, add new client mappings (only for currently connected clients)
	for _, client := range clients {
		key := fmt.Sprintf("%s:%s", client.ClientID, client.Version)

		// Create new mapping
		mapping := &models.ClientMapping{
			ClientID:     client.ClientID,
			Version:      client.Version,
			ControllerID: controllerID,
			LastSeen:     now,
		}

		s.clientMappings[key] = mapping
	}

	return nil
}

func (s *InMemoryStorage) GetClientsByController(ctx context.Context, controllerID string) ([]*models.ClientInfo, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var clients []*models.ClientInfo
	for _, mapping := range s.clientMappings {
		if mapping.ControllerID == controllerID {
			client := &models.ClientInfo{
				ClientID: mapping.ClientID,
				Version:  mapping.Version,
				LastSeen: mapping.LastSeen,
			}
			clients = append(clients, client)
		}
	}

	return clients, nil
}

// GetControllerWithTimeout is a helper method for external controller validation
// with timeout to prevent deadlocks
func (s *InMemoryStorage) GetControllerWithTimeout(ctx context.Context, controllerID string, timeout time.Duration) (*models.ControllerInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return s.GetController(ctx, controllerID)
}

// DeleteController deletes a controller and its associated client mappings from storage
func (s *InMemoryStorage) DeleteController(ctx context.Context, controllerID string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Delete the controller
	delete(s.controllers, controllerID)

	// Delete all client mappings associated with this controller
	for key, mapping := range s.clientMappings {
		if mapping.ControllerID == controllerID {
			delete(s.clientMappings, key)
		}
	}

	return nil
}

// ExportAll returns deep copies of all controllers and client mappings.
// Used by the snapshot writer (leader) to persist state to MongoDB.
func (s *InMemoryStorage) ExportAll() ([]*models.ControllerInfo, []*models.ClientMapping) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	controllers := make([]*models.ControllerInfo, 0, len(s.controllers))
	for _, c := range s.controllers {
		cp := *c
		controllers = append(controllers, &cp)
	}
	mappings := make([]*models.ClientMapping, 0, len(s.clientMappings))
	for _, m := range s.clientMappings {
		mp := *m
		mappings = append(mappings, &mp)
	}
	return controllers, mappings
}

// ImportAll replaces the in-memory state with the supplied snapshot.
// Used by the snapshot reader (standby) to stay warm with the leader's state.
// Existing entries are dropped — this is a full overwrite.
func (s *InMemoryStorage) ImportAll(controllers []*models.ControllerInfo, mappings []*models.ClientMapping) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.controllers = make(map[string]*models.ControllerInfo, len(controllers))
	for _, c := range controllers {
		cp := *c
		s.controllers[cp.ID] = &cp
	}
	s.clientMappings = make(map[string]*models.ClientMapping, len(mappings))
	for _, m := range mappings {
		mp := *m
		key := fmt.Sprintf("%s:%s", mp.ClientID, mp.Version)
		s.clientMappings[key] = &mp
	}
}

// CleanupStaleData removes stale controllers and client mappings
func (s *InMemoryStorage) CleanupStaleData(ctx context.Context, maxAge time.Duration) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	cleanedControllers := 0
	cleanedClientMappings := 0

	// Cleanup stale controllers
	for id, ctrl := range s.controllers {
		if ctrl.LastSeen.Before(cutoff) {
			delete(s.controllers, id)
			cleanedControllers++
		}
	}

	// Cleanup stale client mappings
	for key, mapping := range s.clientMappings {
		if mapping.LastSeen.Before(cutoff) {
			delete(s.clientMappings, key)
			cleanedClientMappings++
		}
	}

	return nil
}
