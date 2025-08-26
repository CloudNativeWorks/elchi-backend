package processor

import (
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

type FRRProcessor struct {
	Logger *logger.Logger
}

func (p *FRRProcessor) ValidateAndTransform(op models.OperationClass, requestDetails models.RequestDetails, _ models.ServiceClients) (any, error) {
	var protocol client.FrrProtocolType
	if frrType := op.GetCommandFRRType(); frrType != nil {
		protocol = *frrType
	}

	service := &client.Command_Frr{
		Frr: &client.RequestFrr{
			Protocol: protocol,
			Bgp:      op.GetCommandBGP(),
		},
	}

	return service, nil
}
