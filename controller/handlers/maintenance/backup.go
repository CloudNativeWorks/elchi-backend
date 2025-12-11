package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/CloudNativeWorks/elchi-backend/controller/backup"
	"github.com/CloudNativeWorks/elchi-backend/pkg/audit"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/gin-gonic/gin"
)

// BackupHandler handles backup and restore operations
type BackupHandler struct {
	Context      *db.AppContext
	Logger       *logger.Logger
	AuditService *audit.Service
}

// ExportBackup handles backup export
// POST /api/v3/setting/maintenance/backup/export
func (h *BackupHandler) ExportBackup(c *gin.Context) {
	var req backup.ExportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid request: %v", err)})
		return
	}

	// Get username for logging
	username := "unknown"
	if uname, exists := c.Get("user_name"); exists {
		if unamePtr, ok := uname.(*string); ok && unamePtr != nil {
			username = *unamePtr
		}
	}

	h.Logger.Infof("📦 Backup export request - type=%s, project=%s, user=%s", req.BackupType, req.ProjectID, username)

	ctx := context.Background()

	// Create exporter and export
	exporter := backup.NewExporter(h.Context, h.Logger)
	backupData, err := exporter.Export(ctx, req, username)

	// Set audit context after export completes
	h.setBackupAuditContext(c, "EXPORT_BACKUP", req.BackupType, req.ProjectID)

	if err != nil {
		h.Logger.Errorf("Backup export failed: %v", err)
		h.setAuditResult(c, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Backup export failed: %v", err)})
		return
	}

	// Set audit changes for successful export
	audit.SetAuditChanges(c, map[string]any{
		"backup_id":   backupData.Metadata.BackupID,
		"backup_type": req.BackupType,
		"project":     req.ProjectID,
	})
	h.setAuditResult(c, nil)

	// Return backup data as JSON for download
	c.Header("Content-Type", "application/json")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"backup-%s-%s.json\"",
		req.BackupType, backupData.Metadata.BackupID))

	c.JSON(http.StatusOK, backupData)
}

// ImportBackup handles backup import
// POST /api/v3/setting/maintenance/backup/import
func (h *BackupHandler) ImportBackup(c *gin.Context) {
	var req backup.ImportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid request: %v", err)})
		return
	}

	// Get username for logging
	username := "unknown"
	if uname, exists := c.Get("user_name"); exists {
		if unamePtr, ok := uname.(*string); ok && unamePtr != nil {
			username = *unamePtr
		}
	}

	h.Logger.Infof("📥 Backup import request - backup_id=%s, dry_run=%v, user=%s",
		req.BackupData.Metadata.BackupID, req.DryRun, username)

	// Validate backup first
	validator := backup.NewValidator()
	validationResult := validator.Validate(&req.BackupData)
	if !validationResult.Valid {
		h.Logger.Errorf("Backup validation failed: %v", validationResult.Errors)
		// Set audit context and result for validation failure
		backupType := req.BackupData.Metadata.BackupType
		project := req.BackupData.Metadata.ProjectID
		h.setBackupAuditContext(c, "IMPORT_BACKUP", backupType, project)
		h.setAuditResult(c, fmt.Errorf("validation failed"))
		c.JSON(http.StatusBadRequest, gin.H{
			"error":    "Backup validation failed",
			"errors":   validationResult.Errors,
			"warnings": validationResult.Warnings,
		})
		return
	}

	if len(validationResult.Warnings) > 0 {
		h.Logger.Warnf("Backup validation warnings: %v", validationResult.Warnings)
	}

	ctx := context.Background()

	// Create importer and import
	importer := backup.NewImporter(h.Context, h.Logger)
	response, err := importer.Import(ctx, &req.BackupData, username, req.DryRun)

	// Set audit context after import completes
	backupType := req.BackupData.Metadata.BackupType
	project := req.BackupData.Metadata.ProjectID

	// Use different action for dry-run to distinguish from actual import
	action := "IMPORT_BACKUP"
	if req.DryRun {
		action = "IMPORT_BACKUP_DRY"
	}
	h.setBackupAuditContext(c, action, backupType, project)

	if err != nil {
		h.Logger.Errorf("Backup import failed: %v", err)
		h.setAuditResult(c, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Backup import failed: %v", err)})
		return
	}

	// Set audit changes for successful import
	audit.SetAuditChanges(c, map[string]any{
		"backup_id":      req.BackupData.Metadata.BackupID,
		"dry_run":        req.DryRun,
		"total_created":  response.Summary.Created,
		"total_updated":  response.Summary.Updated,
		"total_failed":   response.Summary.Failed,
		"success":        response.Success,
	})
	h.setAuditResult(c, nil)

	// Return import response
	if response.Success {
		c.JSON(http.StatusOK, response)
	} else {
		c.JSON(http.StatusPartialContent, response) // 206 for partial success
	}
}

// ValidateBackup validates a backup file without importing
// POST /api/v3/setting/maintenance/backup/validate
func (h *BackupHandler) ValidateBackup(c *gin.Context) {
	var backupData backup.BackupData
	if err := c.ShouldBindJSON(&backupData); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid backup data: %v", err)})
		return
	}

	h.Logger.Infof("🔍 Backup validation request - backup_id=%s", backupData.Metadata.BackupID)

	// Validate backup
	validator := backup.NewValidator()
	result := validator.Validate(&backupData)

	// Prepare response
	response := map[string]interface{}{
		"valid":              result.Valid,
		"version_compatible": result.VersionCompatible,
		"warnings":           result.Warnings,
		"errors":             result.Errors,
		"backup_metadata":    backupData.Metadata,
	}

	if result.Valid {
		c.JSON(http.StatusOK, response)
	} else {
		c.JSON(http.StatusBadRequest, response)
	}
}

// GetBackupMetadata returns metadata from a backup file
// POST /api/v3/setting/maintenance/backup/metadata
func (h *BackupHandler) GetBackupMetadata(c *gin.Context) {
	var data map[string]interface{}
	if err := c.ShouldBindJSON(&data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid data: %v", err)})
		return
	}

	// Extract metadata
	metadata, ok := data["metadata"]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing metadata field in backup"})
		return
	}

	// Return metadata as formatted JSON
	metadataJSON, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to format metadata: %v", err)})
		return
	}

	c.Data(http.StatusOK, "application/json", metadataJSON)
}

// setBackupAuditContext sets audit context for backup operations
func (h *BackupHandler) setBackupAuditContext(c *gin.Context, action, backupType, project string) {
	if h.AuditService == nil {
		return
	}

	audit.SetAuditAction(c, action)
	audit.SetAuditResource(c, "backup", "", backupType, project)
}

// setAuditResult sets audit result (success/error)
func (h *BackupHandler) setAuditResult(c *gin.Context, err error) {
	if h.AuditService == nil {
		return
	}

	if err != nil {
		audit.SetAuditError(c, err.Error())
	} else {
		audit.SetAuditSuccess(c, true)
	}
}
