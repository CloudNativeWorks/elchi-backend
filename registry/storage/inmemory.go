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
	controllers     map[string]*models.ControllerInfo
	clientLocations map[string]*models.ClientLocation
	mu              sync.RWMutex
}

// NewInMemoryStorage creates a new in-memory storage instance
func NewInMemoryStorage() *InMemoryStorage {
	return &InMemoryStorage{
		controllers:     make(map[string]*models.ControllerInfo),
		clientLocations: make(map[string]*models.ClientLocation),
		mu:              sync.RWMutex{},
	}
}

// Controller operations
func (s *InMemoryStorage) RegisterController(ctx context.Context, controller *models.ControllerInfo) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Copy to avoid external modifications
	controllerCopy := *controller
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

// Client location operations
func (s *InMemoryStorage) SetClientLocation(ctx context.Context, location *models.ClientLocation) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.controllers[location.ControllerID]; !exists {
		return fmt.Errorf("controller not found: %s", location.ControllerID)
	}

	// Copy to avoid external modifications
	locationCopy := *location
	s.clientLocations[location.ClientID] = &locationCopy

	return nil
}

func (s *InMemoryStorage) GetClientLocation(ctx context.Context, clientID string) (*models.ClientLocation, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	location, exists := s.clientLocations[clientID]
	if !exists {
		return nil, fmt.Errorf("client location not found: %s", clientID)
	}

	// Return a copy to avoid external modifications
	locationCopy := *location
	return &locationCopy, nil
}

// GetControllerWithTimeout is a helper method for external controller validation
// with timeout to prevent deadlocks
func (s *InMemoryStorage) GetControllerWithTimeout(ctx context.Context, controllerID string, timeout time.Duration) (*models.ControllerInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return s.GetController(ctx, controllerID)
}
