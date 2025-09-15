package authorization

import (
	"context"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/mongo"
)

// ValidateRequestProject validates and enforces project access for a request
// This is the main function to be used in handlers
func ValidateRequestProject(ctx context.Context, db *mongo.Database, userDetails models.UserDetails, projectID string) error {
	validator := NewProjectValidator(db)
	return validator.ValidateProjectAccess(ctx, userDetails, projectID)
}

// ExtractProjectFromRequest safely extracts project ID from various sources
// Priority: Path param > Query param > Request body
func ExtractProjectFromRequest(c *gin.Context) string {
	// First check path parameter
	if project := c.Param("project"); project != "" {
		return project
	}

	// Then check query parameter
	if project := c.Query("project"); project != "" {
		return project
	}

	// Finally check request body (for POST/PUT requests)
	var body struct {
		Project string `json:"project"`
	}
	if c.Request.Method == "POST" || c.Request.Method == "PUT" || c.Request.Method == "PATCH" {
		// Try to bind JSON without consuming the body
		if err := c.ShouldBindJSON(&body); err == nil && body.Project != "" {
			return body.Project
		}
	}

	return ""
}

// EnforceProjectFilter adds project filter to MongoDB queries based on user permissions
func EnforceProjectFilter(ctx context.Context, db *mongo.Database, userDetails models.UserDetails, baseFilter map[string]any, project string) (map[string]any, error) {
	if baseFilter == nil {
		baseFilter = make(map[string]any)
	}

	// Include both accessible projects and all default resources (from any project)
	baseFilter["$or"] = []map[string]any{
		{"general.project": project},
		{"general.metadata.is_default": true},
	}

	return baseFilter, nil
}

// ValidateResourceProject validates that a resource belongs to an accessible project
func ValidateResourceProject(ctx context.Context, db *mongo.Database, userDetails models.UserDetails, resource models.ResourceClass) error {
	if resource == nil {
		return fmt.Errorf("resource cannot be nil")
	}

	general := resource.GetGeneral()

	// Default resources (with is_default metadata) can be read by everyone regardless of project
	if isDefault, exists := general.Metadata["is_default"]; exists && isDefault == true {
		return nil // Allow read access for all default resources
	}

	if general.Project == "" {
		return ErrEmptyProject
	}

	return ValidateRequestProject(ctx, db, userDetails, general.Project)
}

// CanModifyResource checks if user can modify a specific resource
func CanModifyResource(ctx context.Context, db *mongo.Database, userDetails models.UserDetails, resource models.ResourceClass) error {
	// First check if this is a default resource
	general := resource.GetGeneral()
	if isDefault, exists := general.Metadata["is_default"]; exists && isDefault == true {
		// Only owners can modify default resources
		if userDetails.Role != models.RoleOwner {
			return fmt.Errorf("only owners can modify default resources")
		}
		// Owner can modify (but not delete)
		return nil
	}

	// For non-default resources, check project access
	if err := ValidateResourceProject(ctx, db, userDetails, resource); err != nil {
		return err
	}

	// Check role permissions for modification
	switch userDetails.Role {
	case models.RoleOwner, models.RoleAdmin, models.RoleEditor:
		return nil
	case models.RoleViewer:
		return fmt.Errorf("viewers cannot modify resources")
	default:
		return ErrInsufficientPrivileges
	}
}

// CanDeleteResource checks if user can delete a specific resource
func CanDeleteResource(ctx context.Context, db *mongo.Database, userDetails models.UserDetails, resource models.ResourceClass) error {
	// First check if this is a default resource
	general := resource.GetGeneral()
	if isDefault, exists := general.Metadata["is_default"]; exists && isDefault == true {
		// Nobody can delete default resources (including owners)
		return fmt.Errorf("default resources cannot be deleted")
	}

	// For non-default resources, check project access
	if err := ValidateResourceProject(ctx, db, userDetails, resource); err != nil {
		return err
	}

	// Only Owners and Admins can delete resources
	switch userDetails.Role {
	case models.RoleOwner, models.RoleAdmin:
		return nil
	case models.RoleEditor, models.RoleViewer:
		return fmt.Errorf("only owners and admins can delete resources")
	default:
		return ErrInsufficientPrivileges
	}
}

// SetResourcePermissions sets appropriate permissions on a resource based on user's base group or user ID
// This function should be called during resource creation to set proper ownership
// If user has a base group, group gets permission; otherwise user gets permission
func SetResourcePermissions(resource models.ResourceClass, userDetails models.UserDetails) {
	if resource == nil {
		return
	}

	general := resource.GetGeneral()

	// Initialize permissions if empty
	if general.Permissions.Users == nil {
		general.Permissions.Users = make([]string, 0)
	}
	if general.Permissions.Groups == nil {
		general.Permissions.Groups = make([]string, 0)
	}

	// If user has a base group, add the base group to permissions
	if userDetails.BaseGroup != "" {
		general.Permissions.Groups = appendIfNotExists(general.Permissions.Groups, userDetails.BaseGroup)
	} else {
		// If no base group, add user ID to permissions
		general.Permissions.Users = appendIfNotExists(general.Permissions.Users, userDetails.UserID)
	}

	// Set the updated general back to resource
	resource.SetGeneral(&general)
}

// appendIfNotExists appends a string to slice if it doesn't already exist
func appendIfNotExists(slice []string, item string) []string {
	for _, existing := range slice {
		if existing == item {
			return slice
		}
	}
	return append(slice, item)
}
