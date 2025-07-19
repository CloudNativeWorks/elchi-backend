package storage

import (
	"context"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/registry/models"
)

// Storage defines the interface for registry data persistence
type Storage interface {
	// Controller operations
	RegisterController(ctx context.Context, controller *models.ControllerInfo) error
	GetController(ctx context.Context, controllerID string) (*models.ControllerInfo, error)
	GetControllerWithTimeout(ctx context.Context, controllerID string, timeout time.Duration) (*models.ControllerInfo, error)

	// Client location operations
	SetClientLocation(ctx context.Context, location *models.ClientLocation) error
	GetClientLocation(ctx context.Context, clientID string) (*models.ClientLocation, error)
}
