package envoys

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/versioned-go-control-plane/pkg/cache/v3"
)

func (e *EnvoyConnTracker) TrackClientUp(dbClient *mongo.Database, nodeID, address, version, downstreamAddress, clientName string, streamID int64, logger *logger.Logger) {
	count := e.IncAndGet(nodeID)
	e.dbOpChan <- dbOperation{
		nodeID:      nodeID,
		count:       count,
		op:          "inc",
		dbClient:    dbClient,
		address:     address,
		version:     version,
		downAddress: downstreamAddress,
		clientName:  clientName,
		logger:      logger,
	}
}

func (e *EnvoyConnTracker) TrackClientDown(dbClient *mongo.Database, cache cache.SnapshotCache, nodeID string, streamID int64, logger *logger.Logger) {
	count := e.DecAndGet(nodeID)
	e.dbOpChan <- dbOperation{
		nodeID:   nodeID,
		count:    count,
		op:       "dec",
		dbClient: dbClient,
		logger:   logger,
	}
	logger.Infof("Client with NodeID %s removed", nodeID)
}

func (e *EnvoyConnTracker) AddOrUpdateError(dbClient *mongo.Database, nodeID, resourceID, errorMsg, nonce string, logger *logger.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	InsertError(ctx, dbClient, nodeID, resourceID, errorMsg, nonce, logger)
}
