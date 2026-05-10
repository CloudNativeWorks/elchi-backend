package syslog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

const (
	defaultQueueSize        = 10000
	maxQueueSize            = 100000
	configPollInterval      = 30 * time.Second
	dropLogThrottleInterval = 60 * time.Second
)

// ForwarderConfig is the in-memory shape consumed by the forwarder. It
// mirrors models.SyslogConfig but contains only the fields the forwarder
// actually uses, avoiding a circular dependency on pkg/models's mongo tags.
type ForwarderConfig struct {
	Enabled          bool
	Protocol         string
	Host             string
	Port             int
	Facility         string
	Tag              string
	CACert           string
	ClientCert       string
	ClientKey        string
	QueueSize        int
	ConnectTimeoutMs int
	WriteTimeoutMs   int
}

// hash returns a stable digest of the connection-affecting fields. Used by
// the polling loop to detect changes without rebuilding the client on every
// tick.
func (c *ForwarderConfig) hash() string {
	if c == nil {
		return ""
	}
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SettingsLoader resolves the current ForwarderConfig from persistent
// settings. Implementations live in the controller layer.
type SettingsLoader interface {
	LoadSyslogConfig(ctx context.Context) (*ForwarderConfig, error)
}

// Forwarder is the non-blocking pipeline between audit.Service and an
// external syslog endpoint. The mongoDB write path remains the canonical
// audit log; the forwarder is best-effort.
type Forwarder struct {
	loader   SettingsLoader
	logger   *logger.Logger
	queue    chan *models.AuditEntry
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
	started  atomic.Bool

	cfg        atomic.Pointer[ForwarderConfig]
	cfgHash    atomic.Value // string
	clientMu   sync.Mutex
	client     *Client
	queueCap   atomic.Int32
	dropped    atomic.Int64
	lastDropAt atomic.Int64 // unix-nano
}

// NewForwarder builds a forwarder with default capacity. Queue size is
// resized lazily on first config load; the initial channel is sized to
// defaultQueueSize so Enqueue never blocks before Start runs.
func NewForwarder(loader SettingsLoader, log *logger.Logger) *Forwarder {
	f := &Forwarder{
		loader: loader,
		logger: log,
		queue:  make(chan *models.AuditEntry, defaultQueueSize),
		stopCh: make(chan struct{}),
	}
	f.queueCap.Store(int32(defaultQueueSize))
	f.cfgHash.Store("")
	return f
}

// Start launches the config-poll and worker goroutines. Idempotent.
func (f *Forwarder) Start(ctx context.Context) {
	if !f.started.CompareAndSwap(false, true) {
		return
	}
	f.wg.Add(2)
	go f.pollLoop(ctx)
	go f.workerLoop(ctx)
}

// Stop signals shutdown and waits up to 5 seconds for the worker to drain.
// After 5s remaining entries are abandoned (best-effort design). Safe to
// call multiple times; subsequent calls are no-ops.
func (f *Forwarder) Stop() {
	if !f.started.Load() {
		return
	}
	f.stopOnce.Do(func() {
		close(f.stopCh)
		done := make(chan struct{})
		go func() {
			f.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			if f.logger != nil {
				f.logger.Warnf("syslog forwarder shutdown timed out, %d entries pending", len(f.queue))
			}
		}
		f.clientMu.Lock()
		if f.client != nil {
			f.client.Close()
			f.client = nil
		}
		f.clientMu.Unlock()
	})
}

// Enqueue offers an audit entry to the forwarding queue. Non-blocking:
// when the forwarder is disabled the entry is dropped silently; when the
// queue is full the oldest entry is evicted (drop-oldest) and a throttled
// warning is logged.
func (f *Forwarder) Enqueue(entry *models.AuditEntry) {
	if entry == nil {
		return
	}
	cfg := f.cfg.Load()
	if cfg == nil || !cfg.Enabled {
		return
	}

	select {
	case f.queue <- entry:
		return
	default:
	}

	// Queue full — drop oldest, then push new.
	select {
	case <-f.queue:
	default:
	}
	select {
	case f.queue <- entry:
	default:
		// extremely unlikely: still full after eviction.
	}

	dropped := f.dropped.Add(1)
	f.maybeLogDrop(dropped)
}

func (f *Forwarder) maybeLogDrop(_ int64) {
	if f.logger == nil {
		return
	}
	now := time.Now().UnixNano()
	last := f.lastDropAt.Load()
	if last != 0 && now-last < dropLogThrottleInterval.Nanoseconds() {
		return
	}
	if !f.lastDropAt.CompareAndSwap(last, now) {
		return
	}
	dropped := f.dropped.Swap(0)
	f.logger.Warnf("syslog forwarder backpressure: %d audit entries dropped (queue cap=%d)", dropped, f.queueCap.Load())
}

// pollLoop periodically refreshes the SettingsLoader-resolved config.
func (f *Forwarder) pollLoop(ctx context.Context) {
	defer f.wg.Done()

	f.refreshConfig(ctx)

	ticker := time.NewTicker(configPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-f.stopCh:
			return
		case <-ticker.C:
			f.refreshConfig(ctx)
		}
	}
}

