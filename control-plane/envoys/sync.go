package envoys

import (
	"context"
	"errors"
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
		logger.Debug("DEBUG: determineStatus - no downstreams -> Offline\n")
		return "Offline"
	}

	connectedCount := 0
	totalCount := len(downstreams)

	logger.Debugf("DEBUG: determineStatus - analyzing %d downstreams\n", totalCount)

	for i, d := range downstreams {
		if connected, ok := d["connected"].(bool); ok && connected {
			connectedCount++
			logger.Debugf("DEBUG: downstream[%d]: connected=true\n", i)
		} else {
			logger.Debugf("DEBUG: downstream[%d]: connected=false or invalid (ok=%t, connected=%v)\n", i, ok, connected)
		}
	}

	var status string
	switch {
	case connectedCount == totalCount:
		status = "Live"
	case connectedCount > 0:
		status = "Partial"
	default:
		status = "Offline"
	}

	logger.Debugf("DEBUG: determineStatus - %d/%d connected -> status=%s\n", connectedCount, totalCount, status)

	return status
}

// maxEnvoyWriteAttempts bounds the optimistic-concurrency retry loop in
// AddOrUpdateEnvoy / DisconnectNodeIDWithCount. A conflict only occurs
// when a concurrent heartbeat / cleanup / prune commits between this
// writer's read and write; a couple of retries is always enough.
const maxEnvoyWriteAttempts = 5

// envoyDocRev reads the optimistic-concurrency revision from an envoys
// document. A doc written before the `rev` field existed has none — it
// is treated as 0, and withRevGuard then matches it via {$exists:false}.
func envoyDocRev(doc bson.M) int64 {
	switch v := doc["rev"].(type) {
	case int64:
		return v
	case int32:
		return int64(v)
	case int:
		return int64(v)
	}
	return 0
}

// withRevGuard builds the compare-and-swap filter: the guarded write
// commits only if the document's `rev` still equals the value read.
// rev==0 also matches a legacy document that has no `rev` field yet —
// its first guarded write stamps one via the accompanying $inc.
func withRevGuard(name, project string, rev int64) bson.M {
	f := bson.M{"name": name, "project": project}
	if rev == 0 {
		f["$or"] = []bson.M{
			{"rev": int64(0)},
			{"rev": bson.M{"$exists": false}},
		}
	} else {
		f["rev"] = rev
	}
	return f
}

func (e *EnvoyConnTracker) AddOrUpdateEnvoy(ctx context.Context, dbClient *mongo.Database, sourceAddress, nodeID, version, downstreamAddress, clientName, clientID string, connCount int, logger *logger.Logger) {
	if downstreamAddress == "" {
		return
	}
	collection := dbClient.Collection("envoys")
	name, project, _ := GetNodeIDParts(nodeID)
	filter := bson.M{"name": name, "project": project}

	// Optimistic-concurrency (CAS) loop. The whole-array $set below would
	// otherwise clobber a concurrent heartbeat / cleanup / prune write
	// (all of which now $inc `rev`). The guarded write commits only when
	// `rev` is unchanged since the read; on a conflict we re-read and
	// recompute, so no concurrent update is ever lost.
	for attempt := 1; attempt <= maxEnvoyWriteAttempts; attempt++ {
		var existing bson.M
		err := collection.FindOne(ctx, filter).Decode(&existing)
		notFound := errors.Is(err, mongo.ErrNoDocuments)
		if err != nil && !notFound {
			logger.Errorf("Error reading envoys stream: %v", err)
			return
		}

		downstreams := getDownstreams(existing)
		downstreams = e.updateDownstreamsWithCount(downstreams, downstreamAddress, nodeID, version, clientName, clientID, sourceAddress, connCount, false, logger)
		setFields := bson.M{"envoys": downstreams, "status": determineStatus(downstreams, logger)}

		if notFound {
			// First write for this listener — insert with rev=1. The
			// single processDBOperations goroutine serializes connect /
			// disconnect, so a concurrent first-create cannot occur.
			res, uerr := collection.UpdateOne(ctx, filter,
				bson.M{"$set": setFields, "$setOnInsert": bson.M{"rev": int64(1)}},
				options.Update().SetUpsert(true))
			if uerr != nil {
				logger.Errorf("Error creating envoys stream: %v", uerr)
				return
			}
			if res.UpsertedCount == 1 {
				return
			}
			// The document already existed (lost a create race) — retry
			// as a guarded update so the final state stays consistent.
			continue
		}

		oldRev := envoyDocRev(existing)
		res, uerr := collection.UpdateOne(ctx, withRevGuard(name, project, oldRev),
			bson.M{"$set": setFields, "$inc": bson.M{"rev": int64(1)}})
		if uerr != nil {
			logger.Errorf("Error adding or updating envoys stream: %v", uerr)
			return
		}
		if res.MatchedCount == 1 {
			return
		}
		// rev advanced under us — a concurrent writer committed; retry.
		logger.Debugf("DEBUG: AddOrUpdateEnvoy - rev conflict for nodeID %s (attempt %d), retrying\n", nodeID, attempt)
	}
	logger.Warnf("AddOrUpdateEnvoy: gave up after %d attempts (rev conflict) for nodeID %s", maxEnvoyWriteAttempts, nodeID)
}

