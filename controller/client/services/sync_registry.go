package services

import (
	"context"
	"fmt"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/controller/client/client"
	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// SyncClientsWithRegistry synchronizes client states between DB and registry
func (s *ClientService) SyncClientsWithRegistry(ctx context.Context) error {
	if s.registryClient == nil {
		s.logger.Warnf("Registry client not available for sync")
		return nil
	}

	s.logger.Infof("SYNC-START: Starting client-registry sync process")

	// Get all clients marked as connected in DB
	connectedClients, err := s.getConnectedClientsFromDB(ctx)
	if err != nil {
		s.logger.Errorf("Failed to get connected clients from DB: %v", err)
		return err
	}

	s.logger.Infof("SYNC-DEBUG: Found %d clients marked as connected in DB", len(connectedClients))

	// Log each client found in DB
	for i, client := range connectedClients {
		s.logger.WithFields(logger.Fields{
			"index":     i,
			"client_id": client.ClientID,
			"connected": client.Connected,
			"last_seen": client.LastSeen,
		}).Infof("SYNC-DEBUG: DB Client %d - %s", i+1, client.ClientID)
	}

	syncCount := 0
	for _, dbClient := range connectedClients {
		synced := s.syncSingleClient(ctx, dbClient)
		if synced {
			syncCount++
		}
	}

	// Only cleanup unhealthy connections if this controller has active clients
	// This prevents multiple controllers from interfering with each other
	s.clientsMux.RLock()
	hasLocalClients := len(s.clients) > 0
	s.clientsMux.RUnlock()

	if hasLocalClients {
		s.logger.Debugf("Running local unhealthy connection cleanup (%d local clients)", len(s.clients))
		s.CleanupUnhealthyConnections()
	} else {
		s.logger.Debugf("Skipping local cleanup - no active clients in this controller")
	}

	s.logger.Infof("Client sync completed: %d registry syncs + local health cleanup", syncCount)
	return nil
}

// getConnectedClientsFromDB retrieves all clients marked as connected from database
func (s *ClientService) getConnectedClientsFromDB(ctx context.Context) ([]*client.ClientInfo, error) {
	filter := bson.M{"connected": true}
	cursor, err := s.Context.Client.Collection("clients").Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var clients []*client.ClientInfo
	for cursor.Next(ctx) {
		var client client.ClientInfo
		if err := cursor.Decode(&client); err != nil {
			s.logger.Errorf("Failed to decode client: %v", err)
			continue
		}
		clients = append(clients, &client)
	}

	return clients, cursor.Err()
}

// syncSingleClient handles synchronization for a single client
func (s *ClientService) syncSingleClient(ctx context.Context, dbClient *client.ClientInfo) bool {
	clientID := dbClient.ClientID
	lastSeenAge := time.Since(dbClient.LastSeen)
	// Reachable = owned by us, even if the command stream is momentarily down and
	// the client is alive via ping. The strict stream-based check here would make
	// the sync tear down ping-alive clients and drop their registry location.
	// Command delivery still uses the strict stream check at send time.
	isLocallyConnected := s.IsClientReachable(clientID)

	s.logger.Debugf("Sync analysis for client %s: locally_connected=%v, last_seen_age=%v",
		clientID, isLocallyConnected, lastSeenAge)

	// Check if client exists in registry
	location, err := s.registryClient.GetClientLocation(clientID)
	registryFound := err == nil && location.ControllerId != ""

	if registryFound {
		// Only sync if this is the responsible controller OR client is locally connected
		currentControllerID := s.registryClient.GetControllerID()
		isResponsibleController := location.ControllerId == currentControllerID

		if !isResponsibleController && !isLocallyConnected {
			// This client belongs to another controller and we don't have it locally
			// Skip sync to prevent interference - let the owner controller handle it
			s.logger.Debugf("SYNC-SKIP: Client %s belongs to controller %s, we are %s, and not locally connected - skipping sync",
				clientID, location.ControllerId, currentControllerID)
			return false
		}

		return s.handleRegistryFoundClient(ctx, dbClient, location, isLocallyConnected, lastSeenAge)
	} else {
		return s.handleRegistryMissingClient(ctx, dbClient, err, isLocallyConnected, lastSeenAge)
	}
}

// handleRegistryFoundClient handles clients that are found in registry
func (s *ClientService) handleRegistryFoundClient(ctx context.Context, dbClient *client.ClientInfo, location *bridge.GetControllerClusterResponse, isLocallyConnected bool, lastSeenAge time.Duration) bool {
	clientID := dbClient.ClientID
	currentControllerID := s.registryClient.GetControllerID()

	// DEBUG: Add detailed controller ID comparison logging
	s.logger.WithFields(logger.Fields{
		"client_id":           clientID,
		"registry_controller": location.ControllerId,
		"current_controller":  currentControllerID,
		"are_equal":           location.ControllerId == currentControllerID,
		"locally_connected":   isLocallyConnected,
		"last_seen_age":       lastSeenAge,
	}).Infof("SYNC-DEBUG: Controller ID comparison for client %s", clientID)

	if location.ControllerId == currentControllerID {
		// Client correctly registered to this controller
		s.logger.Infof("Client %s correctly registered to THIS controller (%s)", clientID, currentControllerID)
		return s.handleOwnRegistryClient(ctx, clientID, isLocallyConnected, lastSeenAge)
	} else {
		// Client registered to different controller
		s.logger.Warnf("Client %s registered to DIFFERENT controller (registry=%s, current=%s)",
			clientID, location.ControllerId, currentControllerID)
		return s.handleForeignRegistryClient(ctx, clientID, location.ControllerId, isLocallyConnected)
	}
}

// handleOwnRegistryClient handles clients registered to this controller
func (s *ClientService) handleOwnRegistryClient(ctx context.Context, clientID string, isLocallyConnected bool, lastSeenAge time.Duration) bool {
	s.logger.WithFields(logger.Fields{
		"client_id":         clientID,
		"locally_connected": isLocallyConnected,
		"last_seen_age":     lastSeenAge,
	}).Infof("SYNC-DEBUG: handleOwnRegistryClient for client %s", clientID)

	if !isLocallyConnected {
		// Registry says ours but not locally connected - stale registry entry
		s.logger.Warnf("Client %s in registry but not locally connected, cleaning up registry", clientID)
		if notifyErr := s.registryClient.NotifyClientDisconnected(clientID); notifyErr != nil {
			s.logger.Errorf("Failed to cleanup stale registry entry for %s: %v", clientID, notifyErr)
		}

		// Also mark as disconnected in DB if stale enough (aligned with health check threshold)
		if lastSeenAge > 5*time.Minute {
			s.logger.Infof("Client %s not locally connected and stale, marking as disconnected", clientID)
			if syncErr := s.MarkClientDisconnectedInDBWithReason(ctx, clientID, "sync_stale_registry_entry"); syncErr != nil {
				s.logger.Errorf("Failed to mark client %s as disconnected: %v", clientID, syncErr)
				return false
			}
			return true
		}
	} else {
		// Client correctly registered and locally connected - all good
		s.logger.Infof("Client %s correctly registered and locally connected - keeping as connected", clientID)

		// Update connect time in DB to reflect current sync time when confirmed connected
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			filter := bson.M{"client_id": clientID}
			update := bson.M{"$set": bson.M{
				"connected":      true,
				"connect_time":   time.Now(),
				"connect_reason": "sync_confirmed_connected",
			}}

			result, err := s.Context.Client.Collection("clients").UpdateOne(ctx, filter, update)
			if err != nil {
				s.logger.Errorf("Failed to update client connect time in DB (sync): %v", err)
			} else {
				s.logger.WithFields(logger.Fields{
					"client_id":      clientID,
					"matched_count":  result.MatchedCount,
					"modified_count": result.ModifiedCount,
				}).Debugf("Client connect time updated in DB (sync confirmed): %s", clientID)
			}
		}()
	}
	return false
}

