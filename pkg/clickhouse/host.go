package clickhouse

import "strings"

// NormalizeHost normalizes an HTTP :authority / Host value the SAME way the
// collector does at write time (elchi-collector internal/normalize/host.go).
// It strips a trailing DEFAULT port (:443 / :80) and lowercases. Non-default
// ports are preserved — they distinguish services on the same host. Empty
// input returns "".
//
// This MUST match the collector exactly: api_inventory.host and
// api_events_raw.host are stored stripped+lowercased, so any read-side host
// filter (Mongo or ClickHouse) has to apply the identical transform or it
// silently fails to match (e.g. UI sends "api.example.com:443" but the stored
// value is "api.example.com").
//
// Examples:
//
//	"API.example.com:443" → "api.example.com"
//	"45.141.118.95:443"   → "45.141.118.95"
//	"host:8080"           → "host:8080"        (non-default port kept)
//	"[::1]:443"           → "[::1]"            (IPv6, default port stripped)
//	"[2001:db8::1]:8080"  → "[2001:db8::1]:8080"
//	"::1"                 → "::1"              (bare IPv6, no port)
func NormalizeHost(authority string) string {
	if authority == "" {
		return ""
	}
	return strings.ToLower(stripDefaultPort(authority))
}

// stripDefaultPort removes a trailing ":443" / ":80" port, handling bracketed
// IPv6 ("[::1]:443") and leaving bare (unbracketed) IPv6 untouched. Mirrors the
// collector's implementation.
func stripDefaultPort(host string) string {
	i := strings.LastIndexByte(host, ':')
	if i <= 0 {
		return host // no colon (or leading colon) → no port
	}
	if host[0] == '[' {
		// Bracketed IPv6: the port colon must come AFTER the closing ']'.
		closing := strings.IndexByte(host, ']')
		if closing < 0 || i < closing {
			return host // malformed, or the colon is inside the brackets (no port)
		}
	} else if strings.IndexByte(host, ':') != i {
		// Unbracketed with more than one colon → bare IPv6, not host:port.
		return host
	}
	switch host[i+1:] {
	case "443", "80":
		return host[:i]
	default:
		return host
	}
}
