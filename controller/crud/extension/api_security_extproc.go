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

	// Drop any pre-existing elchi-shield entry from BOTH the http_filters list AND
	// the general.config_discovery registration — re-added below if enabled. Every
	// HTTP filter in elchi is delivered via ECDS (config_discovery): the filter entry
	// only points at ADS, and a MATCHING general.config_discovery entry tells the
	// control plane which extension to serve for that name. An inline typed_config is
	// never resolved (http_filters is not a TypedConfigPath) and Envoy rejects it with
	// an empty type URL, so both pieces are required.
	existing := toAnySlice(hcmMap["http_filters"])
	filters := make([]any, 0, len(existing)+1)
	for _, f := range existing {
		if isELCHIShieldEntry(f) {
			continue
		}
		filters = append(filters, f)
	}
	cds := make([]*models.ConfigDiscovery, 0, len(general.ConfigDiscovery)+1)
	for _, cd := range general.ConfigDiscovery {
		if cd != nil && cd.Name == elchiShieldExtensionName {
			continue
		}
		cds = append(cds, cd)
	}

	if general.APISecurity {
		if _, err := resources.GetResourceNGeneral(ctx, dbCtx, models.ExternalProcessor.CollectionString(), elchiShieldExtensionName, general.Project, general.Version); err != nil {
			log.Warnf("api_security: elchi-shield ext_proc extension missing for project=%s version=%s; http_filter not added: %v", general.Project, general.Version, err)
		} else {
			// Prepend so shield runs first (ahead of every other filter + the router),
			// and register it for ECDS so the control plane serves the real config.
			filters = append([]any{buildELCHIShieldExtProcFilter()}, filters...)
			cds = append(cds, buildELCHIShieldConfigDiscovery())
		}
	}

	hcmMap["http_filters"] = filters
	resource.SetResource(hcmMap)
	general.ConfigDiscovery = cds
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
