package processor

import "github.com/CloudNativeWorks/elchi-backend/pkg/models"

type CommandProcessor interface {
	ValidateAndTransform(op models.OperationClass, requestDetails models.RequestDetails, client models.ServiceClients) (any, error)
}

type CommandProcessorFactory struct {
	processors map[string]CommandProcessor
}

func NewCommandProcessorFactory() *CommandProcessorFactory {
	return &CommandProcessorFactory{
		processors: make(map[string]CommandProcessor),
	}
}

func (f *CommandProcessorFactory) RegisterProcessor(cmdType string, processor CommandProcessor) {
	f.processors[cmdType] = processor
}

func (f *CommandProcessorFactory) GetProcessor(cmdType string) (CommandProcessor, bool) {
	processor, exists := f.processors[cmdType]
	return processor, exists
}

func FillRequestDetails(op models.OperationClass, requestDetails models.RequestDetails, bootstrap *models.DBResource) models.RequestDetails {
	requestDetails.ResourceID = bootstrap.ID.Hex()
	requestDetails.Collection = "bootstrap"
	requestDetails.Name = op.GetCommandName()
	requestDetails.Project = op.GetCommandProject()
	return requestDetails
}
