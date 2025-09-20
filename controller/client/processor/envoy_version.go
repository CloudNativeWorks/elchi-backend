package processor

import (
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

type EnvoyVersionProcessor struct {
	Logger *logger.Logger
}

func (p *EnvoyVersionProcessor) ValidateAndTransform(op models.OperationClass, requestDetails models.RequestDetails, _ models.ServiceClients) (any, error) {
	// Get envoy version operation details
	envoyVersionOp := op.GetEnvoyVersion()
	if envoyVersionOp == nil {
		return nil, fmt.Errorf("envoy_version operation is required for ENVOY_VERSION command")
	}

	// Get operation type
	operation := envoyVersionOp.GetOperation()

	// Build request based on operation type
	switch operation {
	case client.EnvoyVersionOperation_GET_VERSIONS:
		return p.buildGetVersionsRequest(op, requestDetails)
	case client.EnvoyVersionOperation_SET_VERSION:
		return p.buildSetVersionRequest(op, requestDetails)
	default:
		return nil, fmt.Errorf("unsupported envoy_version operation: %v (supported: GET_VERSIONS, SET_VERSION)", operation)
	}
}

func (p *EnvoyVersionProcessor) buildGetVersionsRequest(_ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	// GET_VERSIONS doesn't need additional parameters
	service := &client.Command_EnvoyVersion{
		EnvoyVersion: &client.RequestEnvoyVersion{
			Operation: client.EnvoyVersionOperation_GET_VERSIONS,
		},
	}

	p.Logger.Info("Built GET_VERSIONS request", "client_id", requestDetails.ClientID)
	return service, nil
}

func (p *EnvoyVersionProcessor) buildSetVersionRequest(op models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	envoyVersionOp := op.GetEnvoyVersion()

	// Validate required fields for SET_VERSION
	if envoyVersionOp.GetVersion() == "" {
		return nil, fmt.Errorf("envoy_version.version is required for SET_VERSION operation")
	}

	// Validate version format (should start with 'v')
	version := envoyVersionOp.GetVersion()
	if len(version) < 2 || version[0] != 'v' {
		return nil, fmt.Errorf("invalid version format: %s (should start with 'v', e.g., v1.35.0)", version)
	}

	service := &client.Command_EnvoyVersion{
		EnvoyVersion: &client.RequestEnvoyVersion{
			Operation:     client.EnvoyVersionOperation_SET_VERSION,
			Version:       version,
			ForceDownload: envoyVersionOp.GetForceDownload(),
		},
	}

	p.Logger.Info("Built SET_VERSION request",
		"client_id", requestDetails.ClientID,
		"version", version,
		"force_download", envoyVersionOp.GetForceDownload())

	return service, nil
}
