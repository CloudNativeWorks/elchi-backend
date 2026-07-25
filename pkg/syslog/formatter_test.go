package syslog

import (
	"strings"
	"testing"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

func TestEncodeAuditEntryBasic(t *testing.T) {
	entry := &models.AuditEntry{
		ID:           "abc123",
		Timestamp:    time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC),
		UserID:       "user-1",
		Username:     "alice",
		UserRole:     "admin",
		APIType:      models.AuditAPITypeREST,
		Method:       "POST",
		Path:         "/api/v3/xds/clusters",
		Action:       "CREATE",
		ResourceType: "clusters",
		ResourceID:   "cluster-42",
		ResourceName: "edge",
		Project:      "production",
		ClientIP:     "10.0.0.5",
		RequestID:    "req-7",
		Success:      true,
	}

	out := string(EncodeAuditEntry(entry, FacilityFromName("local0"), AppName))

	wantParts := []string{
		"<134>1 2026-05-10T12:00:00Z ",
		" elchi-audit ",
		"[audit@elchi",
		`audit_id="abc123"`,
		`action="CREATE"`,
		`actor="alice"`,
		`resource_type="clusters"`,
		`resource_id="cluster-42"`,
		`resource_name="edge"`,
		`outcome="success"`,
		`project="production"`,
		`method="POST"`,
		`path="/api/v3/xds/clusters"`,
	}
	for _, p := range wantParts {
		if !strings.Contains(out, p) {
			t.Errorf("output missing %q\nfull: %s", p, out)
		}
	}
}

func TestEncodeAuditEntryFailureUsesWarningSeverity(t *testing.T) {
	entry := &models.AuditEntry{
		ID:           "x",
		Timestamp:    time.Now().UTC(),
		Action:       "DELETE",
		ResourceType: "secrets",
		Success:      false,
		ErrorMessage: "permission denied",
	}
	out := string(EncodeAuditEntry(entry, FacilityFromName("local0"), ""))

	// PRI = local0(16)*8 + warning(4) = 132
	if !strings.HasPrefix(out, "<132>1 ") {
		t.Fatalf("expected PRI 132 for failure, got prefix: %s", out[:20])
	}
	if !strings.Contains(out, `outcome="failure"`) {
		t.Errorf("missing outcome=failure in: %s", out)
	}
	if !strings.Contains(out, `error="permission denied"`) {
		t.Errorf("missing error param in: %s", out)
	}
}

func TestEncodeAuditEntryEscapesSDValue(t *testing.T) {
	entry := &models.AuditEntry{
		ID:           "id",
		Timestamp:    time.Now().UTC(),
		Action:       "UPDATE",
		ResourceType: "filters",
		ResourceName: `weird"name]with\bytes`,
		Success:      true,
	}
	out := string(EncodeAuditEntry(entry, FacilityFromName("local0"), ""))
	want := `resource_name="weird\"name\]with\\bytes"`
	if !strings.Contains(out, want) {
		t.Errorf("escape failed; want %q in:\n%s", want, out)
	}
}

func TestEncodeAuditEntryCapsLongFields(t *testing.T) {
	long := strings.Repeat("a", 500)
	entry := &models.AuditEntry{
		ID:           "id",
		Timestamp:    time.Now().UTC(),
		Action:       long,
		ResourceID:   long,
		ResourceType: "ext",
		Success:      true,
	}
	out := string(EncodeAuditEntry(entry, FacilityFromName("local0"), ""))

	// action capped at 64
	if strings.Contains(out, `action="`+long+`"`) {
		t.Errorf("action not capped to 64 chars")
	}
	if !strings.Contains(out, `action="`+strings.Repeat("a", 64)+`"`) {
		t.Errorf("expected action capped at 64 chars")
	}
	// resource_id capped at 128
	if !strings.Contains(out, `resource_id="`+strings.Repeat("a", 128)+`"`) {
		t.Errorf("expected resource_id capped at 128 chars")
	}
}

func TestEncodeAuditEntryNilReturnsNil(t *testing.T) {
	if got := EncodeAuditEntry(nil, 16, ""); got != nil {
		t.Errorf("nil entry should return nil, got: %s", got)
	}
}

func TestFacilityFromName(t *testing.T) {
	cases := map[string]int{
		"local0":  16,
		"local1":  17,
		"local7":  23,
		"":        16,
		"garbage": 16,
	}
	for in, want := range cases {
		if got := FacilityFromName(in); got != want {
			t.Errorf("FacilityFromName(%q)=%d want %d", in, got, want)
		}
	}
}

func TestEscapeSDValueNoOpFastPath(t *testing.T) {
	in := "no-special-chars"
	if got := escapeSDValue(in); got != in {
		t.Errorf("expected unchanged, got %q", got)
	}
}

func TestSanitizeAppName(t *testing.T) {
	cases := map[string]string{
		"":                      "",
		"elchi-audit":           "elchi-audit",
		"my app":                "my_app",                // space replaced
		"tag\nwith\rnl":         "tag_with_nl",           // control bytes replaced
		"  ":                    "",                      // all illegal → empty so caller falls back to default
		"tag]bracket":           "tag]bracket",           // bracket is printable ASCII (93)
		strings.Repeat("a", 60): strings.Repeat("a", 48), // capped at 48
	}
	for in, want := range cases {
		if got := sanitizeAppName(in); got != want {
			t.Errorf("sanitizeAppName(%q)=%q want %q", in, got, want)
		}
	}
}

func TestEncodeAuditEntryUsesSanitisedTag(t *testing.T) {
	entry := &models.AuditEntry{
		ID:           "id",
		Timestamp:    time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC),
		Action:       "X",
		ResourceType: "rt",
		Success:      true,
	}
	out := string(EncodeAuditEntry(entry, 16, "evil tag\nwith newline"))
	// Header layout: "<PRI>1 TS HOST APP - APP "
	if strings.Contains(out, "evil tag") {
		t.Errorf("raw tag leaked: %s", out)
	}
	// Should appear sanitised — space and \n replaced by underscores, then truncated.
	if !strings.Contains(out, "evil_tag_with_newline") {
		t.Errorf("expected sanitised tag, got: %s", out)
	}
}

func TestEncodeAuditEntryStripsNewlines(t *testing.T) {
	entry := &models.AuditEntry{
		ID:           "id",
		Timestamp:    time.Now().UTC(),
		Action:       "CREATE",
		ResourceType: "rt",
		ResourceName: "name\nwith\rnewlines",
		Success:      true,
	}
	out := string(EncodeAuditEntry(entry, 16, ""))
	// freeform message portion is appended after the SD bracket and a space.
	idx := strings.LastIndex(out, "] ")
	if idx == -1 {
		t.Fatalf("missing SD terminator: %s", out)
	}
	tail := out[idx+2:]
	if strings.ContainsAny(tail, "\r\n") {
		t.Errorf("control bytes leaked into freeform message: %q", tail)
	}
}
