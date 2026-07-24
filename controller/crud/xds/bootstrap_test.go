package xds

import (
	"context"
	"encoding/json"
	"testing"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
)

// Real HasMetadata stub produced by the UI for a downstream_connections
// resource monitor named "t_conn" (taken verbatim from a live bootstrap).
const tConnStub = "eyJuYW1lIjoidF9jb25uIiwiY2Fub25pY2FsX25hbWUiOiJlbnZveS5yZXNvdXJjZV9tb25pdG9ycy5kb3duc3RyZWFtX2Nvbm5lY3Rpb25zIiwiZ3R5cGUiOiJlbnZveS5leHRlbnNpb25zLnJlc291cmNlX21vbml0b3JzLmRvd25zdHJlYW1fY29ubmVjdGlvbnMudjMuRG93bnN0cmVhbUNvbm5lY3Rpb25zQ29uZmlnIiwidHlwZSI6InJlc291cmNlX21vbml0b3IiLCJjYXRlZ29yeSI6ImVudm95LnJlc291cmNlX21vbml0b3JzIiwiY29sbGVjdGlvbiI6ImV4dGVuc2lvbnMiLCJ2ZXJzaW9uIjoidjEuMzkuMCJ9"

func newBootstrapResource(resource primitive.M) *models.DBResource {
	d := &models.DBResource{}
	d.SetResource(resource)
	return d
}

func TestDecodeBase64Config_RealStub(t *testing.T) {
	tc, err := resources.DecodeBase64Config(tConnStub)
	if err != nil {
		t.Fatalf("DecodeBase64Config failed: %v", err)
	}
	if tc.Name != "t_conn" {
		t.Errorf("Name = %q, want t_conn", tc.Name)
	}
	if tc.Collection != "extensions" {
		t.Errorf("Collection = %q, want extensions", tc.Collection)
	}
	if string(tc.Gtype) != "envoy.extensions.resource_monitors.downstream_connections.v3.DownstreamConnectionsConfig" {
		t.Errorf("unexpected Gtype: %q", tc.Gtype)
	}
}

func TestShouldSkipCollection_ResourceMonitors(t *testing.T) {
	bc := &BootstrapCollector{}

	cases := []struct {
		name      string
		bootstrap primitive.M
		wantSkip  bool
	}{
		{"no overload_manager", primitive.M{}, true},
		{"overload_manager without monitors", primitive.M{"overload_manager": primitive.M{"refresh_interval": "0.25s"}}, true},
		{"empty monitors array", primitive.M{"overload_manager": primitive.M{"resource_monitors": primitive.A{}}}, true},
		{"one monitor", primitive.M{"overload_manager": primitive.M{"resource_monitors": primitive.A{primitive.M{"name": "x"}}}}, false},
	}

	for _, c := range cases {
		skip, err := bc.shouldSkipCollection(newBootstrapResource(c.bootstrap), "resource_monitors")
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", c.name, err)
		}
		if skip != c.wantSkip {
			t.Errorf("%s: skip = %v, want %v", c.name, skip, c.wantSkip)
		}
	}
}

func TestShouldSkipCollection_DNSResolver(t *testing.T) {
	bc := &BootstrapCollector{}

	skip, err := bc.shouldSkipCollection(newBootstrapResource(primitive.M{}), "dns_resolver")
	if err != nil || !skip {
		t.Errorf("absent resolver: skip = %v, err = %v; want skip=true", skip, err)
	}

	skip, err = bc.shouldSkipCollection(newBootstrapResource(primitive.M{
		"typed_dns_resolver_config": primitive.M{"name": "envoy.network.dns_resolver.cares"},
	}), "dns_resolver")
	if err != nil || skip {
		t.Errorf("present resolver: skip = %v, err = %v; want skip=false", skip, err)
	}
}

