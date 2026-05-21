package registry

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/version"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type RegistryClient struct {
	conn *grpc.ClientConn
	// controllerClient is the gRPC stub. Wrapped in atomic.Pointer so it
	// can be swapped during reconnect without racing with concurrent RPC
	// callers. Interfaces in Go are two-word values; assigning one across
	// goroutines without synchronization can publish a half-written value
	// (type word from the new stub, data word from the old) — race detector
	// catches it, and on rare occasions the half-published value segfaults.
	// atomic.Pointer[T] guarantees a single tagged-pointer load/store so
	// RPC callers see either the old stub OR the new stub, never a torn
	// mix. Helper getControllerClient() centralises Load + nil-check.
	controllerClient atomic.Pointer[bridge.ControllerRoutingServiceClient]
	controllerID     string
	version          string
	grpcAddress      string
	registryAddr     string
	appConfig        *config.AppConfig // retained for TLS dial decisions on (re)connect
	logger           *logger.Logger

	// Connection state tracking
	connectionState int32 // 0: disconnected, 1: connecting, 2: connected
	connectionMutex sync.RWMutex

	// Reconnect control
	reconnectEnabled bool
	reconnectMutex   sync.RWMutex
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup

	// Registration tracking
	isRegistered bool
	regMutex     sync.RWMutex

	// getConnectedClients snapshots the live set of connected client IDs.
	// Set once by StartHealthMonitor; consumed by clientListUpdateLoop's
	// periodic tick AND by ConnectAndRegister's post-register state push.
	// Without the post-register push the controller's view in the registry
	// stays stale for up to 30s after every reconnect — same gap the
	// control-plane manager already closes by sending an initial empty
	// node list + SyncAllNodesWithRegistry right after re-registration.
	getConnectedClients func() []string
	getClientsMu        sync.RWMutex
}

type Config struct {
	RegistryAddress string
	Version         string
}

// Connection states
const (
	ControllerStateDisconnected = iota
	ControllerStateConnecting
	ControllerStateConnected
)

// NewRegistryClient creates a new registry client with default config
func NewRegistryClient(registryAddress string, logger *logger.Logger, appConfig *config.AppConfig) (*RegistryClient, error) {
	return NewRegistryClientWithConfig(&Config{
		RegistryAddress: registryAddress,
		Version:         version.GetVersion(),
	}, logger, appConfig)
}

// NewRegistryClientWithConfig creates a new registry client with custom config
func NewRegistryClientWithConfig(config *Config, logger *logger.Logger, appConfig *config.AppConfig) (*RegistryClient, error) {
	controllerID := ResolveControllerID(appConfig)

	ctx, cancel := context.WithCancel(context.Background())

	client := &RegistryClient{
		controllerID:     controllerID,
		version:          config.Version,
		grpcAddress:      ResolveControllerHTTPAddress(controllerID, appConfig.ControllerPort, appConfig.ElchiNamespace),
		registryAddr:     config.RegistryAddress,
		appConfig:        appConfig,
		logger:           logger,
		connectionState:  ControllerStateDisconnected,
		reconnectEnabled: true,
		ctx:              ctx,
		cancel:           cancel,
		isRegistered:     false,
	}

	return client, nil
}


// Connect establishes gRPC connection to registry.
//
// Pairs with Disconnect: connectionMutex guards the conn pointer; the
// controllerClient stub is published atomically via atomic.Pointer so
// concurrent RPC callers see either the old stub or the new stub but
// never a half-written interface value. setConnectionState is called
// OUTSIDE the lock to avoid re-entering the same mutex (RWMutex isn't
// re-entrant in Go).
func (r *RegistryClient) Connect() error {
	// Use shared gRPC dial options for consistency
	conn, err := grpc.NewClient(r.registryAddr, GetDefaultGRPCDialOptions(r.appConfig)...)
	if err != nil {
		return fmt.Errorf("failed to create connection: %w", err)
	}

	stub := bridge.NewControllerRoutingServiceClient(conn)
	r.connectionMutex.Lock()
	r.conn = conn
	r.controllerClient.Store(&stub)
	r.connectionMutex.Unlock()

	r.setConnectionState(ControllerStateConnected)
	return nil
}

