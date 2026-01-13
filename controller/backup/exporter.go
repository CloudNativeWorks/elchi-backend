package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// Exporter handles backup export operations
type Exporter struct {
	Context *db.AppContext
	Logger  *logger.Logger
}

// NewExporter creates a new backup exporter
func NewExporter(context *db.AppContext, logger *logger.Logger) *Exporter {
	return &Exporter{
		Context: context,
		Logger:  logger,
	}
}

// Export creates a backup based on the request
func (e *Exporter) Export(ctx context.Context, req ExportRequest, username string) (*BackupData, error) {
	e.Logger.Infof("📦 Starting backup export - type=%s, project=%s, user=%s", req.BackupType, req.ProjectID, username)

	// Validate request - ONLY allow project backups
	if req.BackupType != "project" {
		return nil, fmt.Errorf("only project-based backups are supported (backup_type must be 'project')")
	}
	if req.ProjectID == "" {
		return nil, fmt.Errorf("project_id is required for backup")
	}

	// Initialize backup data
	backup := &BackupData{
		Metadata: BackupMetadata{
			BackupID:     uuid.New().String(),
			BackupType:   req.BackupType,
			ProjectID:    req.ProjectID,
			CreatedBy:    username,
			CreatedAt:    time.Now(),
			Description:  req.Description,
			ElchiVersion: "1.0.0", // TODO: Get from version package
			Filters: BackupFilters{
				IncludeDefaults: req.IncludeDefaults,
			},
			Statistics: BackupStatistics{
				Collections: make(map[string]int),
			},
		},
		Settings:     SettingsBackup{},
		XDSResources: XDSResourcesBackup{},
		Templates:    TemplatesBackup{},
		Services:     ServicesBackup{},
		ACME:         ACMEBackup{},
	}

	// Get project name (always required since only project backups are supported)
	projectName, err := e.getProjectName(ctx, req.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to get project name: %w", err)
	}
	backup.Metadata.ProjectName = projectName

	// Export each category
	if err := e.exportSettings(ctx, backup, req); err != nil {
		return nil, fmt.Errorf("failed to export settings: %w", err)
	}

	if err := e.exportXDSResources(ctx, backup, req); err != nil {
		return nil, fmt.Errorf("failed to export XDS resources: %w", err)
	}

	if err := e.exportTemplates(ctx, backup, req); err != nil {
		return nil, fmt.Errorf("failed to export templates: %w", err)
	}

	if err := e.exportServices(ctx, backup, req); err != nil {
		return nil, fmt.Errorf("failed to export services: %w", err)
	}

	if err := e.exportACME(ctx, backup, req); err != nil {
		return nil, fmt.Errorf("failed to export ACME: %w", err)
	}

	// Calculate total statistics
	totalResources := 0
	for _, count := range backup.Metadata.Statistics.Collections {
		totalResources += count
	}
	backup.Metadata.Statistics.TotalResources = totalResources

	// Calculate size
	data, _ := json.Marshal(backup)
	backup.Metadata.Statistics.TotalSizeBytes = int64(len(data))

	e.Logger.Infof("✅ Backup export completed - id=%s, total_resources=%d, size=%d bytes",
		backup.Metadata.BackupID, totalResources, len(data))

	return backup, nil
}

// exportSettings exports settings collections
func (e *Exporter) exportSettings(ctx context.Context, backup *BackupData, req ExportRequest) error {
	// Projects - for project backup, only export the specific project
	projectFilter := e.buildProjectIDFilter(req)
	projects, err := e.exportCollection(ctx, "projects", req, projectFilter)
	if err != nil {
		return err
	}
	backup.Settings.Projects = projects
	backup.Metadata.Statistics.Collections["projects"] = len(projects)

	// Users - only users with base_project matching the project
	users, err := e.exportCollection(ctx, "users", req, e.buildUserFilter(req))
	if err != nil {
		return err
	}
	// Remove sensitive fields from users
	backup.Settings.Users = e.sanitizeUsers(users)
	backup.Metadata.Statistics.Collections["users"] = len(users)

	// Groups - only groups with project field matching
	groups, err := e.exportCollection(ctx, "groups", req, e.buildProjectFilter(req))
	if err != nil {
		return err
	}
	backup.Settings.Groups = groups
	backup.Metadata.Statistics.Collections["groups"] = len(groups)

	// Settings
	settings, err := e.exportCollection(ctx, "settings", req, e.buildProjectFilter(req))
	if err != nil {
		return err
	}
	backup.Settings.Settings = settings
	backup.Metadata.Statistics.Collections["settings"] = len(settings)

	return nil
}

