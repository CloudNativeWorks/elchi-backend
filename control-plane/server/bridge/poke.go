package bridge

import (
	"context"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/control-plane/envoys"
	"github.com/CloudNativeWorks/elchi-backend/control-plane/server/resources/resource"
	"github.com/CloudNativeWorks/elchi-backend/control-plane/server/snapshot"
	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
)

type PokeService struct {
	Snapshot   *snapshot.Context
	appContext *db.AppContext
	Logger     *logger.Logger
}

func NewPokeService(ctxCache *snapshot.Context, appContext *db.AppContext) *PokeService {
	return &PokeService{
		Snapshot:   ctxCache,
		appContext: appContext,
		Logger:     logger.NewLogger("control-plane/poke"),
	}
}

// load snapshot from controller via grpc
func (pss *PokeServiceServer) Poke(ctx context.Context, req *bridge.PokeRequest) (*bridge.PokeResponse, error) {
	rawListenerResource, err := resources.GetResourceNGeneral(ctx, pss.AppContext, "listeners", req.NodeID, req.Project, req.Version)
	if err != nil {
		return nil, err
	}

	allResources, err := resource.GenerateSnapshot(ctx, rawListenerResource, req.NodeID, pss.AppContext, pss.Logger, req.Project, req.Version, req.DownstreamAddress)
	if err != nil {
		return nil, err
	}

	err = pss.context.SetSnapshot(ctx, allResources, pss.Logger, pss.AppContext)
	if err != nil {
		return nil, err
	}

	// Hash-based auto-resolve decision
	nodeID := allResources.GetNodeID()

	if req.IsConfigUpdate {
		// Calculate hash of current snapshot
		newHash := allResources.CalculateContentHash()
		pss.Logger.Debugf("🔍 HASH: Calculated new hash for %s: %s", nodeID, getHashPrefix(newHash))

		// Get previous hash from MongoDB
		oldHash, err := getLastSnapshotHash(ctx, pss.AppContext.Client, nodeID)
		if err != nil {
			pss.Logger.Warnf("⚠️ HASH: Failed to get last snapshot hash: %v", err)
			// Continue anyway, treat as config change
			oldHash = ""
		}

		if oldHash == "" {
			pss.Logger.Debugf("🔍 HASH: No previous hash found for %s (first config or new field)", nodeID)
		} else {
			pss.Logger.Debugf("🔍 HASH: Previous hash for %s: %s", nodeID, getHashPrefix(oldHash))
		}

		// Compare hashes to determine if config actually changed
		if newHash != oldHash {
			// Config actually changed - trigger auto-resolve
			go envoys.AutoResolveAllErrors(context.Background(), pss.AppContext.Client, nodeID, pss.Logger)
			pss.Logger.Infof("✅ AUTO-RESOLVE: Triggered - config changed for %s (hash: %s -> %s)",
				nodeID, getHashPrefix(oldHash), getHashPrefix(newHash))

			// Update stored hash
			if err := updateSnapshotHash(context.Background(), pss.AppContext.Client, nodeID, newHash); err != nil {
				pss.Logger.Warnf("⚠️ HASH: Failed to update snapshot hash: %v", err)
			} else {
				pss.Logger.Debugf("💾 HASH: Updated hash in MongoDB for %s", nodeID)
			}
		} else {
			// Same config re-published - skip auto-resolve
			pss.Logger.Infof("⏭️ AUTO-RESOLVE: Skipped - same config re-published for %s (hash: %s)",
				nodeID, getHashPrefix(newHash))
		}
	} else {
		// Control-plane restart - skip auto-resolve
		pss.Logger.Infof("🔄 AUTO-RESOLVE: Skipped - control-plane restart for NodeID: %s", nodeID)
	}

	response := &bridge.PokeResponse{Message: "Poke successful"}
	return response, nil
}

func (pss *PokeServiceServer) NotifyUndeploy(ctx context.Context, req *bridge.UndeployRequest) (*bridge.UndeployResponse, error) {
	pss.Logger.Infof("Undeploy notification received for NodeID: %s, Service: %s", req.NodeID, req.ServiceName)

	pss.EnvoyConnTracker.TrackUndeploy(pss.AppContext.Client, req.NodeID, pss.Logger)

	pss.Logger.Infof("Undeploy processed successfully for NodeID: %s", req.NodeID)

	response := &bridge.UndeployResponse{Message: "Undeploy notification processed successfully"}
	return response, nil
}

func (ps *PokeService) CheckSnapshot(node string) bool {
	snapshot, err := ps.Snapshot.Cache.Cache.GetSnapshot(node)
	if err != nil {
		ps.Logger.Debugf("Error while fetching snapshot for node %s: %v", node, err)
		return true
	}

	if snapshot == nil {
		ps.Logger.Debugf("Snapshot is nil for node: %s", node)
		return true
	}

	ps.Logger.Debugf("Snapshot exists for node: %s", node)
	return false
}

