package waf

import (
	"strings"
	"testing"
)

func TestValidate_RejectsEmptySets(t *testing.T) {
	d := WAFConfigData{DefaultSet: "x"}
	if err := validateWAFConfigData(&d); err == nil || !strings.Contains(err.Error(), "sets") {
		t.Errorf("expected sets-cannot-be-empty error, got %v", err)
	}
}

func TestValidate_RejectsEmptyDefaultSet(t *testing.T) {
	d := WAFConfigData{
		Sets: []DirectiveSet{{Name: "default", Directives: []string{"SecRuleEngine On"}}},
	}
	if err := validateWAFConfigData(&d); err == nil || !strings.Contains(err.Error(), "default_set") {
		t.Errorf("expected default_set-required error, got %v", err)
	}
}

func TestValidate_RejectsDanglingDefaultSet(t *testing.T) {
	d := WAFConfigData{
		Sets:       []DirectiveSet{{Name: "default", Directives: []string{"SecRuleEngine On"}}},
		DefaultSet: "missing",
	}
	if err := validateWAFConfigData(&d); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("expected dangling default_set error, got %v", err)
	}
}

func TestValidate_RejectsDuplicateSetNames(t *testing.T) {
	d := WAFConfigData{
		Sets: []DirectiveSet{
			{Name: "default", Directives: []string{"A"}},
			{Name: "default", Directives: []string{"B"}},
		},
		DefaultSet: "default",
	}
	if err := validateWAFConfigData(&d); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected duplicate-set-name error, got %v", err)
	}
}

func TestValidate_RejectsEmptySetName(t *testing.T) {
	d := WAFConfigData{
		Sets:       []DirectiveSet{{Name: "", Directives: []string{"A"}}},
		DefaultSet: "",
	}
	if err := validateWAFConfigData(&d); err == nil {
		t.Errorf("expected empty-name error, got nil")
	}
}

func TestValidate_RejectsDanglingPerAuthority(t *testing.T) {
	d := WAFConfigData{
		Sets:                   []DirectiveSet{{Name: "default", Directives: []string{"A"}}},
		DefaultSet:             "default",
		PerAuthorityDirectives: map[string]string{"api.example.com": "missing"},
	}
	if err := validateWAFConfigData(&d); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("expected dangling per-authority error, got %v", err)
	}
}

func TestValidate_RejectsOversizeDescription(t *testing.T) {
	long := strings.Repeat("x", maxSetDescriptionLength+1)
	d := WAFConfigData{
		Sets:       []DirectiveSet{{Name: "default", Description: long, Directives: []string{"A"}}},
		DefaultSet: "default",
	}
	if err := validateWAFConfigData(&d); err == nil || !strings.Contains(err.Error(), "description") {
		t.Errorf("expected description-too-long error, got %v", err)
	}
}

func TestValidate_AcceptsHappyPath(t *testing.T) {
	d := WAFConfigData{
		Sets: []DirectiveSet{
			{Name: "default", Description: "baseline", Directives: []string{"SecRuleEngine On"}},
			{Name: "strict", Directives: []string{"SecRuleEngine On"}},
		},
		DefaultSet:             "default",
		MetricLabels:           map[string]string{"team": "sec"},
		PerAuthorityDirectives: map[string]string{"api.example.com": "strict"},
	}
	if err := validateWAFConfigData(&d); err != nil {
		t.Errorf("happy path rejected: %v", err)
	}
}

func TestNormalizeData_FillsNilContainers(t *testing.T) {
	d := WAFConfigData{}
	normalizeData(&d)
	if d.Sets == nil {
		t.Errorf("Sets should be non-nil after normalize")
	}
	if d.MetricLabels == nil {
		t.Errorf("MetricLabels should be non-nil after normalize")
	}
	if d.PerAuthorityDirectives == nil {
		t.Errorf("PerAuthorityDirectives should be non-nil after normalize")
	}
}

func TestWAFInUseError_FormatsCount(t *testing.T) {
	e := &WAFInUseError{
		Name: "edge",
		References: []WAFUsageRef{
			{Type: "wasm_extension", ID: "1", Name: "ext-1"},
			{Type: "wasm_extension", ID: "2", Name: "ext-2"},
		},
	}
	if !strings.Contains(e.Error(), "2 resource(s)") {
		t.Errorf("expected count in error message, got %q", e.Error())
	}
}
