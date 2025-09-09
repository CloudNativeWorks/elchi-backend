package bridge

import (
	"context"
	"fmt"

	"google.golang.org/grpc/metadata"

	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
)

// GetNodeSnapshot retrieves the snapshot for a specific node
// The request is routed to the correct control-plane via ext_process in registry
func (brg *AppHandler) GetNodeSnapshot(ctx context.Context, nodeID string, version string) (*bridge.NodeSnapshotResponse, error) {
	// Set metadata for routing via ext_process
	md := metadata.Pairs("nodeid", nodeID)
	if version != "" {
		md.Append("envoy-version", version)
	}
	ctxOut := metadata.NewOutgoingContext(ctx, md)
	
	// Call the new GetNodeSnapshot RPC
	resp, err := brg.BSnapshot.GetNodeSnapshot(ctxOut, &bridge.NodeSnapshotRequest{
		NodeId:  nodeID,
		Version: version,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get node snapshot: %w", err)
	}

	return resp, nil
}

// ClearNodeSnapshot clears the snapshot cache for a specific node
// The request is routed to the correct control-plane via ext_process in registry
func (brg *AppHandler) ClearNodeSnapshot(ctx context.Context, nodeID string) (*bridge.ClearSnapshotResponse, error) {
	// Set metadata for routing via ext_process
	md := metadata.Pairs("nodeid", nodeID)
	ctxOut := metadata.NewOutgoingContext(ctx, md)
	
	// Call the new ClearNodeSnapshot RPC
	resp, err := brg.BSnapshot.ClearNodeSnapshot(ctxOut, &bridge.NodeSnapshotRequest{
		NodeId: nodeID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to clear node snapshot: %w", err)
	}

	return resp, nil
}