func (e *EnvoyConnTracker) updateDownstreamsWithCount(downstreams []bson.M, downstreamAddress, nodeID, version, clientName, clientID, sourceAddress string, connCount int, isUndeploy bool, logger *logger.Logger) []bson.M {
	connected := connCount > 0

	logger.Printf("DEBUG: updateDownstreamsWithCount - nodeID=%s, downstreamAddress=%s, connCount=%d, isUndeploy=%t, connected=%t\n",
		nodeID, downstreamAddress, connCount, isUndeploy, connected)
	logger.Printf("DEBUG: updateDownstreamsWithCount - input downstreams count: %d\n", len(downstreams))

	// NodeID is mandatory - if missing, abort operation
	if nodeID == "" {
		logger.Errorf("ERROR: nodeID is empty, cannot update downstreams")
		return downstreams
	}

	if isUndeploy {
		logger.Printf("DEBUG: UNDEPLOY MODE - removing entry for nodeID: %s, downstream: %s\n", nodeID, downstreamAddress)
		filteredDownstreams := []bson.M{}
		removedCount := 0
		for _, m := range downstreams {
			existingNodeID := ""
			if nid, ok := m["nodeid"].(string); ok {
				existingNodeID = nid
			}
			existingClientName := ""
			if cn, ok := m["client_name"].(string); ok {
				existingClientName = cn
			}

			// Match logic same as normal mode
			var shouldRemove bool
			switch {
			case clientName != "" && existingClientName != "":
				// Both have client_name: match by nodeID + client_name
				shouldRemove = (existingNodeID == nodeID && existingClientName == clientName)
			default:
				// Either neither has client_name, or one has and other doesn't: match by nodeID only
				shouldRemove = (existingNodeID == nodeID)
			}

			if !shouldRemove {
				filteredDownstreams = append(filteredDownstreams, m)
			} else {
				removedCount++
				logger.Printf("DEBUG: UNDEPLOY - removing entry: nodeID=%s, client_name=%s, entry=%+v\n", existingNodeID, existingClientName, m)
			}
		}
		logger.Printf("DEBUG: UNDEPLOY - removed %d entries, result count: %d -> %d\n", removedCount, len(downstreams), len(filteredDownstreams))
		return filteredDownstreams
	}

	logger.Printf("DEBUG: NORMAL MODE - updating connected status\n")
	found := false
	for _, m := range downstreams {
		// Extract existing fields for matching
		existingNodeID := ""
		if nid, ok := m["nodeid"].(string); ok {
			existingNodeID = nid
		}
		existingClientName := ""
		if cn, ok := m["client_name"].(string); ok {
			existingClientName = cn
		}

		// Match logic based on client_name presence
		var isMatch bool
		switch {
		case clientName != "" && existingClientName != "":
			// Both have client_name: match by nodeID + client_name (for multiple envoys on same IP)
			isMatch = (existingNodeID == nodeID && existingClientName == clientName)
		default:
			// Either neither has client_name, or one has and other doesn't: match by nodeID only
			isMatch = (existingNodeID == nodeID)
		}

		if isMatch {
			logger.Printf("DEBUG: NORMAL - found entry (nodeID=%s, client_name=%s), updating connected to %t\n", existingNodeID, existingClientName, connected)
			m["connected"] = connected
			m["connections"] = connCount
			m["lastSync"] = time.Now().Unix()
			// Only set nodeID if it was empty before (legacy entries), don't overwrite existing nodeID
			if existingNodeID == "" {
				m["nodeid"] = nodeID
			}
			if sourceAddress != "" {
				m["source_address"] = sourceAddress
			}
			if version != "" {
				m["version"] = version
			}
			if clientName != "" {
				m["client_name"] = clientName
			}
			if clientID != "" {
				m["client_id"] = clientID
			}
			found = true
			break // Stop searching once found
		}
	}

	if !found && !isUndeploy {
		logger.Printf("DEBUG: NORMAL - creating new entry for downstream: %s\n", downstreamAddress)
		entry := bson.M{
			"connected":   connected,
			"nodeid":      nodeID,
			"lastSync":    time.Now().Unix(),
			"connections": connCount,
		}

		if downstreamAddress != "" {
			entry["downstream_address"] = downstreamAddress
		}

		if sourceAddress != "" {
			entry["source_address"] = sourceAddress
		}

		if version != "" {
			entry["version"] = version
		}

		if clientName != "" {
			entry["client_name"] = clientName
		}

		if clientID != "" {
			entry["client_id"] = clientID
		}

		downstreams = append(downstreams, entry)
	}

	// Supersede stale siblings: a client runs exactly one Envoy per
	// listener, so any OTHER entry carrying this same client_id is a
	// ghost of a prior connection — the pod reconnected under a new
	// downstream IP (hence a new nodeID) after a restart. The disconnect
	// path only ever flips `connected:false`; it never removes the dead
	// entry, so left in place a single live client reports as "Partial"
	// forever (determineStatus sees 1 connected of 2). Prune it here, at
	// the point the live entry is recorded. clientID is non-empty only on
	// the connect path (AddOrUpdateEnvoy) — the disconnect / undeploy
	// paths pass "", so this never fires there. The current node's own
	// entry (nodeid == nodeID) is always kept.
	if clientID != "" {
		kept := downstreams[:0]
		for _, m := range downstreams {
			mClientID, cidOK := m["client_id"].(string)
			mNodeID, nidOK := m["nodeid"].(string)
			// Prune ONLY a positively-identified superseded sibling: both
			// fields readable as strings, same client_id, a different
			// nodeID. An entry whose fields cannot be parsed is KEPT — a
			// destructive prune must never act on an entry it failed to
			// read. The current node's own entry (mNodeID == nodeID) is
			// likewise kept.
			if cidOK && nidOK && mClientID == clientID && mNodeID != nodeID {
				logger.Printf("DEBUG: pruning superseded envoy entry nodeID=%s client_id=%s (replaced by nodeID=%s)\n", mNodeID, mClientID, nodeID)
				continue
			}
			kept = append(kept, m)
		}
		downstreams = kept
	}

	logger.Printf("DEBUG: updateDownstreamsWithCount - final result count: %d\n", len(downstreams))
	return downstreams
}

