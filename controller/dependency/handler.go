package dependency

import (
	"context"
	"sync"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

type AppHandler struct {
	Context      *db.AppContext
	Dependencies *Graph
	Cache        map[string]CacheEntry
	CacheMutex   sync.Mutex
	Version      string
	Logger       *logger.Logger
}

func NewDependencyHandler(context *db.AppContext) *AppHandler {
	return &AppHandler{
		Context:      context,
		Dependencies: &Graph{},
		Cache:        make(map[string]CacheEntry),
		Logger:       logger.NewLogger("controller/dependency"),
	}
}

func (h *AppHandler) GetResourceDependencies(ctx context.Context, requestDetails models.RequestDetails) (*Graph, error) {
	activeResource := Depend{
		Collection: requestDetails.Collection,
		Name:       requestDetails.Name,
		Gtype:      requestDetails.GType,
		Project:    requestDetails.Project,
		First:      true,
	}

	h.Dependencies = &Graph{}
	h.ProcessResource(ctx, activeResource, requestDetails.Version)

	return h.Dependencies, nil
}

func (h *AppHandler) CallUpstreamFunction(ctx context.Context, activeResource Depend) (Node, []Depend) {
	return GenericUpstreamCollector(ctx, h, activeResource, h.Version)
}

func (h *AppHandler) CallDownstreamFunction(ctx context.Context, activeResource Depend) (Node, []Depend) {
	visited := make(map[string]bool)
	return GenericDownstreamCollector(ctx, h, activeResource, visited, h.Version)
}
