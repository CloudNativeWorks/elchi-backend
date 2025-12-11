package envoys

import (
	"context"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// StartHeartbeat starts a background goroutine that periodically updates lastSync
// for all active envoys in this control-plane's Counter map.
// This keeps lastSync fresh for connected envoys, enabling cleanup to detect truly stale ones.
//
// Runs every 30 seconds.
// Safe for multi-pod deployment - each control-plane only updates its own connected envoys.
func (e *EnvoyConnTracker) StartHeartbeat(ctx context.Context, db *mongo.Database, logger *logger.Logger) {
	logger.Info("Starting envoy heartbeat service (every 30 seconds)")

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			e.sendHeartbeat(ctx, db, logger)
		case <-ctx.Done():
			logger.Info("Heartbeat service stopped")
			return
		}
	}
}

// sendHeartbeat updates lastSync for all active envoys in Counter map.
// Uses two-phase approach to minimize lock holding time:
// Phase 1: Quick copy of active nodes (with RLock)
// Phase 2: DB updates without holding lock
func (e *EnvoyConnTracker) sendHeartbeat(ctx context.Context, db *mongo.Database, logger *logger.Logger) {
	// PHASE 1: Quick copy of active nodes (minimal lock time)
	e.mu.RLock()
	activeNodes := make(map[string]int)
	for nodeID, count := range e.Counter {
		if count > 0 {
			activeNodes[nodeID] = count
		}
	}
	e.mu.RUnlock()

	if len(activeNodes) == 0 {
		logger.Debug("Heartbeat: No active envoys to update")
		return
	}

	// PHASE 2: Update DB without holding lock
	now := time.Now().Unix()
	updatedCount := 0
	errorCount := 0

	for nodeID, connCount := range activeNodes {
		// Parse nodeID to extract name and project
		name, project, _ := GetNodeIDParts(nodeID)
		if name == "" || project == "" {
			logger.Warnf("Heartbeat: Invalid nodeID format: %s", nodeID)
			continue
		}

		// Build filter to match specific envoy in array
		filter := bson.M{
			"name":          name,
			"project":       project,
			"envoys.nodeid": nodeID,
		}

		// Update only the matched envoy's fields
		update := bson.M{
			"$set": bson.M{
				"envoys.$.lastSync":    now,
				"envoys.$.connected":   true,
				"envoys.$.connections": connCount,
			},
		}

		// Execute update
		result, err := db.Collection("envoys").UpdateOne(ctx, filter, update)
		if err != nil {
			logger.Errorf("Heartbeat: Failed to update envoy %s: %v", nodeID, err)
			errorCount++
			continue
		}

		if result.MatchedCount > 0 {
			updatedCount++
		} else {
			logger.Debugf("Heartbeat: Envoy %s not found in DB (may not be registered yet)", nodeID)
		}
	}

	logger.Debugf("Heartbeat: Updated %d/%d active envoys (errors: %d)",
		updatedCount, len(activeNodes), errorCount)
}
