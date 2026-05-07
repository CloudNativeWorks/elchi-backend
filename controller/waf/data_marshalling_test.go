package waf

import (
	"encoding/json"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

func TestMarshalJSON_AlwaysModernShape(t *testing.T) {
	d := WAFConfigData{
		Sets: []DirectiveSet{
			{Name: "default", Description: "baseline", Directives: []string{"SecRuleEngine On"}},
		},
		DefaultSet:             "default",
		MetricLabels:           map[string]string{"team": "sec"},
		PerAuthorityDirectives: map[string]string{"api.example.com": "default"},
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if _, ok := parsed["sets"]; !ok {
		t.Errorf("modern shape must have 'sets' key, got: %s", string(b))
	}
	if _, ok := parsed["default_set"]; !ok {
		t.Errorf("modern shape must have 'default_set' key")
	}
	if _, ok := parsed["directives_map"]; ok {
		t.Errorf("modern shape must NOT have 'directives_map' key")
	}
	if _, ok := parsed["default_directives"]; ok {
		t.Errorf("modern shape must NOT have 'default_directives' key")
	}
}

func TestMarshalJSON_NilSetsBecomesEmptyArray(t *testing.T) {
	d := WAFConfigData{}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if got := string(b); got == "" || jsonContains(t, b, `"sets":null`) {
		t.Errorf("nil Sets must serialize as [], got: %s", got)
	}
}

func TestUnmarshalJSON_ModernShape(t *testing.T) {
	in := []byte(`{
		"sets": [
			{"name": "strict", "description": "tight", "directives": ["SecRuleEngine On"]},
			{"name": "permissive", "directives": ["SecRuleEngine DetectionOnly"]}
		],
		"default_set": "strict",
		"metric_labels": {"team": "sec"},
		"per_authority_directives": {"api.example.com": "strict"}
	}`)
	var d WAFConfigData
	if err := json.Unmarshal(in, &d); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if len(d.Sets) != 2 {
		t.Fatalf("expected 2 sets, got %d", len(d.Sets))
	}
	if d.Sets[0].Name != "strict" || d.Sets[0].Description != "tight" {
		t.Errorf("first set wrong: %+v", d.Sets[0])
	}
	if d.DefaultSet != "strict" {
		t.Errorf("default_set wrong: %s", d.DefaultSet)
	}
	if d.MetricLabels["team"] != "sec" {
		t.Errorf("metric_labels not propagated")
	}
	if d.PerAuthorityDirectives["api.example.com"] != "strict" {
		t.Errorf("per_authority_directives not propagated")
	}
}

func TestUnmarshalJSON_LegacyShape(t *testing.T) {
	in := []byte(`{
		"directives_map": {
			"strict": ["SecRuleEngine On"],
			"permissive": ["SecRuleEngine DetectionOnly"]
		},
		"default_directives": "strict",
		"metric_labels": {"team": "sec"}
	}`)
	var d WAFConfigData
	if err := json.Unmarshal(in, &d); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if len(d.Sets) != 2 {
		t.Fatalf("expected 2 sets, got %d", len(d.Sets))
	}
	// Legacy → canonical must sort alphabetically (Go map iteration is random).
	if d.Sets[0].Name != "permissive" || d.Sets[1].Name != "strict" {
		t.Errorf("legacy sets must be sorted alphabetically, got %s, %s", d.Sets[0].Name, d.Sets[1].Name)
	}
	if d.DefaultSet != "strict" {
		t.Errorf("default_set wrong: %s", d.DefaultSet)
	}
}

func TestUnmarshalJSON_RejectsEmptyPayload(t *testing.T) {
	var d WAFConfigData
	err := json.Unmarshal([]byte(`{}`), &d)
	if err == nil {
		t.Fatal("expected error for payload missing both 'sets' and 'directives_map'")
	}
}

func TestUnmarshalJSON_PrefersModernIfBothPresent(t *testing.T) {
	in := []byte(`{
		"sets": [{"name": "modern_set", "directives": ["A"]}],
		"directives_map": {"legacy_set": ["B"]},
		"default_set": "modern_set",
		"default_directives": "legacy_set"
	}`)
	var d WAFConfigData
	if err := json.Unmarshal(in, &d); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if len(d.Sets) != 1 || d.Sets[0].Name != "modern_set" {
		t.Errorf("expected modern shape to win, got: %+v", d.Sets)
	}
	if d.DefaultSet != "modern_set" {
		t.Errorf("default_set wrong: %s", d.DefaultSet)
	}
}

func TestBSON_AlwaysLegacyShape_AndRoundTrip(t *testing.T) {
	original := WAFConfigData{
		Sets: []DirectiveSet{
			{Name: "default", Description: "baseline", Directives: []string{"SecRuleEngine On", "Include @owasp_crs/*.conf"}},
			{Name: "strict", Directives: []string{"SecRuleEngine On"}},
		},
		DefaultSet:             "default",
		MetricLabels:           map[string]string{"team": "sec"},
		PerAuthorityDirectives: map[string]string{"api.example.com": "strict"},
	}

	b, err := bson.Marshal(original)
	if err != nil {
		t.Fatalf("MarshalBSON: %v", err)
	}

	// Decode raw to confirm disk shape is legacy.
	var raw bson.M
	if err := bson.Unmarshal(b, &raw); err != nil {
		t.Fatalf("raw bson unmarshal: %v", err)
	}
	if _, ok := raw["directives_map"]; !ok {
		t.Errorf("disk format must have directives_map, got keys: %v", mapKeys(raw))
	}
	if _, ok := raw["default_directives"]; !ok {
		t.Errorf("disk format must have default_directives")
	}
	if _, ok := raw["sets"]; ok {
		t.Errorf("disk format must NOT have sets — old pods cannot decode that")
	}

	// Round-trip via canonical UnmarshalBSON.
	var back WAFConfigData
	if err := bson.Unmarshal(b, &back); err != nil {
		t.Fatalf("UnmarshalBSON: %v", err)
	}
	if len(back.Sets) != 2 {
		t.Fatalf("round-trip lost sets: got %d", len(back.Sets))
	}
	// fromLegacyShape sorts alphabetically; "default" < "strict"
	if back.Sets[0].Name != "default" || back.Sets[0].Description != "baseline" {
		t.Errorf("round-trip lost descriptions: %+v", back.Sets[0])
	}
	if back.DefaultSet != "default" {
		t.Errorf("default_set lost: %s", back.DefaultSet)
	}
}

func TestUnmarshalBSON_LegacyDocWithoutSetDescriptions(t *testing.T) {
	// Simulate an old doc that pre-dates the set_descriptions field.
	legacy := bson.M{
		"directives_map": bson.M{
			"default": bson.A{"SecRuleEngine On"},
		},
		"default_directives": "default",
	}
	b, err := bson.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy: %v", err)
	}
	var d WAFConfigData
	if err := bson.Unmarshal(b, &d); err != nil {
		t.Fatalf("UnmarshalBSON of pre-existing doc: %v", err)
	}
	if len(d.Sets) != 1 || d.Sets[0].Name != "default" || d.Sets[0].Description != "" {
		t.Errorf("legacy doc decode wrong: %+v", d.Sets)
	}
}

// helpers --------------------------------------------------------------------

func jsonContains(t *testing.T, b []byte, substr string) bool {
	t.Helper()
	return string(b) != "" && contains(string(b), substr)
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func mapKeys(m bson.M) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
