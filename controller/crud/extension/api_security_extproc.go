package extension

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
)

// elchiShieldExtensionName is the document name of the per-project
// ExternalProcessor (ext_proc) filter extension seeded by
// CreateDefaultElchiShieldExtProc. It is the resolution target of the
// http_filters reference injected below.
const elchiShieldExtensionName = "elchi-shield"

// applyAPISecurityExtProc keeps an HCM document's http_filters[] in sync with the
// general.api_security toggle — the ext_proc/shield analog of applyAPIDiscoveryALS.
//
// api_security is a one-click "send this listener through the elchi-shield WAF"
// switch. When enabled, the HCM must carry an ext_proc http_filter that references
// the project's `elchi-shield` ExternalProcessor extension. The entry uses the
// exact same base64 typed_config-reference shape the frontend emits for any HTTP
// filter, so the existing snapshot resolver (processTypedConfigPath) and
// DecodeSetTypedConfigs pick it up with no special-casing.
//
// The ext_proc filter is inserted as the FIRST http_filter (before the router and
// before any other filters) so the WAF inspects requests before anything else.
// Persisting the reference (rather than injecting at snapshot time) keeps it
// visible in the UI, durable across saves, and cleanly removed when toggled off.
//
// No-op for non-HCM extensions. Idempotent: an existing elchi-shield ext_proc
// entry is always stripped first, then re-added (as the head) only when enabled,
// so repeated saves never duplicate it or change the ordering of other filters.
//
// Fail-open: if api_security is enabled but the project has no `elchi-shield`
// ext_proc extension (e.g. an old project predating the default), the reference is
// NOT added — a dangling reference would break listener snapshot generation. The
// toggle still persists; re-saving the HCM once the extension exists wires it up.
func applyAPISecurityExtProc(ctx context.Context, resource models.ResourceClass, dbCtx *db.AppContext, log *logger.Logger) {
	general := resource.GetGeneral()
	if general.GType != models.HTTPConnectionManager {
		return
	}

	hcmMap, ok := asStringMap(resource.GetResource())
	if !ok {
		log.Warnf("api_security: HCM resource is not a JSON object; skipping elchi-shield sync")
		return
	}

	// Drop any pre-existing elchi-shield ext_proc entry — re-added at the head below
	// if enabled. The remaining filters keep their relative order (router stays last).
	existing := toAnySlice(hcmMap["http_filters"])
	filters := make([]any, 0, len(existing)+1)
	for _, f := range existing {
		if isELCHIShieldEntry(f) {
			continue
		}
		filters = append(filters, f)
	}

	if general.APISecurity {
		if _, err := resources.GetResourceNGeneral(ctx, dbCtx, models.ExternalProcessor.CollectionString(), elchiShieldExtensionName, general.Project, general.Version); err != nil {
			log.Warnf("api_security: elchi-shield ext_proc extension missing for project=%s version=%s; http_filter reference not added: %v", general.Project, general.Version, err)
		} else {
			// Prepend so shield runs first (ahead of every other filter + the router).
			filters = append([]any{buildELCHIShieldExtProcFilter(general.Version)}, filters...)
		}
	}

	hcmMap["http_filters"] = filters
	resource.SetResource(hcmMap)
}

// buildELCHIShieldExtProcFilter returns an http_filters[] element that references
// the `elchi-shield` ext_proc extension via a base64-encoded typed_config pointer
// — identical in shape to what the frontend produces for any HTTP filter.
func buildELCHIShieldExtProcFilter(version string) map[string]any {
	gt := models.ExternalProcessor
	ref := map[string]any{
		"name":           elchiShieldExtensionName,
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

// isELCHIShieldEntry reports whether an http_filters[] element is the elchi-shield
// ext_proc reference, by decoding its base64 typed_config pointer and matching the
// referenced extension name.
func isELCHIShieldEntry(entry any) bool {
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
	return decoded.Name == elchiShieldExtensionName
}