func (e *EnvoyConnTracker) DisconnectNodeIDWithCount(ctx context.Context, dbClient *mongo.Database, nodeID string, connCount int, isUndeploy bool, logger *logger.Logger) {
	name, project, downstreamAddress := GetNodeIDParts(nodeID)

	logger.Debugf("DEBUG: DisconnectNodeIDWithCount - nodeID=%s, name=%s, project=%s, downstreamAddress='%s', isUndeploy=%t\n",
		nodeID, name, project, downstreamAddress, isUndeploy)

	if name == "" || project == "" {
		logger.Debugf("DEBUG: DisconnectNodeIDWithCount - invalid nodeID format, skipping\n")
		logger.Errorf("Invalid nodeID format: %s", nodeID)
		return
	}

	// downstreamAddress empty olabilir, bu normal
	collection := dbClient.Collection("envoys")
	filter := bson.M{"name": name, "project": project}

	// Optimistic-concurrency (CAS) loop — same rationale as
	// AddOrUpdateEnvoy. This also serialises against the synchronous
	// TrackUndeploy path: whichever guarded write loses the rev race
	// simply re-reads and retries.
	for attempt := 1; attempt <= maxEnvoyWriteAttempts; attempt++ {
		var existing bson.M
		err := collection.FindOne(ctx, filter).Decode(&existing)
		if errors.Is(err, mongo.ErrNoDocuments) {
			// No envoys document — nothing to disconnect.
			logger.Debugf("DEBUG: DisconnectNodeIDWithCount - no envoys doc for %s/%s, skipping\n", name, project)
			return
		}
		if err != nil {
			logger.Debugf("DEBUG: DisconnectNodeIDWithCount - error reading envoys: %v\n", err)
			logger.Errorf("Error reading envoys stream: %v", err)
			return
		}

		downstreams := getDownstreams(existing)
		downstreams = e.updateDownstreamsWithCount(downstreams, downstreamAddress, nodeID, "", "", "", "", connCount, isUndeploy, logger)
		setFields := bson.M{"envoys": downstreams, "status": determineStatus(downstreams, logger)}

		oldRev := envoyDocRev(existing)
		res, uerr := collection.UpdateOne(ctx, withRevGuard(name, project, oldRev),
			bson.M{"$set": setFields, "$inc": bson.M{"rev": int64(1)}})
		if uerr != nil {
			logger.Debugf("DEBUG: DisconnectNodeIDWithCount - error updating: %v\n", uerr)
			logger.Errorf("Error removing node ID: %v", uerr)
			return
		}
		if res.MatchedCount == 1 {
			logger.Debugf("DEBUG: DisconnectNodeIDWithCount - successfully updated MongoDB\n")
			return
		}
		// rev advanced under us — a concurrent writer committed; retry.
		logger.Debugf("DEBUG: DisconnectNodeIDWithCount - rev conflict for nodeID %s (attempt %d), retrying\n", nodeID, attempt)
	}
	logger.Warnf("DisconnectNodeIDWithCount: gave up after %d attempts (rev conflict) for nodeID %s", maxEnvoyWriteAttempts, nodeID)
}

// Legacy InsertError function removed - using enhanced error system only
