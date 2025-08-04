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
	ListControllers(ctx context.Context) ([]*models.ControllerInfo, error)
	ListControllersByVersion(ctx context.Context, version string) ([]*models.ControllerInfo, error)
	UpdateControllerLastSeen(ctx context.Context, controllerID string) error

	// Client mapping operations (similar to NodeMapping in control-plane)
	SetClientMapping(ctx context.Context, mapping *models.ClientMapping) error
	GetClientMapping(ctx context.Context, clientID, version string) (*models.ClientMapping, error)
	RemoveClientMapping(ctx context.Context, clientID, version string) error
	UpdateClientList(ctx context.Context, controllerID string, clients []*models.ClientInfo) error
	GetClientsByController(ctx context.Context, controllerID string) ([]*models.ClientInfo, error)

	// Legacy client location operations (for backward compatibility)
	SetClientLocation(ctx context.Context, location *models.ClientLocation) error
	GetClientLocation(ctx context.Context, clientID string) (*models.ClientLocation, error)

	// Cleanup operations
	CleanupStaleData(ctx context.Context, maxAge time.Duration) error
}