// exportXDSResources exports XDS resource collections
func (e *Exporter) exportXDSResources(ctx context.Context, backup *BackupData, req ExportRequest) error {
	xdsFilter := e.buildXDSFilter(req)

	// Export in dependency order
	var err error

	backup.XDSResources.Secrets, err = e.exportCollection(ctx, "secrets", req, xdsFilter)
	if err != nil {
		return err
	}
	backup.Metadata.Statistics.Collections["secrets"] = len(backup.XDSResources.Secrets)

	backup.XDSResources.Endpoints, err = e.exportCollection(ctx, "endpoints", req, xdsFilter)
	if err != nil {
		return err
	}
	backup.Metadata.Statistics.Collections["endpoints"] = len(backup.XDSResources.Endpoints)

	backup.XDSResources.Extensions, err = e.exportCollection(ctx, "extensions", req, xdsFilter)
	if err != nil {
		return err
	}
	backup.Metadata.Statistics.Collections["extensions"] = len(backup.XDSResources.Extensions)

	backup.XDSResources.Clusters, err = e.exportCollection(ctx, "clusters", req, xdsFilter)
	if err != nil {
		return err
	}
	backup.Metadata.Statistics.Collections["clusters"] = len(backup.XDSResources.Clusters)

	backup.XDSResources.VirtualHosts, err = e.exportCollection(ctx, "virtual_hosts", req, xdsFilter)
	if err != nil {
		return err
	}
	backup.Metadata.Statistics.Collections["virtual_hosts"] = len(backup.XDSResources.VirtualHosts)

	backup.XDSResources.Routes, err = e.exportCollection(ctx, "routes", req, xdsFilter)
	if err != nil {
		return err
	}
	backup.Metadata.Statistics.Collections["routes"] = len(backup.XDSResources.Routes)

	backup.XDSResources.Filters, err = e.exportCollection(ctx, "filters", req, xdsFilter)
	if err != nil {
		return err
	}
	backup.Metadata.Statistics.Collections["filters"] = len(backup.XDSResources.Filters)

	backup.XDSResources.Listeners, err = e.exportCollection(ctx, "listeners", req, xdsFilter)
	if err != nil {
		return err
	}
	backup.Metadata.Statistics.Collections["listeners"] = len(backup.XDSResources.Listeners)

	backup.XDSResources.Bootstrap, err = e.exportCollection(ctx, "bootstrap", req, xdsFilter)
	if err != nil {
		return err
	}
	backup.Metadata.Statistics.Collections["bootstrap"] = len(backup.XDSResources.Bootstrap)

	backup.XDSResources.TLS, err = e.exportCollection(ctx, "tls", req, xdsFilter)
	if err != nil {
		return err
	}
	backup.Metadata.Statistics.Collections["tls"] = len(backup.XDSResources.TLS)

	return nil
}

// exportTemplates exports template collections
func (e *Exporter) exportTemplates(ctx context.Context, backup *BackupData, req ExportRequest) error {
	projectFilter := e.buildProjectFilter(req)

	var err error

	backup.Templates.ResourceTemplates, err = e.exportCollection(ctx, "resource_templates", req, projectFilter)
	if err != nil {
		return err
	}
	backup.Metadata.Statistics.Collections["resource_templates"] = len(backup.Templates.ResourceTemplates)

	backup.Templates.Snippets, err = e.exportCollection(ctx, "snippets", req, projectFilter)
	if err != nil {
		return err
	}
	backup.Metadata.Statistics.Collections["snippets"] = len(backup.Templates.Snippets)

	backup.Templates.Scenarios, err = e.exportCollection(ctx, "scenarios", req, projectFilter)
	if err != nil {
		return err
	}
	backup.Metadata.Statistics.Collections["scenarios"] = len(backup.Templates.Scenarios)

	return nil
}

// exportServices exports service management collections
func (e *Exporter) exportServices(ctx context.Context, backup *BackupData, req ExportRequest) error {
	projectFilter := e.buildProjectFilter(req)

	var err error

	backup.Services.Clients, err = e.exportCollection(ctx, "clients", req, projectFilter)
	if err != nil {
		return err
	}
	backup.Metadata.Statistics.Collections["clients"] = len(backup.Services.Clients)

	backup.Services.Services, err = e.exportCollection(ctx, "services", req, projectFilter)
	if err != nil {
		return err
	}
	backup.Metadata.Statistics.Collections["services"] = len(backup.Services.Services)

	backup.Services.AdminPorts, err = e.exportCollection(ctx, "admin_ports", req, projectFilter)
	if err != nil {
		return err
	}
	backup.Metadata.Statistics.Collections["admin_ports"] = len(backup.Services.AdminPorts)

	backup.Services.WAF, err = e.exportCollection(ctx, "waf", req, projectFilter)
	if err != nil {
		return err
	}
	backup.Metadata.Statistics.Collections["waf"] = len(backup.Services.WAF)

	return nil
}

