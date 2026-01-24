package responser

import (
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
)

type WafVersionResponser struct{}

func (p *WafVersionResponser) ValidateAndTransform(op models.OperationClass, response *pb.CommandResponse) any {
	// Check if waf_version response exists
	if response.GetWafVersion() == nil {
		return response
	}

	wafVersionResponse := response.GetWafVersion()

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

	// Add waf_version specific data
	wafVersionData := map[string]any{
		"status":        wafVersionResponse.GetStatus(),
		"error_message": wafVersionResponse.GetErrorMessage(),
	}

	// Add operation-specific data based on what was requested
	if op != nil && op.GetWafVersion() != nil {
		operation := op.GetWafVersion().GetOperation()
		wafVersionData["operation"] = operation.String()

		switch operation {
		case pb.VersionOperation_GET_VERSIONS:
			if len(wafVersionResponse.GetDownloadedVersions()) > 0 {
				wafVersionData["downloaded_versions"] = wafVersionResponse.GetDownloadedVersions()
			} else {
				wafVersionData["downloaded_versions"] = []string{}
			}

		case pb.VersionOperation_SET_VERSION:
			if wafVersionResponse.GetInstalledVersion() != "" {
				wafVersionData["installed_version"] = wafVersionResponse.GetInstalledVersion()
			}
			if wafVersionResponse.GetDownloadPath() != "" {
				wafVersionData["download_path"] = wafVersionResponse.GetDownloadPath()
			}
			// Add requested version info
			wafVersionData["requested_version"] = op.GetWafVersion().GetVersion()
			wafVersionData["force_download"] = op.GetWafVersion().GetForceDownload()
		}
	}

	// Add status-specific information
	status := wafVersionResponse.GetStatus()
	switch status {
	case pb.VersionStatus_SUCCESS:
		wafVersionData["success_details"] = "Operation completed successfully"
	case pb.VersionStatus_VERSION_NOT_FOUND:
		wafVersionData["error_type"] = "version_not_available"
	case pb.VersionStatus_DOWNLOAD_FAILED:
		wafVersionData["error_type"] = "download_error"
	case pb.VersionStatus_NETWORK_ERROR:
		wafVersionData["error_type"] = "network_connectivity"
	case pb.VersionStatus_PERMISSION_FAILED:
		wafVersionData["error_type"] = "filesystem_permission"
	case pb.VersionStatus_DIRECTORY_ERROR:
		wafVersionData["error_type"] = "directory_access"
	default:
		wafVersionData["unknown_status"] = status.String()
	}

	// Add enhanced error information
	if !response.Success && wafVersionResponse.GetErrorMessage() != "" {
		wafVersionData["detailed_error"] = wafVersionResponse.GetErrorMessage()

		// Categorize errors for better frontend handling
		errorMsg := wafVersionResponse.GetErrorMessage()
		if strings.Contains(errorMsg, "network") || strings.Contains(errorMsg, "connection") {
			wafVersionData["retry_recommended"] = true
		}
		if strings.Contains(errorMsg, "permission") || strings.Contains(errorMsg, "access denied") {
			wafVersionData["permission_issue"] = true
		}
		if strings.Contains(errorMsg, "space") || strings.Contains(errorMsg, "disk") {
			wafVersionData["disk_issue"] = true
		}
	}

	// Add operation summary for logging/monitoring
	if response.Success {
		if op != nil && op.GetWafVersion() != nil {
			operation := op.GetWafVersion().GetOperation()
			switch operation {
			case pb.VersionOperation_SET_VERSION:
				wafVersionData["operation_summary"] = map[string]any{
					"action":  "version_installed",
					"version": wafVersionResponse.GetInstalledVersion(),
					"path":    wafVersionResponse.GetDownloadPath(),
				}
			case pb.VersionOperation_GET_VERSIONS:
				wafVersionData["operation_summary"] = map[string]any{
					"action": "versions_listed",
					"count":  len(wafVersionResponse.GetDownloadedVersions()),
				}
			}
		}
	}

	result["waf_version"] = wafVersionData
	return result
}
