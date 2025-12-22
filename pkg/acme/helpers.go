package acme

import (
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// ValidateProvider validates DNS provider type
func ValidateProvider(provider string) error {
	switch provider {
	case ProviderManual, ProviderGoogle, ProviderGoDaddy, ProviderCloudflare, ProviderDigitalOcean, ProviderRoute53, ProviderLightsail:
		return nil
	default:
		return fmt.Errorf("invalid provider: %s (must be 'manual', 'google', 'godaddy', 'cloudflare', 'digitalocean', 'route53', or 'lightsail')", provider)
	}
}

// HasPermission checks if a user has permission to access a resource
func HasPermission(user models.UserDetails, permissions Permissions) bool {
	// Owner and Admin always have access
	if user.IsOwner || user.Role == models.RoleAdmin {
		return true
	}

	// Check user-specific permission
	for _, userID := range permissions.Users {
		if userID == user.UserID {
			return true
		}
	}

	// Build complete group list (includes base_group)
	allGroups := append([]string{}, user.Groups...)
	if user.BaseGroup != "" {
		allGroups = append(allGroups, user.BaseGroup)
	}

	// Check group-based permission
	for _, group := range permissions.Groups {
		for _, userGroup := range allGroups {
			if group == userGroup {
				return true
			}
		}
	}

	return false
}

// CanModifyResource checks if a user can modify (update/delete) a resource
func CanModifyResource(user models.UserDetails, permissions Permissions, createdBy string) bool {
	// Owner and Admin always can modify
	if user.IsOwner || user.Role == models.RoleAdmin {
		return true
	}

	// Creator can always modify their own resource
	if createdBy == user.UserID {
		return true
	}

	// Editors with permission can modify
	if user.Role == models.RoleEditor && HasPermission(user, permissions) {
		return true
	}

	// Viewers cannot modify
	return false
}

// SanitizeCredentialForDisplay removes sensitive data for API responses
func SanitizeCredentialForDisplay(cred *DNSCredential) map[string]any {
	return map[string]any{
		"id":             cred.ID.Hex(),
		"name":           cred.Name,
		"description":    cred.Description,
		"provider":       cred.Provider,
		"status":         cred.Status,
		"last_validated": cred.LastValidated,
		"last_error":     cred.LastError,
		"permissions":    cred.Permissions,
		"created_by":     cred.CreatedBy,
		"created_at":     cred.CreatedAt,
		"updated_at":     cred.UpdatedAt,
		// NOTE: credentials_encrypted is intentionally excluded for security
	}
}
