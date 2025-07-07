package responser

import (
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
)

type ServiceResponser struct {
}

func (p *ServiceResponser) ValidateAndTransform(op models.OperationClass, response *pb.CommandResponse) any {
	return response
}
