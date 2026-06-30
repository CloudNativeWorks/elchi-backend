package handlers

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// schemaKeys mirrors the UI Builder's supported-field allowlist (POLICY_SPEC_SCHEMA
// in src/pages/shield/state/model.ts). The suggestion engine must emit ONLY these,
// or the Builder drops to raw-YAML mode. Kept here as a contract guard.
var allowedPolicyKeys = map[string]struct{}{
	"mode": {}, "fail_mode": {}, "inspect_request_body": {}, "inspect_response_body": {},
	"max_request_body_bytes": {}, "max_response_body_bytes": {}, "max_header_bytes": {},
	"timeout": {}, "log_level": {}, "sampling_rate": {}, "anomaly_threshold": {},
	"skip_checks": {}, "pipeline": {}, "checks": {}, "engines": {},
}
var allowedEngineKeys = map[string]struct{}{
	"jwt": {}, "jwks": {}, "api_key": {}, "hmac_sign": {}, "http_signature": {}, "xfcc": {},
	"ip_reputation": {}, "rate_limit": {}, "bot": {}, "coraza": {}, "graphql": {}, "openapi": {},
}

func TestBuildSuggestedPolicyMapping(t *testing.T) {
	cases := []struct {
		name        string
		docs        []suggestInvDoc
		wantMode    string   // expected route mode
		wantEngines []string // engine keys expected in the YAML
		wantNoEng   []string // engine keys that must NOT appear
		wantNote    bool     // posture note expected
	}{
		{
			name: "unauth jwt endpoint → jwt enforce, detect",
			docs: []suggestInvDoc{{Host: "api.x", Method: "GET", NormalizedPath: "/users/{id}",
				AuthSchemes: []string{"jwt"}, NoauthObserved: true, RiskFlags: []string{"unauthenticated"}, MaxRiskScore: 7}},
			wantMode: "detect", wantEngines: []string{"jwt"},
		},
		{
			name: "brute-force auth endpoint → rate_limit + coraza, block",
			docs: []suggestInvDoc{{Host: "api.x", Method: "POST", NormalizedPath: "/login",
				EndpointCats: []string{"auth_endpoint"}, RiskFlags: []string{"brute_force_suspect"}, MaxRiskScore: 30}},
			wantMode: "block", wantEngines: []string{"rate_limit", "coraza"},
		},
		{
			name: "pii response → dlp redact, response body inspection",
			docs: []suggestInvDoc{{Host: "api.x", Method: "GET", NormalizedPath: "/profile",
				PIICategories: []string{"email", "ssn", "phone"}, MaxRiskScore: 10}},
			wantMode: "detect", wantEngines: []string{"dlp"},
		},
		{
			name: "scanner + threat feed → bot + ip_reputation",
			docs: []suggestInvDoc{{Host: "api.x", Method: "GET", NormalizedPath: "/.env",
				RiskFlags: []string{"scanner_user_agent", "threat_intel_hit", "vuln_probe_path"}, MaxRiskScore: 40}},
			wantMode: "block", wantEngines: []string{"bot", "ip_reputation"},
		},
		{
			name: "posture-only flag → note, no engine",
			docs: []suggestInvDoc{{Host: "api.x", Method: "GET", NormalizedPath: "/",
				RiskFlags: []string{"permissive_cors", "missing_hsts"}, MaxRiskScore: 5}},
			wantMode: "detect", wantNoEng: []string{"jwt", "rate_limit", "bot"}, wantNote: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yamlStr, rationale, err := buildSuggestedPolicy("test", tc.docs)
			if err != nil {
				t.Fatalf("build failed: %v", err)
			}
			// Must be valid YAML.
			var parsed map[string]any
			if err := yaml.Unmarshal([]byte(yamlStr), &parsed); err != nil {
				t.Fatalf("emitted YAML invalid: %v\n%s", err, yamlStr)
			}
			// Mode + engines surface in the rationale and the YAML.
			if len(rationale) == 0 {
				t.Fatal("no rationale produced")
			}
			if rationale[0].Mode != tc.wantMode {
				t.Errorf("mode = %q, want %q", rationale[0].Mode, tc.wantMode)
			}
			for _, e := range tc.wantEngines {
				if !strings.Contains(yamlStr, e+":") {
					t.Errorf("expected engine %q in YAML, missing:\n%s", e, yamlStr)
				}
			}
			for _, e := range tc.wantNoEng {
				if strings.Contains(yamlStr, "\n              "+e+":") {
					t.Errorf("engine %q should NOT appear", e)
				}
			}
			if tc.wantNote && len(rationale[0].Notes) == 0 {
				t.Errorf("expected a posture note, got none")
			}
		})
	}
}

// TestSuggestedPolicyKeysAreAllowlisted is the contract that guards the Builder:
// every policy/engine key the suggestion engine emits must be one the UI models.
func TestSuggestedPolicyKeysAreAllowlisted(t *testing.T) {
	docs := []suggestInvDoc{
		{Host: "a.x", Method: "POST", NormalizedPath: "/pay", EndpointCats: []string{"payment_endpoint"},
			AuthSchemes: []string{"apikey"}, NoauthObserved: true, PIICategories: []string{"credit_card"},
			RiskFlags: []string{"rate_anomaly", "scanner_user_agent", "threat_intel_hit", "bola_suspect"}, MaxRiskScore: 50},
		{Host: "a.x", Method: "GET", NormalizedPath: "/mtls", AuthSchemes: []string{"mtls"}, NoauthObserved: true, MaxRiskScore: 5},
	}
	yamlStr, _, err := buildSuggestedPolicy("t", docs)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(yamlStr), &doc); err != nil {
		t.Fatal(err)
	}
	spec, _ := doc["spec"].(map[string]any)
	domains, _ := spec["domains"].([]any)
	for _, dAny := range domains {
		d := dAny.(map[string]any)
		routes, _ := d["routes"].([]any)
		for _, rAny := range routes {
			r := rAny.(map[string]any)
			pol, _ := r["policy"].(map[string]any)
			for k := range pol {
				if _, ok := allowedPolicyKeys[k]; !ok {
					t.Errorf("policy key %q not in UI allowlist", k)
				}
			}
			if eng, ok := pol["engines"].(map[string]any); ok {
				for k := range eng {
					if _, ok := allowedEngineKeys[k]; !ok {
						t.Errorf("engine key %q not in UI allowlist", k)
					}
				}
			}
		}
	}
}
