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

func determineStatus(downstreams []bson.M) string {
	if len(downstreams) == 0 {
		return "Offline"
	}

	allConnected := true
	anyConnected := false
	for _, d := range downstreams {
		if connected, ok := d["connected"].(bool); ok && connected {
			anyConnected = true
		} else {
			allConnected = false
		}
	}

	if allConnected {
		return "Live"
	} else if anyConnected {
		return "Partial"
	}

	return "Offline"
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
	downstreams = e.updateDownstreamsWithCount(downstreams, downstreamAddress, nodeID, version, clientName, source_address, connCount)
	updateFields["envoys"] = downstreams
	updateFields["status"] = determineStatus(downstreams)

	update := bson.M{"$set": updateFields}
	opts := options.Update().SetUpsert(true)
	_, err = collection.UpdateOne(ctx, filter, update, opts)
	if err != nil {
		logger.Errorf("Error adding or updating envoys stream: %v", err)
	}
}

func (e *EnvoyConnTracker) updateDownstreamsWithCount(downstreams []bson.M, downstreamAddress, nodeID, version, clientName, source_address string, connCount int) []bson.M {
	connected := connCount > 0

	found := false
	for _, m := range downstreams {
		if m["downstream_address"] == downstreamAddress {
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

	if !found {
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

	return downstreams
}

func (e *EnvoyConnTracker) DisconnectNodeIDWithCount(ctx context.Context, dbClient *mongo.Database, nodeID string, connCount int, logger *logger.Logger) {
	name, project, downstreamAddress := GetNodeIDParts(nodeID)
	if downstreamAddress == "" {
		return
	}
	collection := dbClient.Collection("envoys")
	filter := bson.M{"name": name, "project": project}

	var existing bson.M
	err := collection.FindOne(ctx, filter).Decode(&existing)
	if err != nil {
		logger.Errorf("Error reading envoys stream: %v", err)
		return
	}

	updateFields := bson.M{}
	downstreams := getDownstreams(existing)
	downstreams = e.updateDownstreamsWithCount(downstreams, downstreamAddress, nodeID, "", "", "", connCount)
	updateFields["envoys"] = downstreams
	updateFields["status"] = determineStatus(downstreams)

	update := bson.M{"$set": updateFields}
	_, err = collection.UpdateOne(ctx, filter, update)
	if err != nil {
		logger.Errorf("Error removing node ID: %v", err)
	}
}

func InsertError(ctx context.Context, dbClient *mongo.Database, nodeID, resourceID, errorMsg, nonce string, logger *logger.Logger) {
	name, project, downstreamAddress := GetNodeIDParts(nodeID)
	if downstreamAddress == "" {
		return
	}
	collection := dbClient.Collection("envoys")
	errorEntry := bson.M{
		"message":        errorMsg,
		"type":           resourceID,
		"response_nonce": nonce,
		"timestamp":      time.Now(),
		"nodeid":         nodeID,
	}

	filter := bson.M{"name": name, "project": project}
	update := bson.M{
		"$push": bson.M{
			"errors": bson.M{
				"$each":  []any{errorEntry},
				"$slice": -50,
			},
		},
	}
	opts := options.Update().SetUpsert(true)
	_, err := collection.UpdateOne(ctx, filter, update, opts)
	if err != nil {
		logger.Errorf("Error adding/updating error for nodeID %s: %v", nodeID, err)
	}
}

func GetNodeIDParts(nodeID string) (string, string, string) {
	parts := strings.Split(nodeID, "::")
	if len(parts) == 2 {
		return parts[0], parts[1], ""
	} else if len(parts) == 3 {
		return parts[0], parts[1], parts[2]
	}
	return "", "", ""
}
