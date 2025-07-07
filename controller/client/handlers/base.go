package handlers

import (
	"github.com/CloudNativeWorks/elchi-backend/controller/client/processor"
	"github.com/CloudNativeWorks/elchi-backend/controller/client/responser"
	"github.com/CloudNativeWorks/elchi-backend/controller/client/services"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

type Client struct {
	Context    *db.AppContext
	Service    *services.ClientService
	logger     *logger.Logger
	cmdFactory *processor.CommandProcessorFactory
	responser  *responser.CommandResponserFactory
}

func NewClientHandler(context *db.AppContext, xdsHandler *xds.AppHandler, clientService *services.ClientService) *Client {
	h := &Client{
		Context:    context,
		Service:    clientService,
		logger:     logger.NewLogger("controller/client/handler"),
		responser:  responser.NewCommandResponserFactory(),
		cmdFactory: processor.NewCommandProcessorFactory(),
	}

	processorLogger := logger.NewLogger("controller/client/processor")
	responserLogger := logger.NewLogger("controller/client/responser")

	// Processor Register
	h.cmdFactory.RegisterProcessor("DEPLOY", &processor.DeployProcessor{XDSHandler: xdsHandler, Logger: processorLogger, Service: clientService})
	h.cmdFactory.RegisterProcessor("SERVICE", &processor.ServiceProcessor{XDSHandler: xdsHandler, Logger: processorLogger})
	h.cmdFactory.RegisterProcessor("UPDATE_BOOTSTRAP", &processor.BootstrapProcessor{XDSHandler: xdsHandler, Logger: processorLogger, Service: clientService})
	h.cmdFactory.RegisterProcessor("UNDEPLOY", &processor.UnDeployProcessor{XDSHandler: xdsHandler, Logger: processorLogger})
	h.cmdFactory.RegisterProcessor("PROXY", &processor.ProxyProcessor{XDSHandler: xdsHandler, Logger: processorLogger})
	h.cmdFactory.RegisterProcessor("CLIENT_LOGS", &processor.GeneralLogProcessor{Logger: processorLogger})
	h.cmdFactory.RegisterProcessor("CLIENT_STATS", &processor.ClientStatsProcessor{Logger: processorLogger})
	h.cmdFactory.RegisterProcessor("NETWORK", &processor.NetworkProcessor{Logger: processorLogger})
	h.cmdFactory.RegisterProcessor("FRR", &processor.FRRProcessor{Logger: processorLogger})
	h.cmdFactory.RegisterProcessor("FRR_LOGS", &processor.GeneralLogProcessor{Logger: processorLogger})


	// Responser Register
	h.responser.RegisterResponser("DEPLOY", &responser.DeployResponser{XDSHandler: xdsHandler, Logger: responserLogger})
	h.responser.RegisterResponser("SERVICE", &responser.ServiceResponser{})
	h.responser.RegisterResponser("UPDATE_BOOTSTRAP", &responser.BootstrapResponser{})
	h.responser.RegisterResponser("UNDEPLOY", &responser.UnDeployResponser{XDSHandler: xdsHandler, Logger: responserLogger})
	h.responser.RegisterResponser("PROXY", &responser.ProxyResponser{})
	h.responser.RegisterResponser("CLIENT_LOGS", &responser.GeneralLogResponser{})
	h.responser.RegisterResponser("CLIENT_STATS", &responser.ClientStatsResponser{})
	h.responser.RegisterResponser("NETWORK", &responser.NetworkResponser{})
	h.responser.RegisterResponser("FRR", &responser.FRRResponser{})
	h.responser.RegisterResponser("FRR_LOGS", &responser.GeneralLogResponser{})
	return h
}
