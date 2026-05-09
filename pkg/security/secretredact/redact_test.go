package secretredact

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/r3labs/diff/v3"
)

// ---------------------------------------------------------------------------
// IsSensitiveField
// ---------------------------------------------------------------------------

func TestIsSensitiveField(t *testing.T) {
	cases := []struct {
		field    string
		expected bool
	}{
		// Sensitive leaves — content lives here, must be masked.
		{"inline_string", true},
		{"INLINE_STRING", true},
		{"inline_bytes", true},
		// Container fields — must NOT be masked, FE structure depends on them.
		{"private_key", false},
		{"certificate_chain", false},
		{"password", false},
		{"trusted_ca", false},
		{"crl", false},
		{"keys", false},
		{"secret", false},
		// Non-sensitive primitives.
		{"name", false},
		{"watched_directory", false},
		{"path", false},
		{"filename", false},
		{"environment_variable", false},
	}
	for _, c := range cases {
		if got := IsSensitiveField(c.field); got != c.expected {
			t.Errorf("IsSensitiveField(%q) = %v, want %v", c.field, got, c.expected)
		}
	}
}

// ---------------------------------------------------------------------------
// MaskSecretData
// ---------------------------------------------------------------------------

func TestMaskSecretData_TLSCertResource(t *testing.T) {
	in := map[string]any{
		"resource": []any{
			map[string]any{
				"certificate_chain": map[string]any{
					"inline_string": "-----BEGIN CERTIFICATE-----\nABC...\n",
				},
				"private_key": map[string]any{
					"inline_string": "-----BEGIN RSA PRIVATE KEY-----\nXYZ...\n",
				},
				"watched_directory": map[string]any{
					"path": "/etc/certs",
				},
			},
		},
	}
	out, ok := MaskSecretData(in).(map[string]any)
	if !ok {
		t.Fatalf("MaskSecretData top-level not a map")
	}
	res := out["resource"].([]any)[0].(map[string]any)

	if res["certificate_chain"].(map[string]any)["inline_string"] != RedactionSentinel {
		t.Errorf("certificate_chain.inline_string not masked")
	}
	if res["private_key"].(map[string]any)["inline_string"] != RedactionSentinel {
		t.Errorf("private_key.inline_string not masked")
	}
	if res["watched_directory"].(map[string]any)["path"] != "/etc/certs" {
		t.Errorf("watched_directory.path must NOT be masked")
	}
}

func TestMaskSecretData_InlineBytesUsesBase64Sentinel(t *testing.T) {
	in := map[string]any{
		"private_key": map[string]any{
			"inline_bytes": "BASE64ENCODEDKEYDATAxxxxxxxxx",
		},
	}
	out := MaskSecretData(in).(map[string]any)
	got := out["private_key"].(map[string]any)["inline_bytes"]
	if got != RedactionSentinelBase64 {
		t.Errorf("inline_bytes must be masked with base64 sentinel, got %v", got)
	}
	// Sanity: the base64 sentinel must actually decode.
	if _, err := base64.StdEncoding.DecodeString(RedactionSentinelBase64); err != nil {
		t.Errorf("RedactionSentinelBase64 is not valid base64: %v", err)
	}
}

func TestMaskSecretData_PreservesSDSReferences(t *testing.T) {
	in := map[string]any{
		"common_tls_context": map[string]any{
			"tls_certificate_sds_secret_configs": []any{
				map[string]any{"name": "edge-cert"},
			},
			"validation_context_sds_secret_config": map[string]any{
				"name": "edge-ca",
			},
		},
	}
	out := MaskSecretData(in).(map[string]any)
	ctx := out["common_tls_context"].(map[string]any)
	if ctx["tls_certificate_sds_secret_configs"].([]any)[0].(map[string]any)["name"] != "edge-cert" {
		t.Errorf("SDS secret config reference must not be masked")
	}
	if ctx["validation_context_sds_secret_config"].(map[string]any)["name"] != "edge-ca" {
		t.Errorf("SDS validation context reference must not be masked")
	}
}

// ---------------------------------------------------------------------------
// MergePreservingRedacted — every documented edge case
// ---------------------------------------------------------------------------

// Case 1: simple sentinel preserve from old.
func TestMerge_SimplePreserve(t *testing.T) {
	old := map[string]any{"private_key": map[string]any{"inline_string": "REAL_KEY"}}
	new_ := map[string]any{"private_key": map[string]any{"inline_string": RedactionSentinel}}
	merged := MergePreservingRedacted(new_, old)
	if got := merged.(map[string]any)["private_key"].(map[string]any)["inline_string"]; got != "REAL_KEY" {
		t.Errorf("expected REAL_KEY restored, got %q", got)
	}
}

