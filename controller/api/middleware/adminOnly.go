package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/CloudNativeWorks/elchi-backend/controller/handlers"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// ClientOperationExceptions defines which type+subtype combinations are allowed for editors/viewers
// Empty array means all subtypes allowed for that command type
var ClientOperationExceptions = map[string][]string{
	"CLIENT_LOGS":  {},                                                                          // All client log operations allowed (readonly)
	"CLIENT_STATS": {},                                                                          // All client stats operations allowed (readonly)
	"FRR_LOGS":     {},                                                                          // All FRR log operations allowed (readonly)
	"SERVICE":      {"SUB_LOGS", "SUB_STATUS"},                                                  // Only specific SERVICE subtypes allowed (readonly operations)
	"PROXY":        {},                                                                          // PROXY operations will be checked by path in command authorization
	"NETWORK":      {"SUB_GET_NETWORK_STATE", "SUB_GET_ROUTES", "SUB_GET_POLICIES", "SUB_LIST"}, // NETWORK readonly operations allowed
	"FILEBEAT":     {"GET_FILEBEAT_CONFIG", "GET_FILEBEAT_STATUS", "SUB_LOGS"},                  // FILEBEAT readonly operations allowed (config read, status, logs)
	"RSYSLOG":      {"GET_RSYSLOG_CONFIG", "GET_RSYSLOG_STATUS", "SUB_LOGS"},                    // RSYSLOG readonly operations allowed (config read, status, logs)
}

// isClientOperationAllowedForEditor checks if a client operation is allowed for editors
func isClientOperationAllowedForEditor(c *gin.Context) bool {
	// Try to get the body from middleware cache first
	if originalBody, exists := c.Get("_original_body"); exists {
		if bodyBytes, ok := originalBody.([]byte); ok {
			return checkOperationFromBody(bodyBytes)
		}
	}

	// Fallback: read body directly
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return false
	}

	// Restore body for further processing
	c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	return checkOperationFromBody(bodyBytes)
}

// checkOperationFromBody parses operation body and checks against exceptions
func checkOperationFromBody(bodyBytes []byte) bool {
	var operation struct {
		Type         string `json:"type"`
		SubType      string `json:"sub_type"`
		EnvoyVersion struct {
			Operation string `json:"operation"`
		} `json:"envoy_version"`
		WafVersion struct {
			Operation string `json:"operation"`
		} `json:"waf_version"`
		Command struct {
			Path     string `json:"path"`
			Method   string `json:"method"`
			Protocol string `json:"protocol"`
			BGP      struct {
				Operation string `json:"operation"`
			} `json:"bgp"`
		} `json:"command"`
	}

	if err := json.Unmarshal(bodyBytes, &operation); err != nil {
		return false
	}

	// Special case for PROXY type - check path in command (method doesn't matter, path determines read/write)
	if operation.Type == "PROXY" {
		// For PROXY type, only allow readonly paths
		readonlyPaths := []string{"/clusters", "/envoy"}
		for _, path := range readonlyPaths {
			if operation.Command.Path == path {
				return true
			}
		}
		// Write paths are not allowed for Editor/Viewer
		return false
	}

	// Special case for FRR type - check BGP operation for readonly commands
	if operation.Type == "FRR" {
		if operation.Command.Protocol == "FRR_PROTOCOL_BGP" {
			// BGP GET operations are readonly
			readonlyBGPOps := []string{"BGP_GET_SUMMARY", "BGP_GET_CONFIG", "BGP_GET_NEIGHBORS", "BGP_GET_ROUTES"}
			for _, op := range readonlyBGPOps {
				if operation.Command.BGP.Operation == op {
					return true
				}
			}
		}
		// Other FRR operations are admin-only
		return false
	}

	// Special case for ENVOY_VERSION type - check operation for readonly commands
	if operation.Type == "ENVOY_VERSION" {
		// GET_VERSIONS is readonly, SET_VERSION is admin-only
		if operation.EnvoyVersion.Operation == "GET_VERSIONS" {
			return true
		}
		return false
	}

	// Special case for WAF_VERSION type - check operation for readonly commands
	if operation.Type == "WAF_VERSION" {
		// GET_VERSIONS is readonly, SET_VERSION is admin-only
		if operation.WafVersion.Operation == "GET_VERSIONS" {
			return true
		}
		return false
	}

	// Check if this type+subtype combination is allowed for editors
	if allowedSubTypes, exists := ClientOperationExceptions[operation.Type]; exists {
		// If allowedSubTypes is empty, allow all subtypes for this type
		if len(allowedSubTypes) == 0 {
			return true
		}
		// Otherwise check specific subtypes
		for _, allowedSubType := range allowedSubTypes {
			if operation.SubType == allowedSubType {
				return true
			}
		}
	}

	return false
}

