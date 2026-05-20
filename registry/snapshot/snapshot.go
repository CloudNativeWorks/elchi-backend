// Package snapshot mirrors the registry's in-memory routing state to MongoDB.
// The active leader periodically writes a single document; standby instances
// poll and reload it so they stay warm. On failover the new leader starts
// with state at most ~snapshot_interval old (5 minutes by default), then
// catches up via the regular controller / control-plane heartbeats.
package snapshot

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/registry/models"
	"github.com/CloudNativeWorks/elchi-backend/registry/storage"
)

const (
	// CollectionName is the MongoDB collection holding the singleton snapshot.
	CollectionName = "registry_snapshot"
	// DocumentID is the fixed _id of the snapshot document.
	DocumentID = "current"

	// DefaultWriteInterval is how often the leader persists state.
	// Was 5m; lowered to 30s for two reasons:
	//   1. UI / GetRegistryDataFromSnapshot serve the snapshot doc, so
	//      this interval is the upper bound on observable staleness
	//      (hostname rename, new client registration, etc.). 5 min made
	//      the registry-data view look broken after every restart.
	//   2. It is symmetric with DefaultPollInterval — a standby that
	//      reads every 30 s but a leader that writes every 5 min means
	//      9 out of 10 reads return the same already-cached doc; only
	//      the 1-in-10 read sees fresh data. 30/30 is honest.
	// The cost is one tiny ReplaceOne per 30 s on a singleton doc; on
	// a 3-node deployment that's <1% of MongoDB write capacity.
	DefaultWriteInterval = 30 * time.Second
	// DefaultPollInterval is how often standbys reload state.
	DefaultPollInterval = 30 * time.Second
)

// Document is the on-disk representation. One row, fixed _id="current".
type Document struct {
	ID             string                  `bson:"_id"`
	SnapshotAt     primitive.DateTime      `bson:"snapshot_at"`
	WriterID       string                  `bson:"writer_id"`
	Controllers    []*models.ControllerInfo `bson:"controllers"`
	ClientMappings []*models.ClientMapping  `bson:"client_mappings"`
	ControlPlanes  []*models.ControlPlane   `bson:"control_planes"`
	NodeMappings   []*models.NodeMapping    `bson:"node_mappings"`
}

// Snapshotter persists/loads the registry's two in-memory stores.
type Snapshotter struct {
	db        *mongo.Database
	ctrlStore *storage.InMemoryStorage
	cpStore   *storage.InMemoryRoutingStorage
	writerID  string
	logger    *logger.Logger

	stopMu     sync.Mutex
	stopWriter chan struct{}
	stopReader chan struct{}

	// dirtyCh receives a signal whenever a state-changing RPC mutates one
	// of the in-memory stores (Register/Delete/UpdateClientList etc.).
	// The writer loop drains it and triggers an immediate Save so UI reads
	// (which go straight to the snapshot doc) reflect the change without
	// having to wait for the next 30s tick. Buffered=1 + non-blocking
	// send in Touch() debounces bursts — multiple changes within one tick
	// coalesce into a single Save.
	dirtyCh chan struct{}
}

// New constructs a Snapshotter. writerID is used to mark snapshots in the
// document so we can spot stuck writers in MongoDB.
func New(db *mongo.Database, ctrlStore *storage.InMemoryStorage, cpStore *storage.InMemoryRoutingStorage, writerID string, log *logger.Logger) *Snapshotter {
	return &Snapshotter{
		db:        db,
		ctrlStore: ctrlStore,
		cpStore:   cpStore,
		writerID:  writerID,
		logger:    log,
		dirtyCh:   make(chan struct{}, 1),
	}
}

// Touch signals the writer loop that the in-memory state changed and a
// fresh Save is wanted as soon as possible. Safe to call from any RPC
// handler; non-blocking — if a signal is already pending it is coalesced
// with this one (the Save the writer is about to do covers both).
// On standbys (no active writer) Touch is a no-op aside from filling the
// 1-slot buffer; the buffer is drained whenever a writer eventually
// starts via the immediate Save in writerLoop.
func (s *Snapshotter) Touch() {
	select {
	case s.dirtyCh <- struct{}{}:
	default:
	}
}

// Save writes the current in-memory state to MongoDB as a single document.
func (s *Snapshotter) Save(ctx context.Context) error {
	controllers, clientMappings := s.ctrlStore.ExportAll()
	cps, nodeMappings := s.cpStore.ExportAll()
	doc := Document{
		ID:             DocumentID,
		SnapshotAt:     primitive.NewDateTimeFromTime(time.Now()),
		WriterID:       s.writerID,
		Controllers:    controllers,
		ClientMappings: clientMappings,
		ControlPlanes:  cps,
		NodeMappings:   nodeMappings,
	}
	col := s.db.Collection(CollectionName)
	_, err := col.ReplaceOne(ctx, bson.M{"_id": DocumentID}, doc, options.Replace().SetUpsert(true))
	if err != nil {
		return err
	}
	s.logger.Debugf("registry snapshot: saved (controllers=%d clients=%d cps=%d nodes=%d)",
		len(controllers), len(clientMappings), len(cps), len(nodeMappings))
	return nil
}

