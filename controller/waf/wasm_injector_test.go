package waf

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

// TestWASMInjectionShapeIsLegacyContract is LOAD-BEARING.
//
// The Coraza WASM plugin running in Envoy parses the WAF configuration JSON
// looking for the field names "directives_map" and "default_directives". If
// this test fails, every WAF-protected service in production silently stops
// enforcing rules — the plugin reads no directives and falls open.
//
// Do not "fix" this test by changing the assertions. Fix the code so the test
// passes as written.
func TestWASMInjectionShapeIsLegacyContract(t *testing.T) {
	data := WAFConfigData{
		Sets: []DirectiveSet{
			{Name: "default", Description: "baseline", Directives: []string{"SecRuleEngine On"}},
		},
		DefaultSet: "default",
		MetricLabels: map[string]string{
			"team": "sec",
		},
		PerAuthorityDirectives: map[string]string{
			"api.example.com": "default",
		},
	}

	// This is the exact call buildCorazaConfigJSON makes in wasm_injector.go.
	// If the line there is changed, this test must still hold.
	out, err := json.Marshal(data.toLegacyShape())
	if err != nil {
		t.Fatalf("marshal toLegacyShape: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}

	dm, ok := parsed["directives_map"].(map[string]any)
	if !ok {
		t.Fatalf("WASM contract requires 'directives_map' object, got: %s", string(out))
	}
	if _, hasDefault := dm["default"]; !hasDefault {
		t.Errorf("directives_map must contain set 'default'")
	}

	if got, _ := parsed["default_directives"].(string); got != "default" {
		t.Errorf("default_directives must equal 'default', got %v", parsed["default_directives"])
	}

	if _, present := parsed["sets"]; present {
		t.Errorf("WASM-facing JSON must NOT contain 'sets' — Coraza plugin doesn't recognize it")
	}
	if _, present := parsed["default_set"]; present {
		t.Errorf("WASM-facing JSON must NOT contain 'default_set' — Coraza plugin doesn't recognize it")
	}
	// set_descriptions is BSON-only; it must not leak to WASM.
	if _, present := parsed["set_descriptions"]; present {
		t.Errorf("WASM-facing JSON must NOT contain 'set_descriptions' — that field is disk-only")
	}

	// metric_labels and per_authority_directives are part of the legacy
	// contract and must pass through unchanged.
	if ml, ok := parsed["metric_labels"].(map[string]any); !ok || ml["team"] != "sec" {
		t.Errorf("metric_labels missing or wrong: %v", parsed["metric_labels"])
	}
	if pad, ok := parsed["per_authority_directives"].(map[string]any); !ok || pad["api.example.com"] != "default" {
		t.Errorf("per_authority_directives missing or wrong: %v", parsed["per_authority_directives"])
	}
}

// TestWASMByteEquivalenceWithLegacyEncoder is also LOAD-BEARING.
//
// It verifies that the JSON we feed the WASM plugin is byte-for-byte
// identical to what the pre-refactor code emitted, across the three
// real-world states a WAFConfigData can be in. The Coraza WASM plugin's
// behavior on `null` vs missing vs {} is not contractually specified by
// us, so we keep the wire shape stable as the safest defense.
func TestWASMByteEquivalenceWithLegacyEncoder(t *testing.T) {
	// Reference encoder mimicking the pre-refactor struct (no omitempty,
	// json tags exactly as `directives_map`, `default_directives`,
	// `metric_labels`, `per_authority_directives`).
	type oldShape struct {
		DirectivesMap          map[string][]string `json:"directives_map"`
		DefaultDirectives      string              `json:"default_directives"`
		MetricLabels           map[string]string   `json:"metric_labels"`
		PerAuthorityDirectives map[string]string   `json:"per_authority_directives"`
	}

	scenarios := []struct {
		name string
		data WAFConfigData
		old  oldShape
	}{
		{
			name: "fully populated",
			data: WAFConfigData{
				Sets: []DirectiveSet{
					{Name: "default", Directives: []string{"SecRuleEngine On", "Include @owasp_crs/*.conf"}},
				},
				DefaultSet:             "default",
				MetricLabels:           map[string]string{"team": "sec"},
				PerAuthorityDirectives: map[string]string{"api.example.com": "default"},
			},
			old: oldShape{
				DirectivesMap:          map[string][]string{"default": {"SecRuleEngine On", "Include @owasp_crs/*.conf"}},
				DefaultDirectives:      "default",
				MetricLabels:           map[string]string{"team": "sec"},
				PerAuthorityDirectives: map[string]string{"api.example.com": "default"},
			},
		},
		{
			name: "empty optional maps (post-normalizeData state)",
			data: WAFConfigData{
				Sets:                   []DirectiveSet{{Name: "default", Directives: []string{"SecRuleEngine On"}}},
				DefaultSet:             "default",
				MetricLabels:           map[string]string{},
				PerAuthorityDirectives: map[string]string{},
			},
			old: oldShape{
				DirectivesMap:          map[string][]string{"default": {"SecRuleEngine On"}},
				DefaultDirectives:      "default",
				MetricLabels:           map[string]string{},
				PerAuthorityDirectives: map[string]string{},
			},
		},
		{
			name: "nil optional maps (legacy doc without those fields)",
			data: WAFConfigData{
				Sets:       []DirectiveSet{{Name: "default", Directives: []string{"SecRuleEngine On"}}},
				DefaultSet: "default",
			},
			old: oldShape{
				DirectivesMap:     map[string][]string{"default": {"SecRuleEngine On"}},
				DefaultDirectives: "default",
			},
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			newBytes, err := json.Marshal(sc.data.toLegacyShape())
			if err != nil {
				t.Fatalf("marshal new: %v", err)
			}
			oldBytes, err := json.Marshal(sc.old)
			if err != nil {
				t.Fatalf("marshal old: %v", err)
			}
			if string(newBytes) != string(oldBytes) {
				t.Errorf("WASM-bound JSON drifted from legacy encoder.\n  OLD: %s\n  NEW: %s", oldBytes, newBytes)
			}
		})
	}
}

// TestWASMInjection_FullPipeline simulates the production injection path
// without hitting MongoDB: it constructs a WAF config, runs it through
// buildCorazaConfigJSON, base64-encodes (as the injector does), and
// asserts the StringValue payload that lands in the WASM extension is
// well-formed and matches the WASM plugin's expected schema.
func TestWASMInjection_FullPipeline(t *testing.T) {
	cfg := &WAFConfig{
		Name: "edge-waf",
		Data: WAFConfigData{
			Sets: []DirectiveSet{
				{Name: "default", Description: "baseline", Directives: []string{
					"SecRuleEngine On",
					"Include @crs-setup-conf",
					"Include @owasp_crs/*.conf",
				}},
				{Name: "strict", Directives: []string{"SecRuleEngine On", "SecRuleRemoveById 942100"}},
			},
			DefaultSet:             "default",
			MetricLabels:           map[string]string{"team": "sec"},
			PerAuthorityDirectives: map[string]string{"api.example.com": "strict"},
		},
	}

	w := &WAFWasmInjector{}
	jsonStr, err := w.buildCorazaConfigJSON(cfg)
	if err != nil {
		t.Fatalf("buildCorazaConfigJSON: %v", err)
	}

	// Sanity: encoded as legacy.
	if !contains(jsonStr, `"directives_map"`) || !contains(jsonStr, `"default_directives":"default"`) {
		t.Errorf("WASM JSON missing required legacy keys: %s", jsonStr)
	}
	if contains(jsonStr, `"sets"`) || contains(jsonStr, `"default_set"`) {
		t.Errorf("WASM JSON leaked modern keys: %s", jsonStr)
	}
	if contains(jsonStr, `"set_descriptions"`) {
		t.Errorf("WASM JSON leaked disk-only set_descriptions: %s", jsonStr)
	}

	// Base64 round-trip mirrors what InjectWAFConfig does.
	encoded := base64.StdEncoding.EncodeToString([]byte(jsonStr))
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || string(decoded) != jsonStr {
		t.Errorf("base64 round-trip lost data")
	}

	// Re-parse and walk the structure the way the WASM plugin would.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		t.Fatalf("WASM-side parse failed: %v", err)
	}
	dm, ok := parsed["directives_map"].(map[string]any)
	if !ok {
		t.Fatalf("directives_map not an object")
	}
	defaultRules, ok := dm["default"].([]any)
	if !ok || len(defaultRules) != 3 {
		t.Errorf("default set rules wrong: %v", dm["default"])
	}
	strictRules, ok := dm["strict"].([]any)
	if !ok || len(strictRules) != 2 {
		t.Errorf("strict set rules wrong: %v", dm["strict"])
	}
	// Per-authority routing must reach the WASM plugin verbatim.
	pad, ok := parsed["per_authority_directives"].(map[string]any)
	if !ok || pad["api.example.com"] != "strict" {
		t.Errorf("per_authority_directives wrong: %v", pad)
	}
}

