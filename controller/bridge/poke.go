package bridge

import (
	"context"
	"fmt"

	"google.golang.org/grpc/metadata"

	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
)

func PokeNode(ctx context.Context, poke bridge.PokeServiceClient, nodeID, project, version, downstreamAddress string) (any, error) {
	var nodeid string
	if downstreamAddress != "" {
		nodeid = fmt.Sprintf("%s::%s::%s", nodeID, project, downstreamAddress)
	} else {
		nodeid = fmt.Sprintf("%s::%s", nodeID, project)
	}

	md := metadata.Pairs("nodeid", nodeid, "envoy-version", version)
	ctxOut := metadata.NewOutgoingContext(ctx, md)
	resp, err := poke.Poke(ctxOut, &bridge.PokeRequest{
		NodeID:            nodeID,
		Project:           project,
		Version:           version,
		DownstreamAddress: downstreamAddress,
	})

	if err != nil {
		return nil, err
	}

	return resp, nil
}