// Load reads the latest snapshot and overwrites the in-memory stores.
// No-op (no error) if the snapshot document does not exist yet.
func (s *Snapshotter) Load(ctx context.Context) error {
	col := s.db.Collection(CollectionName)
	var doc Document
	err := col.FindOne(ctx, bson.M{"_id": DocumentID}).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			s.logger.Debugf("registry snapshot: no document yet (cold cluster)")
			return nil
		}
		return err
	}
	s.ctrlStore.ImportAll(doc.Controllers, doc.ClientMappings)
	s.cpStore.ImportAll(doc.ControlPlanes, doc.NodeMappings)
	age := time.Since(doc.SnapshotAt.Time())
	s.logger.Infof("registry snapshot: loaded (age=%s controllers=%d clients=%d cps=%d nodes=%d writer=%s)",
		age.Round(time.Second), len(doc.Controllers), len(doc.ClientMappings),
		len(doc.ControlPlanes), len(doc.NodeMappings), doc.WriterID)
	return nil
}

// StartWriter runs Save() at every interval until the returned stop func is
// called or ctx is canceled. Idempotent: calling again replaces the loop.
func (s *Snapshotter) StartWriter(ctx context.Context, interval time.Duration) {
	s.stopMu.Lock()
	if s.stopWriter != nil {
		close(s.stopWriter)
	}
	stop := make(chan struct{})
	s.stopWriter = stop
	s.stopMu.Unlock()

	if interval <= 0 {
		interval = DefaultWriteInterval
	}
	go s.writerLoop(ctx, stop, interval)
}

// StopWriter halts the writer loop if running.
func (s *Snapshotter) StopWriter() {
	s.stopMu.Lock()
	if s.stopWriter != nil {
		close(s.stopWriter)
		s.stopWriter = nil
	}
	s.stopMu.Unlock()
}

// StartReader runs Load() at every interval until the returned stop func is
// called or ctx is canceled. Idempotent.
func (s *Snapshotter) StartReader(ctx context.Context, interval time.Duration) {
	s.stopMu.Lock()
	if s.stopReader != nil {
		close(s.stopReader)
	}
	stop := make(chan struct{})
	s.stopReader = stop
	s.stopMu.Unlock()

	if interval <= 0 {
		interval = DefaultPollInterval
	}
	// Pull state once immediately so a fresh standby is warm right away.
	if err := s.Load(ctx); err != nil {
		s.logger.Warnf("registry snapshot: initial load failed: %v", err)
	}
	go s.readerLoop(ctx, stop, interval)
}

// StopReader halts the reader loop if running.
func (s *Snapshotter) StopReader() {
	s.stopMu.Lock()
	if s.stopReader != nil {
		close(s.stopReader)
		s.stopReader = nil
	}
	s.stopMu.Unlock()
}

func (s *Snapshotter) writerLoop(ctx context.Context, stop <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// Write once immediately so failover state is fresh shortly after promotion.
	// Then drain ALL stale dirtyCh signals that may have piled up while the
	// writer was stopped (the buffer is size-1 so at most one survives, but
	// the loop is the same code irrespective of buffer size — staying
	// defensive in case anyone bumps the buffer later or a future caller
	// fans Touch out from multiple goroutines).
	if err := s.Save(ctx); err != nil {
		s.logger.Warnf("registry snapshot: initial save failed: %v", err)
	}
drainStale:
	for {
		select {
		case <-s.dirtyCh:
		default:
			break drainStale
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			if err := s.Save(ctx); err != nil {
				s.logger.Errorf("registry snapshot: save failed: %v", err)
			}
		case <-s.dirtyCh:
			// State-changing RPC fired; persist immediately so UI reads
			// see the change in the next request rather than waiting
			// up to `interval` for the next ticker fire. This is the
			// difference between "Controller registered" log line and
			// "Controllers: 1 active" appearing in the UI on refresh.
			if err := s.Save(ctx); err != nil {
				s.logger.Errorf("registry snapshot: dirty save failed: %v", err)
			}
		}
	}
}

func (s *Snapshotter) readerLoop(ctx context.Context, stop <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			if err := s.Load(ctx); err != nil {
				s.logger.Errorf("registry snapshot: load failed: %v", err)
			}
		}
	}
}