// handleForeignRegistryClient handles clients registered to other controllers
func (s *ClientService) handleForeignRegistryClient(ctx context.Context, clientID string, foreignControllerID string, isLocallyConnected bool) bool {
	if isLocallyConnected {
		// Split brain scenario - client locally connected but registry points elsewhere
		s.logger.Warnf("Split brain detected: client %s locally connected but registry points to %s",
			clientID, foreignControllerID)

		// Re-register to this controller (steal the client)
		if notifyErr := s.registryClient.NotifyClientConnected(clientID); notifyErr != nil {
			s.logger.Errorf("Failed to re-register client %s: %v", clientID, notifyErr)
		} else {
			s.logger.Infof("Successfully re-registered client %s to this controller", clientID)
		}
		return false
	} else {
		// Client registered elsewhere and not locally connected - normal case
		s.logger.Debugf("Client %s correctly registered to controller %s", clientID, foreignControllerID)

		// Mark as disconnected in our DB since it's on another controller
		if syncErr := s.MarkClientDisconnectedInDBWithReason(ctx, clientID, fmt.Sprintf("sync_foreign_controller_%s", foreignControllerID)); syncErr != nil {
			s.logger.Errorf("Failed to mark remote client %s as disconnected: %v", clientID, syncErr)
			return false
		}
		return true
	}
}

