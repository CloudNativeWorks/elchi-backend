// Package secretredact centralises masking and preserve-on-redacted helpers
// for Envoy secret payloads (TLS certs, validation contexts, generic secrets).
//
// Two physical wire shapes flow through it:
//
//   - HTTP/JSON management API responses: sensitive *string* fields (e.g.
//     `inline_string`) are replaced with the literal RedactionSentinel.
//   - HTTP/JSON management API requests: when an incoming request body
//     carries the sentinel at a sensitive path, MergePreservingRedacted
//     restores the original value from the existing DB document so a no-op
//     PUT does not wipe a key that the user never intended to change.
//
// `inline_bytes` is a protobuf `bytes` field — base64-encoded on the wire.
// The text sentinel is not valid base64, so we emit a separate
// RedactionSentinelBase64 (the base64 of the text sentinel) for those paths
// to keep FE proto decoders from choking.
package secretredact

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/r3labs/diff/v3"
)

// RedactionSentinel is the literal value substituted into sensitive
// *string* fields on the HTTP wire. Mirror the value already used by
// control-plane/server/bridge/snapshot.go so audit / debug surfaces stay
// consistent.
const RedactionSentinel = "***REDACTED***"

// RedactionSentinelBase64 is the base64 encoding of RedactionSentinel,
// used for protobuf `bytes` fields (e.g. `inline_bytes`). It is itself a
// valid base64 string so FE proto decoders accept it without raising.
var RedactionSentinelBase64 = base64.StdEncoding.EncodeToString([]byte(RedactionSentinel))

// IsSensitiveField reports whether a JSON field name should have its value
// masked on outbound responses (and conversely, whether a sentinel at that
// field on an inbound request should be merged back from the existing
// document).
//
// We deliberately use a NARROWER list than control-plane/server/bridge/
// snapshot.go's debug-endpoint masker. The snapshot debug masker replaces
// entire `private_key` / `password` / `secret` *containers* with a string
// sentinel, which destroys the JSON structure FE editors depend on.
//
// For Envoy secrets the sensitive payload always lands at the
// `DataSource.inline_string` (text PEM) or `DataSource.inline_bytes`
// (base64 binary) leaf — every secret subtype (`TlsCertificate`,
// `CertificateValidationContext`, `TlsSessionTicketKeys`, `GenericSecret`)
// follows the same shape. Masking only those two leaves keeps
// `private_key`, `certificate_chain`, `password`, `trusted_ca`, `crl`,
// `secret`, `keys[]` *containers* intact for the FE form to rehydrate.
//
// `filename` and `environment_variable` (the other DataSource variants)
// are paths/identifiers, not content, and are intentionally NOT masked.
//
// For audit-log redaction use IsAuditSensitivePath instead — that helper
// covers a broader set (passwords, tokens, api keys, etc.) appropriate
// for log scrubbing where there is no FE form to rehydrate.
func IsSensitiveField(fieldName string) bool {
	switch strings.ToLower(fieldName) {
	case "inline_string", "inline_bytes":
		return true
	}
	return false
}

// IsAuditSensitivePath reports whether any segment of an r3labs/diff
// path traverses a field whose value should NOT appear in an audit log
// in plaintext. Broader than IsSensitiveField because audit consumers
// have no need to read the underlying values back.
//
// Includes the leaf set IsSensitiveField returns plus parent containers
// and credential-style fields that may show up in non-secret resources
// (clusters with inline TLS material, settings docs holding API tokens,
// etc.).
func IsAuditSensitivePath(path []string) bool {
	for _, seg := range path {
		segLower := strings.ToLower(seg)
		switch segLower {
		case "inline_string", "inline_bytes",
			"private_key", "password", "passphrase",
			"token", "api_key", "client_secret",
			"credential", "credentials":
			return true
		}
		// Suffix: anything ending in "_secret" (e.g. dns_secret) but
		// NOT SDS reference fields (secret_config / secret_provider).
		if strings.HasSuffix(segLower, "_secret") &&
			!strings.Contains(segLower, "secret_config") &&
			!strings.Contains(segLower, "secret_provider") {
			return true
		}
		// "secret" alone is also sensitive.
		if segLower == "secret" {
			return true
		}
	}
	return false
}

// MaskSecretData walks a JSON-decoded structure (map[string]any / []any)
// and replaces values at sensitive field names with the appropriate
// sentinel. Non-sensitive fields and non-collection leaves are returned as
// is. The input is not mutated; a fresh structure is returned.
func MaskSecretData(data any) any {
	switch v := data.(type) {
	case map[string]any:
		masked := make(map[string]any, len(v))
		for key, value := range v {
			if IsSensitiveField(key) {
				masked[key] = sentinelFor(key)
			} else {
				masked[key] = MaskSecretData(value)
			}
		}
		return masked
	case []any:
		masked := make([]any, len(v))
		for i, item := range v {
			masked[i] = MaskSecretData(item)
		}
		return masked
	default:
		return v
	}
}

