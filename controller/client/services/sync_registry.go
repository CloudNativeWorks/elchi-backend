package services

import (
	"context"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/controller/client/client"
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
		// Check if client exists in registry
		location, err := s.registryClient.GetClientLocation(dbClient.ClientID)
		if err != nil || location.ControllerId == "" {
			// Client not in registry, mark as disconnected in DB
			s.logger.Warnf("Client %s (%s) not found in registry, marking as disconnected",
				dbClient.Name, dbClient.ClientID)

			if syncErr := s.MarkClientDisconnectedInDB(ctx, dbClient.ClientID); syncErr != nil {
				s.logger.Errorf("Failed to mark client %s as disconnected: %v", dbClient.ClientID, syncErr)
			} else {
				syncCount++
			}
		}
	}

	// Add local unhealthy connection cleanup
	s.CleanupUnhealthyConnections()

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
