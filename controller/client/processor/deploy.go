package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"

	"github.com/CloudNativeWorks/elchi-backend/controller/client/services"
	"github.com/CloudNativeWorks/elchi-backend/controller/cloud/openstack"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
	client "github.com/CloudNativeWorks/elchi-proto/client"
	"go.mongodb.org/mongo-driver/bson"
)

type DeployProcessor struct {
	XDSHandler       *xds.AppHandler
	Logger           *logger.Logger
	Service          *services.ClientService
	OpenStackHandler *openstack.Handler
}

func (p *DeployProcessor) ValidateAndTransform(op models.OperationClass, requestDetails models.RequestDetails, cl models.ServiceClients) (any, error) {
	// Debug logging for deploy processor input
	p.Logger.Debugf("Deploy Processor INPUT - ClientID: %s, DownstreamAddress: '%s', InterfaceID: '%s', IPMode: '%s', ServiceName: %s, Project: %s", 
		cl.ClientID, cl.DownstreamAddress, cl.InterfaceID, cl.IPMode, op.GetCommandName(), op.GetCommandProject())
	
	// Get client info for provider validation
	clientInfo, err := p.Service.GetClient(cl.ClientID)
	if err != nil {
		p.Logger.Errorf("Failed to get client info for %s: %v", cl.ClientID, err)
		return nil, err
	}

	p.Logger.Debugf("CLIENT INFO: Provider='%s', InterfaceID='%s', IPMode='%s'", 
		clientInfo.Provider, cl.InterfaceID, cl.IPMode)

	// OpenStack provider validation
	if clientInfo.Provider == "openstack" && cl.InterfaceID != "" {
		p.Logger.Debugf("ENTERING OpenStack validation path - Provider: %s, InterfaceID: %s", 
			clientInfo.Provider, cl.InterfaceID)
		// Validate ip_mode
		if cl.IPMode == "" {
			return nil, fmt.Errorf("ip_mode is required for OpenStack deployments with interface_id")
		}
		if cl.IPMode != "aap" && cl.IPMode != "fixed" {
			return nil, fmt.Errorf("ip_mode must be 'aap' or 'fixed' for OpenStack deployments, got: '%s'", cl.IPMode)
		}
		
		// Validate downstream_address (IP format)
		if cl.DownstreamAddress != "" {
			if net.ParseIP(cl.DownstreamAddress) == nil {
				return nil, fmt.Errorf("invalid IP address format for downstream_address: '%s'", cl.DownstreamAddress)
			}
			p.Logger.Debugf("IP address format validation passed: %s", cl.DownstreamAddress)
			
			// For fixed IP mode, validate that IP is within subnet CIDR
			if cl.IPMode == "fixed" {
				p.Logger.Debugf("VALIDATION: Starting fixed IP subnet validation for IP: %s, InterfaceID: %s, Project: %s", 
					cl.DownstreamAddress, cl.InterfaceID, op.GetCommandProject())
				
				if err := p.validateFixedIPInSubnet(context.TODO(), cl.InterfaceID, cl.DownstreamAddress, op.GetCommandProject()); err != nil {
					p.Logger.Errorf("VALIDATION FAILED: Fixed IP subnet validation failed for IP %s: %v", cl.DownstreamAddress, err)
					return nil, fmt.Errorf("fixed IP subnet validation failed: %v", err)
				}
				p.Logger.Infof("VALIDATION PASSED: Subnet validation successful for fixed IP: %s", cl.DownstreamAddress)
			}
		}
		
		p.Logger.Debugf("OpenStack validation passed - Provider: %s, InterfaceID: '%s', IPMode: '%s', IP: '%s'", 
			clientInfo.Provider, cl.InterfaceID, cl.IPMode, cl.DownstreamAddress)
	} else {
		p.Logger.Debugf("SKIPPING OpenStack validation - Provider: '%s', InterfaceID: '%s' (Condition not met: provider must be 'openstack' AND interfaceID must not be empty)", 
			clientInfo.Provider, cl.InterfaceID)
	}
	
	bootstrap, err := resources.GetDBResource(
		p.XDSHandler.Context.Client,
		"bootstrap",
		bson.M{"general.name": op.GetCommandName(), "general.project": op.GetCommandProject(), "general.version": requestDetails.Version},
	)
	if err != nil {
		return nil, err
	}

	requestDetails = FillRequestDetails(op, requestDetails, bootstrap)
	adminPort, err := resources.GetAdminPortFromBootstrap(bootstrap.Resource.Resource)
	if err != nil {
		return nil, err
	}

	cf := models.ClientFields{
		DownstreamAddress: cl.DownstreamAddress,
		ClientName:        clientInfo.Name,
	}

	bootstrapAny, err := p.XDSHandler.DownloadBootstrap(context.TODO(), requestDetails, cf)
	if err != nil {
		return nil, err
	}

	bootstrapBytes, err := json.Marshal(bootstrapAny)
	if err != nil {
		return nil, err
	}

	deploy := &client.Command_Deploy{
		Deploy: &client.RequestDeploy{
			Name:              op.GetCommandName(),
			DownstreamAddress: cl.DownstreamAddress,
			Port:              adminPort,
			Version:           bootstrap.General.Version,
			Bootstrap:         bootstrapBytes,
			InterfaceId:       cl.InterfaceID,
			IpMode:            cl.IPMode,
		},
	}

	// Debug logging for deploy processor output
	p.Logger.Debugf("Deploy Processor OUTPUT - Sending to client: Name=%s, DownstreamAddress='%s', InterfaceId='%s', IpMode='%s', Port=%d", 
		deploy.Deploy.Name, deploy.Deploy.DownstreamAddress, deploy.Deploy.InterfaceId, deploy.Deploy.IpMode, deploy.Deploy.Port)

	return deploy, nil
}

// validateFixedIPInSubnet validates that the provided IP address is within the subnet CIDR of the interface
func (p *DeployProcessor) validateFixedIPInSubnet(ctx context.Context, interfaceID, ipAddress, projectName string) error {
	if p.OpenStackHandler == nil {
		p.Logger.Errorf("CRITICAL: OpenStack handler not available for subnet validation - validation SKIPPED for IP: %s", ipAddress)
		// This is a critical error - we should not skip validation silently
		return fmt.Errorf("OpenStack handler not available for IP validation")
	}
	
	p.Logger.Debugf("Starting fixed IP validation: IP=%s, Interface=%s, Project=%s", ipAddress, interfaceID, projectName)
	
	// Use handler's validation method (it handles osp_project internally)
	err := p.OpenStackHandler.ValidateFixedIPInSubnet(ctx, interfaceID, ipAddress, projectName)
	if err != nil {
		p.Logger.Errorf("Fixed IP validation FAILED: IP=%s, Interface=%s, Project=%s, Error=%v", 
			ipAddress, interfaceID, projectName, err)
		return fmt.Errorf("IP validation failed: %v", err)
	}
	
	p.Logger.Debugf("Fixed IP validation PASSED: IP=%s, Interface=%s, Project=%s", ipAddress, interfaceID, projectName)
	return nil
}