// Entries that are not HasMetadata stubs must pass through unchanged and in
// order — none of them touch the DB, so a zero-value handler is safe.
func TestCollectResourceMonitors_PassThroughPreservesOrder(t *testing.T) {
	inlineMonitor := primitive.M{
		"name": "envoy.resource_monitors.fixed_heap",
		"typed_config": primitive.M{
			"@type":               "type.googleapis.com/envoy.extensions.resource_monitors.fixed_heap.v3.FixedHeapConfig",
			"max_heap_size_bytes": "1073741824",
		},
	}
	bareMonitor := primitive.M{"name": "envoy.resource_monitors.global_downstream_max_connections"}

	resource := newBootstrapResource(primitive.M{
		"overload_manager": primitive.M{
			"refresh_interval":  "0.25s",
			"actions":           primitive.A{primitive.M{"name": "envoy.overload_actions.shrink_heap"}},
			"resource_monitors": primitive.A{inlineMonitor, bareMonitor},
		},
	})

	handler := &AppHandler{}
	result, err := handler.collectResourceMonitors(context.Background(), resource, models.RequestDetails{}, "v1.39.0")
	if err != nil {
		t.Fatalf("collectResourceMonitors failed: %v", err)
	}

	bootstrapMap := result.GetResource().(primitive.M)
	om := bootstrapMap["overload_manager"].(primitive.M)

	if om["refresh_interval"] != "0.25s" {
		t.Errorf("refresh_interval sibling lost: %v", om["refresh_interval"])
	}
	if _, ok := om["actions"]; !ok {
		t.Errorf("actions sibling lost")
	}

	monitors := om["resource_monitors"].([]any)
	if len(monitors) != 2 {
		t.Fatalf("monitors length = %d, want 2", len(monitors))
	}
	if m := monitors[0].(primitive.M); m["name"] != "envoy.resource_monitors.fixed_heap" {
		t.Errorf("order not preserved, monitors[0] = %v", m["name"])
	}
	if m := monitors[1].(primitive.M); m["name"] != "envoy.resource_monitors.global_downstream_max_connections" {
		t.Errorf("order not preserved, monitors[1] = %v", m["name"])
	}
}

func TestCollectDNSResolver_InlinePassThrough(t *testing.T) {
	inline := primitive.M{
		"name": "envoy.network.dns_resolver.cares",
		"typed_config": primitive.M{
			"@type": "type.googleapis.com/envoy.extensions.network.dns_resolver.cares.v3.CaresDnsResolverConfig",
		},
	}
	resource := newBootstrapResource(primitive.M{"typed_dns_resolver_config": inline})

	handler := &AppHandler{}
	result, err := handler.collectDNSResolver(context.Background(), resource, models.RequestDetails{}, "v1.39.0")
	if err != nil {
		t.Fatalf("collectDNSResolver failed: %v", err)
	}

	got := result.GetResource().(primitive.M)["typed_dns_resolver_config"].(primitive.M)
	if got["name"] != "envoy.network.dns_resolver.cares" {
		t.Errorf("inline resolver mutated: %v", got)
	}
}

func TestResourceMonitorCanonicalName_MatchesEnvoyRegistry(t *testing.T) {
	// Validated against envoyproxy/envoy: the factory is registered under the
	// legacy runtime-key name, not the proto package name.
	got := models.ResourceMonitorDownstreamMax.CanonicalName()
	if got != "envoy.resource_monitors.global_downstream_max_connections" {
		t.Errorf("CanonicalName = %q, want envoy.resource_monitors.global_downstream_max_connections", got)
	}
}

func TestSetBootstrapResourceMonitors_PreservesSiblings(t *testing.T) {
	resource := newBootstrapResource(primitive.M{
		"overload_manager": primitive.M{
			"refresh_interval":  "0.25s",
			"resource_monitors": primitive.A{primitive.M{"old": true}},
		},
	})

	resource.SetBootstrapResourceMonitors([]any{models.TC{Name: "new"}})

	om := resource.GetResource().(primitive.M)["overload_manager"].(primitive.M)
	if om["refresh_interval"] != "0.25s" {
		t.Errorf("sibling field lost: %v", om)
	}
	monitors := om["resource_monitors"].([]any)
	if len(monitors) != 1 || monitors[0].(models.TC).Name != "new" {
		t.Errorf("monitors not replaced: %v", monitors)
	}
}

// The resolved TC must serialize into the exact JSON shape Envoy expects for
// overload_manager.resource_monitors entries.
func TestTCSerializationShape(t *testing.T) {
	tc := models.TC{
		Name: "envoy.resource_monitors.global_downstream_max_connections",
		TypedConfig: map[string]any{
			"@type":                             "type.googleapis.com/envoy.extensions.resource_monitors.downstream_connections.v3.DownstreamConnectionsConfig",
			"max_active_downstream_connections": "1000",
		},
	}

	raw, err := json.Marshal(tc)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if out["name"] != "envoy.resource_monitors.global_downstream_max_connections" {
		t.Errorf("name key wrong: %s", raw)
	}
	typed, ok := out["typed_config"].(map[string]any)
	if !ok {
		t.Fatalf("typed_config missing: %s", raw)
	}
	if typed["@type"] != "type.googleapis.com/envoy.extensions.resource_monitors.downstream_connections.v3.DownstreamConnectionsConfig" {
		t.Errorf("@type wrong: %s", raw)
	}
	if _, hasTypeURL := typed["type_url"]; hasTypeURL {
		t.Errorf("wire-form type_url leaked into JSON: %s", raw)
	}
}