// checkBackupAuthorization checks if user is authorized for backup operation
func checkBackupAuthorization(c *gin.Context, userDetails models.UserDetails) error {
	path := c.Request.URL.Path

	switch {
	case strings.HasSuffix(path, "/export"):
		return checkBackupExportAuth(c, userDetails)
	case strings.HasSuffix(path, "/import"):
		return checkBackupImportAuth(c, userDetails)
	case strings.HasSuffix(path, "/validate"), strings.HasSuffix(path, "/metadata"):
		// validate / metadata are read-only but still expose payload
		// structure and project scope. Restrict to Admin + Owner so an
		// unauthenticated drive-by cannot probe arbitrary backup files.
		if userDetails.IsOwner || userDetails.Role == models.RoleAdmin {
			return nil
		}
		return errors.New("insufficient privileges to inspect backup payloads")
	}
	return nil
}

// checkBackupExportAuth enforces the existing rule set for /backup/export:
// global backups are Owner-only, project backups are Admin (with target
// project access) or Owner.
func checkBackupExportAuth(c *gin.Context, userDetails models.UserDetails) error {
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil // Let handler handle the error
	}
	c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	var req struct {
		BackupType string `json:"backup_type"`
		ProjectID  string `json:"project_id"`
	}
	if json.Unmarshal(bodyBytes, &req) != nil {
		return nil // Let handler validate
	}

	// Global backup = Owner only
	if req.BackupType == "global" {
		if !userDetails.IsOwner {
			return errors.New("only owners can create global backups")
		}
		return nil
	}

	// Project backup: Admin and Owner
	if req.BackupType == "project" {
		if userDetails.IsOwner {
			return nil
		}
		if userDetails.Role == models.RoleAdmin {
			if !userHasProject(userDetails, req.ProjectID) {
				return errors.New("admins can only backup projects they have access to")
			}
			return nil
		}
		return errors.New("insufficient privileges for project backup")
	}

	return nil
}

// checkBackupImportAuth enforces authorization for /backup/import. Imports
// are destructive — they upsert XDS resources into the target project and
// can silently overwrite production config — so the rules mirror /export:
//
//   - global backups (BackupData.Metadata.BackupType == "global"):
//     Owner-only. A global import wipes/replaces resources across every
//     project, including projects the actor has no access to.
//   - project backups: Owner OR Admin with TargetProject in user.Projects.
//
// Body shape and json keys must match controller/backup/models.go::ImportRequest.
func checkBackupImportAuth(c *gin.Context, userDetails models.UserDetails) error {
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil // surface the read error in the handler
	}
	c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	// Pull only the fields needed for the auth decision. The handler still
	// runs ShouldBindJSON afterwards on the full payload.
	var req struct {
		BackupData struct {
			Metadata struct {
				BackupType string `json:"backup_type"`
			} `json:"metadata"`
		} `json:"backup_data"`
		TargetProject string `json:"target_project"`
	}
	if json.Unmarshal(bodyBytes, &req) != nil {
		return nil // let the handler return 400 with a precise message
	}

	backupType := req.BackupData.Metadata.BackupType

	if backupType == "global" {
		if !userDetails.IsOwner {
			return errors.New("only owners can import global backups")
		}
		return nil
	}

	// Default to "project" semantics for any non-global value: importing
	// into a specific target requires Owner or an Admin with access to
	// that target project.
	if userDetails.IsOwner {
		return nil
	}
	if userDetails.Role == models.RoleAdmin {
		if !userHasProject(userDetails, req.TargetProject) {
			return errors.New("admins can only import into projects they have access to")
		}
		return nil
	}
	return errors.New("insufficient privileges for backup import")
}

