package bridge

import (
	"context"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

func (brg *AppHandler) GetSnapshotDetails(ctx context.Context, resourceClass models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	asd, err := brg.GetSnapshotResources(ctx, resourceClass, requestDetails)
	if err != nil {
		return nil, err
	}

	fmt.Println(asd)

	return nil, nil
}

func (brg *AppHandler) GetClients(ctx context.Context, _ models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	return nil, nil
}
