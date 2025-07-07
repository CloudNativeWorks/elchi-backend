package processor

import (
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

type GeneralLogProcessor struct {
	Logger *logger.Logger
}

func (p *GeneralLogProcessor) ValidateAndTransform(op models.OperationClass, requestDetails models.RequestDetails, _ models.ServiceClients) (any, error) {
	service := &client.Command_GeneralLog{
		GeneralLog: &client.RequestGeneralLog{
			Count: op.GetCommandCount(),
		},
	}

	return service, nil
}
