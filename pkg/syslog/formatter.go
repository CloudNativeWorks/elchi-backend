package syslog

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

const (
	// AppName is the RFC5424 APP-NAME field; receivers use this as the
	// program identity for filtering / dashboards.
	AppName = "elchi-audit"

	// SDID is the RFC5424 structured-data identifier. The "@" suffix carries
	// a private-enterprise identifier so it does not collide with IANA names.
	SDID = "audit@elchi"

	severityInfo    = 6
	severityWarning = 4
	facilityLocal0  = 16
)

// Field length caps protect against operator error / unbounded user input
// blowing the message past the receiver's framing limit (rsyslog default 8K).
const (
	maxResourceID   = 128
	maxAction       = 64
	maxResourceName = 128
	maxUsername     = 64
	maxProject      = 64
	maxPath         = 256
	maxMessage      = 256
	maxError        = 256
)

var cachedHostname = resolveHostname()

func resolveHostname() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "-"
}

// Hostname returns the cached host identifier used in syslog HOSTNAME field.
func Hostname() string { return cachedHostname }

// EncodeAuditEntry serialises a models.AuditEntry as an RFC5424 syslog
// frame. facility selects local0..local7 (default local0) and tag is used
// as both APP-NAME and MSGID; pass an empty tag to fall back to AppName.
func EncodeAuditEntry(entry *models.AuditEntry, facility int, tag string) []byte {
	if entry == nil {
		return nil
	}

	app := sanitizeAppName(tag)
	if app == "" {
		app = AppName
	}

	severity := severityInfo
	if !entry.Success {
		severity = severityWarning
	}
	pri := facility*8 + severity

	ts := entry.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	tsStr := ts.UTC().Format(time.RFC3339Nano)

	outcome := "success"
	if !entry.Success {
		outcome = "failure"
	}

	var sd strings.Builder
	sd.WriteByte('[')
	sd.WriteString(SDID)
	writeParam(&sd, "audit_id", entry.ID, 64)
	writeParam(&sd, "action", entry.Action, maxAction)
	writeParam(&sd, "actor", entry.Username, maxUsername)
	writeParam(&sd, "actor_id", entry.UserID, 64)
	writeParam(&sd, "actor_role", entry.UserRole, 32)
	writeParam(&sd, "resource_type", entry.ResourceType, 64)
	writeParam(&sd, "resource_id", entry.ResourceID, maxResourceID)
	writeParam(&sd, "resource_name", entry.ResourceName, maxResourceName)
	writeParam(&sd, "outcome", outcome, 16)
	writeParam(&sd, "project", entry.Project, maxProject)
	writeParam(&sd, "method", entry.Method, 8)
	writeParam(&sd, "path", entry.Path, maxPath)
	writeParam(&sd, "client_ip", entry.ClientIP, 64)
	writeParam(&sd, "request_id", entry.RequestID, 64)
	writeParam(&sd, "api_type", string(entry.APIType), 32)
	if entry.ResponseStatus != 0 {
		writeParam(&sd, "status", fmt.Sprintf("%d", entry.ResponseStatus), 8)
	}
	if entry.SaveOrPublish != "" {
		writeParam(&sd, "save_or_publish", entry.SaveOrPublish, 16)
	}
	if entry.ErrorMessage != "" {
		writeParam(&sd, "error", entry.ErrorMessage, maxError)
	}
	sd.WriteByte(']')

	msg := buildFreeformMessage(entry)

	// <PRI>VERSION TIMESTAMP HOSTNAME APP-NAME PROCID MSGID STRUCTURED-DATA MSG
	header := fmt.Sprintf("<%d>1 %s %s %s - %s ", pri, tsStr, cachedHostname, app, app)
	out := make([]byte, 0, len(header)+sd.Len()+1+len(msg))
	out = append(out, header...)
	out = append(out, sd.String()...)
	if msg != "" {
		out = append(out, ' ')
		out = append(out, msg...)
	}
	return out
}

// EncodeTestMessage produces a synthetic frame suitable for connection tests.
func EncodeTestMessage(_ Config) []byte {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	pri := facilityLocal0*8 + severityInfo
	sd := fmt.Sprintf("[%s audit_id=%q action=%q actor=%q outcome=%q]",
		SDID, "test", "SYSLOG_TEST", AppName, "success")
	return fmt.Appendf(nil, "<%d>1 %s %s %s - %s %s elchi-audit syslog connectivity test\n",
		pri, now, cachedHostname, AppName, AppName, sd)
}

// writeParam appends a `key="value"` pair after the SD-ELEMENT identifier.
// Empty values are skipped so the receiver sees only present fields.
func writeParam(b *strings.Builder, key, value string, cap int) {
	if value == "" {
		return
	}
	v := value
	if len(v) > cap {
		v = v[:cap]
	}
	b.WriteByte(' ')
	b.WriteString(key)
	b.WriteString(`="`)
	b.WriteString(escapeSDValue(v))
	b.WriteByte('"')
}

// escapeSDValue escapes the three characters reserved by RFC5424 §6.3.3:
// `"`, `\`, and `]`.
func escapeSDValue(v string) string {
	if !strings.ContainsAny(v, `"\]`) {
		return v
	}
	var b strings.Builder
	b.Grow(len(v) + 4)
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch c {
		case '"', '\\', ']':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// buildFreeformMessage produces a short human-readable summary appended to
// the structured data. Receivers that don't parse SD elements still get
// useful context.
func buildFreeformMessage(entry *models.AuditEntry) string {
	parts := make([]string, 0, 4)
	if entry.Action != "" {
		parts = append(parts, entry.Action)
	}
	if entry.ResourceType != "" {
		res := entry.ResourceType
		if entry.ResourceName != "" {
			res = res + ":" + entry.ResourceName
		} else if entry.ResourceID != "" {
			res = res + ":" + entry.ResourceID
		}
		parts = append(parts, res)
	}
	if entry.Username != "" {
		parts = append(parts, "by "+entry.Username)
	}
	if !entry.Success && entry.ErrorMessage != "" {
		errMsg := entry.ErrorMessage
		if len(errMsg) > maxMessage {
			errMsg = errMsg[:maxMessage]
		}
		parts = append(parts, "err="+errMsg)
	}
	msg := strings.Join(parts, " ")
	// Strip control bytes that would corrupt the syslog frame.
	msg = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, msg)
	if len(msg) > maxMessage {
		msg = msg[:maxMessage]
	}
	return msg
}

// sanitizeAppName scrubs the APP-NAME / MSGID candidate so it stays within
// the RFC5424 grammar (printable US-ASCII, ≤48 chars). Any byte outside
// 33–126 is replaced with '_'. An all-illegal input returns "" so the
// caller falls back to the package default.
func sanitizeAppName(s string) string {
	if s == "" {
		return ""
	}
	const maxLen = 48
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	out := make([]byte, len(s))
	allReplaced := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 33 && c <= 126 {
			out[i] = c
			allReplaced = false
		} else {
			out[i] = '_'
		}
	}
	if allReplaced {
		return ""
	}
	return string(out)
}

// FacilityFromName resolves "local0".."local7" to its numeric value. Any
// other input returns local0 — the caller is expected to validate before.
func FacilityFromName(name string) int {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "local1":
		return 17
	case "local2":
		return 18
	case "local3":
		return 19
	case "local4":
		return 20
	case "local5":
		return 21
	case "local6":
		return 22
	case "local7":
		return 23
	default:
		return facilityLocal0
	}
}
