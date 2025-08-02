package registry

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/control-plane/server/snapshot"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

type ControlPlaneManager struct {
	client          *ControlPlaneRegistryClient
	config          *ControlPlaneConfig
	logger          *logger.Logger
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	snapshotContext *snapshot.Context

	// Node version tracking
	nodeVersions map[string]string // nodeID -> version
	nodesMutex   sync.RWMutex

	// Registration tracking
	isRegistered bool
	regMutex     sync.RWMutex

	// Connection state tracking
	connectionState int32 // 0: disconnected, 1: connecting, 2: connected
	connectionMutex sync.RWMutex
	
	// Reconnect control
	reconnectEnabled bool
	reconnectMutex   sync.RWMutex
}

// Connection states
const (
	StateDisconnected = iota
	StateConnecting
	StateConnected
)

// NewControlPlaneManager creates a new registry manager for control-plane
func NewControlPlaneManager(config *ControlPlaneConfig, logger *logger.Logger, snapshotContext *snapshot.Context) (*ControlPlaneManager, error) {
	client, err := NewControlPlaneRegistryClient(config, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create registry client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	manager := &ControlPlaneManager{
		client:          client,
		config:          config,
		logger:          logger,
		ctx:             ctx,
		cancel:          cancel,
		snapshotContext: snapshotContext,
		nodeVersions:    make(map[string]string),
		isRegistered:    false,
		connectionState: StateDisconnected,
		reconnectEnabled: true,
	}

	return manager, nil
}

// Start starts the registry manager
func (m *ControlPlaneManager) Start() error {
	// Start initial connection and registration
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.initialConnect()
	}()

	// Start background tasks
	m.wg.Add(3)
	go m.continuousReconnectLoop()  // New: continuous reconnect
	go m.healthCheckLoop()
	go m.nodeListUpdateLoop()

	m.logger.Info("Registry manager started with continuous reconnect capability")
	return nil
}

// Stop stops the registry manager
func (m *ControlPlaneManager) Stop() error {
	// Disable reconnection
	m.setReconnectEnabled(false)
	
	m.cancel()
	m.wg.Wait()

	if err := m.client.Disconnect(); err != nil {
		return fmt.Errorf("failed to disconnect from registry: %v", err)
	}

	m.logger.Info("Registry manager stopped")
	return nil
}

// initialConnect performs the initial connection attempt
func (m *ControlPlaneManager) initialConnect() {
	m.logger.Infof("Starting initial registry connection...")
	
	if err := m.connectAndRegister(); err != nil {
		m.logger.Errorf("Initial connection failed: %v", err)
		m.setConnectionState(StateDisconnected)
	}
}

// continuousReconnectLoop provides continuous reconnection capability
func (m *ControlPlaneManager) continuousReconnectLoop() {
	defer m.wg.Done()

	m.logger.Infof("🔄 Continuous reconnect loop started")
	ticker := time.NewTicker(15 * time.Second) // Check every 15 seconds
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			m.logger.Infof("🔄 Continuous reconnect loop terminated")
			return
		case <-ticker.C:
			if !m.getReconnectEnabled() {
				continue
			}

			state := m.getConnectionState()
			if state == StateDisconnected {
				m.logger.Infof("🔄 Detected disconnected state, attempting reconnection...")
				go m.attemptReconnection()
			}
		}
	}
}

// attemptReconnection performs reconnection attempt
func (m *ControlPlaneManager) attemptReconnection() {
	if !m.setConnectionState(StateConnecting) {
		// Already connecting
		return
	}

	m.logger.Infof("🔄 Starting reconnection attempt...")
	
	if err := m.connectAndRegister(); err != nil {
		m.logger.Errorf("🔄 Reconnection failed: %v", err)
		m.setConnectionState(StateDisconnected)
	}
}

// connectAndRegister performs connection and registration with extended timeout
func (m *ControlPlaneManager) connectAndRegister() error {
	// Extended timeout for better reliability
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	m.logger.Infof("🔗 Attempting to connect to registry at %s...", m.config.RegistryAddress)

	// Disconnect first if needed
	if err := m.client.Disconnect(); err != nil {
		m.logger.Warnf("Error during disconnect: %v", err)
	}

	// Connect with retry - now includes real connectivity test
	if err := m.client.ConnectWithRetry(ctx); err != nil {
		m.logger.Errorf("❌ Registry connection failed: %v", err)
		return fmt.Errorf("failed to connect to registry: %v", err)
	}

	// Register with retry
	if err := m.client.RegisterControlPlaneWithRetry(ctx, m.config); err != nil {
		m.logger.Errorf("❌ Control-plane registration failed: %v", err)
		return fmt.Errorf("failed to register control-plane: %v", err)
	}
	
	// Update states
	m.setConnectionState(StateConnected)
	m.regMutex.Lock()
	m.isRegistered = true
	m.regMutex.Unlock()

	// Send empty node list to clean registry after reconnect
	m.logger.Infof("🧹 Sending empty node list to clean registry")
	if err := m.client.UpdateNodeList(m.config.ControlPlaneID, []ControlPlaneNodeInfo{}); err != nil {
		m.logger.Errorf("Failed to send empty node list: %v", err)
	} else {
		m.logger.Infof("✅ Registry cleaned for this control-plane")
	}

	// Sync existing nodes if any
	nodes := m.GetConnectedNodes()
	if len(nodes) > 0 {
		m.logger.Infof("🔄 Syncing %d existing nodes after reconnection", len(nodes))
		getAllNodes := func() []ControlPlaneNodeInfo {
			return m.GetAllNodes()
		}
		if err := m.client.SyncAllNodesWithRegistry(ctx, m.config.ControlPlaneID, getAllNodes); err != nil {
			m.logger.Errorf("Failed to sync existing nodes: %v", err)
		} else {
			m.logger.Infof("✅ Node sync completed successfully")
		}
	}

	return nil
}