func (ps *PokeService) getAllResourcesFromListener(ctx context.Context, listenerName, project, version, downstreamAddress string) (*resource.AllResources, error) {
	rawListenerResource, err := resources.GetResourceNGeneral(ctx, ps.appContext, "listeners", listenerName, project, version)
	if err != nil {
		return nil, err
	}

	lis, err := resource.GenerateSnapshot(ctx, rawListenerResource, listenerName, ps.appContext, ps.Logger, project, version, downstreamAddress)
	if err != nil {
		return nil, err
	}

	return lis, nil
}

// load snapshot from callback package
// getHashPrefix returns first 8 characters of hash for logging
func getHashPrefix(hash string) string {
	if len(hash) >= 8 {
		return hash[:8]
	}
	if hash == "" {
		return "none"
	}
	return hash
}

// getLastSnapshotHash retrieves the last snapshot hash from MongoDB
func getLastSnapshotHash(ctx context.Context, dbClient *mongo.Database, nodeID string) (string, error) {
	// Extract name and project from nodeID (format: "name::project" or "name::project::downstream")
	parts := strings.Split(nodeID, "::")
	if len(parts) < 2 {
		return "", nil
	}
	name := parts[0]
	project := parts[1]

	collection := dbClient.Collection("envoys")
	filter := bson.M{
		"name":    name,
		"project": project,
	}

	var result struct {
		LastSnapshotHash string `bson:"last_snapshot_hash"`
	}

	err := collection.FindOne(ctx, filter).Decode(&result)
	if err == mongo.ErrNoDocuments {
		// No previous hash stored
		return "", nil
	}
	if err != nil {
		return "", err
	}

	return result.LastSnapshotHash, nil
}

// updateSnapshotHash updates the snapshot hash in MongoDB
func updateSnapshotHash(ctx context.Context, dbClient *mongo.Database, nodeID, hash string) error {
	// Extract name and project from nodeID
	parts := strings.Split(nodeID, "::")
	if len(parts) < 2 {
		return nil
	}
	name := parts[0]
	project := parts[1]

	collection := dbClient.Collection("envoys")
	filter := bson.M{
		"name":    name,
		"project": project,
	}
	update := bson.M{
		"$set": bson.M{
			"last_snapshot_hash": hash,
		},
	}

	_, err := collection.UpdateOne(ctx, filter, update)
	return err
}

func (ps *PokeService) GetResourceSetSnapshot(ctx context.Context, node, project, version, downstreamAddress string, isConfigUpdate bool) error {
	allResource, err := ps.getAllResourcesFromListener(ctx, node, project, version, downstreamAddress)
	if err != nil {
		ps.Logger.Warnf("get resources err (%v:%v): %v", node, project, err)
		return err
	}

	err = ps.Snapshot.SetSnapshot(ctx, allResource, ps.Logger, ps.appContext)
	if err != nil {
		ps.Logger.Warnf("%s", err)
		return err
	}

	// Hash-based auto-resolve decision for callback scenario
	nodeID := allResource.NodeID

	if isConfigUpdate {
		// Calculate hash of current snapshot
		newHash := allResource.CalculateContentHash()
		ps.Logger.Debugf("🔍 HASH: [Callback] Calculated new hash for %s: %s", nodeID, getHashPrefix(newHash))

		// Get previous hash from MongoDB
		oldHash, err := getLastSnapshotHash(ctx, ps.appContext.Client, nodeID)
		if err != nil {
			ps.Logger.Warnf("⚠️ HASH: [Callback] Failed to get last snapshot hash: %v", err)
			oldHash = ""
		}

		if oldHash == "" {
			ps.Logger.Debugf("🔍 HASH: [Callback] No previous hash found for %s (first config or new field)", nodeID)
		} else {
			ps.Logger.Debugf("🔍 HASH: [Callback] Previous hash for %s: %s", nodeID, getHashPrefix(oldHash))
		}

		// Compare hashes to determine if config actually changed
		if newHash != oldHash {
			// Config actually changed - trigger auto-resolve
			go envoys.AutoResolveAllErrors(context.Background(), ps.appContext.Client, nodeID, ps.Logger)
			ps.Logger.Infof("✅ AUTO-RESOLVE: [Callback] Triggered - config changed for %s (hash: %s -> %s)",
				nodeID, getHashPrefix(oldHash), getHashPrefix(newHash))

			// Update stored hash
			if err := updateSnapshotHash(context.Background(), ps.appContext.Client, nodeID, newHash); err != nil {
				ps.Logger.Warnf("⚠️ HASH: [Callback] Failed to update snapshot hash: %v", err)
			} else {
				ps.Logger.Debugf("💾 HASH: [Callback] Updated hash in MongoDB for %s", nodeID)
			}
		} else {
			// Same config - skip auto-resolve
			ps.Logger.Infof("⏭️ AUTO-RESOLVE: [Callback] Skipped - same config for %s (hash: %s)",
				nodeID, getHashPrefix(newHash))
		}
	} else {
		// Control-plane restart scenario (isConfigUpdate=false from callback)
		ps.Logger.Infof("🔄 AUTO-RESOLVE: [Callback] Skipped - control-plane restart for NodeID: %s", nodeID)
	}

	return nil
}
