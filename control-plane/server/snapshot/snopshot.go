package snapshot

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/CloudNativeWorks/versioned-go-control-plane/pkg/cache/types"
	"github.com/CloudNativeWorks/versioned-go-control-plane/pkg/cache/v3"
	"github.com/CloudNativeWorks/versioned-go-control-plane/pkg/resource/v3"
	"github.com/sirupsen/logrus"

	xdsResource "github.com/CloudNativeWorks/elchi-backend/control-plane/server/resources/resource"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
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

func (c *Context) SetSnapshot(ctx context.Context, allRes *xdsResource.AllResources, logger *logrus.Logger, dbContext *db.AppContext) error {
	if allRes == nil {
		return fmt.Errorf("resources cannot be nil")
	}

	// Get current version before increment
	currentVersion := allRes.GetVersion()
	
	// Increment version in DB for centralized version control
	// Extract resource name and project from nodeID (format: "name::project" or "name::project::downstream")
	name, _ := extractNameAndProject(allRes.GetNodeID())
	if name != "" && dbContext != nil {
		newVersion, err := resources.IncrementResourceVersion(ctx, dbContext, name, allRes.Project, allRes.ResourceVersion)
		if err != nil {
			logger.Warnf("Failed to increment resource version for %s: %v", allRes.GetNodeID(), err)
			// Continue with existing version if increment fails
		} else {
			// Update AllResources with new incremented version
			allRes.SetVersion(newVersion)
			logger.Debugf("🔍 SNAPSHOT DEBUG: Version incremented for %s: %s -> %s", name, currentVersion, newVersion)
		}
	}

	snapshotVersion := allRes.GetVersion()
	
	snapshot := GenerateSnapshot(allRes)
	if snapshot == nil {
		return fmt.Errorf("failed to generate snapshot")
	}
	
	if err := c.Cache.Cache.SetSnapshot(ctx, allRes.GetNodeID(), snapshot); err != nil {
		logger.Errorf("Failed to set snapshot for nodeID %s (version: %s): %v", allRes.GetNodeID(), snapshotVersion, err)
		return err
	}

	// Optimistic auto-resolve: assume new snapshot might fix existing errors
	// This will be handled by the callbacks layer which has database access

	logger.Infof("Successfully set snapshot for nodeID: %s (version: %s)", allRes.GetNodeID(), snapshotVersion)
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

	// DEBUG: Log resource counts in snapshot
	logger.Infof("🔍 SNAPSHOT DEBUG: Generating snapshot version '%s' with resources: C:%d R:%d VH:%d E:%d L:%d EXT:%d S:%d", 
		version, 
		len(resources[resource.ClusterType]), 
		len(resources[resource.RouteType]),
		len(resources[resource.VirtualHostType]),
		len(resources[resource.EndpointType]),
		len(resources[resource.ListenerType]),
		len(resources[resource.ExtensionConfigType]),
		len(resources[resource.SecretType]))

	snap, err := cache.NewSnapshot(version, resources)
	if err != nil {
		logger.Errorf("Error creating snapshot: %v", err)
		return nil
	}

	snap.ConstructVersionMap()

	return snap
}

// extractNameAndProject extracts resource name and project from nodeID
// nodeID format: "name::project" or "name::project::downstream"
func extractNameAndProject(nodeID string) (string, string) {
	parts := strings.Split(nodeID, "::")
	if len(parts) < 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