// getControllerClient returns the current gRPC stub or nil if no
// connection has been established. Callers must nil-check the returned
// value; an atomic.Pointer load that returns nil means the conn was
// either never set or Disconnect just cleared it.
func (r *RegistryClient) getControllerClient() bridge.ControllerRoutingServiceClient {
	p := r.controllerClient.Load()
	if p == nil {
		return nil
	}
	return *p
}

// Disconnect closes the gRPC connection.
//
// Holds connectionMutex + nil-checks the conn before closing so a race
// between continuousReconnectLoop calling Disconnect and a peer goroutine
// reaching for r.conn cannot cause a double-close. After Close we also
// nil out r.conn so a subsequent Disconnect is a no-op.
func (r *RegistryClient) Disconnect() error {
	r.connectionMutex.Lock()
	defer r.connectionMutex.Unlock()
	if r.conn == nil {
		return nil
	}
	err := r.conn.Close()
	r.conn = nil
	// Clear the stub so getControllerClient returns nil on next read.
	// In-flight RPCs that already loaded the stub will fail on the
	// now-closed conn with codes.Unavailable, which the caller treats
	// as a reconnect trigger — same recovery path as before.
	r.controllerClient.Store(nil)
	return err
}

// Connection state management
func (r *RegistryClient) getConnectionState() int32 {
	r.connectionMutex.RLock()
	defer r.connectionMutex.RUnlock()
	return r.connectionState
}

func (r *RegistryClient) setConnectionState(state int32) bool {
	r.connectionMutex.Lock()
	defer r.connectionMutex.Unlock()

	// Prevent multiple connecting states
	if state == ControllerStateConnecting && r.connectionState == ControllerStateConnecting {
		return false
	}

	r.connectionState = state
	return true
}

// Reconnect control
func (r *RegistryClient) getReconnectEnabled() bool {
	r.reconnectMutex.RLock()
	defer r.reconnectMutex.RUnlock()
	return r.reconnectEnabled
}

func (r *RegistryClient) setReconnectEnabled(enabled bool) {
	r.reconnectMutex.Lock()
	defer r.reconnectMutex.Unlock()
	r.reconnectEnabled = enabled
}

// Registration state management
func (r *RegistryClient) isRegisteredState() bool {
	r.regMutex.RLock()
	defer r.regMutex.RUnlock()
	return r.isRegistered
}

func (r *RegistryClient) setRegistered(registered bool) {
	r.regMutex.Lock()
	defer r.regMutex.Unlock()
	r.isRegistered = registered
}

// RegisterController registers this controller with registry
func (r *RegistryClient) RegisterController() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := &bridge.RegisterControllerRequest{
		ControllerId: r.controllerID,
		Version:      r.version,
		Timestamp:    timestamppb.New(time.Now()),
	}

	client := r.getControllerClient()
	if client == nil {
		return fmt.Errorf("registry client not connected")
	}
	resp, err := client.RegisterController(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to register controller: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("controller registration failed: %s", resp.Message)
	}

	r.setRegistered(true)
	r.logger.Infof("Controller registered: ID=%s, Version=%s, Address=%s", r.controllerID, r.version, r.grpcAddress)
	return nil
}