// Case 2: NEW field added (e.g. watched_directory) — kept as-is.
func TestMerge_NewFieldAdded(t *testing.T) {
	old := map[string]any{
		"certificate_chain": map[string]any{"inline_string": "REAL_CERT"},
		"private_key":       map[string]any{"inline_string": "REAL_KEY"},
	}
	new_ := map[string]any{
		"certificate_chain": map[string]any{"inline_string": RedactionSentinel},
		"private_key":       map[string]any{"inline_string": RedactionSentinel},
		"watched_directory": map[string]any{"path": "/etc/certs"},
	}
	merged := MergePreservingRedacted(new_, old).(map[string]any)
	if merged["certificate_chain"].(map[string]any)["inline_string"] != "REAL_CERT" {
		t.Errorf("cert not preserved")
	}
	if merged["private_key"].(map[string]any)["inline_string"] != "REAL_KEY" {
		t.Errorf("key not preserved")
	}
	wd, ok := merged["watched_directory"].(map[string]any)
	if !ok || wd["path"] != "/etc/certs" {
		t.Errorf("watched_directory not added: %v", merged["watched_directory"])
	}
}

// Case 3: DataSource type swap (inline_string → filename).
func TestMerge_DataSourceTypeSwap(t *testing.T) {
	old := map[string]any{
		"private_key": map[string]any{"inline_string": "REAL_KEY"},
	}
	new_ := map[string]any{
		"private_key": map[string]any{"filename": "/etc/key.pem"},
	}
	merged := MergePreservingRedacted(new_, old).(map[string]any)
	pk := merged["private_key"].(map[string]any)
	if _, has := pk["inline_string"]; has {
		t.Errorf("old inline_string must drop after type swap")
	}
	if pk["filename"] != "/etc/key.pem" {
		t.Errorf("filename should be kept, got %v", pk["filename"])
	}
}

// Case 4: explicit clear (empty string is NOT the sentinel).
func TestMerge_ExplicitClear(t *testing.T) {
	old := map[string]any{
		"private_key": map[string]any{"inline_string": "REAL_KEY"},
	}
	new_ := map[string]any{
		"private_key": map[string]any{"inline_string": ""},
	}
	merged := MergePreservingRedacted(new_, old).(map[string]any)
	if merged["private_key"].(map[string]any)["inline_string"] != "" {
		t.Errorf("explicit clear (empty string) must NOT be preserved")
	}
}

// Case 5: multiple sensitive fields in same doc — independent restore.
func TestMerge_MultipleSensitiveFields(t *testing.T) {
	old := map[string]any{
		"private_key": map[string]any{"inline_string": "REAL_KEY"},
		"password":    map[string]any{"inline_string": "REAL_PASS"},
	}
	new_ := map[string]any{
		"private_key": map[string]any{"inline_string": RedactionSentinel},
		"password":    map[string]any{"inline_string": RedactionSentinel},
	}
	merged := MergePreservingRedacted(new_, old).(map[string]any)
	if merged["private_key"].(map[string]any)["inline_string"] != "REAL_KEY" {
		t.Errorf("private_key not restored")
	}
	if merged["password"].(map[string]any)["inline_string"] != "REAL_PASS" {
		t.Errorf("password not restored")
	}
}

// Case 6: sentinel literal sent for a NEW field with no old counterpart —
// pass through (degenerate case, defensive).
func TestMerge_SentinelInNewFieldWithNoOld(t *testing.T) {
	old := map[string]any{}
	new_ := map[string]any{
		"private_key": map[string]any{"inline_string": RedactionSentinel},
	}
	merged := MergePreservingRedacted(new_, old).(map[string]any)
	if merged["private_key"].(map[string]any)["inline_string"] != RedactionSentinel {
		t.Errorf("sentinel must pass through when no old value exists")
	}
}

// inline_bytes uses the base64 sentinel; merge must accept either form.
func TestMerge_InlineBytesBase64Sentinel(t *testing.T) {
	old := map[string]any{
		"private_key": map[string]any{"inline_bytes": "REAL_BASE64_BYTES"},
	}
	new_ := map[string]any{
		"private_key": map[string]any{"inline_bytes": RedactionSentinelBase64},
	}
	merged := MergePreservingRedacted(new_, old).(map[string]any)
	if got := merged["private_key"].(map[string]any)["inline_bytes"]; got != "REAL_BASE64_BYTES" {
		t.Errorf("inline_bytes base64 sentinel not restored: got %v", got)
	}
}

