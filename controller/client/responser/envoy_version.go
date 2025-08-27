package responser

import (
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
)

type EnvoyVersionResponser struct {
}

func (p *EnvoyVersionResponser) ValidateAndTransform(op models.OperationClass, response *pb.CommandResponse) any {
	// Check if envoy_version response exists
	if response.GetEnvoyVersion() == nil {
		return response
	}

	envoyVersionResponse := response.GetEnvoyVersion()
	
	// Create enhanced response structure
	result := map[string]any{
		"success": response.Success,
		"error":   response.Error,
	}

	// Add client identity information
	if response.GetIdentity() != nil {
		result["client_id"] = response.GetIdentity().GetClientId()
		result["client_name"] = response.GetIdentity().GetClientName()
	}

	// Add envoy_version specific data
	envoyVersionData := map[string]any{
		"status":        envoyVersionResponse.GetStatus(),
		"error_message": envoyVersionResponse.GetErrorMessage(),
	}

	// Add operation-specific data based on what was requested
	if op != nil && op.GetEnvoyVersion() != nil {
		operation := op.GetEnvoyVersion().GetOperation()
		envoyVersionData["operation"] = operation.String()

		switch operation {
		case pb.EnvoyVersionOperation_GET_VERSIONS:
			if len(envoyVersionResponse.GetDownloadedVersions()) > 0 {
				envoyVersionData["downloaded_versions"] = envoyVersionResponse.GetDownloadedVersions()
			} else {
				envoyVersionData["downloaded_versions"] = []string{}
			}

		case pb.EnvoyVersionOperation_SET_VERSION:
			if envoyVersionResponse.GetInstalledVersion() != "" {
				envoyVersionData["installed_version"] = envoyVersionResponse.GetInstalledVersion()
			}
			if envoyVersionResponse.GetDownloadPath() != "" {
				envoyVersionData["download_path"] = envoyVersionResponse.GetDownloadPath()
			}
			// Add requested version info
			envoyVersionData["requested_version"] = op.GetEnvoyVersion().GetVersion()
			envoyVersionData["force_download"] = op.GetEnvoyVersion().GetForceDownload()
		}
	}

	// Add status-specific information
	status := envoyVersionResponse.GetStatus()
	switch status {
	case pb.EnvoyVersionStatus_SUCCESS:
		envoyVersionData["success_details"] = "Operation completed successfully"
	case pb.EnvoyVersionStatus_VERSION_NOT_FOUND:
		envoyVersionData["error_type"] = "version_not_available"
	case pb.EnvoyVersionStatus_DOWNLOAD_FAILED:
		envoyVersionData["error_type"] = "download_error"
	case pb.EnvoyVersionStatus_NETWORK_ERROR:
		envoyVersionData["error_type"] = "network_connectivity"
	case pb.EnvoyVersionStatus_PERMISSION_FAILED:
		envoyVersionData["error_type"] = "filesystem_permission"
	case pb.EnvoyVersionStatus_DIRECTORY_ERROR:
		envoyVersionData["error_type"] = "directory_access"
	default:
		envoyVersionData["unknown_status"] = status.String()
	}

	// Add enhanced error information
	if !response.Success && envoyVersionResponse.GetErrorMessage() != "" {
		envoyVersionData["detailed_error"] = envoyVersionResponse.GetErrorMessage()
		
		// Categorize errors for better frontend handling
		errorMsg := envoyVersionResponse.GetErrorMessage()
		if strings.Contains(errorMsg, "network") || strings.Contains(errorMsg, "connection") {
			envoyVersionData["retry_recommended"] = true
		}
		if strings.Contains(errorMsg, "permission") || strings.Contains(errorMsg, "access denied") {
			envoyVersionData["permission_issue"] = true
		}
		if strings.Contains(errorMsg, "space") || strings.Contains(errorMsg, "disk") {
			envoyVersionData["disk_issue"] = true
		}
	}

	// Add operation summary for logging/monitoring
	if response.Success {
		if op != nil && op.GetEnvoyVersion() != nil {
			operation := op.GetEnvoyVersion().GetOperation()
			switch operation {
			case pb.EnvoyVersionOperation_SET_VERSION:
				envoyVersionData["operation_summary"] = map[string]any{
					"action": "version_installed",
					"version": envoyVersionResponse.GetInstalledVersion(),
					"path": envoyVersionResponse.GetDownloadPath(),
				}
			case pb.EnvoyVersionOperation_GET_VERSIONS:
				envoyVersionData["operation_summary"] = map[string]any{
					"action": "versions_listed", 
					"count": len(envoyVersionResponse.GetDownloadedVersions()),
				}
			}
		}
	}

	result["envoy_version"] = envoyVersionData
	return result
}