// NotifyClientConnected notifies registry about client connection
func (r *RegistryClient) NotifyClientConnected(clientID string) error {
	// Check connection state
	if r.getConnectionState() != ControllerStateConnected {
		r.logger.Warnf("Not connected to registry, skipping client connected notification for: %s", clientID)
		return fmt.Errorf("not connected to registry")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req := &bridge.NotifyClientConnectedRequest{
		ControllerId: r.controllerID,
		ClientId:     clientID,
		Version:      r.version,
		Timestamp:    timestamppb.New(time.Now()),
	}

	client := r.getControllerClient()
	if client == nil {
		return fmt.Errorf("registry client not connected")
	}
	resp, err := client.NotifyClientConnected(ctx, req)
	if err != nil {
		// Mark as disconnected on error to trigger reconnection
		r.setConnectionState(ControllerStateDisconnected)
		return fmt.Errorf("failed to notify client connected: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("notify client connected failed: %s", resp.Message)
	}

	r.logger.Debugf("Client connected notification sent: %s -> %s (version: %s)", clientID, r.controllerID, r.version)
	return nil
}

// NotifyClientDisconnected notifies registry about client disconnection
func (r *RegistryClient) NotifyClientDisconnected(clientID string) error {
	// Check connection state
	if r.getConnectionState() != ControllerStateConnected {
		r.logger.Warnf("Not connected to registry, skipping client disconnected notification for: %s", clientID)
		return fmt.Errorf("not connected to registry")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req := &bridge.NotifyClientDisconnectedRequest{
		ControllerId: r.controllerID,
		ClientId:     clientID,
		Version:      r.version,
		Timestamp:    timestamppb.New(time.Now()),
	}

	client := r.getControllerClient()
	if client == nil {
		return fmt.Errorf("registry client not connected")
	}
	resp, err := client.NotifyClientDisconnected(ctx, req)
	if err != nil {
		// Mark as disconnected on error to trigger reconnection
		r.setConnectionState(ControllerStateDisconnected)
		return fmt.Errorf("failed to notify client disconnected: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("notify client disconnected failed: %s", resp.Message)
	}

	r.logger.Debugf("Client disconnected notification sent: %s -> %s (version: %s)", clientID, r.controllerID, r.version)
	return nil
}

// UpdateClientList reports all currently connected clients
func (r *RegistryClient) UpdateClientList(clientIDs []string) error {
	r.logger.Debugf("Updating client list for controller %s: %d clients", r.controllerID, len(clientIDs))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Convert to ClientInfo array
	var clients []*bridge.ClientInfo
	for _, clientID := range clientIDs {
		clients = append(clients, &bridge.ClientInfo{
			ClientId: clientID,
			Version:  r.version,
			LastSeen: timestamppb.New(time.Now()),
		})
	}

	req := &bridge.UpdateClientListRequest{
		ControllerId: r.controllerID,
		Clients:      clients,
		Timestamp:    timestamppb.New(time.Now()),
	}

	client := r.getControllerClient()
	if client == nil {
		return fmt.Errorf("registry client not connected")
	}
	resp, err := client.UpdateClientList(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to update client list: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("update client list failed: %s", resp.Message)
	}

	r.logger.Debugf("Successfully updated client list: %d clients processed", resp.UpdatedCount)
	return nil
}

// GetClientLocation finds which controller a client is connected to
func (r *RegistryClient) GetClientLocation(clientID string) (*bridge.GetControllerClusterResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r.logger.Infof("Getting client location from registry: clientID=%s (searching all versions)", clientID)

	req := &bridge.GetControllerClusterRequest{
		ClientId:  clientID,
		Version:   "", // Empty version to search across all versions
		Timestamp: timestamppb.New(time.Now()),
	}

	client := r.getControllerClient()
	if client == nil {
		return nil, fmt.Errorf("registry client not connected")
	}
	resp, err := client.GetControllerCluster(ctx, req)
	if err != nil {
		r.logger.Errorf("Registry GetControllerCluster failed: %v", err)
		return nil, fmt.Errorf("failed to get client location: %w", err)
	}

	r.logger.Infof("Registry response: Found=%v, ControllerId=%s", resp.Found, resp.ControllerId)

	if !resp.Found {
		return nil, fmt.Errorf("client location not found: %s (searched all versions)", clientID)
	}

	return resp, nil
}

// GetRegistryData returns all registry data via gRPC
func (r *RegistryClient) GetRegistryData(ctx context.Context) (map[string]any, error) {
	// Snapshot the conn pointer under the mutex so a concurrent
	// Disconnect/Connect cycle cannot turn `r.conn` into nil between the
	// check and the NewClient calls below. Without this snapshot the read
	// races with Disconnect's `r.conn = nil` write — best case stale stub,
	// worst case nil-deref panic.
	r.connectionMutex.RLock()
	conn := r.conn
	r.connectionMutex.RUnlock()
	if conn == nil {
		return nil, fmt.Errorf("registry connection not available")
	}

	// Get controller data from controller routing service
	controllerClient := bridge.NewControllerRoutingServiceClient(conn)

	// Get control plane data from control plane routing service
	controlPlaneClient := bridge.NewEnvoyRoutingServiceClient(conn)

	// Get all controller registry data
	controllerDataReq := &bridge.GetAllControllerRegistryDataRequest{}
	controllerResp, err := controllerClient.GetAllRegistryData(ctx, controllerDataReq)
	if err != nil {
		r.logger.Errorf("Failed to get controller registry data: %v", err)
		return nil, fmt.Errorf("failed to get controller registry data: %w", err)
	}

	// Get all control plane registry data
	controlPlaneDataReq := &bridge.GetAllRegistryDataRequest{}
	controlPlaneResp, err := controlPlaneClient.GetAllRegistryData(ctx, controlPlaneDataReq)
	if err != nil {
		r.logger.Errorf("Failed to get control plane registry data: %v", err)
		return nil, fmt.Errorf("failed to get control plane registry data: %w", err)
	}

	// Combine all data
	registryData := map[string]any{
		"message": "Full registry data retrieved successfully",
		"status":  "connected",
		"controller_data": map[string]any{
			"controllers":           controllerResp.Data.Controllers,
			"clients_by_controller": controllerResp.Data.ClientsByController,
		},
		"control_plane_data": map[string]any{
			"control_planes":         controlPlaneResp.Data.ControlPlanes,
			"nodes_by_control_plane": controlPlaneResp.Data.NodesByControlPlane,
		},
		"client_info": map[string]any{
			"controller_id": r.controllerID,
			"version":       r.version,
			"grpc_address":  r.grpcAddress,
		},
		"registry_address": r.registryAddr,
		"timestamp":        time.Now().UTC(),
	}

	r.logger.Infof("Retrieved full registry data: %d controllers, %d control planes",
		len(controllerResp.Data.Controllers),
		len(controlPlaneResp.Data.ControlPlanes))

	return registryData, nil
}

// GetControllerID returns the controller ID
func (r *RegistryClient) GetControllerID() string {
	r.logger.Debugf("Getting controller ID: %s", r.controllerID)
	return r.controllerID
}

// DeleteController deletes a controller from registry
func (r *RegistryClient) DeleteController(controllerID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := &bridge.DeleteControllerRequest{
		ControllerId: controllerID,
	}

	client := r.getControllerClient()
	if client == nil {
		return fmt.Errorf("registry client not connected")
	}
	resp, err := client.DeleteController(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to call DeleteController: %w", err)
	}

	if !resp.GetSuccess() {
		return fmt.Errorf("registry delete controller failed: %s", resp.GetMessage())
	}

	r.logger.Infof("Controller %s deleted from registry successfully", controllerID)
	return nil
}

// DeleteControlPlane deletes a control plane from registry
func (r *RegistryClient) DeleteControlPlane(controlPlaneID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Snapshot the conn under the mutex (see GetRegistryData for rationale).
	r.connectionMutex.RLock()
	conn := r.conn
	r.connectionMutex.RUnlock()
	if conn == nil {
		return fmt.Errorf("registry connection not available")
	}

	// Use control-plane routing service client
	controlPlaneClient := bridge.NewEnvoyRoutingServiceClient(conn)

	req := &bridge.DeleteControlPlaneRequest{
		ControlPlaneId: controlPlaneID,
	}

	resp, err := controlPlaneClient.DeleteControlPlane(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to call DeleteControlPlane: %w", err)
	}

	if !resp.GetSuccess() {
		return fmt.Errorf("registry delete control plane failed: %s", resp.GetMessage())
	}

	r.logger.Infof("Control plane %s deleted from registry successfully", controlPlaneID)
	return nil
}

// IsConnected checks if the gRPC connection is established
func (r *RegistryClient) IsConnected() bool {
	r.connectionMutex.RLock()
	connOK := r.conn != nil
	r.connectionMutex.RUnlock()
	return connOK && r.controllerClient.Load() != nil
}

// StartHealthMonitor starts enhanced health monitoring with continuous reconnect capability
func (r *RegistryClient) StartHealthMonitor(getConnectedClients func() []string) {
	// Stash the snapshot fn so ConnectAndRegister can push state on every
	// reconnect — closing the asymmetry with the control-plane manager
	// which already sends an initial node list right after re-register.
	r.getClientsMu.Lock()
	r.getConnectedClients = getConnectedClients
	r.getClientsMu.Unlock()

	// Start continuous reconnect loop
	r.wg.Add(1)
	go r.continuousReconnectLoop()

	// Start client list update loop
	r.wg.Add(1)
	go r.clientListUpdateLoop(getConnectedClients)

	r.logger.Infof("Enhanced health monitor started with continuous reconnect capability")
}

// snapshotConnectedClients returns the live client-id set if a provider
// has been installed (via StartHealthMonitor) or nil otherwise. Nil is
// the cue for ConnectAndRegister to skip the post-register push — the
// very first ConnectAndRegister at boot runs BEFORE StartHealthMonitor
// is wired up, and pushing an empty list there would wipe the (empty
// anyway) registry view without buying anything.
func (r *RegistryClient) snapshotConnectedClients() []string {
	r.getClientsMu.RLock()
	defer r.getClientsMu.RUnlock()
	if r.getConnectedClients == nil {
		return nil
	}
	return r.getConnectedClients()
}

// continuousReconnectLoop provides continuous reconnection capability
func (r *RegistryClient) continuousReconnectLoop() {
	defer r.wg.Done()

	r.logger.Infof("Controller continuous reconnect loop started")
	ticker := time.NewTicker(15 * time.Second) // Check every 15 seconds
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			r.logger.Infof("Controller continuous reconnect loop terminated")
			return
		case <-ticker.C:
			if !r.getReconnectEnabled() {
				continue
			}

			state := r.getConnectionState()
			if state == ControllerStateDisconnected {
				r.logger.Infof("Detected disconnected state, attempting reconnection...")
				go r.attemptReconnection()
			}
		}
	}
}

// attemptReconnection performs reconnection attempt
func (r *RegistryClient) attemptReconnection() {
	if !r.setConnectionState(ControllerStateConnecting) {
		// Already connecting
		return
	}

	r.logger.Infof("Starting controller reconnection attempt...")

	if err := r.ConnectAndRegister(); err != nil {
		r.logger.Errorf("Controller reconnection failed: %v", err)
		r.setConnectionState(ControllerStateDisconnected)
	}
}

// connectAndRegisterTimeout bounds a single connect+register attempt.
// Lowered from 5m to 2m: at 5m a network partition would keep the client
// in StateConnecting for the full window, and attemptReconnection's
// "already connecting" guard would silently block any new retry — so a
// transient partition could hold the controller out of the registry for
// minutes after the partition healed. 2m is long enough to absorb a
// Mongo PRIMARY election + retry sequence, short enough that recovery
// kicks in promptly (~135s worst case: 120s timeout + 15s reconnect
// loop tick).
const connectAndRegisterTimeout = 2 * time.Minute

// ConnectAndRegister performs connection and registration with extended timeout
func (r *RegistryClient) ConnectAndRegister() error {
	ctx, cancel := context.WithTimeout(context.Background(), connectAndRegisterTimeout)
	defer cancel()

	r.logger.Infof("Attempting to connect controller to registry at %s...", r.registryAddr)

	// Disconnect first if needed
	if err := r.Disconnect(); err != nil {
		r.logger.Warnf("Error during disconnect: %v", err)
	}

	// Connect with retry - now includes real connectivity test
	if err := r.ConnectWithRetry(ctx); err != nil {
		// Force Disconnected so continuousReconnectLoop will retry. Connect()
		// flips state to Connected on a successful (lazy) gRPC dial; if a
		// later step fails we MUST roll it back, otherwise the reconnect loop
		// sees Connected and never re-attempts — the controller stays wedged
		// out of the registry until the pod is recreated.
		r.setConnectionState(ControllerStateDisconnected)
		r.logger.Errorf("Controller registry connection failed: %v", err)
		return fmt.Errorf("failed to connect to registry: %w", err)
	}

	r.logger.Infof("Controller registry connection established successfully")

	// Register with retry
	if err := r.RegisterControllerWithRetry(ctx); err != nil {
		// CRITICAL: Connect() above already set state to Connected (gRPC dial
		// is lazy and "succeeds" before the real TCP connect). If registration
		// then fails — e.g. transient network EPERM during pod startup before
		// the CNI is ready, or "not the active leader" during a failover — we
		// must roll state back to Disconnected. Otherwise continuousReconnect-
		// Loop only fires on Disconnected and would never retry, leaving the
		// controller permanently unregistered (observed: 14 attempts → 2m
		// timeout → silent give-up until manual pod delete).
		r.setConnectionState(ControllerStateDisconnected)
		r.logger.Errorf("Controller registration failed: %v", err)
		return fmt.Errorf("failed to register controller: %w", err)
	}

	r.logger.Infof("Controller registered successfully with registry")

	// Push live client list to the registry immediately after a successful
	// (re)registration so its view is fresh without waiting for the 30s
	// periodic tick. Mirrors what ControlPlaneManager already does post-
	// register (control_plane_manager.go:196). Skip if no snapshot fn is
	// installed yet — that is the very first boot-time call, before
	// StartHealthMonitor wires the provider up.
	connectedClients := r.snapshotConnectedClients()
	if connectedClients != nil {
		r.logger.Infof("Pushing initial client list after registration: %d clients", len(connectedClients))
		if err := r.UpdateClientList(connectedClients); err != nil {
			// Soft failure: registration itself succeeded, the periodic
			// loop will retry on the next tick. Log + continue.
			r.logger.Warnf("Initial client list push failed after registration: %v", err)
		} else {
			r.logger.Infof("Initial client list push completed: %d clients", len(connectedClients))
		}
	}
	return nil
}

// clientListUpdateLoop handles periodic client list updates
func (r *RegistryClient) clientListUpdateLoop(getConnectedClients func() []string) {
	defer r.wg.Done()

	r.logger.Infof("Controller client list update loop started")
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			r.logger.Infof("Controller client list update loop terminated")
			return
		case <-ticker.C:
			// Check if we're connected and registered
			if r.getConnectionState() != ControllerStateConnected {
				continue
			}

			if !r.isRegisteredState() {
				r.logger.Debugf("Controller not registered yet - skipping client list update")
				continue
			}

			// Get currently connected clients
			connectedClients := getConnectedClients()
			r.logger.Debugf("Periodic client list update: %d connected clients", len(connectedClients))

			// Always send client list (even if empty) to indicate controller is alive
			if err := r.UpdateClientList(connectedClients); err != nil {
				r.logger.Errorf("Failed to update client list: %v", err)
				r.handleConnectionFailure("client list update")
			} else {
				r.logger.Debugf("Client list update completed successfully: %d clients", len(connectedClients))
			}
		}
	}
}

