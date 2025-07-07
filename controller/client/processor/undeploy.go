package processor

import (
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

type UnDeployProcessor struct {
	XDSHandler *xds.AppHandler
	Logger     *logger.Logger
}

func (p *UnDeployProcessor) ValidateAndTransform(op models.OperationClass, requestDetails models.RequestDetails, cl models.ServiceClients) (any, error) {
	adminPort, err := resources.GetAdminPortFromService(p.XDSHandler.Context.Client, op)
	if err != nil {
		return nil, err
	}

	undeploy := &client.Command_Undeploy{
		Undeploy: &client.RequestUnDeploy{
			Name:              op.GetCommandName(),
			Port:              adminPort,
			DownstreamAddress: cl.DownstreamAddress,
		},
	}

	return undeploy, nil
}
