package extension

import "testing"

// TestELCHIShieldEntryRoundTrip guards the idempotent strip-then-prepend logic:
// the http_filter built by buildELCHIShieldExtProcFilter must be recognized by
// isELCHIShieldEntry (so a re-save strips the old one), and an unrelated filter
// must NOT be (so user filters are never dropped).
func TestELCHIShieldEntryRoundTrip(t *testing.T) {
	entry := buildELCHIShieldExtProcFilter()

	if !isELCHIShieldEntry(entry) {
		t.Fatal("the built elchi-shield ext_proc filter must be recognized by isELCHIShieldEntry")
	}

	// HTTP filters in elchi are delivered via ECDS: the entry must be named after the
	// extension (so the control plane serves its config by name) and carry a
	// config_discovery block — NOT an inline typed_config (which http_filters never
	// resolves, so Envoy rejects it with an empty type URL).
	if got := entry["name"]; got != elchiShieldExtensionName {
		t.Errorf("filter name = %v, want %s", got, elchiShieldExtensionName)
	}
	if _, ok := entry["config_discovery"]; !ok {
		t.Error("filter must use config_discovery (ECDS), not inline typed_config")
	}
	if _, ok := entry["typed_config"]; ok {
		t.Error("filter must NOT carry an inline typed_config")
	}

	// The paired ECDS registration must point at the elchi-shield extension.
	cd := buildELCHIShieldConfigDiscovery()
	if cd.Name != elchiShieldExtensionName || cd.CanonicalName != "envoy.filters.http.ext_proc" {
		t.Errorf("config_discovery registration = %+v", cd)
	}

	// A different (operator-defined) filter must never be mistaken for elchi-shield.
	for _, other := range []any{
		map[string]any{"name": "envoy.filters.http.router"},
		map[string]any{"name": "envoy.filters.http.cors"},
		"not-a-map",
		nil,
	} {
		if isELCHIShieldEntry(other) {
			t.Errorf("non-shield entry %v wrongly recognized as elchi-shield", other)
		}
	}
}
