package responser

import (
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
)

type CommandResponser interface {
	ValidateAndTransform(roperation models.OperationClass, esponse *pb.CommandResponse) any
}

type CommandResponserFactory struct {
	responser map[string]CommandResponser
}

func NewCommandResponserFactory() *CommandResponserFactory {
	return &CommandResponserFactory{
		responser: make(map[string]CommandResponser),
	}
}

func (f *CommandResponserFactory) RegisterResponser(cmdType string, processor CommandResponser) {
	f.responser[cmdType] = processor
}

func (f *CommandResponserFactory) GetResponser(cmdType string) (CommandResponser, bool) {
	processor, exists := f.responser[cmdType]
	return processor, exists
}