// Round-trip: mask then merge with original → result equals original.
func TestRoundTrip_MaskThenMerge(t *testing.T) {
	original := map[string]any{
		"resource": []any{
			map[string]any{
				"certificate_chain": map[string]any{"inline_string": "CERT_PEM"},
				"private_key":       map[string]any{"inline_string": "KEY_PEM"},
				"watched_directory": map[string]any{"path": "/etc/certs"},
			},
		},
	}
	// Deep-copy original via JSON so we can compare without mutation.
	originalJSON, _ := json.Marshal(original)
	var originalCopy any
	_ = json.Unmarshal(originalJSON, &originalCopy)

	masked := MaskSecretData(original)
	merged := MergePreservingRedacted(masked, original)
	if !reflect.DeepEqual(merged, originalCopy) {
		mb, _ := json.Marshal(merged)
		ob, _ := json.Marshal(originalCopy)
		t.Errorf("round-trip mismatch.\n  original: %s\n  merged:   %s", ob, mb)
	}
}

// ---------------------------------------------------------------------------
// RedactChangelog
// ---------------------------------------------------------------------------

func TestRedactChangelog_MasksSensitivePathsKeepsOthers(t *testing.T) {
	cl := diff.Changelog{
		{Path: []string{"resource", "0", "private_key", "inline_string"}, From: "OLD_KEY", To: "NEW_KEY", Type: diff.UPDATE},
		{Path: []string{"resource", "0", "watched_directory", "path"}, From: "/old", To: "/new", Type: diff.UPDATE},
	}
	out := RedactChangelog(cl)
	if out[0].From != RedactionSentinel || out[0].To != RedactionSentinel {
		t.Errorf("private_key changelog must be redacted: %+v", out[0])
	}
	if !reflect.DeepEqual(out[0].Path, cl[0].Path) {
		t.Errorf("path must be preserved")
	}
	if out[1].From != "/old" || out[1].To != "/new" {
		t.Errorf("non-sensitive path must be untouched: %+v", out[1])
	}
}

// ---------------------------------------------------------------------------
// MaskJSON / MergeJSON (handler-facing wrappers)
// ---------------------------------------------------------------------------

type fakeResource struct {
	Name string `json:"name"`
	Data struct {
		PrivateKey struct {
			InlineString string `json:"inline_string"`
		} `json:"private_key"`
	} `json:"data"`
}

func TestMaskJSON_TypedStructRoundTrip(t *testing.T) {
	r := fakeResource{Name: "edge"}
	r.Data.PrivateKey.InlineString = "REAL_KEY"
	masked, err := MaskJSON(r)
	if err != nil {
		t.Fatalf("MaskJSON: %v", err)
	}
	got := masked["data"].(map[string]any)["private_key"].(map[string]any)["inline_string"]
	if got != RedactionSentinel {
		t.Errorf("MaskJSON did not redact, got %v", got)
	}
}

// TestMerge_ArrayReorderIsPositional documents the limitation noted in the
// comment on the []any branch of MergePreservingRedacted: array merge is
// positional, so reordering elements while leaving redaction sentinels in
// place restores values from the new index's old slot — i.e. cross-element
// contamination. Consumers that need reorderable secret arrays must send a
// fully-resolved (sentinel-free) array.
func TestMerge_ArrayReorderIsPositional(t *testing.T) {
	old := map[string]any{
		"keys": []any{
			map[string]any{"inline_string": "KEY_A"},
			map[string]any{"inline_string": "KEY_B"},
		},
	}
	// Caller "reorders" by swapping the array order, but each element still
	// carries the sentinel (typical FE behavior if it didn't materialize the
	// keys before reorder).
	new_ := map[string]any{
		"keys": []any{
			map[string]any{"inline_string": RedactionSentinel}, // intends KEY_B
			map[string]any{"inline_string": RedactionSentinel}, // intends KEY_A
		},
	}
	merged := MergePreservingRedacted(new_, old).(map[string]any)
	keys := merged["keys"].([]any)
	// Positional merge → index 0 restores KEY_A (the OLD index 0), not KEY_B.
	// This is the documented behavior; assert it so the limitation is locked.
	if got := keys[0].(map[string]any)["inline_string"]; got != "KEY_A" {
		t.Errorf("array merge is positional (intentional limitation): index 0 should restore KEY_A, got %q", got)
	}
	if got := keys[1].(map[string]any)["inline_string"]; got != "KEY_B" {
		t.Errorf("index 1 should restore KEY_B, got %q", got)
	}
}

