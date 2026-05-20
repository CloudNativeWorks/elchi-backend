package registry

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/control-plane/server/snapshot"
	appcfg "github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

type ControlPlaneManager struct {
	client          *ControlPlaneRegistryClient
	Config          *ControlPlaneConfig
	logger          *logger.Logger
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	snapshotContext *snapshot.Context

	// Node version tracking
	NodeVersions map[string]string // nodeID -> version
	NodesMutex   sync.RWMutex

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

// NewControlPlaneManager creates a new registry manager for control-plane.
// appConfig is forwarded to the registry client so it can decide on TLS vs
// plaintext for the registry dial (controlled by REGISTRY_TLS_ENABLED).
func NewControlPlaneManager(config *ControlPlaneConfig, logger *logger.Logger, snapshotContext *snapshot.Context, appConfig *appcfg.AppConfig) (*ControlPlaneManager, error) {
	client, err := NewControlPlaneRegistryClient(config, logger, appConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create registry client: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	manager := &ControlPlaneManager{
		client:           client,
		Config:           config,
		logger:           logger,
		ctx:              ctx,
		cancel:           cancel,
		snapshotContext:  snapshotContext,
		NodeVersions:     make(map[string]string),
		isRegistered:     false,
		connectionState:  StateDisconnected,
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
	go m.continuousReconnectLoop() // New: continuous reconnect
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
		return fmt.Errorf("failed to disconnect from registry: %w", err)
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

	m.logger.Infof("Continuous reconnect loop started")
	ticker := time.NewTicker(15 * time.Second) // Check every 15 seconds
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			m.logger.Infof("Continuous reconnect loop terminated")
			return
		case <-ticker.C:
			if !m.getReconnectEnabled() {
				continue
			}

			state := m.getConnectionState()
			if state == StateDisconnected {
				m.logger.Infof("Detected disconnected state, attempting reconnection...")
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

	m.logger.Infof("Starting reconnection attempt...")

	if err := m.connectAndRegister(); err != nil {
		m.logger.Errorf("Reconnection failed: %v", err)
		m.setConnectionState(StateDisconnected)
	}
}

// connectAndRegister performs connection and registration with extended timeout.
// Uses the same 2m bound as the controller-side ConnectAndRegister — see
// connectAndRegisterTimeout (controller.go) for the rationale.
func (m *ControlPlaneManager) connectAndRegister() error {
	ctx, cancel := context.WithTimeout(context.Background(), connectAndRegisterTimeout)
	defer cancel()

	m.logger.Infof("🔗 Attempting to connect to registry at %s...", m.Config.RegistryAddress)

	// Disconnect first if needed
	if err := m.client.Disconnect(); err != nil {
		m.logger.Warnf("Error during disconnect: %v", err)
	}

	// Connect with retry - now includes real connectivity test
	if err := m.client.ConnectWithRetry(ctx); err != nil {
		m.logger.Errorf("Registry connection failed: %v", err)
		return fmt.Errorf("failed to connect to registry: %w", err)
	}

	// CRITICAL: Explicit registration with version BEFORE any node list operations
	m.logger.Infof("Explicitly registering control-plane %s with version %s", m.Config.ControlPlaneID, m.Config.Version)
	if err := m.client.RegisterControlPlaneWithRetry(ctx, m.Config); err != nil {
		m.logger.Errorf("Control-plane registration failed: %v", err)
		return fmt.Errorf("failed to register control-plane: %w", err)
	}
	m.logger.Infof("Control-plane explicitly registered with version")

	// Update states
	m.setConnectionState(StateConnected)
	m.regMutex.Lock()
	m.isRegistered = true
	m.regMutex.Unlock()

	// Send empty node list ONLY AFTER successful registration
	// This prevents auto-registration attempts that cause EOF errors
	m.logger.Infof("Sending initial empty node list (control-plane is now registered)")
	if err := m.client.UpdateNodeList(m.Config.ControlPlaneID, []ControlPlaneNodeInfo{}, m.Config.Version); err != nil {
		m.logger.Errorf("Failed to send empty node list after registration: %v", err)
		// Don't fail here since registration succeeded
	} else {
		m.logger.Infof("Initial empty node list sent successfully")
	}

	// Sync existing nodes if any
	nodes := m.GetConnectedNodes()
	if len(nodes) > 0 {
		m.logger.Infof("Syncing %d existing nodes after reconnection", len(nodes))
		getAllNodes := func() []ControlPlaneNodeInfo {
			return m.GetAllNodes()
		}
		if err := m.client.SyncAllNodesWithRegistry(ctx, m.Config.ControlPlaneID, getAllNodes, m.Config.Version); err != nil {
			m.logger.Errorf("Failed to sync existing nodes: %v", err)
		} else {
			m.logger.Infof("Node sync completed successfully")
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
	m.logger.Infof("DEBUG: NotifySnapshotDelivered called for node: %s, version: %s", nodeID, version)

	// CRITICAL: Defense-in-depth validation - never allow empty nodeID to reach registry
	if nodeID == "" {
		m.logger.Errorf("CRITICAL: Attempted to notify registry with empty nodeID - blocking!")
		return
	}

	// Check connection state
	if m.getConnectionState() != StateConnected {
		m.logger.Warnf("Not connected to registry, skipping snapshot notification for node: %s", nodeID)
		return
	}

	// Store node version for future reference
	m.NodesMutex.Lock()
	m.NodeVersions[nodeID] = version
	m.NodesMutex.Unlock()

	m.logger.Infof("DEBUG: Node version stored, notifying registry")

	// Notify registry with retry logic
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := m.client.NotifySnapshotDeliveredWithRetry(ctx, m.Config.ControlPlaneID, nodeID, version); err != nil {
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
		m.logger.Errorf("CRITICAL: Attempted to remove node with empty nodeID - blocking!")
		return
	}

	m.NodesMutex.Lock()
	version, exists := m.NodeVersions[nodeID]
	delete(m.NodeVersions, nodeID)
	m.NodesMutex.Unlock()

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
		m.logger.Errorf("CRITICAL: Attempted to notify registry with empty nodeID - blocking!")
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

		if err := m.client.NotifyNodeDisconnected(ctx, m.Config.ControlPlaneID, nodeID, version); err != nil {
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

	m.logger.Infof("DEBUG: Health check loop started")
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			m.logger.Debugf("Health check loop terminated by context")
			return
		case <-ticker.C:
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
			}
		}
	}
}

// nodeListUpdateLoop updates the node list periodically
func (m *ControlPlaneManager) nodeListUpdateLoop() {
	defer m.wg.Done()

	m.logger.Infof("DEBUG: Node list update loop started")
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			m.logger.Debug("Node list update loop terminated by context")
			return
		case <-ticker.C:
			m.logger.Debug("Node list update tick")

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

			if err := m.client.UpdateNodeList(m.Config.ControlPlaneID, nodes, m.Config.Version); err != nil {
				m.logger.Errorf("Failed to update node list: %v", err)
				m.handleConnectionFailure("node list update")
			} else {
				m.logger.Infof("Node list update completed successfully: %d nodes", len(nodes))
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

	// NOTE: no NodesMutex here — resolveNodeVersion now manages its own
	// locking. Holding RLock around the call used to cause an RLock-held-
	// during-write race when the fallback path wrote NodeVersions.

	for _, nodeID := range statusKeys {
		status := m.snapshotContext.Cache.Cache.GetStatusInfo(nodeID)
		if status != nil {
			// ROBUST VERSION RESOLUTION for all nodes
			finalVersion := m.resolveNodeVersion(nodeID, false) // false = may not be connected

			// Get last watch time
			lastWatchTime := status.GetLastDeltaWatchRequestTime()
			if lastWatchTime.IsZero() {
				lastWatchTime = time.Now()
			}

			nodes = append(nodes, ControlPlaneNodeInfo{
				NodeID:   nodeID,
				Version:  finalVersion,
				LastSeen: lastWatchTime,
			})
		}
	}

	return nodes
}

// resolveNodeVersion provides robust version resolution for nodes with
// context-aware logging. Holds its own locking — callers MUST NOT hold
// NodesMutex when invoking this. Earlier versions read+wrote the map
// while callers (GetAllNodes/GetConnectedNodes) held an RLock; that
// race-traps the Go race detector and risks map corruption on a
// missed-version fallback. The pattern below: bounded RLock for the
// read, then a separate Lock for the cache write so no upgrade is ever
// attempted on the same RWMutex (sync.RWMutex is not re-entrant).
func (m *ControlPlaneManager) resolveNodeVersion(nodeID string, isConnected bool) string {
	// 1. Primary: Use stored version from NotifySnapshotDelivered (most reliable)
	m.NodesMutex.RLock()
	storedVersion, exists := m.NodeVersions[nodeID]
	m.NodesMutex.RUnlock()
	if exists && storedVersion != "" {
		return storedVersion
	}

	// 2. Fallback 1: Use control-plane version as last resort
	if m.Config != nil && m.Config.Version != "" {
		// Use different log levels based on whether the node is connected
		if isConnected {
			// Connected nodes should have version, so this is more critical
			m.logger.Errorf("CRITICAL: No reliable version for connected node %s, falling back to control-plane version: %s", nodeID, m.Config.Version)
		} else {
			// Disconnected nodes might not have version, less critical
			m.logger.Warnf("No reliable version for node %s, falling back to control-plane version: %s", nodeID, m.Config.Version)
		}
		// Store fallback version — separate Lock cycle, no RLock→Lock upgrade.
		m.NodesMutex.Lock()
		m.NodeVersions[nodeID] = m.Config.Version
		m.NodesMutex.Unlock()
		return m.Config.Version
	}

	// 3. Absolute fallback: Use unknown but warn heavily
	m.logger.Errorf("FATAL: No version available anywhere for node %s, using 'unknown' - THIS SHOULD NEVER HAPPEN!", nodeID)
	return "unknown"
}

// GetConnectedNodes returns the list of connected nodes from snapshot context
func (m *ControlPlaneManager) GetConnectedNodes() []ControlPlaneNodeInfo {
	connectedNodes := m.snapshotContext.GetConnectedNodes()

	// NOTE: no NodesMutex here — see GetAllNodes for rationale. The
	// underlying snapshotContext provides its own synchronization, and
	// resolveNodeVersion handles NodeVersions locking internally.

	// Convert to ControlPlaneNodeInfo slice
	var nodes []ControlPlaneNodeInfo
	for _, node := range connectedNodes {
		if node.Connected {
			// ROBUST VERSION RESOLUTION with prioritized fallbacks
			finalVersion := m.resolveNodeVersion(node.NodeID, true) // true = connected node

			nodes = append(nodes, ControlPlaneNodeInfo{
				NodeID:   node.NodeID,
				Version:  finalVersion,
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
