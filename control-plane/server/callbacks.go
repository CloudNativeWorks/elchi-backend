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

func (c *Callbacks) OnDeltaStreamOpen(ctx context.Context, id int64, typ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	c.logger.Infof("🔍 DEBUG: OnDeltaStreamOpen called (stream ID: %d, type: %s)", id, typ)
	
	address, nodeID, version, downstreamAddress, clientName := GetMetadata(ctx, c.logger)
	
	// CRITICAL: Validate nodeID FIRST before any registry operations
	if nodeID == "" {
		c.logger.Warn("NodeID missing from metadata - skipping all operations")
		return errors.New("nodeID missing from metadata")
	}

	// ROBUST: Validate nodeID format using existing GetNodeIDParts function
	name, project, _ := GetNodeIDParts(nodeID)
	if name == "" || project == "" {
		c.logger.Warnf("Invalid nodeID format detected: '%s' (name='%s', project='%s') - skipping registry operations", nodeID, name, project)
		return errors.New("invalid nodeID format")
	}

	c.logger.Infof("🔍 DEBUG: Node metadata - NodeID: %s, Version: %s", nodeID, version)

	// Check if routing manager is set
	if c.routingManager == nil {
		c.logger.Errorf("🚨 CRITICAL: Routing manager is NULL in callbacks!")
	} else {
		c.logger.Infof("✅ DEBUG: Routing manager is properly set, calling NotifySnapshotDelivered")
		
		// Now safe to notify registry (nodeID is validated)
		c.routingManager.NotifySnapshotDelivered(nodeID, version)
		c.logger.Debugf("Notified routing manager about snapshot delivery for node: %s (version: %s)", nodeID, version)
	}

	if err := c.CheckSetSnapshot(nodeID, version); err != nil {
		c.logger.Warnf("Error checking snapshot: %v", err)
		return err
	}

	c.envoyConnTracker.TrackClientUp(c.appContext.Client, nodeID, address, version, downstreamAddress, clientName, id, c.logger)
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

	if errDetail := req.GetErrorDetail(); errDetail != nil {
		nodeID := req.GetNode().GetId()
		if nodeID == "" {
			c.logger.Warn("NodeID missing in error request")
			return nil
		}

		typeURL := req.GetTypeUrl()
		responseNonce := req.GetResponseNonce()
		c.logger.Errorf("Delta Discovery Request Error (Node %s, Resource %s): %s", nodeID, typeURL, errDetail.Message)
		c.envoyConnTracker.AddOrUpdateError(c.appContext.Client, nodeID, typeURL, errDetail.Message, responseNonce, c.logger)
	}
	return nil
}

func (c *Callbacks) OnStreamDeltaResponse(_ int64, req *discovery.DeltaDiscoveryRequest, resp *discovery.DeltaDiscoveryResponse) {
	// No routing notification needed here
}

func (c *Callbacks) CheckSetSnapshot(nodeID, version string) error {
	if nodeID == "" {
		return errors.New("nodeID is empty")
	}

	name, project, downstreamAddress := GetNodeIDParts(nodeID)
	if name == "" || project == "" {
		c.logger.Errorf("Invalid nodeID format: %s", nodeID)
		return errors.New("invalid nodeID format")
	}

	if c.poke.CheckSnapshot(nodeID) {
		return c.poke.GetResourceSetSnapshot(context.Background(), name, project, version, downstreamAddress)
	}
	return nil
}
