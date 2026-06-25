package extension

import "testing"

// TestELCHIShieldEntryRoundTrip guards the idempotent strip-then-prepend logic:
// the http_filter built by buildELCHIShieldExtProcFilter must be recognized by
// isELCHIShieldEntry (so a re-save strips the old one), and an unrelated filter
// must NOT be (so user filters are never dropped).
func TestELCHIShieldEntryRoundTrip(t *testing.T) {
	entry := buildELCHIShieldExtProcFilter("v1.36.2")

	if !isELCHIShieldEntry(entry) {
		t.Fatal("the built elchi-shield ext_proc filter must be recognized by isELCHIShieldEntry")
	}

	// The injected filter's name must be the ext_proc canonical name so Envoy and
	// the UI render it as a normal http filter.
	if got := entry["name"]; got != "envoy.filters.http.ext_proc" {
		t.Errorf("filter name = %v, want envoy.filters.http.ext_proc", got)
	}

	// A different (operator-defined) filter must never be mistaken for elchi-shield.
	for _, other := range []any{
		map[string]any{"name": "envoy.filters.http.router"},
		map[string]any{"name": "envoy.filters.http.cors", "typed_config": map[string]any{"type_url": "x", "value": "bm90LXNoaWVsZA=="}},
		"not-a-map",
		nil,
	} {
		if isELCHIShieldEntry(other) {
			t.Errorf("non-shield entry %v wrongly recognized as elchi-shield", other)
		}
	}
}
