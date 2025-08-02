package dependency

import (
	"context"
	"fmt"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
)

func (h *AppHandler) getCacheEntry(key string) (CacheEntry, bool) {
	h.CacheMutex.Lock()
	defer h.CacheMutex.Unlock()

	entry, found := h.Cache[key]
	if !found {
		return CacheEntry{}, false
	}

	if time.Since(entry.Timestamp) > entry.TTL {
		delete(h.Cache, key)
		return CacheEntry{}, false
	}

	return entry, true
}

func (h *AppHandler) setCacheEntry(key string, entry CacheEntry) {
	h.CacheMutex.Lock()
	defer h.CacheMutex.Unlock()

	entry.Timestamp = time.Now()
	entry.TTL = 5 * time.Minute
	h.Cache[key] = entry
}

func (h *AppHandler) StartCacheCleanup(interval time.Duration) {
	go func() {
		for {
			time.Sleep(interval)
			h.cleanupCache()
		}
	}()
}

func (h *AppHandler) cleanupCache() {
	h.CacheMutex.Lock()
	defer h.CacheMutex.Unlock()

	now := time.Now()
	for key, entry := range h.Cache {
		if now.Sub(entry.Timestamp) > entry.TTL {
			delete(h.Cache, key)
		}
	}
}

func (h *AppHandler) getResourceData(ctx context.Context, collection, name, project, version string) (string, string) {
	cacheKey := fmt.Sprintf("%s|%s|%s", collection, name, project)

	if cacheEntry, found := h.getCacheEntry(cacheKey); found {
		return cacheEntry.ID, cacheEntry.JSON
	}

	resource, err := resources.GetResourceNGeneral(ctx, h.Context, collection, name, project, version)
	if err != nil {
		h.Logger.Debugf("Error fetching resource: %v", err)
		return "", ""
	}

	resourceID := resource.ID.Hex()
	jsonResource := helper.ConvertToJSON(resource, h.Logger.Logger)

	h.setCacheEntry(cacheKey, CacheEntry{
		ID:   resourceID,
		JSON: jsonResource,
	})

	return resourceID, jsonResource
}

func getDynamicJSONPaths(gtype models.GTypes) map[string]models.GTypes {
	paths := gtype.UpstreamPaths()

	if len(paths) == 0 {
		return map[string]models.GTypes{}
	}

	if gtype == models.VirtualHost {
		paths = addPrefixToPaths(paths, "#.")
	}

	return paths
}

func addPrefixToPaths(paths map[string]models.GTypes, prefix string) map[string]models.GTypes {
	newPaths := make(map[string]models.GTypes)
	for path, gtype := range paths {
		newPaths[prefix+path] = gtype
	}
	return newPaths
}