// handleRegistryMissingClient handles clients not found in registry
func (s *ClientService) handleRegistryMissingClient(ctx context.Context, dbClient *client.ClientInfo, registryErr error, isLocallyConnected bool, lastSeenAge time.Duration) bool {
	clientID := dbClient.ClientID

	// Client NOT found in registry
	if registryErr != nil {
		s.logger.Warnf("Registry lookup failed for client %s: %v - skipping sync decision", clientID, registryErr)
		return false // Skip this client due to registry communication error
	}

	if isLocallyConnected {
		// Client locally connected but not in registry - re-register
		return s.handleMissingButConnectedClient(clientID)
	} else {
		// Client not in registry and not locally connected
		return s.handleMissingAndDisconnectedClient(ctx, dbClient, lastSeenAge)
	}
}

// handleMissingButConnectedClient handles clients missing from registry but locally connected
func (s *ClientService) handleMissingButConnectedClient(clientID string) bool {
	s.logger.Infof("Client %s locally connected but missing from registry, re-registering", clientID)

	if notifyErr := s.registryClient.NotifyClientConnected(clientID); notifyErr != nil {
		s.logger.Errorf("Failed to re-register client %s: %v", clientID, notifyErr)
	} else {
		s.logger.Infof("Successfully re-registered client %s", clientID)
	}
	return false
}

// handleMissingAndDisconnectedClient handles clients missing from registry and not locally connected
func (s *ClientService) handleMissingAndDisconnectedClient(ctx context.Context, dbClient *client.ClientInfo, lastSeenAge time.Duration) bool {
	clientID := dbClient.ClientID

	// Only mark as disconnected if genuinely stale (aligned with health check threshold)
	if lastSeenAge > 5*time.Minute {
		s.logger.Warnf("Client %s (%s) not in registry, not locally connected, and stale (last seen: %v ago), marking as disconnected",
			dbClient.Name, clientID, lastSeenAge)

		if syncErr := s.MarkClientDisconnectedInDBWithReason(ctx, clientID, "sync_missing_not_local_stale"); syncErr != nil {
			s.logger.Errorf("Failed to mark client %s as disconnected: %v", clientID, syncErr)
			return false
		}
		return true
	} else {
		s.logger.Debugf("Client %s not in registry and not locally connected but recently active (last seen: %v ago), keeping connected for grace period",
			clientID, lastSeenAge)
		return false
	}
}

