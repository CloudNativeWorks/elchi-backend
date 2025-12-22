package backup

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// BackupMetadata contains information about the backup
type BackupMetadata struct {
	BackupID         string            `json:"backup_id"`
	BackupType       string            `json:"backup_type"` // "project" or "global"
	ProjectID        string            `json:"project_id,omitempty"`
	ProjectName      string            `json:"project_name,omitempty"`
	ElchiVersion     string            `json:"elchi_version"`
	CreatedBy        string            `json:"created_by"`
	CreatedAt        time.Time         `json:"created_at"`
	Description      string            `json:"description,omitempty"`
	Filters          BackupFilters     `json:"filters"`
	Statistics       BackupStatistics  `json:"statistics"`
}

// BackupFilters defines what was included in the backup
type BackupFilters struct {
	IncludeDefaults bool `json:"include_defaults"`
}

// BackupStatistics contains counts of backed up resources
type BackupStatistics struct {
	TotalResources int                `json:"total_resources"`
	TotalSizeBytes int64              `json:"total_size_bytes"`
	Collections    map[string]int     `json:"collections"`
}

// BackupData represents the complete backup file structure
type BackupData struct {
	Metadata     BackupMetadata         `json:"metadata"`
	Settings     SettingsBackup         `json:"settings"`
	XDSResources XDSResourcesBackup     `json:"xds_resources"`
	Templates    TemplatesBackup        `json:"templates"`
	Services     ServicesBackup         `json:"services"`
	ACME         ACMEBackup             `json:"acme"`
}

// SettingsBackup contains settings and authorization data
type SettingsBackup struct {
	Projects []primitive.M `json:"projects"`
	Users    []primitive.M `json:"users"`
	Groups   []primitive.M `json:"groups"`
	Settings []primitive.M `json:"settings"`
}

// XDSResourcesBackup contains all XDS resource collections
type XDSResourcesBackup struct {
	Secrets      []primitive.M `json:"secrets"`
	Endpoints    []primitive.M `json:"endpoints"`
	Extensions   []primitive.M `json:"extensions"`
	Clusters     []primitive.M `json:"clusters"`
	VirtualHosts []primitive.M `json:"virtual_hosts"`
	Routes       []primitive.M `json:"routes"`
	Filters      []primitive.M `json:"filters"`
	Listeners    []primitive.M `json:"listeners"`
	Bootstrap    []primitive.M `json:"bootstrap"`
	TLS          []primitive.M `json:"tls"`
}

// TemplatesBackup contains template and scenario data
type TemplatesBackup struct {
	ResourceTemplates []primitive.M `json:"resource_templates"`
	Snippets          []primitive.M `json:"snippets"`
	Scenarios         []primitive.M `json:"scenarios"`
}

// ServicesBackup contains service management data
type ServicesBackup struct {
	Clients    []primitive.M `json:"clients"`
	Services   []primitive.M `json:"services"`
	AdminPorts []primitive.M `json:"admin_ports"`
	WAF        []primitive.M `json:"waf"`
}

// ACMEBackup contains ACME certificate management data
type ACMEBackup struct {
	ACMEAccounts       []primitive.M `json:"acme_accounts"`
	ACMEDNSCredentials []primitive.M `json:"acme_dns_credentials"`
	ACMETempKeys       []primitive.M `json:"acme_temp_keys"`
	ACMECertificates   []primitive.M `json:"acme_certificates"`
}

// ExportRequest represents the request to export a backup
type ExportRequest struct {
	BackupType      string `json:"backup_type" binding:"required,oneof=project global"`
	ProjectID       string `json:"project_id"`
	IncludeDefaults bool   `json:"include_defaults"`
	Description     string `json:"description"`
}

// ImportRequest represents the request to import a backup
type ImportRequest struct {
	BackupData         BackupData `json:"backup_data" binding:"required"`
	TargetProject      string     `json:"target_project"`
	PermissionStrategy string     `json:"permission_strategy" binding:"omitempty,oneof=preserve reassign clear"`
	DryRun             bool       `json:"dry_run"`
}

// ImportResponse represents the result of an import operation
type ImportResponse struct {
	Success     bool                   `json:"success"`
	DryRun      bool                   `json:"dry_run"`
	Summary     ImportSummary          `json:"summary"`
	Details     map[string]CollectionImportDetail `json:"details"`
	Errors      []string               `json:"errors,omitempty"`
	ImportedBy  string                 `json:"imported_by,omitempty"`
	ImportedAt  time.Time              `json:"imported_at,omitempty"`
}

// ImportSummary provides high-level statistics
type ImportSummary struct {
	TotalResources int `json:"total_resources"`
	Created        int `json:"created"`
	Updated        int `json:"updated"`
	Failed         int `json:"failed"`
}

// CollectionImportDetail provides per-collection statistics
type CollectionImportDetail struct {
	Total    int      `json:"total"`
	Created  int      `json:"created"`
	Updated  int      `json:"updated"`
	Failed   int      `json:"failed"`
	Warnings []string `json:"warnings,omitempty"`
}

// ValidationResult represents backup validation results
type ValidationResult struct {
	Valid              bool     `json:"valid"`
	VersionCompatible  bool     `json:"version_compatible"`
	Warnings           []string `json:"warnings,omitempty"`
	Errors             []string `json:"errors,omitempty"`
}
