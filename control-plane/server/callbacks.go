package server

import (
	"context"
	"errors"
	"sync"

	core "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/core/v3"
	discovery "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/service/discovery/v3"

	"github.com/CloudNativeWorks/elchi-backend/control-plane/envoys"
	"github.com/CloudNativeWorks/elchi-backend/control-plane/server/bridge"
	"github.com/CloudNativeWorks/elchi-backend/control-plane/server/snapshot"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/registry"
)

type Callbacks struct {
	poke             *bridge.PokeService
	mu               sync.RWMutex
	cache            *snapshot.Context
	envoyConnTracker *envoys.EnvoyConnTracker
	appContext       *db.AppContext
	logger           *logger.Logger
	routingManager   *registry.ControlPlaneManager
}

func NewCallbacks(pokeService *bridge.PokeService, cache *snapshot.Context, appContext *db.AppContext, envoyConnTracker *envoys.EnvoyConnTracker, routingManager *registry.ControlPlaneManager) *Callbacks {
	return &Callbacks{
		poke:             pokeService,
		cache:            cache,
		envoyConnTracker: envoyConnTracker,
		appContext:       appContext,
		logger:           logger.NewLogger("control-plane/callbacks"),
		routingManager:   routingManager,
	}
}

func (c *Callbacks) OnFetchResponse(*discovery.DiscoveryRequest, *discovery.DiscoveryResponse) {}

func (c *Callbacks) OnStreamRequest(_ int64, _ *discovery.DiscoveryRequest) error {
	return nil
}

func (c *Callbacks) OnStreamResponse(_ context.Context, _ int64, _ *discovery.DiscoveryRequest, _ *discovery.DiscoveryResponse) {
}

func (c *Callbacks) OnFetchRequest(_ context.Context, _ *discovery.DiscoveryRequest) error {
	return nil
}

func (c *Callbacks) OnStreamOpen(_ context.Context, _ int64, _ string) error {
	return nil
}

func (c *Callbacks) OnStreamClosed(_ int64, _ *core.Node) {
}

// getValidVersionForNode provides robust version validation and fallbacks
func (c *Callbacks) getValidVersionForNode(nodeID, metadataVersion string) string {
	// 1. Primary: Use metadata version if present (this should be the case since you set it)
	if metadataVersion != "" {
		c.logger.Debugf("Using metadata version for node %s: %s", nodeID, metadataVersion)
		return metadataVersion
	}

	// 2. Fallback 1: Try to get version from nodeVersions map in routing manager
	if c.routingManager != nil {
		c.routingManager.NodesMutex.RLock()
		if storedVersion, exists := c.routingManager.NodeVersions[nodeID]; exists && storedVersion != "" {
			c.routingManager.NodesMutex.RUnlock()
			c.logger.Warnf("Metadata version missing for node %s, using stored version: %s", nodeID, storedVersion)
			return storedVersion
		}
		c.routingManager.NodesMutex.RUnlock()
	}

	// 3. Last Resort: Use control-plane version (should never happen if you set metadata correctly)
	if c.routingManager != nil && c.routingManager.Config != nil && c.routingManager.Config.Version != "" {
		c.logger.Errorf("🚨 CRITICAL: No version found anywhere for node %s, falling back to control-plane version: %s", nodeID, c.routingManager.Config.Version)
		return c.routingManager.Config.Version
	}

	// 4. Absolute Fallback: Use a default version
	c.logger.Errorf("🚨 FATAL: No version available anywhere for node %s, using default 'unknown'", nodeID)
	return "unknown"
}

func (c *Callbacks) OnDeltaStreamOpen(ctx context.Context, id int64, typ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.logger.Infof("DEBUG: OnDeltaStreamOpen called (stream ID: %d, type: %s)", id, typ)

	address, nodeID, version, downstreamAddress, clientName, clientID := GetMetadata(ctx, c.logger)

	// CRITICAL: Validate nodeID FIRST before any registry operations
	if nodeID == "" {
		c.logger.Warn("NodeID missing from metadata - skipping all operations")
		return errors.New("nodeID missing from metadata")
	}

	// ROBUST: Validate nodeID format using existing GetNodeIDParts function
	name, project, _ := envoys.GetNodeIDParts(nodeID)
	if name == "" || project == "" {
		c.logger.Warnf("Invalid nodeID format detected: '%s' (name='%s', project='%s') - skipping registry operations", nodeID, name, project)
		return errors.New("invalid nodeID format")
	}

	finalVersion := c.getValidVersionForNode(nodeID, version)
	c.logger.Infof("DEBUG: Node metadata - NodeID: %s, Metadata Version: '%s', Final Version: '%s'", nodeID, version, finalVersion)

	// Check if routing manager is set
	if c.routingManager == nil {
		c.logger.Errorf("🚨 CRITICAL: Routing manager is NULL in callbacks!")
	} else {
		c.logger.Infof("DEBUG: Routing manager is properly set, calling NotifySnapshotDelivered")

		// Now safe to notify registry (nodeID is validated) with robust version
		c.routingManager.NotifySnapshotDelivered(nodeID, finalVersion)
		c.logger.Infof("Notified routing manager about snapshot delivery for node: %s (metadata_version: '%s', final_version: '%s')", nodeID, version, finalVersion)
	}

	if err := c.CheckSetSnapshot(nodeID, finalVersion, false); err != nil { // false: control-plane restart scenario
		c.logger.Warnf("Error checking snapshot: %v", err)
		return err
	}

	c.envoyConnTracker.TrackClientUp(c.appContext.Client, nodeID, address, version, downstreamAddress, clientName, clientID, id, c.logger)
	c.logger.Infof("Delta stream %d opened for NodeID %s", id, nodeID)
	return nil
}