// handleConnectionFailure handles connection failures by marking disconnected
func (r *RegistryClient) handleConnectionFailure(operation string) {
	r.logger.Warnf("Connection failure detected during %s - marking as disconnected", operation)

	// Reset states
	r.setConnectionState(ControllerStateDisconnected)
	r.setRegistered(false)

	// The continuous reconnect loop will handle reconnection
}

// Stop stops the health monitor gracefully
func (r *RegistryClient) Stop() {
	r.setReconnectEnabled(false)
	r.cancel()
	r.wg.Wait()
	if err := r.Disconnect(); err != nil {
		r.logger.Errorf("Failed to disconnect from registry: %v", err)
	}
	r.logger.Infof("Controller registry client stopped")
}

// ConnectWithRetry establishes gRPC connection to registry with retry logic
func (r *RegistryClient) ConnectWithRetry(ctx context.Context) error {
	return RetryWithBackoff(ctx, "connect to registry", AggressiveRetryConfig(), r.logger, func() error {
		return r.Connect()
	})
}

// RegisterControllerWithRetry registers controller with retry logic
func (r *RegistryClient) RegisterControllerWithRetry(ctx context.Context) error {
	return RetryWithBackoff(ctx, "register controller", AggressiveRetryConfig(), r.logger, func() error {
		return r.RegisterController()
	})
}

