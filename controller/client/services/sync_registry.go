package services

import (
	"context"
	"fmt"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/controller/client/client"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
	"go.mongodb.org/mongo-driver/bson"
)

// SyncClientsWithRegistry synchronizes client states between DB and registry
func (s *ClientService) SyncClientsWithRegistry(ctx context.Context) error {
	if s.registryClient == nil {
		s.logger.Warnf("Registry client not available for sync")
		return nil
	}

	s.logger.Debugf("Starting client-registry sync process")

	// Get all clients marked as connected in DB
	connectedClients, err := s.getConnectedClientsFromDB(ctx)
	if err != nil {
		s.logger.Errorf("Failed to get connected clients from DB: %v", err)
		return err
	}

	s.logger.Debugf("Found %d clients marked as connected in DB", len(connectedClients))

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
	isLocallyConnected := s.IsClientConnected(clientID)
	
	s.logger.Debugf("Sync analysis for client %s: locally_connected=%v, last_seen_age=%v", 
		clientID, isLocallyConnected, lastSeenAge)

	// Check if client exists in registry
	location, err := s.registryClient.GetClientLocation(clientID)
	registryFound := err == nil && location.ControllerId != ""
	
	if registryFound {
		return s.handleRegistryFoundClient(ctx, dbClient, location, isLocallyConnected, lastSeenAge)
	} else {
		return s.handleRegistryMissingClient(ctx, dbClient, err, isLocallyConnected, lastSeenAge)
	}
}

// handleRegistryFoundClient handles clients that are found in registry
func (s *ClientService) handleRegistryFoundClient(ctx context.Context, dbClient *client.ClientInfo, location *pb.GetControllerClusterResponse, isLocallyConnected bool, lastSeenAge time.Duration) bool {
	clientID := dbClient.ClientID
	currentControllerID := s.registryClient.GetControllerID()
	
	if location.ControllerId == currentControllerID {
		// Client correctly registered to this controller
		return s.handleOwnRegistryClient(ctx, clientID, isLocallyConnected, lastSeenAge)
	} else {
		// Client registered to different controller
		return s.handleForeignRegistryClient(ctx, clientID, location.ControllerId, isLocallyConnected)
	}
}

// handleOwnRegistryClient handles clients registered to this controller
func (s *ClientService) handleOwnRegistryClient(ctx context.Context, clientID string, isLocallyConnected bool, lastSeenAge time.Duration) bool {
	if !isLocallyConnected {
		// Registry says ours but not locally connected - stale registry entry
		s.logger.Warnf("Client %s in registry but not locally connected, cleaning up registry", clientID)
		if notifyErr := s.registryClient.NotifyClientDisconnected(clientID); notifyErr != nil {
			s.logger.Errorf("Failed to cleanup stale registry entry for %s: %v", clientID, notifyErr)
		}
		
		// Also mark as disconnected in DB if stale enough
		if lastSeenAge > 10*time.Minute {
			s.logger.Infof("Client %s not locally connected and stale, marking as disconnected", clientID)
			if syncErr := s.MarkClientDisconnectedInDBWithReason(ctx, clientID, "sync_stale_registry_entry"); syncErr != nil {
				s.logger.Errorf("Failed to mark client %s as disconnected: %v", clientID, syncErr)
				return false
			}
			return true
		}
	}
	// else: Client correctly registered and locally connected - all good
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
	
	// Only mark as disconnected if genuinely stale
	// Increased threshold to 15 minutes for more stability
	if lastSeenAge > 11*time.Minute {
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

// StartPeriodicSync starts periodic sync between DB and registry
func (s *ClientService) StartPeriodicSync(interval time.Duration) {
	if s.registryClient == nil {
		s.logger.Warnf("Registry client not available, skipping periodic sync")
		return
	}

	s.logger.Infof("Starting periodic client-registry sync (interval: %v)", interval)

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := s.SyncClientsWithRegistry(ctx); err != nil {
				s.logger.Errorf("Periodic sync failed: %v", err)
			}
			cancel()
		}
	}()
}