// Connection state management
func (m *ControlPlaneManager) getConnectionState() int32 {
	m.connectionMutex.RLock()
	defer m.connectionMutex.RUnlock()
	return m.connectionState
}

func (m *ControlPlaneManager) setConnectionState(state int32) bool {
	m.connectionMutex.Lock()
	defer m.connectionMutex.Unlock()
	
	// Prevent multiple connecting states
	if state == StateConnecting && m.connectionState == StateConnecting {
		return false
	}
	
	m.connectionState = state
	return true
}

// Reconnect control
func (m *ControlPlaneManager) getReconnectEnabled() bool {
	m.reconnectMutex.RLock()
	defer m.reconnectMutex.RUnlock()
	return m.reconnectEnabled
}

func (m *ControlPlaneManager) setReconnectEnabled(enabled bool) {
	m.reconnectMutex.Lock()
	defer m.reconnectMutex.Unlock()
	m.reconnectEnabled = enabled
}

// NotifySnapshotDelivered notifies registry about snapshot delivery
func (m *ControlPlaneManager) NotifySnapshotDelivered(nodeID, version string) {
	m.logger.Infof("🔍 DEBUG: NotifySnapshotDelivered called for node: %s, version: %s", nodeID, version)
	
	// CRITICAL: Defense-in-depth validation - never allow empty nodeID to reach registry
	if nodeID == "" {
		m.logger.Errorf("🚨 CRITICAL: Attempted to notify registry with empty nodeID - blocking!")
		return
	}
	
	// Check connection state
	if m.getConnectionState() != StateConnected {
		m.logger.Warnf("Not connected to registry, skipping snapshot notification for node: %s", nodeID)
		return
	}
	
	// Store node version for future reference
	m.nodesMutex.Lock()
	m.nodeVersions[nodeID] = version
	m.nodesMutex.Unlock()

	m.logger.Infof("🔍 DEBUG: Node version stored, notifying registry")

	// Notify registry with retry logic
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := m.client.NotifySnapshotDeliveredWithRetry(ctx, m.config.ControlPlaneID, nodeID, version); err != nil {
			m.logger.Errorf("Failed to notify snapshot delivery: %v", err)
			// Mark as disconnected on error to trigger reconnection
			m.setConnectionState(StateDisconnected)
		}
	}()
}

// RemoveNode removes a node from version tracking
func (m *ControlPlaneManager) RemoveNode(nodeID string) {
	// CRITICAL: Validate nodeID before removal
	if nodeID == "" {
		m.logger.Errorf("🚨 CRITICAL: Attempted to remove node with empty nodeID - blocking!")
		return
	}

	m.nodesMutex.Lock()
	version, exists := m.nodeVersions[nodeID]
	delete(m.nodeVersions, nodeID)
	m.nodesMutex.Unlock()

	m.logger.Debugf("Removed node version tracking for: %s", nodeID)

	// Notify registry about node disconnection if we had the node
	if exists && version != "" {
		m.NotifyNodeDisconnected(nodeID, version)
	}
}

// NotifyNodeDisconnected notifies registry about node disconnection
func (m *ControlPlaneManager) NotifyNodeDisconnected(nodeID, version string) {
	m.logger.Infof("Notifying registry about node disconnection: %s (version: %s)", nodeID, version)
	
	// CRITICAL: Defense-in-depth validation
	if nodeID == "" {
		m.logger.Errorf("🚨 CRITICAL: Attempted to notify registry with empty nodeID - blocking!")
		return
	}
	
	// Check connection state
	if m.getConnectionState() != StateConnected {
		m.logger.Warnf("Not connected to registry, skipping node disconnection notification for node: %s", nodeID)
		return
	}

	// Notify registry with retry logic (similar to snapshot notification)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := m.client.NotifyNodeDisconnected(ctx, m.config.ControlPlaneID, nodeID, version); err != nil {
			m.logger.Errorf("Failed to notify node disconnection: %v", err)
			// Don't mark as disconnected for this error - it's less critical than snapshot delivery
		} else {
			m.logger.Infof("Successfully notified registry about node disconnection: %s", nodeID)
		}
	}()
}

