package snapshot

import (
	"github.com/CloudNativeWorks/versioned-go-control-plane/pkg/cache/v3"
)

type Cache struct {
	Cache cache.SnapshotCache
}

type Context struct {
	Cache *Cache
}
