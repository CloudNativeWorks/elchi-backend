package processor

import (
	"github.com/CloudNativeWorks/elchi-backend/controller/client/services"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
	client "github.com/CloudNativeWorks/elchi-proto/client"
	"go.mongodb.org/mongo-driver/bson"
)

type UpgradeProcessor struct {
	XDSHandler *xds.AppHandler
	Logger     *logger.Logger
	Service    *services.ClientService
}

func (p *UpgradeProcessor) ValidateAndTransform(op models.OperationClass, requestDetails models.RequestDetails, cl models.ServiceClients) (any, error) {
	p.Logger.Debugf("Upgrade Processor - ClientID: %s, ServiceName: %s, Project: %s, ToVersion: %s",
		cl.ClientID, op.GetCommandName(), op.GetCommandProject(), requestDetails.Version)

	// Get target version bootstrap
	bootstrap, err := resources.GetDBResource(
		p.XDSHandler.Context.Client,
		"bootstrap",
		bson.M{"general.name": op.GetCommandName(), "general.project": op.GetCommandProject(), "general.version": requestDetails.Version},
	)
	if err != nil {
		return nil, err
	}

	// Get admin port from bootstrap
	adminPort, err := resources.GetAdminPortFromBootstrap(bootstrap.Resource.Resource)
	if err != nil {
		return nil, err
	}

	// Fill request details
	requestDetails = FillRequestDetails(op, requestDetails, bootstrap)

	// Get FromVersion from command (set by upgrade job)
	// This is the source Envoy version (e.g., "v1.35.3")
	fromVersion := op.GetCommands().FromVersion
	if fromVersion == "" {
		p.Logger.Warnf("FromVersion not set in command, using empty string")
	}

	// Build upgrade command
	// Note: Bootstrap version is already updated in DB by the upgrade job
	// Client will fetch the new bootstrap config based on the ToVersion during restart
	upgrade := &client.Command_UpgradeListener{
		UpgradeListener: &client.RequestUpgradeListener{
			Name:              op.GetCommandName(),
			FromVersion:       fromVersion,            // Source Envoy version from command
			ToVersion:         requestDetails.Version, // Target Envoy version
			Port:              adminPort,
			Graceful:          true, // Default to graceful upgrade
			DrainTimeSeconds:  30,   // Default 30 seconds drain time
		},
	}

	p.Logger.Debugf("Upgrade Processor OUTPUT - Name=%s, FromVersion=%s, ToVersion=%s, Port=%d",
		upgrade.UpgradeListener.Name, upgrade.UpgradeListener.FromVersion, upgrade.UpgradeListener.ToVersion, upgrade.UpgradeListener.Port)

	return upgrade, nil
}