// TestMerge_LeafSentinelInNonSensitiveStringNotPreserved verifies that a
// sentinel literal sent in a NON-sensitive string field does NOT get
// restored from the old value. This closes a previously-present bug where
// the leaf-level fallback would let an attacker drop "***REDACTED***"
// into e.g. a tags array element to read back the corresponding old value.
func TestMerge_LeafSentinelInNonSensitiveStringNotPreserved(t *testing.T) {
	old := map[string]any{
		"tags": []any{"prod-secret-cert"},
	}
	new_ := map[string]any{
		"tags": []any{RedactionSentinel}, // attacker tries to "read back" old tag
	}
	merged := MergePreservingRedacted(new_, old).(map[string]any)
	tags := merged["tags"].([]any)
	if got := tags[0]; got == "prod-secret-cert" {
		t.Errorf("leaf-level sentinel must NOT preserve from old in non-sensitive context (would be info leak), got %v", got)
	}
	if got := tags[0]; got != RedactionSentinel {
		t.Errorf("expected literal sentinel pass-through, got %v", got)
	}
}

// TestMerge_TypeMismatchSilentPassThrough documents that when old and new
// values have incompatible types at the same path, merge silently passes
// the new value through. This is intentional for forward-compat with
// schema evolution but worth a regression test.
func TestMerge_TypeMismatchSilentPassThrough(t *testing.T) {
	// Old has a map, new has a string at the same key.
	old := map[string]any{"private_key": map[string]any{"inline_string": "OLD"}}
	new_ := map[string]any{"private_key": "raw-string"}
	merged := MergePreservingRedacted(new_, old).(map[string]any)
	if got := merged["private_key"]; got != "raw-string" {
		t.Errorf("type-mismatch new should pass through, got %v", got)
	}
}

// TestIsAuditSensitivePath verifies the audit-time predicate covers the
// broader credential surface (not just inline_*).
func TestIsAuditSensitivePath(t *testing.T) {
	cases := []struct {
		path     []string
		expected bool
	}{
		{[]string{"data", "private_key", "inline_string"}, true},
		{[]string{"data", "password"}, true},
		{[]string{"data", "passphrase"}, true},
		{[]string{"data", "token"}, true},
		{[]string{"data", "api_key"}, true},
		{[]string{"data", "credential"}, true},
		{[]string{"data", "credentials"}, true},
		{[]string{"data", "client_secret"}, true},
		{[]string{"data", "dns_secret"}, true},
		{[]string{"data", "secret"}, true},
		{[]string{"data", "secret_config", "name"}, false}, // SDS reference, NOT sensitive
		{[]string{"data", "secret_provider", "name"}, false},
		{[]string{"data", "watched_directory", "path"}, false},
		{[]string{"data", "name"}, false},
		{[]string{"data", "filename"}, false},
		{[]string{"PRIVATE_KEY"}, true}, // case-insensitive
	}
	for _, c := range cases {
		if got := IsAuditSensitivePath(c.path); got != c.expected {
			t.Errorf("IsAuditSensitivePath(%v) = %v, want %v", c.path, got, c.expected)
		}
	}
}

func TestMergeJSON_RawBodies(t *testing.T) {
	old, _ := json.Marshal(map[string]any{
		"private_key": map[string]any{"inline_string": "REAL_KEY"},
	})
	new_, _ := json.Marshal(map[string]any{
		"private_key": map[string]any{"inline_string": RedactionSentinel},
		"watched_directory": map[string]any{"path": "/etc/certs"},
	})
	merged, err := MergeJSON(new_, old)
	if err != nil {
		t.Fatalf("MergeJSON: %v", err)
	}
	var out map[string]any
	_ = json.Unmarshal(merged, &out)
	if got := out["private_key"].(map[string]any)["inline_string"]; got != "REAL_KEY" {
		t.Errorf("MergeJSON failed to restore, got %v", got)
	}
	if out["watched_directory"].(map[string]any)["path"] != "/etc/certs" {
		t.Errorf("MergeJSON dropped watched_directory")
	}
}
