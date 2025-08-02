package bridge

import (
	"context"

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

	allResources, err := resource.GenerateSnapshot(ctx, rawListenerResource, req.NodeID, pss.AppContext, pss.Logger.Logger, req.Project, req.Version, req.DownstreamAddress)
	if err != nil {
		return nil, err
	}

	err = pss.context.SetSnapshot(ctx, allResources, pss.Logger.Logger)
	if err != nil {
		return nil, err
	}
	response := &bridge.PokeResponse{Message: "Poke successful"}

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

	lis, err := resource.GenerateSnapshot(ctx, rawListenerResource, listenerName, ps.appContext, ps.Logger.Logger, project, version, downstreamAddress)
	if err != nil {
		return nil, err
	}

	return lis, nil
}

// load snapshot from callback package
func (ps *PokeService) GetResourceSetSnapshot(ctx context.Context, node, project, version, downstreamAddress string) error {
	allResource, err := ps.getAllResourcesFromListener(ctx, node, project, version, downstreamAddress)
	if err != nil {
		ps.Logger.Warnf("get resources err (%v:%v): %v", node, project, err)
		return err
	}

	err = ps.Snapshot.SetSnapshot(ctx, allResource, ps.Logger.Logger)
	if err != nil {
		ps.Logger.Warnf("%s", err)
	}
	return nil
}
