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
	}

	return manager, nil
}

// Start starts the routing manager
func (m *Manager) Start() error {
	// Connect to registry
	if err := m.client.Connect(); err != nil {
		return fmt.Errorf("failed to connect to registry: %v", err)
	}

	// Register control-plane with registry
	if err := m.client.RegisterControlPlane(m.config); err != nil {
		return fmt.Errorf("failed to register control-plane: %v", err)
	}

	// Start background tasks
	m.wg.Add(2)
	go m.healthCheckLoop()
	go m.nodeListUpdateLoop()

	m.logger.Info("Routing manager started and registered with registry")
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
	// Notify registry
	go func() {
		if err := m.client.NotifySnapshotDelivered(m.config.ControlPlaneID, nodeID, version); err != nil {
			m.logger.Errorf("Failed to notify snapshot delivery: %v", err)
		}
	}()
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
	// Get connected nodes from snapshot context
	connectedNodes := m.snapshotContext.GetConnectedNodes()

	// Convert to NodeInfo slice
	var nodes []NodeInfo
	for _, node := range connectedNodes {
		if node.Connected {
			nodes = append(nodes, NodeInfo{
				NodeID:   node.NodeID,
				Version:  node.Version,
				LastSeen: node.LastSeen,
			})
		}
	}

	if len(nodes) > 0 {
		if err := m.client.UpdateNodeList(m.config.ControlPlaneID, nodes); err != nil {
			m.logger.Errorf("Failed to update node list: %v", err)
		}
	}
}

// GetConnectedNodes returns the list of connected nodes from snapshot context
func (m *Manager) GetConnectedNodes() []NodeInfo {
	connectedNodes := m.snapshotContext.GetConnectedNodes()

	var nodes []NodeInfo
	for _, node := range connectedNodes {
		if node.Connected {
			nodes = append(nodes, NodeInfo{
				NodeID:   node.NodeID,
				Version:  node.Version,
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