func (f *Forwarder) refreshConfig(ctx context.Context) {
	if f.loader == nil {
		return
	}
	loadCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cfg, err := f.loader.LoadSyslogConfig(loadCtx)
	if err != nil {
		if f.logger != nil {
			f.logger.Warnf("syslog forwarder config reload failed: %v", err)
		}
		return
	}

	if cfg == nil {
		cfg = &ForwarderConfig{}
	}
	newHash := cfg.hash()
	prev, _ := f.cfgHash.Load().(string)
	if newHash == prev {
		return
	}

	// Effective queue cap is reported via QueueDepth/DroppedTotal logs. We
	// don't resize the underlying channel: the buffered cap is fixed at
	// process start to keep Enqueue lock-free and predictable.
	queueSize := cfg.QueueSize
	if queueSize <= 0 {
		queueSize = defaultQueueSize
	}
	if queueSize > maxQueueSize {
		queueSize = maxQueueSize
	}
	f.queueCap.Store(int32(queueSize))

	// Rebuild the client BEFORE publishing the new cfg. Otherwise producers
	// could observe Enabled=true via cfg.Load and start enqueueing before
	// f.client is set, leaving the worker briefly clientless.
	f.rebuildClient(cfg)

	f.cfg.Store(cfg)
	f.cfgHash.Store(newHash)

	if f.logger != nil {
		f.logger.Infof("syslog forwarder reloaded config (enabled=%t protocol=%s host=%s port=%d)",
			cfg.Enabled, cfg.Protocol, cfg.Host, cfg.Port)
	}
}

func (f *Forwarder) rebuildClient(cfg *ForwarderConfig) {
	var newClient *Client
	if cfg.Enabled {
		newClient = NewClient(Config{
			Protocol:         cfg.Protocol,
			Host:             cfg.Host,
			Port:             cfg.Port,
			CACert:           cfg.CACert,
			ClientCert:       cfg.ClientCert,
			ClientKey:        cfg.ClientKey,
			ConnectTimeoutMs: cfg.ConnectTimeoutMs,
			WriteTimeoutMs:   cfg.WriteTimeoutMs,
		})
		// Best-effort initial connect — Send will retry on demand.
		if err := newClient.Connect(); err != nil && f.logger != nil {
			f.logger.Warnf("syslog forwarder initial connect failed: %v", err)
		}
	}

	f.clientMu.Lock()
	old := f.client
	f.client = newClient
	f.clientMu.Unlock()

	if old != nil {
		old.Close()
	}
}

func (f *Forwarder) currentClient() *Client {
	f.clientMu.Lock()
	defer f.clientMu.Unlock()
	return f.client
}

// workerLoop drains the queue and ships each entry to the syslog endpoint.
// On Send error, an exponential backoff sleep is applied before the next
// dequeue. Successive errors increase the backoff up to 5 minutes.
func (f *Forwarder) workerLoop(ctx context.Context) {
	defer f.wg.Done()

	consecutiveFailures := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-f.stopCh:
			f.drain()
			return
		case entry := <-f.queue:
			cfg := f.cfg.Load()
			if cfg == nil || !cfg.Enabled {
				// Configuration disabled while message was queued — skip silently.
				continue
			}
			client := f.currentClient()
			if client == nil {
				// Should be rare; rebuild on demand.
				f.rebuildClient(cfg)
				client = f.currentClient()
				if client == nil {
					consecutiveFailures++
					f.sleepBackoff(ctx, consecutiveFailures)
					continue
				}
			}

			payload := EncodeAuditEntry(entry, FacilityFromName(cfg.Facility), cfg.Tag)
			if len(payload) == 0 {
				continue
			}

			if err := client.Send(payload); err != nil {
				consecutiveFailures++
				if f.logger != nil {
					f.logger.Warnf("syslog forwarder send failed (attempt=%d): %v", consecutiveFailures, err)
				}
				f.sleepBackoff(ctx, consecutiveFailures)
				continue
			}
			consecutiveFailures = 0
		}
	}
}

// drain flushes whatever is in the queue best-effort during shutdown. Bound
// by Stop()'s 5s timeout so this loop returns promptly.
func (f *Forwarder) drain() {
	cfg := f.cfg.Load()
	if cfg == nil || !cfg.Enabled {
		return
	}
	client := f.currentClient()
	if client == nil {
		return
	}
	deadline := time.Now().Add(4 * time.Second)
	for {
		select {
		case entry := <-f.queue:
			payload := EncodeAuditEntry(entry, FacilityFromName(cfg.Facility), cfg.Tag)
			_ = client.Send(payload)
		default:
			return
		}
		if time.Now().After(deadline) {
			return
		}
	}
}

// sleepBackoff sleeps with ±20% jitter; 5s → 10s → 30s → 1m → 5m (cap).
func (f *Forwarder) sleepBackoff(ctx context.Context, attempt int) {
	delays := []time.Duration{
		5 * time.Second,
		10 * time.Second,
		30 * time.Second,
		1 * time.Minute,
		5 * time.Minute,
	}
	idx := max(attempt-1, 0)
	if idx >= len(delays) {
		idx = len(delays) - 1
	}
	base := delays[idx]
	jitter := time.Duration(float64(base) * 0.2 * (2*rand.Float64() - 1))
	wait := max(base+jitter, time.Second)

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-f.stopCh:
	case <-timer.C:
	}
}

// QueueDepth returns the current queue length (mainly useful for tests/metrics).
func (f *Forwarder) QueueDepth() int { return len(f.queue) }

// DroppedTotal returns the cumulative dropped-entry counter.
func (f *Forwarder) DroppedTotal() int64 { return f.dropped.Load() }