// healthCheckLoop performs periodic health checks
func (m *ControlPlaneManager) healthCheckLoop() {
	defer m.wg.Done()

	m.logger.Infof("🔍 DEBUG: Health check loop started")
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			m.logger.Infof("🔍 DEBUG: Health check loop terminated by context")
			return
		case <-ticker.C:
			m.logger.Infof("🔍 DEBUG: Health check tick")
			
			// Check if we're connected
			if m.getConnectionState() != StateConnected {
				continue
			}

			// Check if we're registered
			m.regMutex.RLock()
			registered := m.isRegistered
			m.regMutex.RUnlock()

			if !registered {
				m.logger.Debugf("Control-plane not registered yet - skipping health check")
				continue
			}

			if err := m.client.HealthCheck(); err != nil {
				m.logger.Errorf("Health check failed: %v", err)
				m.handleConnectionFailure("health check")
			} else {
				m.logger.Debugf("Health check passed - registry connection healthy")
			}
		}
	}
}

// nodeListUpdateLoop updates the node list periodically
func (m *ControlPlaneManager) nodeListUpdateLoop() {
	defer m.wg.Done()

	m.logger.Infof("🔍 DEBUG: Node list update loop started")
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			m.logger.Infof("🔍 DEBUG: Node list update loop terminated by context")
			return
		case <-ticker.C:
			m.logger.Infof("🔍 DEBUG: Node list update tick")
			
			// Check if we're connected and registered
			if m.getConnectionState() != StateConnected {
				continue
			}

			m.regMutex.RLock()
			registered := m.isRegistered
			m.regMutex.RUnlock()

			if !registered {
				m.logger.Debugf("Control-plane not registered yet - skipping node list update")
				continue
			}

			// Get connected nodes
			nodes := m.GetConnectedNodes()
			m.logger.Infof("Periodic node list update: %d connected nodes", len(nodes))

			// CRITICAL FIX: Always send node list (even if empty) to keep control-plane alive in registry
			if err := m.client.UpdateNodeList(m.config.ControlPlaneID, nodes); err != nil {
				m.logger.Errorf("Failed to update node list: %v", err)
				m.handleConnectionFailure("node list update")
			} else {
				m.logger.Infof("✅ Node list update completed successfully: %d nodes", len(nodes))
			}
		}
	}
}

// handleConnectionFailure handles connection failures by marking disconnected
func (m *ControlPlaneManager) handleConnectionFailure(operation string) {
	m.logger.Warnf("Connection failure detected during %s - marking as disconnected", operation)
	
	// Reset states
	m.setConnectionState(StateDisconnected)
	m.regMutex.Lock()
	m.isRegistered = false
	m.regMutex.Unlock()
	
	// The continuous reconnect loop will handle reconnection
}

// GetAllNodes returns all nodes from snapshot context (both connected and disconnected)
func (m *ControlPlaneManager) GetAllNodes() []ControlPlaneNodeInfo {
	// Get all nodes from snapshot cache
	statusKeys := m.snapshotContext.Cache.Cache.GetStatusKeys()
	var nodes []ControlPlaneNodeInfo

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

			nodes = append(nodes, ControlPlaneNodeInfo{
				NodeID:   nodeID,
				Version:  version,
				LastSeen: lastWatchTime,
			})
		}
	}

	return nodes
}

// GetConnectedNodes returns the list of connected nodes from snapshot context
func (m *ControlPlaneManager) GetConnectedNodes() []ControlPlaneNodeInfo {
	connectedNodes := m.snapshotContext.GetConnectedNodes()

	m.nodesMutex.RLock()
	defer m.nodesMutex.RUnlock()

	// Convert to ControlPlaneNodeInfo slice
	var nodes []ControlPlaneNodeInfo
	for _, node := range connectedNodes {
		if node.Connected {
			// Use stored version from metadata, fallback to what's in snapshot context
			version := node.Version
			if storedVersion, exists := m.nodeVersions[node.NodeID]; exists {
				version = storedVersion
			}

			nodes = append(nodes, ControlPlaneNodeInfo{
				NodeID:   node.NodeID,
				Version:  version,
				LastSeen: node.LastSeen,
			})
		}
	}

	return nodes
}

// GetConnectedNodeCount returns the number of connected nodes
func (m *ControlPlaneManager) GetConnectedNodeCount() int {
	return len(m.GetConnectedNodes())
}

// GetAllNodeCount returns the total number of nodes (connected and disconnected)
func (m *ControlPlaneManager) GetAllNodeCount() int {
	return len(m.GetAllNodes())
} 