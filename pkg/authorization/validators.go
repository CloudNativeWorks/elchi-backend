package authorization

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

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

// ExtractProjectFromRequest safely extracts project ID from various sources.
// Priority: Path param > Query param > Request body.
//
// IMPORTANT: When falling back to the body, this reads from BodyCaptureMiddleware's
// cached copy (`_original_body`) rather than calling c.ShouldBindJSON. Calling
// ShouldBindJSON here would consume c.Request.Body and any handler that later
// tries to bind its own request struct gets "EOF". This bug existed silently
// for endpoints with no `?project=` query param (e.g. /setting/license/activate)
// — those calls fell through to the body-read branch and broke the handler.
func ExtractProjectFromRequest(c *gin.Context) string {
	if project := c.Param("project"); project != "" {
		return project
	}
	if project := c.Query("project"); project != "" {
		return project
	}
	if c.Request.Method != http.MethodPost && c.Request.Method != http.MethodPut && c.Request.Method != http.MethodPatch {
		return ""
	}
	cached, exists := c.Get("_original_body")
	if !exists {
		return ""
	}
	bodyBytes, ok := cached.([]byte)
	if !ok || len(bodyBytes) == 0 {
		return ""
	}
	var body struct {
		Project string `json:"project"`
	}
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		return ""
	}
	return body.Project
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
