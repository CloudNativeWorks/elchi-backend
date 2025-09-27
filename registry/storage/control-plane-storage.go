package storage

import (
	"context"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/registry/models"
)

// RoutingStorage defines the interface for routing service data persistence
type RoutingStorage interface {
	// Control Plane operations
	RegisterControlPlane(ctx context.Context, controlPlane *models.ControlPlane) error
	GetControlPlane(ctx context.Context, controlPlaneID string) (*models.ControlPlane, error)
	ListControlPlanes(ctx context.Context) ([]*models.ControlPlane, error)
	ListControlPlanesByVersion(ctx context.Context, version string) ([]*models.ControlPlane, error)
	UpdateControlPlaneLastSeen(ctx context.Context, controlPlaneID string) error
	DeleteControlPlane(ctx context.Context, controlPlaneID string) error

	// Node mapping operations
	SetNodeMapping(ctx context.Context, mapping *models.NodeMapping) error
	GetNodeMapping(ctx context.Context, nodeID, version string) (*models.NodeMapping, error)
	UpdateNodeList(ctx context.Context, controlPlaneID string, nodes []*models.NodeInfo) error
	GetNodesByControlPlane(ctx context.Context, controlPlaneID string) ([]*models.NodeInfo, error)

	// Cleanup operations (optional, for long-running services)
	CleanupStaleData(ctx context.Context, maxAge time.Duration) error
}
