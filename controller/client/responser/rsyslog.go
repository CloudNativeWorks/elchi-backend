package responser

import (
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
)

type RsyslogResponser struct{}

func (p *RsyslogResponser) ValidateAndTransform(op models.OperationClass, response *pb.CommandResponse) any {
	// Check if rsyslog response exists
	if response.GetRsyslog() == nil {
		return response
	}

	rsyslogResponse := response.GetRsyslog()

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

	// Add rsyslog specific data
	rsyslogData := map[string]any{
		"success": rsyslogResponse.GetSuccess(),
		"message": rsyslogResponse.GetMessage(),
	}

	// Add operation-specific data based on subtype
	if op != nil {
		subType := op.GetSubTypeNum()
		rsyslogData["operation"] = subType.String()

		switch subType {
		case pb.SubCommandType_GET_RSYSLOG_CONFIG:
			// Return current config
			if rsyslogResponse.GetCurrentConfig() != nil {
				currentConfig := rsyslogResponse.GetCurrentConfig()
				if currentConfig.GetRsyslogOutput() != nil {
					output := currentConfig.GetRsyslogOutput()
					configData := map[string]any{
						"target":   output.GetTarget(),
						"port":     output.GetPort(),
						"protocol": output.GetProtocol(),
					}

					rsyslogData["current_config"] = configData
				}
			}

		case pb.SubCommandType_GET_RSYSLOG_STATUS:
			// Return service status
			if rsyslogResponse.GetServiceStatus() != "" {
				rsyslogData["service_status"] = rsyslogResponse.GetServiceStatus()
				// Derive is_running from service_status
				status := rsyslogResponse.GetServiceStatus()
				rsyslogData["is_running"] = (status == "active" || status == "running")
			}

		case pb.SubCommandType_UPDATE_RSYSLOG_CONFIG:
			// Config update result
			rsyslogData["config_updated"] = rsyslogResponse.GetSuccess()

		case pb.SubCommandType_SUB_START, pb.SubCommandType_SUB_STOP, pb.SubCommandType_SUB_RESTART:
			// Service control result
			action := ""
			switch subType {
			case pb.SubCommandType_SUB_START:
				action = "started"
			case pb.SubCommandType_SUB_STOP:
				action = "stopped"
			case pb.SubCommandType_SUB_RESTART:
				action = "restarted"
			}
			rsyslogData["action"] = action
			rsyslogData["service_status"] = rsyslogResponse.GetServiceStatus()

		case pb.SubCommandType_SUB_LOGS:
			// Return logs (array of log entries)
			if len(rsyslogResponse.GetLogs()) > 0 {
				logs := make([]map[string]any, 0, len(rsyslogResponse.GetLogs()))
				for _, log := range rsyslogResponse.GetLogs() {
					logs = append(logs, map[string]any{
						"timestamp": log.GetTimestamp(),
						"message":   log.GetMessage(),
						"level":     log.GetLevel(),
						"component": log.GetComponent(),
					})
				}
				rsyslogData["logs"] = logs
			} else {
				rsyslogData["logs"] = []map[string]any{}
			}
		}
	}

	// Add error details if operation failed
	if !response.Success || !rsyslogResponse.GetSuccess() {
		rsyslogData["error_message"] = rsyslogResponse.GetMessage()
		if rsyslogResponse.GetMessage() != "" {
			rsyslogData["detailed_error"] = rsyslogResponse.GetMessage()
		}
	}

	// Add operation summary for logging/monitoring
	if response.Success && rsyslogResponse.GetSuccess() {
		if op != nil {
			subType := op.GetSubTypeNum()
			switch subType {
			case pb.SubCommandType_UPDATE_RSYSLOG_CONFIG:
				rsyslogData["operation_summary"] = map[string]any{
					"action": "config_updated",
				}
			case pb.SubCommandType_GET_RSYSLOG_CONFIG:
				rsyslogData["operation_summary"] = map[string]any{
					"action": "config_retrieved",
				}
			case pb.SubCommandType_GET_RSYSLOG_STATUS:
				status := rsyslogResponse.GetServiceStatus()
				rsyslogData["operation_summary"] = map[string]any{
					"action":     "status_checked",
					"is_running": (status == "active" || status == "running"),
				}
			case pb.SubCommandType_SUB_START, pb.SubCommandType_SUB_STOP, pb.SubCommandType_SUB_RESTART:
				action := subType.String()
				rsyslogData["operation_summary"] = map[string]any{
					"action": action,
					"status": rsyslogResponse.GetServiceStatus(),
				}
			case pb.SubCommandType_SUB_LOGS:
				rsyslogData["operation_summary"] = map[string]any{
					"action": "logs_retrieved",
				}
			}
		}
	}

	result["rsyslog"] = rsyslogData
	return result
}