// exportACME exports ACME certificate management collections
func (e *Exporter) exportACME(ctx context.Context, backup *BackupData, req ExportRequest) error {
	projectFilter := e.buildProjectFilter(req)

	var err error

	backup.ACME.ACMEAccounts, err = e.exportCollection(ctx, "acme_accounts", req, projectFilter)
	if err != nil {
		return err
	}
	backup.Metadata.Statistics.Collections["acme_accounts"] = len(backup.ACME.ACMEAccounts)

	backup.ACME.ACMEDNSCredentials, err = e.exportCollection(ctx, "acme_dns_credentials", req, projectFilter)
	if err != nil {
		return err
	}
	backup.Metadata.Statistics.Collections["acme_dns_credentials"] = len(backup.ACME.ACMEDNSCredentials)

	backup.ACME.ACMETempKeys, err = e.exportCollection(ctx, "acme_temp_keys", req, projectFilter)
	if err != nil {
		return err
	}
	backup.Metadata.Statistics.Collections["acme_temp_keys"] = len(backup.ACME.ACMETempKeys)

	backup.ACME.ACMECertificates, err = e.exportCollection(ctx, "acme_certificates", req, projectFilter)
	if err != nil {
		return err
	}
	backup.Metadata.Statistics.Collections["acme_certificates"] = len(backup.ACME.ACMECertificates)

	return nil
}

// exportCollection exports a single collection with filters
func (e *Exporter) exportCollection(ctx context.Context, collectionName string, _ ExportRequest, filter bson.M) ([]primitive.M, error) {
	collection := e.Context.Client.Collection(collectionName)

	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to query %s: %w", collectionName, err)
	}
	defer cursor.Close(ctx)

	var results []primitive.M
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("failed to decode %s: %w", collectionName, err)
	}

	e.Logger.Debugf("Exported %d documents from %s", len(results), collectionName)
	return results, nil
}

// buildProjectIDFilter builds filter for projects collection (_id match)
func (e *Exporter) buildProjectIDFilter(req ExportRequest) bson.M {
	// Only project backups are supported - export the specific project by _id
	objectID, err := primitive.ObjectIDFromHex(req.ProjectID)
	if err != nil {
		e.Logger.Errorf("Invalid project ID format: %s", req.ProjectID)
		return bson.M{"_id": req.ProjectID} // Try as string if conversion fails
	}
	return bson.M{"_id": objectID}
}

// buildProjectFilter builds filter for project-scoped collections (project field match)
func (e *Exporter) buildProjectFilter(req ExportRequest) bson.M {
	// Only project backups are supported - filter by project ID
	return bson.M{"project": req.ProjectID}
}

// buildUserFilter builds filter for users collection (base_project match)
func (e *Exporter) buildUserFilter(req ExportRequest) bson.M {
	// Only project backups are supported - filter by base_project
	return bson.M{"base_project": req.ProjectID}
}

// buildXDSFilter builds filter for XDS resource collections
func (e *Exporter) buildXDSFilter(req ExportRequest) bson.M {
	// Only project backups are supported - filter by project
	// Global resources (project: null or not exists) are NOT included
	filter := bson.M{"general.project": req.ProjectID}

	// Exclude default resources unless requested
	if !req.IncludeDefaults {
		filter = bson.M{
			"$and": []bson.M{
				filter,
				{"general.metadata.is_default": bson.M{"$ne": true}},
			},
		}
	}

	return filter
}

// sanitizeUsers removes sensitive fields from user documents
func (e *Exporter) sanitizeUsers(users []primitive.M) []primitive.M {
	for i := range users {
		// Remove JWT tokens (they expire anyway)
		delete(users[i], "tokens")
		delete(users[i], "refresh_tokens")
		// Keep password hash (needed for restore)
	}
	return users
}

// getProjectName retrieves project name by ID
func (e *Exporter) getProjectName(ctx context.Context, projectID string) (string, error) {
	collection := e.Context.Client.Collection("projects")

	objectID, err := primitive.ObjectIDFromHex(projectID)
	if err != nil {
		return "", fmt.Errorf("invalid project ID: %w", err)
	}

	var project models.Project
	err = collection.FindOne(ctx, bson.M{"_id": objectID}).Decode(&project)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return "", fmt.Errorf("project not found: %s", projectID)
		}
		return "", err
	}

	if project.ProjectName != nil {
		return *project.ProjectName, nil
	}
	return "", fmt.Errorf("project name is nil")
}
