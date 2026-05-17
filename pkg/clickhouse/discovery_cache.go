package clickhouse

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"sync"
	"time"
)

// discoveryCache memoises DiscoverInventoryKeys results so repeated
// dashboard hits within the TTL skip the full ClickHouse scan that
// SELECT DISTINCT (listener_name, normalized_path) FROM api_events_raw
// otherwise costs. Keyed on (project, country, asn, source_ip,
// user_agent, from-unix, to-unix) — every dimension that participates
// in the discovery WHERE.
//
// TTL is intentionally short (seconds): the inventory cross-filter
// lives in front of a near-real-time event stream, so an entry that
// outlives the next collector flush would surface stale path lists.
// The eviction policy is FIFO with a hard cap: simpler than LRU and
// the access pattern (one entry per dashboard load) doesn't benefit
// from recency tracking.
type discoveryCache struct {
	mu      sync.Mutex
	entries map[string]discoveryCacheEntry
	order   []string // FIFO insertion order for eviction
	ttl     time.Duration
	maxSize int
	now     func() time.Time // injectable for tests; nil → time.Now
}

type discoveryCacheEntry struct {
	keys      []GeoInventoryKey
	expiresAt time.Time
}

// newDiscoveryCache builds an empty cache. ttl<=0 disables caching
// (Get always misses, Set is a no-op). maxSize<=0 falls back to a
// 256-entry ceiling — tens of distinct (project × dimension)
// combinations is the realistic ceiling per controller.
func newDiscoveryCache(ttl time.Duration, maxSize int) *discoveryCache {
	if maxSize <= 0 {
		maxSize = 256
	}
	return &discoveryCache{
		entries: make(map[string]discoveryCacheEntry, maxSize),
		order:   make([]string, 0, maxSize),
		ttl:     ttl,
		maxSize: maxSize,
	}
}

// Get returns the cached keys (cloned, see below) when the entry is
// still live. Returns nil + false on miss / expiry. Cloning the slice
// before handing it back keeps the cache state immutable from the
// caller's point of view — DiscoverInventoryKeys callers mutate the
// returned slice (the inventory handler builds a $or array off it),
// and a shared backing array would race across concurrent requests.
func (c *discoveryCache) Get(key string) ([]GeoInventoryKey, bool) {
	if c == nil || c.ttl <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if c.timeNow().After(entry.expiresAt) {
		delete(c.entries, key)
		c.removeFromOrder(key)
		return nil, false
	}
	out := make([]GeoInventoryKey, len(entry.keys))
	copy(out, entry.keys)
	return out, true
}

// Set stores keys against the cache key. Empty input is also cached —
// "no traffic in this window" is a legitimate result that should not
// re-scan on every dashboard refresh.
func (c *discoveryCache) Set(key string, keys []GeoInventoryKey) {
	if c == nil || c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists {
		// FIFO eviction: drop the oldest entry once the cap is hit.
		if len(c.order) >= c.maxSize {
			oldest := c.order[0]
			c.order = c.order[1:]
			delete(c.entries, oldest)
		}
		c.order = append(c.order, key)
	}
	stored := make([]GeoInventoryKey, len(keys))
	copy(stored, keys)
	c.entries[key] = discoveryCacheEntry{
		keys:      stored,
		expiresAt: c.timeNow().Add(c.ttl),
	}
}

func (c *discoveryCache) removeFromOrder(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

func (c *discoveryCache) timeNow() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// discoveryCacheKey serialises an InventoryDiscoveryFilter into a
// collision-resistant SHA-256 fingerprint. Hashing (vs. simple
// concat) defends against user-controlled fields — UserAgent in
// particular — containing the separator we'd otherwise rely on,
// which would let two distinct filters collapse onto the same cache
// key and serve one user's results to another.
//
// Each field is length-prefixed inside the hash so "ab|c" and
// "a|bc" cannot share a digest even though both could otherwise
// collapse under a delimiter-only scheme. Time fields go in as
// unix-second offsets so the key stays stable regardless of how
// the caller spells the timestamps.
func discoveryCacheKey(f InventoryDiscoveryFilter) string {
	h := sha256.New()
	writeField := func(label, value string) {
		// label + length-prefix + value + NUL terminator — length
		// prefix is the actual collision defence; label keeps the
		// hash interpretable in a debugger if you ever dump the
		// pre-image.
		h.Write([]byte(label))
		h.Write([]byte{byte(len(value) >> 24), byte(len(value) >> 16), byte(len(value) >> 8), byte(len(value))})
		h.Write([]byte(value))
		h.Write([]byte{0})
	}
	writeField("p", f.ProjectID)
	writeField("c", f.Country)
	writeField("a", f.ASN)
	writeField("s", f.SourceIP)
	writeField("u", f.UserAgent)
	writeField("f", strconv.FormatInt(f.From.Unix(), 10))
	writeField("t", strconv.FormatInt(f.To.Unix(), 10))
	writeField("l", strconv.Itoa(f.Limit))
	return hex.EncodeToString(h.Sum(nil))
}