func (c *Callbacks) OnDeltaStreamClosed(id int64, node *core.Node) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if node == nil || node.Id == "" {
		c.logger.Warn("NodeID missing, skipping client cleanup")
		return
	}

	// Remove node from routing manager's version tracking
	if c.routingManager != nil {
		c.routingManager.RemoveNode(node.Id)
	}

	// Node will be removed from tracking when it's not seen for a while
	// (handled by the periodic UpdateNodeList which only sends active nodes)

	c.envoyConnTracker.TrackClientDown(c.appContext.Client, c.cache.Cache.Cache, node.Id, id, c.logger)
	c.logger.Infof("Delta stream %d closed for NodeID %s", id, node.Id)
}

func (c *Callbacks) OnStreamDeltaRequest(id int64, req *discovery.DeltaDiscoveryRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	nodeID := req.GetNode().GetId()
	typeURL := req.GetTypeUrl()
	responseNonce := req.GetResponseNonce()

	// DEBUG: Log all delta requests
	c.logger.Debugf("DELTA REQUEST: StreamID=%d, NodeID=%s, Type=%s, Nonce=%s",
		id, nodeID, typeURL, responseNonce)

	if errDetail := req.GetErrorDetail(); errDetail != nil {
		if nodeID == "" {
			c.logger.Warn("NodeID missing in error request")
			return nil
		}

		// DEBUG: Enhanced NACK logging
		c.logger.Errorf("🚨 NACK RECEIVED: Node %s rejected %s (nonce: %s): %s",
			nodeID, typeURL, responseNonce, errDetail.Message)
		c.logger.Debugf("🚨 NACK DEBUG: This indicates config was rejected, expecting user to fix and republish")

		c.envoyConnTracker.AddOrUpdateError(c.appContext.Client, nodeID, typeURL, errDetail.Message, responseNonce, c.logger)
	} else {
		// DEBUG: Log successful ACK
		c.logger.Debugf("ACK RECEIVED: Node %s accepted %s (nonce: %s)",
			nodeID, typeURL, responseNonce)
	}
	return nil
}

func (c *Callbacks) OnStreamDeltaResponse(streamID int64, req *discovery.DeltaDiscoveryRequest, resp *discovery.DeltaDiscoveryResponse) {
	// Note: Auto-resolve is handled at snapshot set time in poke.go
	// This callback is kept for logging successful resource acceptance

	nodeID := ""
	resourceType := ""
	if req != nil && req.GetNode() != nil {
		nodeID = req.GetNode().GetId()
		resourceType = req.GetTypeUrl()
	}

	resourceCount := len(resp.GetResources())
	nonce := resp.GetNonce()

	// DEBUG: Log all delta responses being sent to Envoy
	c.logger.Debugf("DELTA RESPONSE: StreamID=%d, NodeID=%s, Type=%s, Resources=%d, Nonce=%s",
		streamID, nodeID, resourceType, resourceCount, nonce)

	if req != nil && req.GetNode() != nil && req.GetErrorDetail() == nil && nodeID != "" {
		c.logger.Debugf("🎉 SUCCESS: Node %s accepted %s", nodeID, resourceType)
	}
}

func (c *Callbacks) CheckSetSnapshot(nodeID, version string, isConfigUpdate bool) error {
	if nodeID == "" {
		return errors.New("nodeID is empty")
	}

	name, project, downstreamAddress := envoys.GetNodeIDParts(nodeID)
	if name == "" || project == "" {
		c.logger.Errorf("Invalid nodeID format: %s", nodeID)
		return errors.New("invalid nodeID format")
	}

	if c.poke.CheckSnapshot(nodeID) {
		return c.poke.GetResourceSetSnapshot(context.Background(), name, project, version, downstreamAddress, isConfigUpdate)
	}
	return nil
}
