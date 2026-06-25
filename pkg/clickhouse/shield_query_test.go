package clickhouse

import (
	"strings"
	"testing"
	"time"
)

// TestClampBucketSeconds proves the summary series can never exceed
// maxSummaryBuckets buckets (the unbounded-series perf guard) and enforces the
// 60s floor.
func TestClampBucketSeconds(t *testing.T) {
	cases := []struct {
		name        string
		bucket      int
		window      time.Duration
		wantMin     int  // result must be >= this
		wantBounded bool // window/result must be <= maxSummaryBuckets
	}{
		{"floor", 1, time.Hour, 60, true},
		{"already fine 24h", 1440, 24 * time.Hour, 60, true},
		{"huge window tiny bucket", 60, 365 * 24 * time.Hour, 60, true},
		{"zero window", 0, 0, 60, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := clampBucketSeconds(c.bucket, c.window)
			if got < c.wantMin {
				t.Errorf("clampBucketSeconds(%d,%s) = %d, want >= %d", c.bucket, c.window, got, c.wantMin)
			}
			if c.wantBounded && c.window > 0 {
				if buckets := int(c.window.Seconds()) / got; buckets > maxSummaryBuckets {
					t.Errorf("window yields %d buckets (> %d cap)", buckets, maxSummaryBuckets)
				}
			}
		})
	}
}

// TestBuildShieldWhere proves the shield events WHERE is fully parameterised
// (no value interpolation → injection-safe) and that optional filters add their
// clause + arg in lockstep.
func TestBuildShieldWhere(t *testing.T) {
	from := time.Unix(1000, 0).UTC()
	to := time.Unix(2000, 0).UTC()

	t.Run("minimal", func(t *testing.T) {
		where, args := buildShieldWhere(ShieldEventsFilter{ProjectID: "p1", From: from, To: to})
		want := "WHERE project_id = ? AND ts >= ? AND ts < ?"
		if where != want {
			t.Fatalf("where = %q, want %q", where, want)
		}
		if len(args) != 3 || args[0] != "p1" || args[1] != from || args[2] != to {
			t.Fatalf("args mismatch: %#v", args)
		}
	})

	t.Run("all filters + findings only", func(t *testing.T) {
		f := ShieldEventsFilter{
			ProjectID: "p1", NodeID: "lst::p1::ip", Instance: "edge1-shield", Engine: "coraza",
			Action: "block", Severity: "high", Host: "api.example.com", Method: "POST", Path: "/x",
			FindingsOnly: true, From: from, To: to,
		}
		where, args := buildShieldWhere(f)
		for _, frag := range []string{
			"node_id = ?", "instance = ?", "engine = ?", "action = ?", "severity = ?",
			"host = ?", "method = ?", "path = ?", "action NOT IN ('allow','continue')",
		} {
			if !strings.Contains(where, frag) {
				t.Errorf("where missing %q: %s", frag, where)
			}
		}
		// 3 base args + 8 optional (findings_only adds a static clause, no arg).
		if len(args) != 11 {
			t.Fatalf("expected 11 args, got %d: %#v", len(args), args)
		}
		// No raw value should be interpolated into the SQL text.
		if strings.Contains(where, "api.example.com") || strings.Contains(where, "coraza") {
			t.Fatalf("values must be bound as args, not interpolated: %s", where)
		}
	})
}

// TestBuildShieldRollupWhere proves the rollup WHERE filters on `bucket` (not
// `ts`), carries only the rollup's dimensions, and stays fully parameterised.
func TestBuildShieldRollupWhere(t *testing.T) {
	from := time.Unix(1000, 0).UTC()
	to := time.Unix(2000, 0).UTC()
	f := ShieldEventsFilter{
		ProjectID: "p1", NodeID: "lst::p1::ip", Engine: "coraza", Action: "block",
		Severity: "high", FindingsOnly: true, From: from, To: to,
	}
	where, args := buildShieldRollupWhere(f)
	if !strings.Contains(where, "bucket >= ?") || !strings.Contains(where, "bucket < ?") {
		t.Fatalf("rollup WHERE must filter on bucket: %s", where)
	}
	if strings.Contains(where, "ts ") {
		t.Fatalf("rollup WHERE must not reference ts: %s", where)
	}
	for _, frag := range []string{"project_id = ?", "node_id = ?", "engine = ?", "action = ?", "severity = ?"} {
		if !strings.Contains(where, frag) {
			t.Errorf("rollup WHERE missing %q: %s", frag, where)
		}
	}
	// 3 base (project + from/to) + 4 optional (node/engine/action/severity).
	if len(args) != 7 {
		t.Fatalf("expected 7 args, got %d: %#v", len(args), args)
	}
}

// TestShieldRollupUsable proves the summary only reads the rollup when opted in
// AND no raw-only filter (instance/host/method/path) is set.
func TestShieldRollupUsable(t *testing.T) {
	base := ShieldEventsFilter{ProjectID: "p1"}
	cases := []struct {
		name      string
		useRollup bool
		f         ShieldEventsFilter
		want      bool
	}{
		{"disabled", false, base, false},
		{"enabled clean", true, base, true},
		{"enabled + node/engine ok", true, ShieldEventsFilter{ProjectID: "p1", NodeID: "n", Engine: "coraza"}, true},
		{"enabled but host", true, ShieldEventsFilter{ProjectID: "p1", Host: "api"}, false},
		{"enabled but path", true, ShieldEventsFilter{ProjectID: "p1", Path: "/x"}, false},
		{"enabled but method", true, ShieldEventsFilter{ProjectID: "p1", Method: "GET"}, false},
		{"enabled but instance", true, ShieldEventsFilter{ProjectID: "p1", Instance: "edge-shield"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cl := &Client{shieldUseRollup: c.useRollup}
			if got := cl.shieldRollupUsable(c.f); got != c.want {
				t.Errorf("shieldRollupUsable = %v, want %v", got, c.want)
			}
		})
	}
}
