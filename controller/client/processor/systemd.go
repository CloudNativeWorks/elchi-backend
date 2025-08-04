package processor

import (
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

type ServiceProcessor struct {
	XDSHandler *xds.AppHandler
	Logger     *logger.Logger
}

func (p *ServiceProcessor) ValidateAndTransform(op models.OperationClass, requestDetails models.RequestDetails, _ models.ServiceClients) (any, error) {
	adminPort, err := resources.GetAdminPortFromService(p.XDSHandler.Context.Client, op)
	if err != nil {
		return nil, err
	}

	service := &client.Command_Service{
		Service: &client.RequestService{
			Name:       op.GetCommandName(),
			Port:       adminPort,
			Count:      op.GetCommandCount(),
			Search:     op.GetCommandSearch(),
			Components: op.GetCommandComponents(),
			Levels:     op.GetCommandLevels(),
		},
	}

	return service, nil
}