// TestWASMInjection_FromBSONDoc verifies the disk → WASM path. We build a
// BSON document in the legacy on-disk shape (what MongoDB actually holds),
// decode through UnmarshalBSON, then re-encode for WASM and confirm we
// haven't lost or mutated any directive ordering.
func TestWASMInjection_FromBSONDoc(t *testing.T) {
	diskDoc := bson.M{
		"directives_map": bson.M{
			"default": bson.A{"SecRuleEngine On", "Include @owasp_crs/*.conf"},
			"strict":  bson.A{"SecRuleEngine On", "SecRuleRemoveById 942100"},
		},
		"default_directives":       "default",
		"metric_labels":            bson.M{"team": "sec"},
		"per_authority_directives": bson.M{"api.example.com": "strict"},
	}
	raw, err := bson.Marshal(diskDoc)
	if err != nil {
		t.Fatalf("marshal disk: %v", err)
	}

	var d WAFConfigData
	if err := bson.Unmarshal(raw, &d); err != nil {
		t.Fatalf("UnmarshalBSON of legacy disk doc: %v", err)
	}
	if len(d.Sets) != 2 {
		t.Fatalf("decoded wrong set count: %d", len(d.Sets))
	}

	cfg := &WAFConfig{Name: "x", Data: d}
	w := &WAFWasmInjector{}
	jsonStr, err := w.buildCorazaConfigJSON(cfg)
	if err != nil {
		t.Fatalf("buildCorazaConfigJSON: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	dm := parsed["directives_map"].(map[string]any)

	// The first directive in the "default" set must still be SecRuleEngine
	// On — order preservation is a hard requirement (validators rely on
	// SecRuleEngine being declared before any rules).
	defaultRules := dm["default"].([]any)
	if defaultRules[0] != "SecRuleEngine On" {
		t.Errorf("directive ordering lost in disk → WASM round-trip: %v", defaultRules)
	}
	strictRules := dm["strict"].([]any)
	if strictRules[1] != "SecRuleRemoveById 942100" {
		t.Errorf("strict set directive ordering lost: %v", strictRules)
	}
}
