package db

import (
	"testing"

	cluster "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/cluster/v3"
	ext_proc "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/http/ext_proc/v3"
	"google.golang.org/protobuf/encoding/protojson"
)

// TestDefaultElchiShieldExtProcValidProto verifies the ext_proc config seeded by
// CreateDefaultElchiShieldExtProc actually decodes into Envoy's ExternalProcessor
// message for the pinned control-plane version — catching a wrong field name,
// enum value, or a deprecated/removed field that go build/vet can't (the seed is
// a free-form bson.M). The JSON below MUST mirror that bson.M exactly.
func TestDefaultElchiShieldExtProcValidProto(t *testing.T) {
	const j = `{
		"grpc_service": {"envoy_grpc": {"cluster_name": "elchi-shield"}},
		"failure_mode_allow": true,
		"allow_mode_override": true,
		"message_timeout": "1s",
		"request_attributes": ["xds.node.id"],
		"processing_mode": {
			"request_header_mode": "SEND",
			"response_header_mode": "SEND",
			"request_body_mode": "NONE",
			"response_body_mode": "NONE"
		}
	}`
	var m ext_proc.ExternalProcessor
	if err := protojson.Unmarshal([]byte(j), &m); err != nil {
		t.Fatalf("elchi-shield ext_proc default is not a valid ExternalProcessor proto: %v", err)
	}
	if m.GetGrpcService().GetEnvoyGrpc().GetClusterName() != "elchi-shield" {
		t.Errorf("grpc_service cluster_name not parsed: %q", m.GetGrpcService().GetEnvoyGrpc().GetClusterName())
	}
	if !m.GetFailureModeAllow() {
		t.Error("failure_mode_allow must be true (fail-open)")
	}
	if !m.GetAllowModeOverride() {
		t.Error("allow_mode_override must be true (shield requests the body dynamically)")
	}
	if len(m.GetRequestAttributes()) != 1 || m.GetRequestAttributes()[0] != "xds.node.id" {
		t.Errorf("request_attributes must carry the node id: %v", m.GetRequestAttributes())
	}
}

// TestDefaultElchiShieldClusterValidProto verifies the STATIC + UDS-pipe cluster
// seeded by CreateDefaultElchiShieldCluster decodes into Envoy's Cluster message.
// typed_extension_protocol_options carries an elchi base64 reference that is
// resolved BEFORE Envoy sees it, so it's omitted here; the rest (type + pipe
// endpoint) is what this guards.
func TestDefaultElchiShieldClusterValidProto(t *testing.T) {
	const j = `{
		"name": "elchi-shield",
		"type": "STATIC",
		"connect_timeout": "1s",
		"load_assignment": {
			"cluster_name": "elchi-shield",
			"endpoints": [
				{"lb_endpoints": [
					{"endpoint": {"address": {"pipe": {"path": "/run/elchi-shield/extproc.sock"}}}}
				]}
			]
		}
	}`
	var c cluster.Cluster
	if err := protojson.Unmarshal([]byte(j), &c); err != nil {
		t.Fatalf("elchi-shield cluster default is not a valid Cluster proto: %v", err)
	}
	if c.GetType() != cluster.Cluster_STATIC {
		t.Errorf("cluster type must be STATIC, got %v", c.GetType())
	}
	ep := c.GetLoadAssignment().GetEndpoints()
	if len(ep) != 1 || len(ep[0].GetLbEndpoints()) != 1 {
		t.Fatalf("expected one lb endpoint, got %v", ep)
	}
	if p := ep[0].GetLbEndpoints()[0].GetEndpoint().GetAddress().GetPipe().GetPath(); p != "/run/elchi-shield/extproc.sock" {
		t.Errorf("UDS pipe path not parsed: %q", p)
	}
}
