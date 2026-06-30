package extension

import (
	"context"

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
// Delivered via ECDS: the http_filter holds a config_discovery pointer and a paired
// general.config_discovery entry tells the control plane which extension to serve
// for that name. Persisting it (rather than injecting at snapshot time) keeps it
// visible + reorderable in the UI, durable across saves, and cleanly removed off.
//
// No-op for non-HCM extensions. Position-preserving & idempotent: on first enable
// the filter is dropped in at the head (a sensible WAF default), but once present
// it is LEFT exactly where the user put it — they may reorder it (e.g. run a Lua
// filter ahead of it). Disabling strips it from both lists. It is never re-pinned
// to the head on save, and other filters' ordering is never touched.
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

	existing := toAnySlice(hcmMap["http_filters"])
	cd := general.ConfigDiscovery

	shieldPresent := false
	for _, f := range existing {
		if isELCHIShieldEntry(f) {
			shieldPresent = true
			break
		}
	}

	// Enable only when the toggle is on AND the project actually has the elchi-shield
	// ExternalProcessor extension — a dangling ECDS reference would break the listener
	// snapshot. A missing extension is treated like "off" (the entry is not added).
	enable := false
	if general.APISecurity {
		if _, err := resources.GetResourceNGeneral(ctx, dbCtx, models.ExternalProcessor.CollectionString(), elchiShieldExtensionName, general.Project, general.Version); err != nil {
			log.Warnf("api_security: elchi-shield ext_proc extension missing for project=%s version=%s; not added: %v", general.Project, general.Version, err)
		} else {
			enable = true
		}
	}

	switch {
	case enable && shieldPresent:
		// Already in the chain — leave it EXACTLY where the user placed it. They may
		// have reordered it (e.g. dropped a Lua filter ahead of it to pre-compute
		// something); never re-pin it to the head on save.
		return
	case enable && !shieldPresent:
		// First enable: drop it in at the head as a sensible default (a WAF usually
		// wants to inspect first); the user is then free to move it — the branch above
		// preserves whatever position it ends up at. Both lists get the entry: the
		// http_filter (ECDS pointer) AND the general.config_discovery registration the
		// control plane needs to serve the real ExternalProcessor config by name.
		hcmMap["http_filters"] = append([]any{buildELCHIShieldExtProcFilter()}, existing...)
		general.ConfigDiscovery = append([]*models.ConfigDiscovery{buildELCHIShieldConfigDiscovery()}, cd...)
	default:
		// Disabled (or extension missing): strip elchi-shield from both lists.
		filters := make([]any, 0, len(existing))
		for _, f := range existing {
			if isELCHIShieldEntry(f) {
				continue
			}
			filters = append(filters, f)
		}
		newCD := make([]*models.ConfigDiscovery, 0, len(cd))
		for _, c := range cd {
			if c != nil && c.Name == elchiShieldExtensionName {
				continue
			}
			newCD = append(newCD, c)
		}
		hcmMap["http_filters"] = filters
		general.ConfigDiscovery = newCD
	}

	resource.SetResource(hcmMap)
	resource.SetGeneral(&general)
}

// buildELCHIShieldExtProcFilter returns an http_filters[] element that delivers the
// `elchi-shield` ext_proc config via ECDS (config_discovery) — exactly like every
// other elchi HTTP filter. Envoy fetches the real ExternalProcessor config over ADS
// keyed by the filter name; pairs with buildELCHIShieldConfigDiscovery.
func buildELCHIShieldExtProcFilter() map[string]any {
	return map[string]any{
		"name": elchiShieldExtensionName,
		"config_discovery": map[string]any{
			"config_source": map[string]any{
				"ads":                   map[string]any{},
				"initial_fetch_timeout": "5.0s",
				"resource_api_version":  "V3",
			},
			"type_urls": []string{models.ExternalProcessor.String()},
		},
		"is_optional": false,
		"disabled":    false,
	}
}

// buildELCHIShieldConfigDiscovery returns the general.config_discovery registration
// that tells the control plane to serve the `elchi-shield` ExternalProcessor config
// (collection "filters") over ECDS for the http_filter of the same name.
func buildELCHIShieldConfigDiscovery() *models.ConfigDiscovery {
	gt := models.ExternalProcessor
	return &models.ConfigDiscovery{
		Name:          elchiShieldExtensionName,
		ParentName:    elchiShieldExtensionName,
		GType:         gt,
		Priority:      0, // head — shield runs first
		Category:      gt.Category(),
		CanonicalName: gt.CanonicalName(),
	}
}

// isELCHIShieldEntry reports whether an http_filters[] element is the injected
// elchi-shield ext_proc filter, matched by its ECDS filter name.
func isELCHIShieldEntry(entry any) bool {
	m, ok := asStringMap(entry)
	if !ok {
		return false
	}
	name, _ := m["name"].(string)
	return name == elchiShieldExtensionName
}