// SyncAllClientsWithRegistry syncs all existing clients with registry
func (r *RegistryClient) SyncAllClientsWithRegistry(ctx context.Context, getAllClients func() []string) error {
	clients := getAllClients()
	if len(clients) == 0 {
		r.logger.Infof("No clients to sync with registry")
		return nil
	}

	r.logger.Infof("Starting sync of %d existing clients with registry", len(clients))

	// Log each client to be synced
	for i, clientID := range clients {
		r.logger.Infof("Client %d/%d: %s (version: %s)", i+1, len(clients), clientID, r.version)
	}

	// First, update the client list
	if err := r.UpdateClientList(clients); err != nil {
		r.logger.Errorf("Failed to update client list during sync: %v", err)
		return err
	}

	// Then, notify about each client connection
	successCount := 0
	for i, clientID := range clients {
		r.logger.Infof("Syncing client %d/%d: %s", i+1, len(clients), clientID)

		if err := r.NotifyClientConnected(clientID); err != nil {
			r.logger.Errorf("Failed to sync client notification for client %s: %v", clientID, err)
		} else {
			r.logger.Infof("Successfully synced client: %s", clientID)
			successCount++
		}
	}

	r.logger.Infof("Sync completed: %d/%d clients successfully synced with registry", successCount, len(clients))
	return nil
}
