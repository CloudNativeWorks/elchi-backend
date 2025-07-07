package bridge

import (
	"context"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

func (brg *AppHandler) GetSnapshotDetails(ctx context.Context, _ models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	return nil, nil
}

func (brg *AppHandler) GetClients(ctx context.Context, _ models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	return nil, nil
}
