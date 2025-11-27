package handlers

import (
	"github.com/CloudNativeWorks/elchi-backend/controller/handlers/maintenance"
	"github.com/CloudNativeWorks/elchi-backend/pkg/audit"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

// MaintenanceHandler wraps all maintenance sub-handlers
type MaintenanceHandler struct {
	Cleanup *maintenance.CleanupHandler
	Backup  *maintenance.BackupHandler
}

// NewMaintenanceHandler creates a new MaintenanceHandler with all sub-handlers
func NewMaintenanceHandler(context *db.AppContext, logger *logger.Logger, auditService *audit.Service) *MaintenanceHandler {
	return &MaintenanceHandler{
		Cleanup: &maintenance.CleanupHandler{
			Context: context,
			Logger:  logger,
		},
		Backup: &maintenance.BackupHandler{
			Context:      context,
			Logger:       logger,
			AuditService: auditService,
		},
	}
}
