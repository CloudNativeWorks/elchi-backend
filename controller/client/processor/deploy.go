package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"

	"github.com/CloudNativeWorks/elchi-backend/controller/client/services"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
	client "github.com/CloudNativeWorks/elchi-proto/client"
	"go.mongodb.org/mongo-driver/bson"
)

type DeployProcessor struct {
	XDSHandler *xds.AppHandler
	Logger     *logger.Logger
	Service    *services.ClientService
}

func (p *DeployProcessor) ValidateAndTransform(op models.OperationClass, requestDetails models.RequestDetails, cl models.ServiceClients) (any, error) {
	// Debug logging for deploy processor input
	p.Logger.Debugf("Deploy Processor INPUT - ClientID: %s, DownstreamAddress: '%s', InterfaceID: '%s', IPMode: '%s', ServiceName: %s, Project: %s", 
		cl.ClientID, cl.DownstreamAddress, cl.InterfaceID, cl.IPMode, op.GetCommandName(), op.GetCommandProject())
	
	// Get client info for provider validation
	clientInfo, err := p.Service.GetClient(cl.ClientID)
	if err != nil {
		return nil, err
	}

	// OpenStack provider validation
	if clientInfo.Provider == "openstack" && cl.InterfaceID != "" {
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
		}
		
		p.Logger.Debugf("OpenStack validation passed - Provider: %s, InterfaceID: '%s', IPMode: '%s', IP: '%s'", 
			clientInfo.Provider, cl.InterfaceID, cl.IPMode, cl.DownstreamAddress)
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
