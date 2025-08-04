package processor

import (
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

type NetworkProcessor struct {
	Logger *logger.Logger
}

func (p *NetworkProcessor) ValidateAndTransform(op models.OperationClass, requestDetails models.RequestDetails, _ models.ServiceClients) (any, error) {
	service := &client.Command_Network{
		Network: &client.RequestNetwork{
			Interfaces: op.GetCommandInterfaces(),
		},
	}

	return service, nil
}
