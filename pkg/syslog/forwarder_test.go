package syslog

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

type stubLoader struct {
	mu  sync.Mutex
	cfg *ForwarderConfig
	err error
}

func (s *stubLoader) set(cfg *ForwarderConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
}

func (s *stubLoader) LoadSyslogConfig(_ context.Context) (*ForwarderConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	if s.cfg == nil {
		return nil, nil
	}
	cp := *s.cfg
	return &cp, nil
}

func newDisabledForwarder() (*Forwarder, *stubLoader) {
	loader := &stubLoader{}
	f := NewForwarder(loader, nil)
	return f, loader
}

func TestForwarderEnqueueDropsWhenDisabled(t *testing.T) {
	f, _ := newDisabledForwarder()
	for range 100 {
		f.Enqueue(&models.AuditEntry{ID: "x"})
	}
	if depth := f.QueueDepth(); depth != 0 {
		t.Errorf("expected disabled forwarder to drop entries, queue depth=%d", depth)
	}
}

func TestForwarderEnqueueAcceptsWhenEnabled(t *testing.T) {
	f, _ := newDisabledForwarder()
	f.cfg.Store(&ForwarderConfig{Enabled: true, Protocol: ProtocolUDP, Host: "127.0.0.1", Port: 1})
	for range 5 {
		f.Enqueue(&models.AuditEntry{ID: "id", Action: "X"})
	}
	if depth := f.QueueDepth(); depth != 5 {
		t.Errorf("expected 5 queued, got %d", depth)
	}
}

func TestForwarderDropOldestOnFull(t *testing.T) {
	loader := &stubLoader{}
	f := &Forwarder{
		loader: loader,
		queue:  make(chan *models.AuditEntry, 4),
		stopCh: make(chan struct{}),
	}
	f.queueCap.Store(4)
	f.cfg.Store(&ForwarderConfig{Enabled: true})

	// Fill queue.
	for range 4 {
		f.Enqueue(&models.AuditEntry{ID: "fill"})
	}
	if depth := f.QueueDepth(); depth != 4 {
		t.Fatalf("expected queue to be full, got %d", depth)
	}

	// Pushing 3 more should drop oldest each time and keep depth bounded.
	for range 3 {
		f.Enqueue(&models.AuditEntry{ID: "new"})
	}
	if depth := f.QueueDepth(); depth > 4 {
		t.Errorf("queue exceeded cap: %d", depth)
	}
	if dropped := f.DroppedTotal(); dropped < 1 {
		t.Errorf("expected dropped > 0, got %d", dropped)
	}
}

func TestForwarderRefreshConfigSwapsClient(t *testing.T) {
	loader := &stubLoader{}
	f := NewForwarder(loader, nil)
	loader.set(&ForwarderConfig{Enabled: true, Protocol: ProtocolUDP, Host: "127.0.0.1", Port: 1})

	f.refreshConfig(context.Background())
	first := f.currentClient()
	if first == nil {
		t.Fatalf("expected client to be built after enabling config")
	}
	cfg1 := f.cfg.Load()
	if cfg1 == nil || !cfg1.Enabled || cfg1.Port != 1 {
		t.Fatalf("config not stored correctly: %+v", cfg1)
	}

	loader.set(&ForwarderConfig{Enabled: true, Protocol: ProtocolUDP, Host: "127.0.0.1", Port: 2})
	f.refreshConfig(context.Background())
	second := f.currentClient()
	if second == nil || second == first {
		t.Errorf("expected new client after config change")
	}

	loader.set(&ForwarderConfig{Enabled: false})
	f.refreshConfig(context.Background())
	if f.currentClient() != nil {
		t.Errorf("expected client to be torn down when disabled")
	}
}

func TestForwarderStopWithoutStartIsNoop(t *testing.T) {
	f, _ := newDisabledForwarder()
	done := make(chan struct{})
	go func() { f.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop on unstarted forwarder hung")
	}
}

func TestForwarderStopIsIdempotent(t *testing.T) {
	loader := &stubLoader{cfg: &ForwarderConfig{Enabled: false}}
	f := NewForwarder(loader, nil)
	f.Start(context.Background())

	// Two concurrent Stops must not panic on close(stopCh).
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.Stop()
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("concurrent Stop calls hung")
	}

	// A fourth synchronous call should be a no-op.
	f.Stop()
}

func TestForwarderConfigHashStableAcrossCallsIfUnchanged(t *testing.T) {
	cfg := &ForwarderConfig{Enabled: true, Host: "h", Port: 5}
	first := cfg.hash()
	second := cfg.hash()
	if first != second {
		t.Fatal("hash not stable for same config")
	}
	cfg2 := &ForwarderConfig{Enabled: true, Host: "h", Port: 6}
	if first == cfg2.hash() {
		t.Fatal("hash collision for differing port")
	}
}

func TestForwarderEnqueueNonNilSafe(t *testing.T) {
	f, _ := newDisabledForwarder()
	// Should not panic on nil entry.
	f.Enqueue(nil)
}
