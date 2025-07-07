package processor

import (
	"context"
	"encoding/json"

	"github.com/CloudNativeWorks/elchi-backend/controller/client/services"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
	client "github.com/CloudNativeWorks/elchi-proto/client"
	"go.mongodb.org/mongo-driver/bson"
)

type BootstrapProcessor struct {
	XDSHandler *xds.AppHandler
	Logger     *logger.Logger
	Service    *services.ClientService
}

func (p *BootstrapProcessor) ValidateAndTransform(op models.OperationClass, requestDetails models.RequestDetails, cl models.ServiceClients) (any, error) {
	bootstrap, err := resources.GetDBResource(
		p.XDSHandler.Context.Client,
		"bootstrap",
		bson.M{"general.name": op.GetCommandName(), "general.project": op.GetCommandProject()},
	)

	if err != nil {
		return nil, err
	}

	clientInfo, err := p.Service.GetClient(cl.ClientID)
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

	deploy := &client.Command_UpdateBootstrap{
		UpdateBootstrap: &client.RequestUpdateBootstrap{
			Name:      op.GetCommandName(),
			Port:      adminPort,
			Bootstrap: bootstrapBytes,
		},
	}

	return deploy, nil
}
