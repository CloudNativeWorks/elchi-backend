package processor

import (
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

type WafVersionProcessor struct {
	Logger *logger.Logger
}

func (p *WafVersionProcessor) ValidateAndTransform(op models.OperationClass, requestDetails models.RequestDetails, _ models.ServiceClients) (any, error) {
	// Get waf version operation details
	wafVersionOp := op.GetWafVersion()
	if wafVersionOp == nil {
		return nil, fmt.Errorf("waf_version operation is required for WAF_VERSION command")
	}

	// Get operation type
	operation := wafVersionOp.GetOperation()

	// Build request based on operation type
	switch operation {
	case client.VersionOperation_GET_VERSIONS:
		return p.buildGetVersionsRequest(op, requestDetails)
	case client.VersionOperation_SET_VERSION:
		return p.buildSetVersionRequest(op, requestDetails)
	default:
		return nil, fmt.Errorf("unsupported waf_version operation: %v (supported: GET_VERSIONS, SET_VERSION)", operation)
	}
}

func (p *WafVersionProcessor) buildGetVersionsRequest(_ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	// GET_VERSIONS doesn't need additional parameters
	service := &client.Command_WafVersion{
		WafVersion: &client.RequestWafVersion{
			Operation: client.VersionOperation_GET_VERSIONS,
		},
	}

	p.Logger.Info("Built WAF GET_VERSIONS request", "client_id", requestDetails.ClientID)
	return service, nil
}

func (p *WafVersionProcessor) buildSetVersionRequest(op models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	wafVersionOp := op.GetWafVersion()

	// Validate required fields for SET_VERSION
	if wafVersionOp.GetVersion() == "" {
		return nil, fmt.Errorf("waf_version.version is required for SET_VERSION operation")
	}

	// Validate version format (should start with 'v')
	version := wafVersionOp.GetVersion()
	if len(version) < 2 || version[0] != 'v' {
		return nil, fmt.Errorf("invalid version format: %s (should start with 'v', e.g., v4.9.0)", version)
	}

	service := &client.Command_WafVersion{
		WafVersion: &client.RequestWafVersion{
			Operation:     client.VersionOperation_SET_VERSION,
			Version:       version,
			ForceDownload: wafVersionOp.GetForceDownload(),
		},
	}

	p.Logger.Info("Built WAF SET_VERSION request",
		"client_id", requestDetails.ClientID,
		"version", version,
		"force_download", wafVersionOp.GetForceDownload())

	return service, nil
}
