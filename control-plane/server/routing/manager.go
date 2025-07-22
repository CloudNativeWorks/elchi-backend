package routing

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/control-plane/server/snapshot"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

type Manager struct {
	client          *RoutingClient
	config          *Config
	logger          *logger.Logger
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	snapshotContext *snapshot.Context
	
	// Node version tracking
	nodeVersions map[string]string // nodeID -> version
	nodesMutex   sync.RWMutex
}

// NewManager creates a new routing manager
func NewManager(config *Config, logger *logger.Logger, snapshotContext *snapshot.Context) (*Manager, error) {
	client, err := NewRoutingClient(config, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create routing client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	manager := &Manager{
		client:          client,
		config:          config,
		logger:          logger,
		ctx:             ctx,
		cancel:          cancel,
		snapshotContext: snapshotContext,
		nodeVersions:    make(map[string]string),
	}

	return manager, nil
}

// Start starts the routing manager
func (m *Manager) Start() error {
	// Start registry connection and registration in background with retry
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		
		m.logger.Infof("Starting registry connection and registration process...")

		// Connect to registry with retry
		if err := m.client.ConnectWithRetry(m.ctx); err != nil {
			m.logger.Errorf("Failed to connect to registry: %v", err)
			return
		}

		// Register control-plane with retry
		if err := m.client.RegisterControlPlaneWithRetry(m.ctx, m.config); err != nil {
			m.logger.Errorf("Failed to register control-plane: %v", err)
			return
		}

		m.logger.Infof("Successfully connected to registry and registered control-plane")

		// Sync all existing nodes/snapshots with registry
		m.logger.Infof("Starting initial node sync with registry...")
		syncCtx, syncCancel := context.WithTimeout(context.Background(), 60*time.Second)
		getAllNodes := func() []NodeInfo {
			return m.GetAllNodes()
		}
		if err := m.client.SyncAllNodesWithRegistry(syncCtx, m.config.ControlPlaneID, getAllNodes); err != nil {
			m.logger.Errorf("Failed to sync existing nodes with registry: %v", err)
		} else {
			m.logger.Infof("Initial node sync completed successfully")
		}
		syncCancel()
	}()

	// Start background tasks
	m.wg.Add(2)
	go m.healthCheckLoop()
	go m.nodeListUpdateLoop()

	m.logger.Info("Routing manager started")
	return nil
}

// Stop stops the routing manager
func (m *Manager) Stop() error {
	m.cancel()
	m.wg.Wait()

	if err := m.client.Disconnect(); err != nil {
		return fmt.Errorf("failed to disconnect from registry: %v", err)
	}

	m.logger.Info("Routing manager stopped")
	return nil
}

// NotifySnapshotDelivered notifies registry about snapshot delivery
func (m *Manager) NotifySnapshotDelivered(nodeID, version string) {
	// Store node version for future reference
	m.nodesMutex.Lock()
	m.nodeVersions[nodeID] = version
	m.nodesMutex.Unlock()

	// Notify registry with retry logic
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		
		if err := m.client.NotifySnapshotDeliveredWithRetry(ctx, m.config.ControlPlaneID, nodeID, version); err != nil {
			m.logger.Errorf("Failed to notify snapshot delivery: %v", err)
		}
	}()
}

// RemoveNode removes a node from version tracking
func (m *Manager) RemoveNode(nodeID string) {
	m.nodesMutex.Lock()
	delete(m.nodeVersions, nodeID)
	m.nodesMutex.Unlock()
	
	m.logger.Debugf("Removed node version tracking for: %s", nodeID)
}

// healthCheckLoop performs periodic health checks
func (m *Manager) healthCheckLoop() {
	defer m.wg.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			if err := m.client.HealthCheck(); err != nil {
				m.logger.Errorf("Health check failed: %v", err)
				
				// Connection might be lost, try to reconnect and re-register
				m.logger.Infof("Attempting to reconnect and re-register due to health check failure")
				
				// Create context with timeout for reconnection
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				
				// Try to reconnect
				if err := m.client.Disconnect(); err != nil {
					m.logger.Warnf("Error disconnecting: %v", err)
				}
				
				if err := m.client.ConnectWithRetry(ctx); err != nil {
					m.logger.Errorf("Failed to reconnect to registry: %v", err)
					cancel()
					continue
				}
				
				// Try to re-register
				if err := m.client.RegisterControlPlaneWithRetry(ctx, m.config); err != nil {
					m.logger.Errorf("Failed to re-register control-plane: %v", err)
				} else {
					m.logger.Infof("Successfully reconnected and re-registered control-plane")
					
					// Sync existing nodes after reconnection
					m.logger.Infof("Starting node sync after health check reconnection...")
					getAllNodes := func() []NodeInfo {
						return m.GetAllNodes()
					}
					if err := m.client.SyncAllNodesWithRegistry(ctx, m.config.ControlPlaneID, getAllNodes); err != nil {
						m.logger.Errorf("Failed to sync existing nodes after reconnection: %v", err)
					} else {
						m.logger.Infof("Node sync completed successfully after health check reconnection")
					}
				}
				
				cancel()
			}
		}
	}
}

