package processor

import (
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

type ProxyProcessor struct {
	XDSHandler *xds.AppHandler
	Logger     *logger.Logger
}

func (p *ProxyProcessor) ValidateAndTransform(op models.OperationClass, requestDetails models.RequestDetails, _ models.ServiceClients) (any, error) {
	adminPort, err := resources.GetAdminPortFromService(p.XDSHandler.Context.Client, op)
	if err != nil {
		return nil, err
	}

	commandPath, err := op.GetCommandPath()
	if err != nil {
		return nil, err
	}

	proxyRequest := &client.RequestEnvoyAdmin{
		Name:    op.GetCommandName(),
		Port:    adminPort,
		Method:  op.GetCommandMethod(),
		Path:    commandPath,
		Queries: op.GetCommandQueries(),
	}

	proxy := &client.Command_EnvoyAdmin{
		EnvoyAdmin: proxyRequest,
	}

	return proxy, nil
}