// MergePreservingRedacted produces a copy of `newData` with any value at a
// sensitive path that equals the redaction sentinel replaced by the
// corresponding value from `oldData`. Paths that exist only in `newData`
// (e.g. user adding a `watched_directory`) are kept unchanged. Paths that
// exist only in `oldData` are dropped (so an explicit empty string `""`
// from the user truly clears a field).
//
// The walk is structural: sentinel detection happens at every node, not
// just at known TLS paths. This keeps the helper schema-agnostic across
// all four secret subtypes.
func MergePreservingRedacted(newData, oldData any) any {
	switch newVal := newData.(type) {
	case map[string]any:
		oldMap, _ := oldData.(map[string]any)
		merged := make(map[string]any, len(newVal))
		for k, v := range newVal {
			oldChild, hasOld := mapLookup(oldMap, k)
			if isRedacted(v, k) {
				if hasOld {
					merged[k] = oldChild
				} else {
					// Sentinel literal sent for a NEW field with no
					// corresponding old value — pass through. This is
					// virtually never a real user intent but harmless.
					merged[k] = v
				}
				continue
			}
			merged[k] = MergePreservingRedacted(v, oldChild)
		}
		return merged
	case []any:
		// LIMITATION: array merge is positional. If the caller reorders an
		// array (e.g. swaps Sets[0] and Sets[1]) and any element contains
		// a redaction sentinel, the sentinel will be restored from the
		// new position's old value — i.e. cross-element contamination.
		// This is acceptable for TLS cert subtypes today (single-element
		// `resource[N]` arrays, append-only `keys[]`, etc.) but consumers
		// that build reorderable UIs should send a fully-resolved array
		// (no sentinels) when reordering. Tracked in TestMerge_ArrayReorderIsPositional.
		oldSlice, _ := oldData.([]any)
		merged := make([]any, len(newVal))
		for i, item := range newVal {
			var oldItem any
			if i < len(oldSlice) {
				oldItem = oldSlice[i]
			}
			merged[i] = MergePreservingRedacted(item, oldItem)
		}
		return merged
	default:
		// Leaves: pass through verbatim. We deliberately do NOT preserve
		// the sentinel from old here because we have no field-name
		// context — an attacker could otherwise drop the literal sentinel
		// into a non-secret string field (e.g. a tags array element) to
		// "read back" the corresponding old value. Sentinel restore must
		// only happen in the field-aware map case above (isRedacted gates
		// on IsSensitiveField).
		return newVal
	}
}

// RedactChangelog walks an r3labs/diff Changelog and replaces both the
// `From` and `To` values of any change whose path traverses a sensitive
// field name (per IsAuditSensitivePath, which is broader than
// IsSensitiveField — covers passwords, tokens, credentials, etc. that
// can appear in non-secret resources too) with the redaction sentinel.
// Paths are preserved so the audit log still reports *that* a sensitive
// field changed without disclosing *what* it was.
//
// Safe to call on every changelog regardless of collection: non-
// sensitive paths pass through untouched.
func RedactChangelog(changelog diff.Changelog) diff.Changelog {
	if len(changelog) == 0 {
		return changelog
	}
	out := make(diff.Changelog, len(changelog))
	for i, change := range changelog {
		out[i] = change
		if IsAuditSensitivePath(change.Path) {
			out[i].From = RedactionSentinel
			out[i].To = RedactionSentinel
		}
	}
	return out
}

// --- helpers ---------------------------------------------------------------

// sentinelFor returns the appropriate sentinel for a given field name.
// `inline_bytes` is a protobuf bytes field (base64-encoded on the wire) so
// we emit a base64-valid sentinel; everything else gets the text sentinel.
func sentinelFor(fieldName string) string {
	if strings.EqualFold(fieldName, "inline_bytes") {
		return RedactionSentinelBase64
	}
	return RedactionSentinel
}

// isRedacted decides whether `v` at field `fieldName` should trigger
// preserve-from-old. We branch by field name to accept either sentinel
// where appropriate.
func isRedacted(v any, fieldName string) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	if !IsSensitiveField(fieldName) {
		return false
	}
	if strings.EqualFold(fieldName, "inline_bytes") {
		return s == RedactionSentinelBase64 || s == RedactionSentinel
	}
	return s == RedactionSentinel
}

// mapLookup is a nil-safe map access.
func mapLookup(m map[string]any, k string) (any, bool) {
	if m == nil {
		return nil, false
	}
	v, ok := m[k]
	return v, ok
}

// --- convenience wrappers used by handlers ---------------------------------

// MaskJSON marshals `v` to JSON, walks the result with MaskSecretData, and
// returns the masked map. Useful when the caller has a typed struct and
// just wants to hand back something Gin can re-serialize. Any internal
// marshal error is returned so the caller can propagate.
func MaskJSON(v any) (map[string]any, error) {
	intermediate, err := jsonRoundtrip(v)
	if err != nil {
		return nil, err
	}
	masked, ok := MaskSecretData(intermediate).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("masked top-level value was not an object (got %T)", intermediate)
	}
	return masked, nil
}

// MaskJSONList is the slice-of-objects variant of MaskJSON.
func MaskJSONList(items []any) ([]any, error) {
	out := make([]any, 0, len(items))
	for _, item := range items {
		masked, err := MaskJSON(item)
		if err != nil {
			return nil, err
		}
		out = append(out, masked)
	}
	return out, nil
}

// MergeJSON parses both byte slices, runs MergePreservingRedacted, and
// returns the merged JSON bytes. Used by handlers that mediate raw HTTP
// bodies (e.g. the secrets PUT path that overrides _original_body).
func MergeJSON(newJSON, oldJSON []byte) ([]byte, error) {
	var newAny, oldAny any
	if err := json.Unmarshal(newJSON, &newAny); err != nil {
		return nil, fmt.Errorf("merge: parse new: %w", err)
	}
	if err := json.Unmarshal(oldJSON, &oldAny); err != nil {
		return nil, fmt.Errorf("merge: parse old: %w", err)
	}
	merged := MergePreservingRedacted(newAny, oldAny)
	return json.Marshal(merged)
}

// jsonRoundtrip marshals `v` then unmarshals into a generic any so the
// MaskSecretData walker (which operates on map[string]any / []any) can
// process arbitrary typed structs.
func jsonRoundtrip(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}
