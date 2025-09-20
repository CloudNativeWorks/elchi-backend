package processor

import (
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

type ClientStatsProcessor struct {
	Logger *logger.Logger
}

func (p *ClientStatsProcessor) ValidateAndTransform(op models.OperationClass, requestDetails models.RequestDetails, _ models.ServiceClients) (any, error) {
	statsRequest := &client.Command_ClientStats{
		ClientStats: &client.RequestClientStats{},
	}

	return statsRequest, nil
}