// userHasProject reports whether the trigger user's project scope
// contains the requested ID. Owner-bypass is the caller's responsibility.
func userHasProject(userDetails models.UserDetails, projectID string) bool {
	for _, pid := range userDetails.Projects {
		if pid == projectID {
			return true
		}
	}
	return false
}

func InitSettingMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		userDetails, _ := handlers.GetUserDetails(c)

		// Special case for OpenRouter token GET - allow editors to read
		if c.Request.URL.Path == "/api/v3/setting/openrouter-token" && c.Request.Method == http.MethodGet {
			// Editors, Admins, and Owners can GET OpenRouter token
			if userDetails.Role == models.RoleEditor || userDetails.Role == models.RoleAdmin || userDetails.IsOwner {
				c.Next()
				return
			}
		}

		// Special case for license status GET - any authenticated user can read.
		// UI banners need plan/limit info regardless of role; mutating endpoints
		// (activate/api-key/check) still require Admin/Owner via the default path below.
		if c.Request.URL.Path == "/api/v3/setting/license" && c.Request.Method == http.MethodGet {
			c.Next()
			return
		}

		if strings.HasPrefix(c.Request.URL.Path, "/api/v3/setting/users/") && c.Request.Method == http.MethodGet {
			c.Next()
			return
		}

		// Special case for client list - Editor/Viewer can GET (list/view clients)
		if strings.HasPrefix(c.Request.URL.Path, "/api/op/clients") && c.Request.Method == http.MethodGet {
			// Editor and Viewer can view clients
			if userDetails.Role == models.RoleEditor || userDetails.Role == models.RoleViewer {
				c.Next()
				return
			}
		}

		// Special case for client operations - check for editor/viewer readonly commands
		if strings.HasPrefix(c.Request.URL.Path, "/api/op/clients") && c.Request.Method == http.MethodPost {
			if userDetails.Role == models.RoleEditor || userDetails.Role == models.RoleViewer {
				// Check if this operation is allowed for editors/viewers (readonly commands)
				if isClientOperationAllowedForEditor(c) {
					c.Next()
					return
				}
			}
		}

		// Special case for service operations - Editor/Viewer can GET (list/view services)
		if strings.HasPrefix(c.Request.URL.Path, "/api/op/services") && c.Request.Method == http.MethodGet {
			// Editor and Viewer can view services (with permission filtering in handler)
			if userDetails.Role == models.RoleEditor || userDetails.Role == models.RoleViewer {
				c.Next()
				return
			}
		}

		// Special case for jobs endpoints - Let all authenticated users pass (handler will do role checks)
		if strings.HasPrefix(c.Request.URL.Path, "/api/v3/jobs") {
			// Jobs handler performs its own Admin/Owner checks where needed
			c.Next()
			return
		}

		// Special case for WAF CRS rules - All users can GET CRS rules (readonly)
		if strings.HasPrefix(c.Request.URL.Path, "/api/v3/waf/crs") && c.Request.Method == http.MethodGet {
			// All authenticated users can read CRS rules
			c.Next()
			return
		}

		// Special case for WAF config GET operations - All users can view configs
		if strings.HasPrefix(c.Request.URL.Path, "/api/v3/waf/config") && c.Request.Method == http.MethodGet {
			// All authenticated users can read WAF configs
			// Editor and Viewer have readonly access
			c.Next()
			return
		}

		// Special case for WAF config CRUD operations - Handler performs Admin/Owner checks
		if strings.HasPrefix(c.Request.URL.Path, "/api/v3/waf/config") {
			// POST, PUT, DELETE operations
			// Handler will check for Admin/Owner role
			c.Next()
			return
		}

		// Special case for backup operations - check backup type authorization
		if strings.HasPrefix(c.Request.URL.Path, "/api/v3/setting/maintenance/backup/") {
			if err := checkBackupAuthorization(c, userDetails); err != nil {
				c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
				c.Abort()
				return
			}
			c.Next()
			return
		}

		// For all other operations, only owners and admins
		if !userDetails.IsOwner && userDetails.Role != models.RoleAdmin {
			c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
			c.Abort()
			return
		}
		c.Next()
	}
}