// nodeListUpdateLoop updates the node list periodically
func (m *Manager) nodeListUpdateLoop() {
	defer m.wg.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.updateNodeList()
		}
	}
}

	// updateNodeList updates the node list in registry
func (m *Manager) updateNodeList() {
	// Get connected nodes
	nodes := m.GetConnectedNodes()
	
	m.logger.Infof("Periodic node list update: %d connected nodes", len(nodes))

	// Always try to update node list (even if empty) to indicate control-plane is alive
	if err := m.client.UpdateNodeList(m.config.ControlPlaneID, nodes); err != nil {
		m.logger.Errorf("Failed to update node list: %v", err)
		
		// Connection might be lost, try to reconnect and re-register
		m.logger.Infof("Attempting to reconnect and re-register due to node list update failure")
		
		// Create context with timeout for reconnection
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		
		// Try to reconnect
		if err := m.client.Disconnect(); err != nil {
			m.logger.Warnf("Error disconnecting: %v", err)
		}
		
		if err := m.client.ConnectWithRetry(ctx); err != nil {
			m.logger.Errorf("Failed to reconnect to registry: %v", err)
			return
		}
		
		// Try to re-register
		if err := m.client.RegisterControlPlaneWithRetry(ctx, m.config); err != nil {
			m.logger.Errorf("Failed to re-register control-plane: %v", err)
		} else {
			m.logger.Infof("Successfully reconnected and re-registered control-plane")
			
			// Sync existing nodes after reconnection
			m.logger.Infof("Starting node sync after reconnection...")
			getAllNodes := func() []NodeInfo {
				return m.GetAllNodes()
			}
			if err := m.client.SyncAllNodesWithRegistry(ctx, m.config.ControlPlaneID, getAllNodes); err != nil {
				m.logger.Errorf("Failed to sync existing nodes after reconnection: %v", err)
			} else {
				m.logger.Infof("Node sync completed successfully after reconnection")
			}
		}
	} else {
		m.logger.Debugf("Periodic node list update completed successfully")
	}
}

// GetAllNodes returns all nodes from snapshot context (both connected and disconnected)
func (m *Manager) GetAllNodes() []NodeInfo {
	// Get all nodes from snapshot cache
	statusKeys := m.snapshotContext.Cache.Cache.GetStatusKeys()
	var nodes []NodeInfo
	
	m.nodesMutex.RLock()
	defer m.nodesMutex.RUnlock()
	
	for _, nodeID := range statusKeys {
		status := m.snapshotContext.Cache.Cache.GetStatusInfo(nodeID)
		if status != nil {
			// Use stored version from metadata, fallback to control-plane version
			version := m.config.Version
			if storedVersion, exists := m.nodeVersions[nodeID]; exists {
				version = storedVersion
			}
			
			// Get last watch time
			lastWatchTime := status.GetLastDeltaWatchRequestTime()
			if lastWatchTime.IsZero() {
				lastWatchTime = time.Now()
			}
			
			nodes = append(nodes, NodeInfo{
				NodeID:   nodeID,
				Version:  version,
				LastSeen: lastWatchTime,
			})
		}
	}
	
	return nodes
}

// GetConnectedNodes returns the list of connected nodes from snapshot context
func (m *Manager) GetConnectedNodes() []NodeInfo {
	connectedNodes := m.snapshotContext.GetConnectedNodes()
	
	m.nodesMutex.RLock()
	defer m.nodesMutex.RUnlock()

	// Convert to NodeInfo slice
	var nodes []NodeInfo
	for _, node := range connectedNodes {
		if node.Connected {
			// Use stored version from metadata, fallback to what's in snapshot context
			version := node.Version
			if storedVersion, exists := m.nodeVersions[node.NodeID]; exists {
				version = storedVersion
			}
			
			nodes = append(nodes, NodeInfo{
				NodeID:   node.NodeID,
				Version:  version,
				LastSeen: node.LastSeen,
			})
		}
	}

	return nodes
}

// GetConnectedNodeCount returns the number of connected nodes
func (m *Manager) GetConnectedNodeCount() int {
	connectedNodes := m.snapshotContext.GetConnectedNodes()

	count := 0
	for _, node := range connectedNodes {
		if node.Connected {
			count++
		}
	}

	return count
}
