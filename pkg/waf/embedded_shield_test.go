package waf

import (
	"encoding/json"
	"testing"
)

func TestShieldCRSEmbedded(t *testing.T) {
	versions := ShieldCRSVersions()
	found := false
	for _, v := range versions {
		if v == "v4.25.0" {
			found = true
		}
	}
	if !found {
		t.Fatalf("shield CRS v4.25.0 not embedded; discovered %v", versions)
	}

	rules, meta, ok := ShieldCRSData("v4.25.0")
	if !ok || len(rules) == 0 || len(meta) == 0 {
		t.Fatalf("ShieldCRSData(v4.25.0): ok=%v rulesLen=%d metaLen=%d", ok, len(rules), len(meta))
	}
	// Rules must parse into the catalog schema.
	var parsed []Rule
	if err := json.Unmarshal(rules, &parsed); err != nil {
		t.Fatalf("rules JSON does not parse into []Rule: %v", err)
	}
	if len(parsed) < 500 {
		t.Fatalf("expected the full CRS ruleset (~625 rules), got %d", len(parsed))
	}

	if _, _, ok := ShieldCRSData("v9.9.9"); ok {
		t.Fatal("ShieldCRSData for an absent version should return ok=false")
	}
}
