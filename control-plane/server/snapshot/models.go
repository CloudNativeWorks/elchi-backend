package snapshot

import (
	"time"

	"github.com/CloudNativeWorks/versioned-go-control-plane/pkg/cache/v3"
)

type Cache struct {
	Cache cache.SnapshotCache
}

type Context struct {
	Cache *Cache
}

// ConnectedNode represents a connected node with its status
type ConnectedNode struct {
	NodeID    string
	Version   string
	LastSeen  time.Time
	Connected bool
}

// GetConnectedNodes returns all connected nodes from the snapshot cache
func (c *Context) GetConnectedNodes() []ConnectedNode {
	statusKeys := c.Cache.Cache.GetStatusKeys()
	var nodes []ConnectedNode
	
	for _, nodeID := range statusKeys {
		status := c.Cache.Cache.GetStatusInfo(nodeID)
		if status != nil {
			// Check if node is connected (has active watches)
			connected := status.GetNumDeltaWatches() > 0
			
			// Get last watch time
			lastWatchTime := status.GetLastDeltaWatchRequestTime()
			if lastWatchTime.IsZero() {
				lastWatchTime = time.Now()
			}
			
			// Get snapshot to extract version
			snapshot, err := c.Cache.Cache.GetSnapshot(nodeID)
			version := "unknown"
			if err == nil && snapshot != nil {
				version = snapshot.GetVersion("")
			}
			
			nodes = append(nodes, ConnectedNode{
				NodeID:    nodeID,
				Version:   version,
				LastSeen:  lastWatchTime,
				Connected: connected,
			})
		}
	}
	
	return nodes
}