// CleanupStaleClientsFromDB performs global DB cleanup for stale clients
// This runs independently of registry and cleans up old disconnected clients
func (s *ClientService) CleanupStaleClientsFromDB(ctx context.Context) error {
	s.logger.Infof("CLEANUP-START: Starting global stale client cleanup from DB")

	// Get all clients marked as connected in DB
	connectedClients, err := s.getConnectedClientsFromDB(ctx)
	if err != nil {
		s.logger.Errorf("Failed to get connected clients from DB: %v", err)
		return err
	}

	s.logger.Infof("CLEANUP-DEBUG: Found %d clients marked as connected in DB", len(connectedClients))

	cleanupCount := 0
	for _, dbClient := range connectedClients {
		clientID := dbClient.ClientID
		lastSeenAge := time.Since(dbClient.LastSeen)
		// Reachable (in-memory + Connected), not strictly stream-connected: a
		// ping-alive client whose command stream is momentarily down must not be
		// marked disconnected by this DB cleanup. Truly dead in-memory entries
		// are removed by CleanupUnhealthyConnections via stale LastSeen, which
		// then lets this cleanup mark them in DB on a later pass.
		isLocallyConnected := s.IsClientReachable(clientID)

		// Skip locally connected clients
		if isLocallyConnected {
			s.logger.Debugf("CLEANUP-SKIP: Client %s is locally reachable, skipping", clientID)
			continue
		}

		// Check if client is connected to another controller via registry
		if s.registryClient != nil {
			location, err := s.registryClient.GetClientLocation(clientID)
			if err == nil && location.Found {
				s.logger.Debugf("CLEANUP-SKIP: Client %s is connected to controller %s (registry), skipping",
					clientID, location.ControllerId)
				continue
			}
		}

		// Mark as disconnected if last_seen older than 5 minutes (stale, aligned with health check threshold)
		if lastSeenAge > 5*time.Minute {
			s.logger.Warnf("CLEANUP-MARK: Client %s (%s) is stale (last seen: %v ago), marking as disconnected",
				dbClient.Name, clientID, lastSeenAge)

			if err := s.MarkClientDisconnectedInDBWithReason(ctx, clientID, "global_cleanup_stale"); err != nil {
				s.logger.Errorf("Failed to mark client %s as disconnected: %v", clientID, err)
			} else {
				cleanupCount++
			}
		}
	}

	s.logger.Infof("CLEANUP-DONE: Global cleanup completed, marked %d stale clients as disconnected", cleanupCount)
	return nil
}

// RecalculateServiceStatuses recalculates status field for all services based on envoy connected states
// Status logic: Live (all connected), Partial (some connected), Offline (none connected)
func (s *ClientService) RecalculateServiceStatuses(ctx context.Context) error {
	// Get all services
	cursor, err := s.Context.Client.Collection("envoys").Find(ctx, bson.M{})
	if err != nil {
		return fmt.Errorf("failed to fetch services: %w", err)
	}
	defer cursor.Close(ctx)

	updatedCount := 0
	for cursor.Next(ctx) {
		var service bson.M
		if err := cursor.Decode(&service); err != nil {
			s.logger.Errorf("Failed to decode service: %v", err)
			continue
		}

		// Extract envoys array
		envoys, ok := service["envoys"].(bson.A)
		if !ok {
			continue
		}

		// Count connected envoys
		connectedCount := 0
		totalCount := len(envoys)

		for _, envoyInterface := range envoys {
			envoy, ok := envoyInterface.(bson.M)
			if !ok {
				continue
			}
			if connected, ok := envoy["connected"].(bool); ok && connected {
				connectedCount++
			}
		}

		// Determine status
		var newStatus string
		switch {
		case totalCount == 0:
			newStatus = "Offline"
		case connectedCount == totalCount:
			newStatus = "Live"
		case connectedCount > 0:
			newStatus = "Partial"
		default:
			newStatus = "Offline"
		}

		// Update only if status changed
		currentStatus, _ := service["status"].(string)
		if currentStatus != newStatus {
			// CAS guard: commit the recomputed status only if the document
			// is unchanged since this cursor read. If a concurrent
			// AddOrUpdateEnvoy / DisconnectNodeIDWithCount committed in
			// between, it already wrote a correct status from fresh state
			// — skip rather than clobber it with this stale recomputation.
			oldRev := int64(0)
			switch v := service["rev"].(type) {
			case int64:
				oldRev = v
			case int32:
				oldRev = int64(v)
			case int:
				oldRev = int64(v)
			}
			filter := bson.M{"_id": service["_id"]}
			if oldRev == 0 {
				// Legacy doc with no rev field yet — match it, the $inc
				// below stamps the first rev.
				filter["$or"] = []bson.M{
					{"rev": int64(0)},
					{"rev": bson.M{"$exists": false}},
				}
			} else {
				filter["rev"] = oldRev
			}
			// $inc `rev` so the control-plane CAS observes this status
			// write and does not overwrite it from a stale read.
			update := bson.M{
				"$set": bson.M{"status": newStatus},
				"$inc": bson.M{"rev": int64(1)},
			}

			res, err := s.Context.Client.Collection("envoys").UpdateOne(ctx, filter, update)
			switch {
			case err != nil:
				s.logger.Errorf("Failed to update status for service %v: %v", service["name"], err)
			case res.MatchedCount == 0:
				// A concurrent writer advanced rev — it owns a fresher
				// status; leave it untouched.
				s.logger.Debugf("Skipped status update for service %v: concurrent change", service["name"])
			default:
				s.logger.Debugf("Updated service %v status: %s -> %s", service["name"], currentStatus, newStatus)
				updatedCount++
			}
		}
	}

	if updatedCount > 0 {
		s.logger.Infof("Recalculated status for %d services", updatedCount)
	}

	return cursor.Err()
}

