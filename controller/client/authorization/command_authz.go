// Package authorization provides command authorization for client operations
// including role-based access control and command category permissions.
package authorization

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// CheckCommandAuthorization checks if a user can execute a command
// Returns nil if authorized, error if not
func CheckCommandAuthorization(
	ctx context.Context,
	appContext *db.AppContext,
	user models.UserDetails,
	commandType string,
	subType string,
	path string,
	serviceName string,
	project string,
	version string,
) error {
	// Owner and Admin can execute all commands
	if user.IsOwner || user.Role == models.RoleAdmin {
		return nil
	}

	// Use path-aware categorization for PROXY commands
	category := GetCommandCategoryWithPath(commandType, subType, path)

	switch category {
	case ReadonlyCommand:
		// Readonly commands are allowed for Editor and Viewer
		// For service-related readonly commands, check service permission
		if isServiceRelatedReadonly(commandType, subType) && serviceName != "" {
			return checkServicePermission(ctx, appContext, user, serviceName, project, version)
		}
		return nil

	case ServicePermissionCommand:
		// Viewer cannot execute service-permission commands
		if user.Role == models.RoleViewer {
			return fmt.Errorf("insufficient privileges: viewers cannot execute %s commands", commandType)
		}

		// Editor can execute with service permission
		if serviceName == "" {
			return fmt.Errorf("service name required for %s command", commandType)
		}
		return checkServicePermission(ctx, appContext, user, serviceName, project, version)

	case AdminOnlyCommand:
		// Only Admin and Owner allowed
		return fmt.Errorf("insufficient privileges: only admin and owner can execute %s commands", commandType)

	default:
		return fmt.Errorf("unknown command category")
	}
}

// isServiceRelatedReadonly checks if a readonly command is service-related
func isServiceRelatedReadonly(commandType string, subType string) bool {
	switch commandType {
	case "SERVICE":
		return subType == "SUB_STATUS" || subType == "SUB_LOGS"
	case "CLIENT_LOGS", "CLIENT_STATS":
		// These are client-related, not service-related
		return false
	default:
		return false
	}
}

// checkServicePermission checks if user has permission to access a service
func checkServicePermission(
	ctx context.Context,
	appContext *db.AppContext,
	user models.UserDetails,
	serviceName string,
	project string,
	version string,
) error {
	if appContext == nil || appContext.Client == nil {
		return fmt.Errorf("database context not available")
	}

	serviceCollection := appContext.Client.Collection("services")

	// Find the service
	filter := bson.M{
		"name":    serviceName,
		"project": project,
		"version": version,
	}

	var service models.Service
	err := serviceCollection.FindOne(ctx, filter).Decode(&service)
	if err != nil {
		return fmt.Errorf("service not found or access denied")
	}

	// Check if user has permission
	// Build complete group list including base_group
	allGroups := append([]string{}, user.Groups...)
	if user.BaseGroup != "" {
		found := false
		for _, g := range allGroups {
			if g == user.BaseGroup {
				found = true
				break
			}
		}
		if !found {
			allGroups = append(allGroups, user.BaseGroup)
		}
	}

	// Check if user is in permissions.users[]
	for _, userID := range service.Permissions.Users {
		if userID == user.UserID {
			return nil
		}
	}

	// Check if any of user's groups is in permissions.groups[]
	for _, groupID := range service.Permissions.Groups {
		for _, userGroupID := range allGroups {
			if groupID == userGroupID {
				return nil
			}
		}
	}

	return fmt.Errorf("you don't have permission to access this service")
}
