package responser

import (
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
)

type FRRResponser struct {
}

func (p *FRRResponser) ValidateAndTransform(op models.OperationClass, response *pb.CommandResponse) any {
	return response
}
