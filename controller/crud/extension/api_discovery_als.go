package extension

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
)

// elchiALSExtensionName is the document name of the per-project
// HttpGrpcAccessLogConfig extension seeded by CreateDefaultElchiALS.
// It is the resolution target of the access_log reference injected
// below.
const elchiALSExtensionName = "elchi-als"

// applyAPIDiscoveryALS keeps an HCM document's access_log[] in sync
// with the general.api_discovery toggle.
//
// api_discovery is a one-click "ship my logs to elchi-collector"
// switch. When enabled, the HCM must carry an access_log entry that
// references the project's `elchi-als` extension; the entry uses the
// exact same base64 typed_config-reference shape the frontend emits
// for any other access logger, so the existing snapshot resolver
// (processTypedConfigPath) and DecodeSetTypedConfigs pick it up with
// no special-casing — the reference both reaches Envoy AND shows up
// in general.typed_config like a normal logger.
//
// Persisting the reference (rather than injecting at snapshot time)
// means the entry is visible in the UI, survives across saves, and is
// cleanly removed when the toggle is turned off.
//
// No-op for non-HCM extensions. Idempotent: an existing elchi-als
// entry is always stripped first, then re-added only when enabled, so
// repeated saves never duplicate it.
//
// Fail-open: if api_discovery is enabled but the project has no
// `elchi-als` extension (e.g. an old project predating the default),
// the reference is NOT added — adding a dangling reference would break
// listener snapshot generation. The toggle still persists; re-saving
// the HCM once the extension exists wires it up.
func applyAPIDiscoveryALS(ctx context.Context, resource models.ResourceClass, dbCtx *db.AppContext, log *logger.Logger) {
	general := resource.GetGeneral()
	if general.GType != models.HTTPConnectionManager {
		return
	}

	hcmMap, ok := asStringMap(resource.GetResource())
	if !ok {
		log.Warnf("api_discovery: HCM resource is not a JSON object; skipping elchi-als sync")
		return
	}

	// Drop any pre-existing elchi-als entry — re-added below if enabled.
	existing := toAnySlice(hcmMap["access_log"])
	accessLogs := make([]any, 0, len(existing)+1)
	for _, al := range existing {
		if isELCHIALSEntry(al) {
			continue
		}
		accessLogs = append(accessLogs, al)
	}

	if general.APIDiscovery {
		if _, err := resources.GetResourceNGeneral(ctx, dbCtx, "extensions", elchiALSExtensionName, general.Project, general.Version); err != nil {
			log.Warnf("api_discovery: elchi-als extension missing for project=%s version=%s; access_log reference not added: %v", general.Project, general.Version, err)
		} else {
			accessLogs = append(accessLogs, buildELCHIALSAccessLogEntry(general.Version))
		}
	}

	if len(accessLogs) == 0 {
		delete(hcmMap, "access_log")
	} else {
		hcmMap["access_log"] = accessLogs
	}
	resource.SetResource(hcmMap)
}

// buildELCHIALSAccessLogEntry returns an access_log[] element that
// references the `elchi-als` extension via a base64-encoded
// typed_config pointer — identical in shape to what the frontend
// produces for operator-defined access loggers.
func buildELCHIALSAccessLogEntry(version string) map[string]any {
	gt := models.HTTPGRPCAccessLog
	ref := map[string]any{
		"name":           elchiALSExtensionName,
		"canonical_name": gt.CanonicalName(),
		"gtype":          gt.String(),
		"type":           gt.Type(),
		"category":       gt.Category(),
		"collection":     gt.CollectionString(),
		"version":        version,
	}
	raw, _ := json.Marshal(ref)
	return map[string]any{
		"name": gt.CanonicalName(),
		"typed_config": map[string]any{
			"type_url": gt.String(),
			"value":    base64.StdEncoding.EncodeToString(raw),
		},
	}
}

// isELCHIALSEntry reports whether an access_log[] element is the
// elchi-als reference, by decoding its base64 typed_config pointer and
// matching the referenced extension name.
func isELCHIALSEntry(entry any) bool {
	m, ok := asStringMap(entry)
	if !ok {
		return false
	}
	tc, ok := asStringMap(m["typed_config"])
	if !ok {
		return false
	}
	val, ok := tc["value"].(string)
	if !ok || val == "" {
		return false
	}
	decoded, err := resources.DecodeBase64Config(val)
	if err != nil || decoded == nil {
		return false
	}
	return decoded.Name == elchiALSExtensionName
}

// asStringMap normalizes the two map encodings the resource payload
// can arrive as — map[string]any from a JSON request body, primitive.M
// from a BSON-decoded document.
func asStringMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case primitive.M:
		return map[string]any(m), true
	}
	return nil, false
}

// toAnySlice normalizes the two slice encodings (JSON []any vs BSON
// primitive.A); returns nil for a missing or non-array value.
func toAnySlice(v any) []any {
	switch s := v.(type) {
	case []any:
		return s
	case primitive.A:
		return []any(s)
	}
	return nil
}
