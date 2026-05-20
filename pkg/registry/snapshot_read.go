package registry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	registrymodels "github.com/CloudNativeWorks/elchi-backend/registry/models"
)

// snapshotCollection / snapshotDocID mirror the constants in
// registry/snapshot/snapshot.go. Duplicated here so the controller can
// read the snapshot without dragging in the registry package's in-memory
// stores; the cost is a single-doc/fixed-id contract to keep in sync.
const (
	snapshotCollection = "registry_snapshot"
	snapshotDocID      = "current"
)

// ErrSnapshotNotFound signals a missing snapshot doc — e.g. fresh cluster
// where no leader has written one yet. Caller should fall back to the
// live gRPC aggregation path.
var ErrSnapshotNotFound = errors.New("registry snapshot not found")

// snapshotDoc decodes the persisted singleton from MongoDB. Bson tags
// match the fields written by registry/snapshot/snapshot.go::Save.
type snapshotDoc struct {
	ID             string                           `bson:"_id"`
	SnapshotAt     time.Time                        `bson:"snapshot_at"`
	WriterID       string                           `bson:"writer_id"`
	Controllers    []*registrymodels.ControllerInfo `bson:"controllers"`
	ClientMappings []*registrymodels.ClientMapping  `bson:"client_mappings"`
	ControlPlanes  []*registrymodels.ControlPlane   `bson:"control_planes"`
	NodeMappings   []*registrymodels.NodeMapping    `bson:"node_mappings"`
}

// GetRegistryDataFromSnapshot reads the registry's singleton snapshot
// document from MongoDB and assembles a response identical in shape to
// the gRPC-aggregated version (so the UI does not change). The single
// FindOne returns an atomic view across controllers + control planes +
// mappings — no "Frankenstein" mix the way two parallel gRPC calls into
// different registry instances can produce (each one returning its own
// in-memory subset).
//
// Returns ErrSnapshotNotFound when the doc has never been written;
// callers should fall back to the live gRPC path so admin diagnostics
// still work during a cold start.
func (r *RegistryClient) GetRegistryDataFromSnapshot(ctx context.Context, db *mongo.Database) (map[string]any, time.Duration, error) {
	if db == nil {
		return nil, 0, fmt.Errorf("mongo database not available")
	}
	var doc snapshotDoc
	err := db.Collection(snapshotCollection).
		FindOne(ctx, bson.M{"_id": snapshotDocID}).
		Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, 0, ErrSnapshotNotFound
		}
		return nil, 0, fmt.Errorf("read registry snapshot: %w", err)
	}
	age := time.Since(doc.SnapshotAt)

	// Group the flat mappings into the per-parent maps the gRPC response
	// shape uses (matches bridge.ClientsData{clients:[]} / NodesData{nodes:[]}).
	// Pre-allocate with one entry per controller / control plane so the
	// caller sees keys even for parents with zero children — same as the
	// live gRPC aggregation, which initialises an empty entry from the
	// parent set.
	clientsByController := make(map[string]any, len(doc.Controllers))
	for _, c := range doc.Controllers {
		clientsByController[c.ID] = map[string]any{
			"clients": clientsForController(c.ID, doc.ClientMappings),
		}
	}
	nodesByControlPlane := make(map[string]any, len(doc.ControlPlanes))
	for _, cp := range doc.ControlPlanes {
		nodesByControlPlane[cp.ID] = map[string]any{
			"nodes": nodesForControlPlane(cp.ID, doc.NodeMappings),
		}
	}

	out := map[string]any{
		"message": "Full registry data retrieved successfully (from snapshot)",
		"status":  "connected",
		"controller_data": map[string]any{
			"controllers":           controllersOut(doc.Controllers),
			"clients_by_controller": clientsByController,
		},
		"control_plane_data": map[string]any{
			"control_planes":         controlPlanesOut(doc.ControlPlanes),
			"nodes_by_control_plane": nodesByControlPlane,
		},
		"client_info": map[string]any{
			"controller_id": r.controllerID,
			"version":       r.version,
			"grpc_address":  r.grpcAddress,
		},
		"registry_address": r.registryAddr,
		"timestamp":        time.Now().UTC(),
		// Snapshot-source metadata so the UI / operator can see exactly
		// how fresh this view is and which registry instance produced it.
		"source":             "snapshot",
		"snapshot_at":        doc.SnapshotAt,
		"snapshot_writer_id": doc.WriterID,
		"snapshot_age_ms":    age.Milliseconds(),
	}
	return out, age, nil
}

// protoTime mirrors the JSON shape encoding/json produces for the
// proto-generated google.protobuf.Timestamp — {seconds, nanos} — so the
// snapshot-sourced response is byte-compatible with the existing gRPC
// path that the UI was built against.
type protoTime struct {
	Seconds int64 `json:"seconds"`
	Nanos   int32 `json:"nanos"`
}

func toProtoTime(t time.Time) protoTime {
	return protoTime{Seconds: t.Unix(), Nanos: int32(t.Nanosecond())}
}

func controllersOut(in []*registrymodels.ControllerInfo) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, c := range in {
		if c == nil {
			continue
		}
		out = append(out, map[string]any{
			"controller_id": c.ID,
			"version":       c.Version,
			"http_address":  c.HTTPAddress,
			"last_seen":     toProtoTime(c.LastSeen),
		})
	}
	return out
}

func controlPlanesOut(in []*registrymodels.ControlPlane) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, cp := range in {
		if cp == nil {
			continue
		}
		out = append(out, map[string]any{
			"control_plane_id": cp.ID,
			"version":          cp.Version,
			"last_seen":        toProtoTime(cp.LastSeen),
		})
	}
	return out
}

func clientsForController(ctrlID string, mappings []*registrymodels.ClientMapping) []map[string]any {
	out := make([]map[string]any, 0)
	for _, m := range mappings {
		if m == nil || m.ControllerID != ctrlID {
			continue
		}
		out = append(out, map[string]any{
			"client_id": m.ClientID,
			"version":   m.Version,
			"last_seen": toProtoTime(m.LastSeen),
		})
	}
	return out
}

func nodesForControlPlane(cpID string, mappings []*registrymodels.NodeMapping) []map[string]any {
	out := make([]map[string]any, 0)
	for _, m := range mappings {
		if m == nil || m.ControlPlaneID != cpID {
			continue
		}
		out = append(out, map[string]any{
			"node_id":   m.NodeID,
			"version":   m.Version,
			"last_seen": toProtoTime(m.LastSeen),
		})
	}
	return out
}
