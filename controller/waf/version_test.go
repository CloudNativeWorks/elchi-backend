package waf

import "testing"

// TestSnapshotMatches_IdenticalContent ensures the dedup logic correctly
// identifies a no-op PUT (same Name + same Data) so the version stream
// doesn't accumulate duplicate entries.
func TestSnapshotMatches_IdenticalContent(t *testing.T) {
	data := WAFConfigData{
		Sets: []DirectiveSet{
			{Name: "default", Directives: []string{"SecRuleEngine On", "Include @owasp_crs/*.conf"}},
		},
		DefaultSet:             "default",
		MetricLabels:           map[string]string{"team": "sec"},
		PerAuthorityDirectives: map[string]string{},
	}
	latest := &WAFConfigVersion{Name: "edge", Data: data, Version: 5}
	cfg := &WAFConfig{Name: "edge", Data: data}
	if !snapshotMatches(latest, cfg) {
		t.Errorf("identical content must dedup")
	}
}

func TestSnapshotMatches_DirectiveAdded(t *testing.T) {
	latest := &WAFConfigVersion{
		Name: "edge",
		Data: WAFConfigData{
			Sets: []DirectiveSet{
				{Name: "default", Directives: []string{"SecRuleEngine On"}},
			},
			DefaultSet: "default",
		},
	}
	cfg := &WAFConfig{
		Name: "edge",
		Data: WAFConfigData{
			Sets: []DirectiveSet{
				{Name: "default", Directives: []string{"SecRuleEngine On", "Include @owasp_crs/*.conf"}},
			},
			DefaultSet: "default",
		},
	}
	if snapshotMatches(latest, cfg) {
		t.Errorf("directive list change must NOT dedup")
	}
}

func TestSnapshotMatches_DirectiveReordered(t *testing.T) {
	latest := &WAFConfigVersion{
		Name: "edge",
		Data: WAFConfigData{
			Sets: []DirectiveSet{
				{Name: "default", Directives: []string{"SecRuleEngine On", "Include @owasp_crs/*.conf"}},
			},
			DefaultSet: "default",
		},
	}
	cfg := &WAFConfig{
		Name: "edge",
		Data: WAFConfigData{
			Sets: []DirectiveSet{
				{Name: "default", Directives: []string{"Include @owasp_crs/*.conf", "SecRuleEngine On"}},
			},
			DefaultSet: "default",
		},
	}
	if snapshotMatches(latest, cfg) {
		t.Errorf("directive reorder must NOT dedup — order is semantically meaningful")
	}
}

func TestSnapshotMatches_SetRenamed(t *testing.T) {
	// Mirrors the user's "asd → asd2" scenario: only the set name changes,
	// directives are identical. We must capture this as a new version.
	latest := &WAFConfigVersion{
		Name: "edge",
		Data: WAFConfigData{
			Sets: []DirectiveSet{
				{Name: "asd", Directives: []string{"SecRuleEngine On"}},
			},
			DefaultSet: "asd",
		},
	}
	cfg := &WAFConfig{
		Name: "edge",
		Data: WAFConfigData{
			Sets: []DirectiveSet{
				{Name: "asd2", Directives: []string{"SecRuleEngine On"}},
			},
			DefaultSet: "asd2",
		},
	}
	if snapshotMatches(latest, cfg) {
		t.Errorf("set rename must NOT dedup — name is part of the user-meaningful state")
	}
}

func TestSnapshotMatches_ConfigNameChanged(t *testing.T) {
	data := WAFConfigData{
		Sets:       []DirectiveSet{{Name: "default", Directives: []string{"SecRuleEngine On"}}},
		DefaultSet: "default",
	}
	latest := &WAFConfigVersion{Name: "edge", Data: data}
	cfg := &WAFConfig{Name: "edge-renamed", Data: data}
	if snapshotMatches(latest, cfg) {
		t.Errorf("config-name change must NOT dedup")
	}
}

func TestSnapshotMatches_NilGuards(t *testing.T) {
	if snapshotMatches(nil, &WAFConfig{}) {
		t.Errorf("nil latest must not match")
	}
	if snapshotMatches(&WAFConfigVersion{}, nil) {
		t.Errorf("nil config must not match")
	}
}

func TestSnapshotMatches_DescriptionChanged(t *testing.T) {
	latest := &WAFConfigVersion{
		Name: "edge",
		Data: WAFConfigData{
			Sets:       []DirectiveSet{{Name: "default", Description: "baseline", Directives: []string{"SecRuleEngine On"}}},
			DefaultSet: "default",
		},
	}
	cfg := &WAFConfig{
		Name: "edge",
		Data: WAFConfigData{
			Sets:       []DirectiveSet{{Name: "default", Description: "stricter baseline", Directives: []string{"SecRuleEngine On"}}},
			DefaultSet: "default",
		},
	}
	if snapshotMatches(latest, cfg) {
		t.Errorf("set description change must NOT dedup")
	}
}
