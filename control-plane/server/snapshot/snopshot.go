package snapshot

import (
	"context"
	"fmt"
	"sync"

	"github.com/CloudNativeWorks/versioned-go-control-plane/pkg/cache/types"
	"github.com/CloudNativeWorks/versioned-go-control-plane/pkg/cache/v3"
	"github.com/CloudNativeWorks/versioned-go-control-plane/pkg/resource/v3"
	"github.com/sirupsen/logrus"

	xdsResource "github.com/CloudNativeWorks/elchi-backend/control-plane/server/resources/resource"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

var (
	once sync.Once
	ctx  *Context
)

func NewCache() *Cache {
	logger := logger.NewLogger("control-plane/snapshot")
	return &Cache{
		Cache: cache.NewSnapshotCache(true, cache.IDHash{}, logger),
	}
}

func GetContext() *Context {
	once.Do(func() {
		ctx = &Context{
			Cache: NewCache(),
		}
	})
	return ctx
}

func (c *Context) SetSnapshot(ctx context.Context, resources *xdsResource.AllResources, logger *logrus.Logger) error {
	if resources == nil {
		return fmt.Errorf("resources cannot be nil")
	}

	snapshot := GenerateSnapshot(resources)
	if snapshot == nil {
		return fmt.Errorf("failed to generate snapshot")
	}

	if err := c.Cache.Cache.SetSnapshot(ctx, resources.NodeID, snapshot); err != nil {
		logger.Errorf("Failed to set snapshot for nodeID %s: %v", resources.NodeID, err)
		return err
	}

	logger.Infof("Successfully set snapshot for nodeID: %s", resources.NodeID)
	return nil
}

func GenerateSnapshot(r *xdsResource.AllResources) *cache.Snapshot {
	version := r.GetVersion()

	resources := map[resource.Type][]types.Resource{
		resource.ClusterType:         r.GetClusterT(),
		resource.RouteType:           r.GetRouteT(),
		resource.VirtualHostType:     r.GetVirtualHostT(),
		resource.EndpointType:        r.GetEndpointT(),
		resource.ListenerType:        r.GetListenerT(),
		resource.ExtensionConfigType: r.GetExtensionsT(),
		resource.SecretType:          r.GetSecretT(),
	}

	snap, err := cache.NewSnapshot(version, resources)
	if err != nil {
		logger.Errorf("Error creating snapshot: %v", err)
		return nil
	}

	snap.ConstructVersionMap()

	return snap
}

/*
func GenerateSnapshot(r *xdsResource.AllResources) *cache.Snapshot {
	ts := fmt.Sprintf("%d", time.Now().UnixNano())

	// her TÜR için farklı version ve Items
	resMap := map[resource.Type][]types.ResourceWithTTL{
		resource.ClusterType:         wrap("clus-"+ts, r.GetClusterT()),
		resource.RouteType:           wrap("rout-"+ts, r.GetRouteT()),
		resource.VirtualHostType:     wrap("vhst-"+ts, r.GetVirtualHostT()),
		resource.EndpointType:        wrap("endp-"+ts, r.GetEndpointT()),
		resource.ListenerType:        wrap("list-"+ts, r.GetListenerT()),
		resource.ExtensionConfigType: wrap("ext-"+ts, r.GetExtensionsT()),
		resource.SecretType:          wrap("secr-"+ts, r.GetSecretT()),
	}

	snap, _ := cache.NewSnapshotWithTTLs(ts, resMap)

	snap.ConstructVersionMap()

	return snap
} */
/*
func wrap(ver string, objs []types.Resource) []types.ResourceWithTTL {
	out := make([]types.ResourceWithTTL, len(objs))
	for i, o := range objs {
		name := o.(interface{ GetName() string }).GetName()
		anyMsg, _ := anypb.New(o.(proto.Message))

		out[i] = types.ResourceWithTTL{
			Resource: &discovery.Resource{
				Name:     name,
				Version:  ver,
				Resource: anyMsg,
			},
			TTL: nil,
		}
	}
	return out
} */