// CleanupStaleEnvoysFromDB performs global DB cleanup for stale envoys
// This marks envoys with lastSync > 2 minutes as disconnected (connected: false)
// Idempotent: Safe to run from multiple controller pods simultaneously
func (s *ClientService) CleanupStaleEnvoysFromDB(ctx context.Context) error {
	s.logger.Infof("ENVOY-CLEANUP-START: Starting global stale envoy cleanup from DB")

	// Calculate stale threshold (2 minutes ago)
	staleThreshold := time.Now().Add(-2 * time.Minute).Unix()

	// Build filter to find services with stale connected envoys
	// Find services where at least one envoy is: connected=true AND lastSync < threshold
	filter := bson.M{
		"envoys": bson.M{
			"$elemMatch": bson.M{
				"connected": true,
				"lastSync":  bson.M{"$lt": staleThreshold},
			},
		},
	}

	// Update: Set connected=false for stale envoys using arrayFilters
	// This updates ONLY the envoys that match the stale condition.
	// The $inc on `rev` makes the change visible to the control-plane
	// AddOrUpdateEnvoy/DisconnectNodeIDWithCount compare-and-swap so a
	// concurrent whole-array $set there cannot revive a stale envoy.
	update := bson.M{
		"$set": bson.M{
			"envoys.$[elem].connected": false,
		},
		"$inc": bson.M{"rev": int64(1)},
	}

	// ArrayFilters: Apply update only to envoys with lastSync < threshold
	arrayFilters := options.ArrayFilters{
		Filters: []any{
			bson.M{
				"elem.connected": true,
				"elem.lastSync":  bson.M{"$lt": staleThreshold},
			},
		},
	}

	// Execute update with arrayFilters
	updateOpts := options.Update().SetArrayFilters(arrayFilters)
	result, err := s.Context.Client.Collection("envoys").UpdateMany(
		ctx,
		filter,
		update,
		updateOpts,
	)
	if err != nil {
		s.logger.Errorf("ENVOY-CLEANUP-ERROR: Failed to cleanup stale envoys: %v", err)
		return fmt.Errorf("failed to cleanup stale envoys: %w", err)
	}

	if result.ModifiedCount > 0 {
		s.logger.Infof("ENVOY-CLEANUP-DONE: Marked envoys in %d services as disconnected (stale lastSync < %d)",
			result.ModifiedCount, staleThreshold)
	} else {
		s.logger.Debugf("ENVOY-CLEANUP-DONE: No stale envoys found (all healthy)")
	}

	// Prune orphan entries (a connection no longer part of the listener's
	// deployment) BEFORE recalculating status, so the recalculated status
	// reflects the cleaned array. Non-fatal: a prune failure must not stop
	// the status recalculation below.
	if err := s.PruneOrphanEnvoys(ctx); err != nil {
		s.logger.Errorf("ENVOY-CLEANUP-ERROR: Failed to prune orphan envoys: %v", err)
	}

	// IMPORTANT: ALWAYS recalculate status (even if no envoys were modified)
	// This fixes status field that was not updated by previous cleanup runs
	// Status may be stale from old code that didn't call RecalculateServiceStatuses
	if err := s.RecalculateServiceStatuses(ctx); err != nil {
		s.logger.Errorf("ENVOY-CLEANUP-ERROR: Failed to recalculate service statuses: %v", err)
		return fmt.Errorf("failed to recalculate service statuses: %w", err)
	}
	s.logger.Debugf("ENVOY-CLEANUP-STATUS: Recalculated service statuses")

	return nil
}

