package processor

import (
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

type FilebeatProcessor struct {
	Logger *logger.Logger
}

func (p *FilebeatProcessor) ValidateAndTransform(op models.OperationClass, requestDetails models.RequestDetails, _ models.ServiceClients) (any, error) {
	// Get filebeat operation details
	filebeatOp := op.GetFilebeat()

	// Get subtype to determine operation
	subType := op.GetSubTypeNum()

	// Build request based on subtype
	switch subType {
	case client.SubCommandType_GET_FILEBEAT_CONFIG:
		return p.buildGetConfigRequest(op, requestDetails)
	case client.SubCommandType_GET_FILEBEAT_STATUS:
		return p.buildGetStatusRequest(op, requestDetails)
	case client.SubCommandType_UPDATE_FILEBEAT_CONFIG:
		return p.buildUpdateConfigRequest(op, requestDetails, filebeatOp)
	case client.SubCommandType_SUB_START, client.SubCommandType_SUB_STOP, client.SubCommandType_SUB_RESTART:
		return p.buildServiceControlRequest(op, requestDetails, subType)
	case client.SubCommandType_SUB_LOGS:
		return p.buildGetLogsRequest(op, requestDetails)
	default:
		return nil, fmt.Errorf("unsupported filebeat subcommand: %v", subType)
	}
}

// buildGetConfigRequest builds a GET_FILEBEAT_CONFIG request
func (p *FilebeatProcessor) buildGetConfigRequest(_ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	service := &client.Command_Filebeat{
		Filebeat: &client.RequestFilebeat{
			// Empty request - just signals to read current config
		},
	}

	p.Logger.Info("Built GET_FILEBEAT_CONFIG request", "client_id", requestDetails.ClientID)
	return service, nil
}

// buildGetStatusRequest builds a GET_FILEBEAT_STATUS request
func (p *FilebeatProcessor) buildGetStatusRequest(_ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	service := &client.Command_Filebeat{
		Filebeat: &client.RequestFilebeat{
			// Empty request - just signals to get status
		},
	}

	p.Logger.Info("Built GET_FILEBEAT_STATUS request", "client_id", requestDetails.ClientID)
	return service, nil
}

// buildUpdateConfigRequest builds an UPDATE_FILEBEAT_CONFIG request
func (p *FilebeatProcessor) buildUpdateConfigRequest(_ models.OperationClass, requestDetails models.RequestDetails, filebeatOp *client.RequestFilebeat) (any, error) {
	if filebeatOp == nil {
		return nil, fmt.Errorf("filebeat configuration is required for UPDATE_FILEBEAT_CONFIG operation")
	}

	// Validate required fields
	if len(filebeatOp.GetInputs()) == 0 {
		return nil, fmt.Errorf("filebeat.inputs is required for UPDATE_FILEBEAT_CONFIG operation")
	}

	// Validate each input
	for i, input := range filebeatOp.GetInputs() {
		if input.GetType() == "" {
			return nil, fmt.Errorf("filebeat.inputs[%d].type is required", i)
		}
		if input.GetId() == "" {
			return nil, fmt.Errorf("filebeat.inputs[%d].id is required", i)
		}
		if len(input.GetPaths()) == 0 {
			return nil, fmt.Errorf("filebeat.inputs[%d].paths is required and must not be empty", i)
		}
	}

	// Validate output configuration (at least one output must be configured)
	if filebeatOp.GetFilebeatOutput() == nil {
		return nil, fmt.Errorf("filebeat.filebeat_output is required for UPDATE_FILEBEAT_CONFIG operation")
	}

	// Validate that one of the outputs is configured (oneof validation)
	output := filebeatOp.GetFilebeatOutput()
	hasOutput := false
	outputType := "none"

	if output.GetElasticsearch() != nil {
		hasOutput = true
		outputType = "elasticsearch"
		// Validate Elasticsearch output
		if len(output.GetElasticsearch().GetHosts()) == 0 {
			return nil, fmt.Errorf("filebeat.filebeat_output.elasticsearch.hosts is required and must not be empty")
		}
	}

	if output.GetLogstash() != nil {
		if hasOutput {
			return nil, fmt.Errorf("only one output type can be configured (elasticsearch or logstash)")
		}
		hasOutput = true
		outputType = "logstash"
		// Validate Logstash output
		if len(output.GetLogstash().GetHosts()) == 0 {
			return nil, fmt.Errorf("filebeat.filebeat_output.logstash.hosts is required and must not be empty")
		}
	}

	if !hasOutput {
		return nil, fmt.Errorf("filebeat.filebeat_output must have either elasticsearch or logstash configured")
	}

	service := &client.Command_Filebeat{
		Filebeat: &client.RequestFilebeat{
			Inputs:              filebeatOp.GetInputs(),
			TimestampProcessor:  filebeatOp.GetTimestampProcessor(),
			DropFieldsProcessor: filebeatOp.GetDropFieldsProcessor(),
			FilebeatOutput:      filebeatOp.GetFilebeatOutput(),
		},
	}

	p.Logger.Info("Built UPDATE_FILEBEAT_CONFIG request",
		"client_id", requestDetails.ClientID,
		"inputs_count", len(filebeatOp.GetInputs()),
		"output_type", outputType)

	return service, nil
}

// buildServiceControlRequest builds START/STOP/RESTART requests
func (p *FilebeatProcessor) buildServiceControlRequest(_ models.OperationClass, requestDetails models.RequestDetails, subType client.SubCommandType) (any, error) {
	service := &client.Command_Filebeat{
		Filebeat: &client.RequestFilebeat{
			// Empty request - subtype indicates the action
		},
	}

	action := ""
	switch subType {
	case client.SubCommandType_SUB_START:
		action = "START"
	case client.SubCommandType_SUB_STOP:
		action = "STOP"
	case client.SubCommandType_SUB_RESTART:
		action = "RESTART"
	}

	p.Logger.Info("Built Filebeat service control request",
		"client_id", requestDetails.ClientID,
		"action", action)

	return service, nil
}

// buildGetLogsRequest builds a SUB_LOGS request for filebeat logs
func (p *FilebeatProcessor) buildGetLogsRequest(_ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	service := &client.Command_Filebeat{
		Filebeat: &client.RequestFilebeat{
			// Empty request - just signals to get logs
		},
	}

	p.Logger.Info("Built GET_FILEBEAT_LOGS request", "client_id", requestDetails.ClientID)
	return service, nil
}
