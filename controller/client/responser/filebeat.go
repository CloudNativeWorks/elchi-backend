package responser

import (
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
)

type FilebeatResponser struct {
}

func (p *FilebeatResponser) ValidateAndTransform(op models.OperationClass, response *pb.CommandResponse) any {
	// Check if filebeat response exists
	if response.GetFilebeat() == nil {
		return response
	}

	filebeatResponse := response.GetFilebeat()

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

	// Add filebeat specific data
	filebeatData := map[string]any{
		"success": filebeatResponse.GetSuccess(),
		"message": filebeatResponse.GetMessage(),
	}

	// Add operation-specific data based on subtype
	if op != nil {
		subType := op.GetSubTypeNum()
		filebeatData["operation"] = subType.String()

		switch subType {
		case pb.SubCommandType_GET_FILEBEAT_CONFIG:
			// Return current config
			if filebeatResponse.GetCurrentConfig() != nil {
				currentConfig := filebeatResponse.GetCurrentConfig()
				configData := map[string]any{}

				// Add inputs
				if len(currentConfig.GetInputs()) > 0 {
					inputs := make([]map[string]any, 0, len(currentConfig.GetInputs()))
					for _, input := range currentConfig.GetInputs() {
						inputs = append(inputs, map[string]any{
							"type":    input.GetType(),
							"enabled": input.GetEnabled(),
							"id":      input.GetId(),
							"paths":   input.GetPaths(),
						})
					}
					configData["inputs"] = inputs
				}

				// Add timestamp processor
				if currentConfig.GetTimestampProcessor() != nil {
					ts := currentConfig.GetTimestampProcessor()
					configData["timestamp_processor"] = map[string]any{
						"field":   ts.GetField(),
						"layouts": ts.GetLayouts(),
						"test":    ts.GetTest(),
					}
				}

				// Add drop fields processor
				if currentConfig.GetDropFieldsProcessor() != nil {
					df := currentConfig.GetDropFieldsProcessor()
					configData["drop_fields_processor"] = map[string]any{
						"fields": df.GetFields(),
					}
				}

				// Add filebeat output (oneof: elasticsearch or logstash)
				if currentConfig.GetFilebeatOutput() != nil {
					output := currentConfig.GetFilebeatOutput()
					outputData := map[string]any{}

					// Check which output type is configured
					if output.GetElasticsearch() != nil {
						es := output.GetElasticsearch()
						esData := map[string]any{
							"hosts":           es.GetHosts(),
							"loadbalance":     es.GetLoadbalance(),
							"skip_ssl_verify": es.GetSkipSslVerify(),
						}

						// Add authentication info if present
						if es.GetApiKey() != "" {
							esData["auth_type"] = "api_key"
							esData["api_key_configured"] = true // Don't expose the actual key
						} else if es.GetBasicAuth() != nil {
							esData["auth_type"] = "basic_auth"
							esData["username"] = es.GetBasicAuth().GetUsername()
							// Don't expose password
						}

						outputData["elasticsearch"] = esData
						configData["output_type"] = "elasticsearch"
					}

					if output.GetLogstash() != nil {
						ls := output.GetLogstash()
						outputData["logstash"] = map[string]any{
							"hosts":       ls.GetHosts(),
							"loadbalance": ls.GetLoadbalance(),
						}
						configData["output_type"] = "logstash"
					}

					configData["filebeat_output"] = outputData
				}

				filebeatData["current_config"] = configData
			}

		case pb.SubCommandType_GET_FILEBEAT_STATUS:
			// Return service status
			if filebeatResponse.GetServiceStatus() != "" {
				filebeatData["service_status"] = filebeatResponse.GetServiceStatus()
				// Derive is_running from service_status
				status := filebeatResponse.GetServiceStatus()
				filebeatData["is_running"] = (status == "active" || status == "running")
			}

		case pb.SubCommandType_UPDATE_FILEBEAT_CONFIG:
			// Config update result
			filebeatData["config_updated"] = filebeatResponse.GetSuccess()

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
			filebeatData["action"] = action
			filebeatData["service_status"] = filebeatResponse.GetServiceStatus()

		case pb.SubCommandType_SUB_LOGS:
			// Return logs (array of log entries)
			if len(filebeatResponse.GetLogs()) > 0 {
				logs := make([]map[string]any, 0, len(filebeatResponse.GetLogs()))
				for _, log := range filebeatResponse.GetLogs() {
					logs = append(logs, map[string]any{
						"timestamp": log.GetTimestamp(),
						"message":   log.GetMessage(),
						"level":     log.GetLevel(),
					})
				}
				filebeatData["logs"] = logs
			}
		}
	}

	// Add error details if operation failed
	if !response.Success || !filebeatResponse.GetSuccess() {
		filebeatData["error_message"] = filebeatResponse.GetMessage()
		if filebeatResponse.GetMessage() != "" {
			filebeatData["detailed_error"] = filebeatResponse.GetMessage()
		}
	}

	// Add operation summary for logging/monitoring
	if response.Success && filebeatResponse.GetSuccess() {
		if op != nil {
			subType := op.GetSubTypeNum()
			switch subType {
			case pb.SubCommandType_UPDATE_FILEBEAT_CONFIG:
				filebeatData["operation_summary"] = map[string]any{
					"action": "config_updated",
				}
			case pb.SubCommandType_GET_FILEBEAT_CONFIG:
				filebeatData["operation_summary"] = map[string]any{
					"action": "config_retrieved",
				}
			case pb.SubCommandType_GET_FILEBEAT_STATUS:
				status := filebeatResponse.GetServiceStatus()
				filebeatData["operation_summary"] = map[string]any{
					"action":     "status_checked",
					"is_running": (status == "active" || status == "running"),
				}
			case pb.SubCommandType_SUB_START, pb.SubCommandType_SUB_STOP, pb.SubCommandType_SUB_RESTART:
				action := subType.String()
				filebeatData["operation_summary"] = map[string]any{
					"action": action,
					"status": filebeatResponse.GetServiceStatus(),
				}
			case pb.SubCommandType_SUB_LOGS:
				filebeatData["operation_summary"] = map[string]any{
					"action": "logs_retrieved",
				}
			}
		}
	}

	result["filebeat"] = filebeatData
	return result
}
