package server

import (
	"context"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"google.golang.org/grpc/metadata"
)

func GetMetadata(ctx context.Context, logger *logger.Logger) (address, nodeID, version, downstreamAddress, clientName, clientID string) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		nodeID = getMetadataValue(md, "nodeid", "NodeID", logger)
		version = getMetadataValue(md, "envoy-version", "Version", logger)
		downstreamAddress = getMetadataValue(md, "downstream_address", "DownstreamAddress", logger)
		address = getMetadataValue(md, "x-envoy-external-address", "Address", logger)
		clientName = getMetadataValue(md, "client_name", "ClientName", logger)
		clientID = getMetadataValue(md, "client_id", "ClientID", logger)
	}
	return
}

func getMetadataValue(md metadata.MD, key, logKey string, logger *logger.Logger) string {
	if vals := md.Get(key); len(vals) > 0 {
		logger.Debugf("Stream opened with %s: %s", logKey, vals[0])
		return vals[0]
	}
	logger.Debugf("Stream opened without %s in metadata", logKey)
	return ""
}
