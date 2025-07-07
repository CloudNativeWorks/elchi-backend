package bridge

import (
	"context"

	"google.golang.org/grpc/metadata"

	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

func (brg *AppHandler) GetSnapshotResources(ctx context.Context, _ models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	md := metadata.Pairs("nodeid", requestDetails.Name, "envoy-version", requestDetails.Version)
	ctxOut := metadata.NewOutgoingContext(ctx, md)
	resp, err := brg.BSnapshot.GetSnapshotResources(ctxOut, &bridge.SnapshotKey{Key: requestDetails.Name})
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (brg *AppHandler) GetSnapshotKeys(ctx context.Context, _ models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	md := metadata.Pairs("nodeid", requestDetails.Name, "envoy-version", requestDetails.Version)
	ctxOut := metadata.NewOutgoingContext(ctx, md)
	resp, err := brg.BSnapshot.GetSnapshotKeys(ctxOut, &bridge.Empty{})
	if err != nil {
		return nil, err
	}

	return resp, nil
}
