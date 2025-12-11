package backup

import (
	"fmt"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Validator handles backup validation
type Validator struct{}

// NewValidator creates a new validator
func NewValidator() *Validator {
	return &Validator{}
}

// Validate validates the backup data structure
func (v *Validator) Validate(backup *BackupData) *ValidationResult {
	result := &ValidationResult{
		Valid:             true,
		VersionCompatible: true,
		Warnings:          []string{},
		Errors:            []string{},
	}

	// Validate metadata
	if backup.Metadata.BackupID == "" {
		result.Errors = append(result.Errors, "Missing backup_id in metadata")
		result.Valid = false
	}

	if backup.Metadata.BackupType != "project" && backup.Metadata.BackupType != "global" {
		result.Errors = append(result.Errors, fmt.Sprintf("Invalid backup_type: %s (must be 'project' or 'global')", backup.Metadata.BackupType))
		result.Valid = false
	}

	if backup.Metadata.BackupType == "project" && backup.Metadata.ProjectID == "" {
		result.Errors = append(result.Errors, "Missing project_id for project backup")
		result.Valid = false
	}

	// Validate duplicate IDs
	if err := v.validateDuplicateIDs(backup); err != nil {
		result.Errors = append(result.Errors, err.Error())
		result.Valid = false
	}

	// Check for empty backup
	if backup.Metadata.Statistics.TotalResources == 0 {
		result.Warnings = append(result.Warnings, "Backup contains no resources")
	}

	return result
}

// validateDuplicateIDs checks for duplicate _id values within the backup
func (v *Validator) validateDuplicateIDs(backup *BackupData) error {
	seen := make(map[string]string) // _id -> collection_name

	// Check all collections for duplicate IDs
	collections := []struct {
		name string
		docs []primitive.M
	}{
		{"projects", backup.Settings.Projects},
		{"users", backup.Settings.Users},
		{"groups", backup.Settings.Groups},
		{"settings", backup.Settings.Settings},
		{"secrets", backup.XDSResources.Secrets},
		{"endpoints", backup.XDSResources.Endpoints},
		{"extensions", backup.XDSResources.Extensions},
		{"clusters", backup.XDSResources.Clusters},
		{"virtual_hosts", backup.XDSResources.VirtualHosts},
		{"routes", backup.XDSResources.Routes},
		{"filters", backup.XDSResources.Filters},
		{"listeners", backup.XDSResources.Listeners},
		{"bootstrap", backup.XDSResources.Bootstrap},
		{"tls", backup.XDSResources.TLS},
		{"resource_templates", backup.Templates.ResourceTemplates},
		{"snippets", backup.Templates.Snippets},
		{"scenarios", backup.Templates.Scenarios},
		{"clients", backup.Services.Clients},
		{"services", backup.Services.Services},
		{"admin_ports", backup.Services.AdminPorts},
		{"waf", backup.Services.WAF},
	}

	for _, coll := range collections {
		for _, doc := range coll.docs {
			id, ok := doc["_id"]
			if !ok {
				return fmt.Errorf("document in %s missing _id field", coll.name)
			}

			// Convert to string representation
			var idStr string
			switch v := id.(type) {
			case primitive.ObjectID:
				idStr = v.Hex()
			case string:
				idStr = v
			default:
				idStr = fmt.Sprintf("%v", v)
			}

			// Check for duplicates
			if existingColl, exists := seen[idStr]; exists {
				return fmt.Errorf("duplicate _id '%s' found in %s and %s", idStr, existingColl, coll.name)
			}
			seen[idStr] = coll.name
		}
	}

	return nil
}