// PruneOrphanEnvoys removes envoys[] entries that are no longer part of
// the listener's deployment. An entry is an orphan when it is
// disconnected AND its downstream_address is not among the matching
// `services` document's clients[] — e.g. a pod that connected once from
// an IP the listener was later moved off. The disconnect path only ever
// flips connected:false and never removes such an entry, so without this
// prune one stale connection pins a listener to "Partial" forever.
//
// Conservative by construction:
//   - connected:true entries are never touched (the $pull requires
//     connected:false).
//   - a disconnected entry whose downstream IS a current deployment
//     target (a deployed-but-down client) is kept — "Partial" stays
//     truthful for it.
//   - a listener with no `services` document is skipped entirely —
//     never prune on the absence of the deploy config.
//
// Each prune is an atomic $pull (no read-modify-write), so it is safe
// against the concurrent heartbeat / connect updates and idempotent
// across controller pods.
func (s *ClientService) PruneOrphanEnvoys(ctx context.Context) error {
	cursor, err := s.Context.Client.Collection("envoys").Find(ctx, bson.M{})
	if err != nil {
		return fmt.Errorf("failed to fetch envoys for orphan prune: %w", err)
	}
	defer cursor.Close(ctx)

	prunedDocs := 0
	for cursor.Next(ctx) {
		var doc bson.M
		if err := cursor.Decode(&doc); err != nil {
			s.logger.Errorf("ENVOY-PRUNE: Failed to decode envoys doc: %v", err)
			continue
		}
		name, _ := doc["name"].(string)
		project, _ := doc["project"].(string)
		if name == "" || project == "" {
			continue
		}

		// Deploy config is the source of truth for the downstreams the
		// listener is actually deployed to. No services doc / read error
		// → cannot decide → skip (never prune on absence).
		var svc bson.M
		if err := s.Context.Client.Collection("services").
			FindOne(ctx, bson.M{"name": name, "project": project}).Decode(&svc); err != nil {
			continue
		}
		deployed := []string{}
		if clients, ok := svc["clients"].(bson.A); ok {
			for _, c := range clients {
				if cm, ok := c.(bson.M); ok {
					if ds, ok := cm["downstream_address"].(string); ok && ds != "" {
						deployed = append(deployed, ds)
					}
				}
			}
		}

		// Atomic $pull: drop every disconnected entry whose downstream is
		// not a current deployment target. The filter's $elemMatch means
		// the write (and its rev bump) only fires when an orphan actually
		// exists — no churn on clean documents. The $inc on `rev` makes
		// the prune visible to the control-plane CAS so a stale-read
		// whole-array $set there cannot re-introduce the orphan.
		orphanCond := bson.M{
			"connected":          false,
			"downstream_address": bson.M{"$nin": deployed},
		}
		res, err := s.Context.Client.Collection("envoys").UpdateOne(ctx,
			bson.M{"_id": doc["_id"], "envoys": bson.M{"$elemMatch": orphanCond}},
			bson.M{
				"$pull": bson.M{"envoys": orphanCond},
				"$inc":  bson.M{"rev": int64(1)},
			},
		)
		if err != nil {
			s.logger.Errorf("ENVOY-PRUNE: Failed to prune orphans for listener %s: %v", name, err)
			continue
		}
		if res.ModifiedCount > 0 {
			prunedDocs++
			s.logger.Infof("ENVOY-PRUNE: Removed orphan envoy entr(ies) from listener %s", name)
		}
	}

	if prunedDocs > 0 {
		s.logger.Infof("ENVOY-PRUNE-DONE: Pruned orphan envoys from %d listener(s)", prunedDocs)
	}
	return cursor.Err()
}

// StartPeriodicSync starts periodic sync between DB and registry
func (s *ClientService) StartPeriodicSync(interval time.Duration) {
	s.logger.Infof("Starting periodic client-registry sync (interval: %v)", interval)

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

			// Run registry sync if available
			if s.registryClient != nil {
				if err := s.SyncClientsWithRegistry(ctx); err != nil {
					s.logger.Errorf("Periodic sync failed: %v", err)
				}
			} else {
				s.logger.Debugf("Registry client not available, skipping registry sync")
			}

			// ALWAYS run global DB cleanup regardless of registry availability
			if err := s.CleanupStaleClientsFromDB(ctx); err != nil {
				s.logger.Errorf("Global client cleanup failed: %v", err)
			}

			// ALWAYS run global envoy cleanup (idempotent across multiple controller pods)
			if err := s.CleanupStaleEnvoysFromDB(ctx); err != nil {
				s.logger.Errorf("Global envoy cleanup failed: %v", err)
			}

			cancel()
		}
	}()
}
