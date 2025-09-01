package envoys

import (
	"context"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

func getDownstreams(existing bson.M) []bson.M {
	downstreams := []bson.M{}
	var arr []any
	if a, ok := existing["envoys"].(bson.A); ok {
		arr = a
	} else if a, ok := existing["envoys"].([]any); ok {
		arr = a
	}

	for _, v := range arr {
		if m, ok := v.(bson.M); ok {
			downstreams = append(downstreams, m)
		}
	}

	return downstreams
}

func determineStatus(downstreams []bson.M, logger *logger.Logger) string {
	if len(downstreams) == 0 {
		logger.Debug("🔍 DEBUG: determineStatus - no downstreams → Offline\n")
		return "Offline"
	}

	connectedCount := 0
	totalCount := len(downstreams)

	logger.Debugf("🔍 DEBUG: determineStatus - analyzing %d downstreams\n", totalCount)

	for i, d := range downstreams {
		if connected, ok := d["connected"].(bool); ok && connected {
			connectedCount++
			logger.Debugf("🔍 DEBUG: downstream[%d]: connected=true\n", i)
		} else {
			logger.Debugf("🔍 DEBUG: downstream[%d]: connected=false or invalid (ok=%t, connected=%v)\n", i, ok, connected)
		}
	}

	var status string
	if connectedCount == totalCount {
		status = "Live"
	} else if connectedCount > 0 {
		status = "Partial"
	} else {
		status = "Offline"
	}

	logger.Debugf("🔍 DEBUG: determineStatus - %d/%d connected → status=%s\n", connectedCount, totalCount, status)

	return status
}

func (e *EnvoyConnTracker) AddOrUpdateEnvoy(ctx context.Context, dbClient *mongo.Database, source_address, nodeID, version, downstreamAddress, clientName string, connCount int, logger *logger.Logger) {
	if downstreamAddress == "" {
		return
	}
	collection := dbClient.Collection("envoys")
	name, project, _ := GetNodeIDParts(nodeID)
	filter := bson.M{"name": name, "project": project}

	var existing bson.M
	err := collection.FindOne(ctx, filter).Decode(&existing)
	if err != nil && err != mongo.ErrNoDocuments {
		logger.Errorf("Error reading envoys stream: %v", err)
		return
	}

	updateFields := bson.M{}
	downstreams := getDownstreams(existing)
	downstreams = e.updateDownstreamsWithCount(downstreams, downstreamAddress, nodeID, version, clientName, source_address, connCount, false, logger)
	updateFields["envoys"] = downstreams
	updateFields["status"] = determineStatus(downstreams, logger)

	update := bson.M{"$set": updateFields}
	opts := options.Update().SetUpsert(true)
	_, err = collection.UpdateOne(ctx, filter, update, opts)
	if err != nil {
		logger.Errorf("Error adding or updating envoys stream: %v", err)
	}
}

func (e *EnvoyConnTracker) updateDownstreamsWithCount(downstreams []bson.M, downstreamAddress, nodeID, version, clientName, source_address string, connCount int, isUndeploy bool, logger *logger.Logger) []bson.M {
	connected := connCount > 0

	logger.Printf("🔍 DEBUG: updateDownstreamsWithCount - nodeID=%s, downstreamAddress=%s, connCount=%d, isUndeploy=%t, connected=%t\n",
		nodeID, downstreamAddress, connCount, isUndeploy, connected)
	logger.Printf("🔍 DEBUG: updateDownstreamsWithCount - input downstreams count: %d\n", len(downstreams))

	if isUndeploy {
		logger.Printf("🔍 DEBUG: UNDEPLOY MODE - removing entry for downstream: %s\n", downstreamAddress)
		filteredDownstreams := []bson.M{}
		removedCount := 0
		for _, m := range downstreams {
			if m["downstream_address"] != downstreamAddress {
				filteredDownstreams = append(filteredDownstreams, m)
			} else {
				removedCount++
				logger.Printf("🔍 DEBUG: UNDEPLOY - removing entry: %+v\n", m)
			}
		}
		logger.Printf("🔍 DEBUG: UNDEPLOY - removed %d entries, result count: %d → %d\n", removedCount, len(downstreams), len(filteredDownstreams))
		return filteredDownstreams
	}

	logger.Printf("🔍 DEBUG: NORMAL MODE - updating connected status\n")
	found := false
	for _, m := range downstreams {
		if m["downstream_address"] == downstreamAddress {
			logger.Printf("🔍 DEBUG: NORMAL - found entry, updating connected to %t\n", connected)
			m["connected"] = connected
			m["connections"] = connCount
			m["lastSync"] = time.Now().Unix()
			if source_address != "" {
				m["source_address"] = source_address
			}
			if version != "" {
				m["version"] = version
			}
			if clientName != "" {
				m["client_name"] = clientName
			}
			found = true
		}
	}

	if !found && !isUndeploy {
		logger.Printf("🔍 DEBUG: NORMAL - creating new entry for downstream: %s\n", downstreamAddress)
		entry := bson.M{
			"connected":   connected,
			"nodeid":      nodeID,
			"lastSync":    time.Now().Unix(),
			"connections": connCount,
		}

		if downstreamAddress != "" {
			entry["downstream_address"] = downstreamAddress
		}

		if source_address != "" {
			entry["source_address"] = source_address
		}

		if version != "" {
			entry["version"] = version
		}

		if clientName != "" {
			entry["client_name"] = clientName
		}

		downstreams = append(downstreams, entry)
	}

	logger.Printf("🔍 DEBUG: updateDownstreamsWithCount - final result count: %d\n", len(downstreams))
	return downstreams
}

func (e *EnvoyConnTracker) DisconnectNodeIDWithCount(ctx context.Context, dbClient *mongo.Database, nodeID string, connCount int, isUndeploy bool, logger *logger.Logger) {
	name, project, downstreamAddress := GetNodeIDParts(nodeID)

	logger.Debugf("🔍 DEBUG: DisconnectNodeIDWithCount - nodeID=%s, name=%s, project=%s, downstreamAddress='%s', isUndeploy=%t\n",
		nodeID, name, project, downstreamAddress, isUndeploy)

	if name == "" || project == "" {
		logger.Debugf("🔍 DEBUG: DisconnectNodeIDWithCount - invalid nodeID format, skipping\n")
		logger.Errorf("Invalid nodeID format: %s", nodeID)
		return
	}

	// downstreamAddress empty olabilir, bu normal
	collection := dbClient.Collection("envoys")
	filter := bson.M{"name": name, "project": project}

	var existing bson.M
	err := collection.FindOne(ctx, filter).Decode(&existing)
	if err != nil {
		logger.Debugf("🔍 DEBUG: DisconnectNodeIDWithCount - error reading envoys: %v\n", err)
		logger.Errorf("Error reading envoys stream: %v", err)
		return
	}

	updateFields := bson.M{}
	downstreams := getDownstreams(existing)
	downstreams = e.updateDownstreamsWithCount(downstreams, downstreamAddress, nodeID, "", "", "", connCount, isUndeploy, logger)
	updateFields["envoys"] = downstreams
	updateFields["status"] = determineStatus(downstreams, logger)

	update := bson.M{"$set": updateFields}
	_, err = collection.UpdateOne(ctx, filter, update)
	if err != nil {
		logger.Debugf("🔍 DEBUG: DisconnectNodeIDWithCount - error updating: %v\n", err)
		logger.Errorf("Error removing node ID: %v", err)
	} else {
		logger.Debugf("🔍 DEBUG: DisconnectNodeIDWithCount - successfully updated MongoDB\n")
	}
}

// Legacy InsertError function removed - using enhanced error system only

func GetNodeIDParts(nodeID string) (string, string, string) {
	parts := strings.Split(nodeID, "::")
	if len(parts) == 2 {
		return parts[0], parts[1], ""
	} else if len(parts) == 3 {
		return parts[0], parts[1], parts[2]
	}
	return "", "", ""
}
