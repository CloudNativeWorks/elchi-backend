package processor

import (
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

type RsyslogProcessor struct {
	Logger *logger.Logger
}

func (p *RsyslogProcessor) ValidateAndTransform(op models.OperationClass, requestDetails models.RequestDetails, _ models.ServiceClients) (any, error) {
	// Get rsyslog operation details
	rsyslogOp := op.GetRsyslog()

	// Get subtype to determine operation
	subType := op.GetSubTypeNum()

	// Build request based on subtype
	switch subType {
	case client.SubCommandType_GET_RSYSLOG_CONFIG:
		return p.buildGetConfigRequest(op, requestDetails)
	case client.SubCommandType_GET_RSYSLOG_STATUS:
		return p.buildGetStatusRequest(op, requestDetails)
	case client.SubCommandType_UPDATE_RSYSLOG_CONFIG:
		return p.buildUpdateConfigRequest(op, requestDetails, rsyslogOp)
	case client.SubCommandType_SUB_START, client.SubCommandType_SUB_STOP, client.SubCommandType_SUB_RESTART:
		return p.buildServiceControlRequest(op, requestDetails, subType)
	case client.SubCommandType_SUB_LOGS:
		return p.buildGetLogsRequest(op, requestDetails)
	default:
		return nil, fmt.Errorf("unsupported rsyslog subcommand: %v", subType)
	}
}

// buildGetConfigRequest builds a GET_RSYSLOG_CONFIG request
func (p *RsyslogProcessor) buildGetConfigRequest(_ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	service := &client.Command_Rsyslog{
		Rsyslog: &client.RequestRsyslog{
			// Empty request - just signals to read current config
		},
	}

	p.Logger.Info("Built GET_RSYSLOG_CONFIG request", "client_id", requestDetails.ClientID)
	return service, nil
}

// buildGetStatusRequest builds a GET_RSYSLOG_STATUS request
func (p *RsyslogProcessor) buildGetStatusRequest(_ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	service := &client.Command_Rsyslog{
		Rsyslog: &client.RequestRsyslog{
			// Empty request - just signals to get status
		},
	}

	p.Logger.Info("Built GET_RSYSLOG_STATUS request", "client_id", requestDetails.ClientID)
	return service, nil
}

// buildUpdateConfigRequest builds an UPDATE_RSYSLOG_CONFIG request
func (p *RsyslogProcessor) buildUpdateConfigRequest(_ models.OperationClass, requestDetails models.RequestDetails, rsyslogOp *client.RequestRsyslog) (any, error) {
	if rsyslogOp == nil {
		return nil, fmt.Errorf("rsyslog configuration is required for UPDATE_RSYSLOG_CONFIG operation")
	}

	// Validate config structure
	if rsyslogOp.GetRsyslogConfig() == nil {
		return nil, fmt.Errorf("rsyslog.rsyslog_config is required for UPDATE_RSYSLOG_CONFIG operation")
	}

	config := rsyslogOp.GetRsyslogConfig()
	if config.GetRsyslogOutput() == nil {
		return nil, fmt.Errorf("rsyslog.rsyslog_config.rsyslog_output is required for UPDATE_RSYSLOG_CONFIG operation")
	}

	output := config.GetRsyslogOutput()

	// Validate required fields
	if output.GetTarget() == "" {
		return nil, fmt.Errorf("rsyslog.rsyslog_output.target is required for UPDATE_RSYSLOG_CONFIG operation")
	}

	if output.GetPort() == 0 {
		return nil, fmt.Errorf("rsyslog.rsyslog_output.port is required for UPDATE_RSYSLOG_CONFIG operation")
	}

	if output.GetProtocol() == "" {
		return nil, fmt.Errorf("rsyslog.rsyslog_output.protocol is required for UPDATE_RSYSLOG_CONFIG operation (udp or tcp)")
	}

	// Validate protocol value
	protocol := output.GetProtocol()
	if protocol != "udp" && protocol != "tcp" {
		return nil, fmt.Errorf("rsyslog.rsyslog_output.protocol must be either 'udp' or 'tcp', got: %s", protocol)
	}

	service := &client.Command_Rsyslog{
		Rsyslog: rsyslogOp, // Pass the entire structure
	}

	p.Logger.Info("Built UPDATE_RSYSLOG_CONFIG request",
		"client_id", requestDetails.ClientID,
		"target", output.GetTarget(),
		"port", output.GetPort(),
		"protocol", output.GetProtocol())

	return service, nil
}

// buildServiceControlRequest builds START/STOP/RESTART requests
func (p *RsyslogProcessor) buildServiceControlRequest(_ models.OperationClass, requestDetails models.RequestDetails, subType client.SubCommandType) (any, error) {
	service := &client.Command_Rsyslog{
		Rsyslog: &client.RequestRsyslog{
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

	p.Logger.Info("Built Rsyslog service control request",
		"client_id", requestDetails.ClientID,
		"action", action)

	return service, nil
}

// buildGetLogsRequest builds a SUB_LOGS request for rsyslog logs
func (p *RsyslogProcessor) buildGetLogsRequest(_ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	service := &client.Command_Rsyslog{
		Rsyslog: &client.RequestRsyslog{
			// Empty request - just signals to get logs
		},
	}

	p.Logger.Info("Built GET_RSYSLOG_LOGS request", "client_id", requestDetails.ClientID)
	return service, nil
}
