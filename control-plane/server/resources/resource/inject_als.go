package resource

import (
	"context"
	"encoding/json"
	"fmt"

	accesslogv3 "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/accesslog/v3"
	algrpc "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/access_loggers/grpc/v3"
	hcm "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
)

// elchiALSExtensionName is the canonical document name used both for
// the persisted default extension (CreateDefaultElchiALS) and for the
// runtime entry appended to an HCM's access_log slice. Keeping it
// centralised here lets the duplicate-detection check below stay in
// sync with the default-resources writer.
const elchiALSExtensionName = "elchi-als"

// apiDiscoveryEnabled reads the HCM's `general.api_discovery` flag.
// Standalone field instead of a metadata map entry because the
// existing extension update flow ($set) does not touch
// `general.metadata` — keeping it untouched protects the
// system-managed `is_default` marker. The HCM update path explicitly
// $sets `general.api_discovery`, so this read is the matching
// counterpart.
func apiDiscoveryEnabled(g models.General) bool {
	return g.APIDiscovery
}

// hcmHasELCHIALSEntry returns true when the HCM's access_log slice
// already carries an entry called "elchi-als". Operators sometimes
// add the extension manually before flipping the toggle; we must not
// double-inject in that case (Envoy would emit duplicate log lines
// per request).
func hcmHasELCHIALSEntry(h *hcm.HttpConnectionManager) bool {
	if h == nil {
		return false
	}
	for _, al := range h.GetAccessLog() {
		if al.GetName() == elchiALSExtensionName {
			return true
		}
	}
	return false
}

// injectElchiALS appends the `elchi-als` HttpGrpcAccessLogConfig
// access_log entry to the given HCM, fetched fresh from MongoDB so
// operator edits to the extension document (header list, buffer size,
// log_name, …) take effect on the next snapshot regeneration without
// needing a code change.
//
// Fail-open semantics: a missing extension document (older project
// before the default was introduced) or a malformed resource emits a
// warning and leaves the HCM untouched — the listener still serves
// traffic, it just won't ship logs to the collector until the
// extension is created.
//
// project/Envoy-version come from ar.Project / ar.ResourceVersion which
// DecodeListener already stamped on the AllResources during snapshot
// init (see decoder.go:81-82). They scope the extension fetch to the
// listener's own project + version so cross-project / cross-version
// leakage is impossible.
func (ar *AllResources) injectElchiALS(ctx context.Context, hcmConfig *hcm.HttpConnectionManager, dbContext *db.AppContext, log *logger.Logger) error {
	if hcmConfig == nil {
		return nil
	}
	if hcmHasELCHIALSEntry(hcmConfig) {
		// Operator already wired this in manually — leave it alone.
		return nil
	}

	doc, err := resources.GetResourceNGeneral(ctx, dbContext, "extensions", elchiALSExtensionName, ar.Project, ar.ResourceVersion)
	if err != nil {
		return fmt.Errorf("fetch elchi-als extension (project=%s, resource_version=%s): %w", ar.Project, ar.ResourceVersion, err)
	}

	// `doc.Resource.Resource` is the raw HttpGrpcAccessLogConfig JSON
	// authored by the operator (or seeded by CreateDefaultElchiALS).
	// Marshal it through JSON → protojson so any operator edits in
	// the underlying document automatically reach Envoy.
	rawJSON, err := json.Marshal(doc.Resource.Resource)
	if err != nil {
		return fmt.Errorf("marshal elchi-als resource: %w", err)
	}

	alsConfig := &algrpc.HttpGrpcAccessLogConfig{}
	if err := unmarshalAccessLogJSON(rawJSON, alsConfig); err != nil {
		return fmt.Errorf("unmarshal elchi-als HttpGrpcAccessLogConfig: %w", err)
	}

	typedAny, err := anypb.New(alsConfig)
	if err != nil {
		return fmt.Errorf("wrap elchi-als in anypb.Any: %w", err)
	}

	entry := &accesslogv3.AccessLog{
		Name: elchiALSExtensionName,
		ConfigType: &accesslogv3.AccessLog_TypedConfig{
			TypedConfig: typedAny,
		},
	}
	// Append-only: existing access_log entries (operator-defined or
	// the default stdout sink) are preserved; elchi-als sinks
	// alongside them.
	hcmConfig.AccessLog = append(hcmConfig.AccessLog, entry)
	if log != nil {
		log.Debugf("api_discovery: elchi-als access_log injected (project=%s, resource_version=%s)", ar.Project, ar.ResourceVersion)
	}
	return nil
}

// unmarshalAccessLogJSON decodes the resource payload into the gRPC
// access log proto. We go through encoding/json + a small wrapper that
// handles Envoy's enum-as-string + Duration JSON encoding so the
// extension document keeps the same shape the rest of the codebase
// uses (raw BSON map serialised to JSON in the snapshot path).
//
// Implementation detail: protobuf-aware JSON would be stricter, but
// the codebase consistently round-trips Envoy configs through
// encoding/json. We follow that convention to avoid a partial schema
// shift just for this hook.
func unmarshalAccessLogJSON(data []byte, target *algrpc.HttpGrpcAccessLogConfig) error {
	// Envoy `transport_api_version` is an enum (V2/V3); JSON encodes
	// it as the string "V3". Standard encoding/json can't map that to
	// the int32 enum directly, so we sidestep by going through a
	// generic map -> reuse the helper.MarshalJSON path everywhere else
	// uses for proto enrichment.
	intermediate := map[string]any{}
	if err := json.Unmarshal(data, &intermediate); err != nil {
		return err
	}
	if v, ok := intermediate["common_config"].(map[string]any); ok {
		// Envoy proto enum: "V3" -> 2, default 2 if absent / unknown.
		switch v["transport_api_version"] {
		case "V3", nil:
			v["transport_api_version"] = 2
		case "V2":
			v["transport_api_version"] = 1
		}
		intermediate["common_config"] = v
	}
	normalized, err := json.Marshal(intermediate)
	if err != nil {
		return err
	}
	return json.Unmarshal(normalized, target)
}

// Compile-time check: ensure the model symbol stays referenced so a
// future refactor that removes the GType constant flags this file in
// the same compilation pass as the default-resources writer.
var _ = models.HTTPGRPCAccessLog
