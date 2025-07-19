package service

import (
	"context"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/registry/models"
	"github.com/CloudNativeWorks/elchi-backend/registry/storage"
)

// RegistryService handles all registry operations
type RegistryService struct {
	storage storage.Storage
	logger  *logger.Logger
}

// NewRegistryService creates a new registry service
func NewRegistryService(storage storage.Storage, logger *logger.Logger) *RegistryService {
	return &RegistryService{
		storage: storage,
		logger:  logger,
	}
}

// Controller operations
func (s *RegistryService) RegisterController(ctx context.Context, info *models.ControllerInfo) error {
	s.logger.Infof("Registering controller: %s (%s)", info.ID, info.GRPCAddress)

	if info.ID == "" {
		return fmt.Errorf("controller ID cannot be empty")
	}

	if info.GRPCAddress == "" {
		return fmt.Errorf("controller gRPC address cannot be empty")
	}

	return s.storage.RegisterController(ctx, info)
}

func (s *RegistryService) GetController(ctx context.Context, controllerID string) (*models.ControllerInfo, error) {
	return s.storage.GetController(ctx, controllerID)
}

// Client location operations
func (s *RegistryService) SetClientLocation(ctx context.Context, clientID, controllerID string) error {
	s.logger.Infof("Setting client location: %s -> %s", clientID, controllerID)

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
	}

	return s.storage.SetClientLocation(ctx, location)
}

func (s *RegistryService) GetClientLocation(ctx context.Context, clientID string) (*models.ClientLocation, error) {
	clientLocation, err := s.storage.GetClientLocation(ctx, clientID)
	s.logger.Debugf("Getting client location: %s, %v", clientLocation, err)
	return clientLocation, err
}
