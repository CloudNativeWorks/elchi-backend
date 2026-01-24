package authorization

// CommandCategory defines the authorization level required for a command
type CommandCategory int

const (
	// ReadonlyCommand - Available to Editor and Viewer (with service permission check for service-related)
	ReadonlyCommand CommandCategory = iota

	// ServicePermissionCommand - Requires service permission check for Editor, Viewer readonly only
	ServicePermissionCommand

	// AdminOnlyCommand - Only Admin and Owner can execute
	AdminOnlyCommand
)

// Envoy admin API readonly paths (only read data, no modifications)
var envoyReadonlyPaths = []string{
	"/clusters", // View cluster information
	"/envoy",    // View Envoy configuration
}

// Envoy admin API write paths (modify runtime state)
var envoyWritePaths = []string{
	"/logging",          // Change log levels
	"/healthcheck/ok",   // Change health check status
	"/healthcheck/fail", // Change health check status
	"/reset_counters",   // Reset statistics counters
	"/reopen_logs",      // Reopen log files
	"/runtime_modify",   // Modify runtime configuration
}

// isEnvoyReadonlyPath checks if the given path is a readonly Envoy admin API path
func isEnvoyReadonlyPath(path string) bool {
	for _, readonlyPath := range envoyReadonlyPaths {
		if path == readonlyPath {
			return true
		}
	}
	return false
}

// isEnvoyWritePath checks if the given path is a write Envoy admin API path
func isEnvoyWritePath(path string) bool {
	for _, writePath := range envoyWritePaths {
		if path == writePath {
			return true
		}
	}
	return false
}

// GetCommandCategoryWithPath returns the authorization category for a command, considering path for PROXY commands
func GetCommandCategoryWithPath(commandType string, subType string, path string) CommandCategory {
	// PROXY command requires path-based decision
	if commandType == "PROXY" {
		if isEnvoyReadonlyPath(path) {
			return ReadonlyCommand
		}
		if isEnvoyWritePath(path) {
			return AdminOnlyCommand
		}
		// Unknown path, default to admin-only for safety
		return AdminOnlyCommand
	}

	// For other commands, use standard categorization
	return GetCommandCategory(commandType, subType)
}

// GetCommandCategory returns the authorization category for a given command type and subtype
func GetCommandCategory(commandType string, subType string) CommandCategory {
	switch commandType {
	// Readonly commands - Editor/Viewer allowed
	case "CLIENT_LOGS", "CLIENT_STATS", "FRR_LOGS":
		return ReadonlyCommand

	// PROXY requires path-based decision, default to admin-only for safety
	case "PROXY":
		return AdminOnlyCommand

	// Service-permission commands - Editor can do, Viewer readonly only
	case "DEPLOY", "UNDEPLOY", "UPDATE_BOOTSTRAP":
		return ServicePermissionCommand

	case "SERVICE":
		// SERVICE has both readonly and write operations
		switch subType {
		case "SUB_STATUS", "SUB_LOGS":
			return ReadonlyCommand
		case "SUB_START", "SUB_STOP", "SUB_RESTART", "SUB_RELOAD":
			return ServicePermissionCommand
		default:
			return ServicePermissionCommand
		}

	// Admin/Owner-only commands
	case "NETWORK":
		// NETWORK has both readonly and write operations
		switch subType {
		case "SUB_NETPLAN_APPLY", "SUB_NETPLAN_ROLLBACK":
			return AdminOnlyCommand
		case "SUB_ROUTE_MANAGE", "SUB_POLICY_MANAGE", "SUB_TABLE_MANAGE":
			return AdminOnlyCommand
		default:
			// List operations are readonly
			return ReadonlyCommand
		}

	case "FRR":
		// FRR configuration is admin-only
		return AdminOnlyCommand

	case "ENVOY_VERSION":
		// Changing Envoy version is admin-only
		if subType == "SUB_SET_VERSION" {
			return AdminOnlyCommand
		}
		// Reading version is readonly
		return ReadonlyCommand

	default:
		// Default to admin-only for unknown commands (safe default)
		return AdminOnlyCommand
	}
}
