package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/proto"
	"golang.org/x/sync/errgroup"
)

// clickHouseUnknownTable is ClickHouse's UNKNOWN_TABLE server error code. The
// shield audit table (elchi_shield_audit) is created lazily by elchi-shield on
// its first write, so before any edge reports a security decision the table does
// not exist yet. A read against it then fails with this code — which is the
// legitimate "no events yet" state, not a query failure.
const clickHouseUnknownTable = 60

// isUnknownTable reports whether err is ClickHouse UNKNOWN_TABLE, so the shield
// read paths can treat a not-yet-created audit table as an empty result instead
// of a 502.
func isUnknownTable(err error) bool {
	var ex *proto.Exception
	if errors.As(err, &ex) {
		return ex.Code == clickHouseUnknownTable
	}
	return false
}

// ShieldEvent is one row from elchi-shield's ClickHouse audit table
// (`elchi_shield_audit`). It is redacted by construction on the shield side:
// no header values or body content, query string stripped from path. Schema is
// pinned by elchi-shield/internal/audit/clickhouse.
type ShieldEvent struct {
	Ts            time.Time `json:"ts"`
	Instance      string    `json:"instance"`
	NodeID        string    `json:"node_id"`
	ProjectID     string    `json:"project_id"`
	Listener      string    `json:"listener"`
	RequestID     string    `json:"request_id"`
	Phase         string    `json:"phase"`
	Direction     string    `json:"direction"`
	Action        string    `json:"action"`
	Severity      string    `json:"severity"`
	Reason        string    `json:"reason"`
	RuleID        string    `json:"rule_id"`
	PolicyID      string    `json:"policy_id"`
	Engine        string    `json:"engine"`
	Host          string    `json:"host"`
	Path          string    `json:"path"`
	Method        string    `json:"method"`
	StatusCode    uint16    `json:"status_code"`
	ConfigVersion string    `json:"config_version"`
}

// ShieldEventsFilter selects shield audit rows for a project over a time
// window, with optional narrowing. ProjectID and a valid From/To are required.
type ShieldEventsFilter struct {
	ProjectID string
	NodeID    string // a specific edge node (raw Envoy node id)
	Instance  string // a specific shield instance (<hostname>-shield) — used to scope to one edge
	Engine    string
	Action    string // exact action: block|detect|shadow|allow
	Severity  string
	Host      string
	Method    string
	Path      string // exact normalized path
	// Search is a case-insensitive substring matched against host, path and
	// request_id (OR) — the server-side counterpart of the UI's quick-search box,
	// so the summary/facets count the same rows the feed shows.
	Search string
	// FindingsOnly keeps only block/detect/shadow rows (drops the allow stream),
	// which is what the "what is shield blocking/detecting" feed wants.
	FindingsOnly bool
	From         time.Time
	To           time.Time
	Limit        int
	Offset       int
	IncludeTotal bool
}

// buildShieldWhere renders the parameterised WHERE shared by the feed and the
// summary so both apply identical scoping/filtering.
func buildShieldWhere(f ShieldEventsFilter) (string, []any) {
	clauses := []string{"project_id = ?", "ts >= ?", "ts < ?"}
	args := []any{f.ProjectID, f.From, f.To}
	if f.NodeID != "" {
		clauses = append(clauses, "node_id = ?")
		args = append(args, f.NodeID)
	}
	if f.Instance != "" {
		clauses = append(clauses, "instance = ?")
		args = append(args, f.Instance)
	}
	if f.Engine != "" {
		clauses = append(clauses, "engine = ?")
		args = append(args, f.Engine)
	}
	if f.Action != "" {
		clauses = append(clauses, "action = ?")
		args = append(args, f.Action)
	}
	if f.Severity != "" {
		clauses = append(clauses, "severity = ?")
		args = append(args, f.Severity)
	}
	if f.Host != "" {
		clauses = append(clauses, "host = ?")
		args = append(args, f.Host)
	}
	if f.Method != "" {
		clauses = append(clauses, "method = ?")
		args = append(args, f.Method)
	}
	if f.Path != "" {
		clauses = append(clauses, "path = ?")
		args = append(args, f.Path)
	}
	if f.Search != "" {
		// Substring (not LIKE) so user input needs no %/_ escaping; still bound as
		// args, never interpolated.
		clauses = append(clauses, "(positionCaseInsensitive(host, ?) > 0 OR positionCaseInsensitive(path, ?) > 0 OR positionCaseInsensitive(request_id, ?) > 0)")
		args = append(args, f.Search, f.Search, f.Search)
	}
	if f.FindingsOnly {
		clauses = append(clauses, "action NOT IN ('allow','continue')")
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

// buildShieldRollupWhere renders the WHERE for the per-minute rollup. The rollup
// carries only (project_id, node_id, engine, action, severity, bucket), so it
// filters on `bucket` (not `ts`) and omits the raw-only columns
// (instance/host/method/path) — shieldRollupUsable guarantees none are set.
func buildShieldRollupWhere(f ShieldEventsFilter) (string, []any) {
	clauses := []string{"project_id = ?", "bucket >= ?", "bucket < ?"}
	args := []any{f.ProjectID, f.From, f.To}
	if f.NodeID != "" {
		clauses = append(clauses, "node_id = ?")
		args = append(args, f.NodeID)
	}
	if f.Engine != "" {
		clauses = append(clauses, "engine = ?")
		args = append(args, f.Engine)
	}
	if f.Action != "" {
		clauses = append(clauses, "action = ?")
		args = append(args, f.Action)
	}
	if f.Severity != "" {
		clauses = append(clauses, "severity = ?")
		args = append(args, f.Severity)
	}
	if f.FindingsOnly {
		clauses = append(clauses, "action NOT IN ('allow','continue')")
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

// shieldRollupUsable reports whether the summary can read the per-minute rollup
// for this filter: the operator must have opted in AND the filter must use only
// dimensions the rollup carries. instance/host/method/path/search are raw-only,
// so any of them forces a raw scan.
func (c *Client) shieldRollupUsable(f ShieldEventsFilter) bool {
	return c.shieldUseRollup &&
		f.Instance == "" && f.Host == "" && f.Method == "" && f.Path == "" && f.Search == ""
}

// QueryShieldEvents returns shield audit rows newest-first for the filter, plus
// the total row count when IncludeTotal is set (-1 otherwise). The query is
// project-partition + time-range bounded (ORDER BY (project_id, ts)).
func (c *Client) QueryShieldEvents(ctx context.Context, f ShieldEventsFilter) ([]ShieldEvent, int64, error) {
	if c == nil || c.conn == nil {
		return nil, 0, fmt.Errorf("clickhouse client not initialised")
	}
	if f.ProjectID == "" {
		return nil, 0, fmt.Errorf("project_id is required")
	}
	if f.From.IsZero() || f.To.IsZero() || !f.To.After(f.From) {
		return nil, 0, fmt.Errorf("from/to time window invalid")
	}
	limit := f.Limit
	switch {
	case limit <= 0:
		limit = 50
	case limit > 500:
		limit = 500
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	whereSQL, args := buildShieldWhere(f)
	dataSQL := fmt.Sprintf(
		`SELECT ts, instance, node_id, project_id, listener, request_id, phase,
		        direction, action, severity, reason, rule_id, policy_id, engine,
		        host, path, method, status_code, config_version
		FROM %s
		%s
		ORDER BY ts DESC
		LIMIT %d OFFSET %d`,
		c.shieldQualifiedTable(), whereSQL, limit, offset,
	)

	qctx, cancel := c.withTimeout(ctx)
	defer cancel()

	// Run the data page and the (optional) count() concurrently so wall time is
	// max(data, count) rather than their sum — count() over the audit table can
	// be as expensive as the page scan, and the UI requests it per page.
	var (
		out   []ShieldEvent
		total int64 = -1
	)
	g, gctx := errgroup.WithContext(qctx)
	g.Go(func() error {
		rows, err := c.conn.Query(gctx, dataSQL, args...)
		if err != nil {
			return fmt.Errorf("query shield events: %w", err)
		}
		defer rows.Close()
		res := make([]ShieldEvent, 0, limit)
		for rows.Next() {
			var e ShieldEvent
			if err := rows.Scan(
				&e.Ts, &e.Instance, &e.NodeID, &e.ProjectID, &e.Listener, &e.RequestID, &e.Phase,
				&e.Direction, &e.Action, &e.Severity, &e.Reason, &e.RuleID, &e.PolicyID, &e.Engine,
				&e.Host, &e.Path, &e.Method, &e.StatusCode, &e.ConfigVersion,
			); err != nil {
				return fmt.Errorf("scan shield event: %w", err)
			}
			e.Ts = e.Ts.UTC()
			res = append(res, e)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("rows error: %w", err)
		}
		out = res
		return nil
	})
	if f.IncludeTotal {
		g.Go(func() error {
			countSQL := fmt.Sprintf(`SELECT count() FROM %s %s`, c.shieldQualifiedTable(), whereSQL)
			var totalU uint64
			if err := c.conn.QueryRow(gctx, countSQL, args...).Scan(&totalU); err != nil {
				return fmt.Errorf("count shield events: %w", err)
			}
			total = int64(totalU)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		if isUnknownTable(err) {
			total := int64(-1)
			if f.IncludeTotal {
				total = 0
			}
			return []ShieldEvent{}, total, nil
		}
		return nil, 0, err
	}
	return out, total, nil
}

// ShieldEventsFacets are the distinct filter values present in the window, for
// building UI filter dropdowns from real data instead of a hardcoded list.
type ShieldEventsFacets struct {
	Engines    []string `json:"engines"`
	Actions    []string `json:"actions"`
	Severities []string `json:"severities"`
	Hosts      []string `json:"hosts"`
	Nodes      []string `json:"nodes"`
}

// QueryShieldEventsFacets returns the distinct engine/action/severity/host/node
// values for a project's events in the window — one bounded scan via
// groupUniqArray (each capped so a high-cardinality dimension can't explode).
func (c *Client) QueryShieldEventsFacets(ctx context.Context, f ShieldEventsFilter) (ShieldEventsFacets, error) {
	var out ShieldEventsFacets
	if c == nil || c.conn == nil {
		return out, fmt.Errorf("clickhouse client not initialised")
	}
	if f.ProjectID == "" {
		return out, fmt.Errorf("project_id is required")
	}
	if f.From.IsZero() || f.To.IsZero() || !f.To.After(f.From) {
		return out, fmt.Errorf("from/to time window invalid")
	}
	whereSQL, args := buildShieldWhere(f)
	sql := fmt.Sprintf(
		`SELECT
		    groupUniqArray(100)(engine)    AS engines,
		    groupUniqArray(20)(action)     AS actions,
		    groupUniqArray(20)(severity)   AS severities,
		    groupUniqArray(1000)(host)     AS hosts,
		    groupUniqArray(1000)(node_id)  AS nodes
		FROM %s %s`,
		c.shieldQualifiedTable(), whereSQL,
	)
	qctx, cancel := c.withTimeout(ctx)
	defer cancel()
	if err := c.conn.QueryRow(qctx, sql, args...).Scan(
		&out.Engines, &out.Actions, &out.Severities, &out.Hosts, &out.Nodes,
	); err != nil {
		if isUnknownTable(err) {
			return out, nil // no events yet → empty facets
		}
		return out, fmt.Errorf("query shield facets: %w", err)
	}
	return out, nil
}

// ShieldEventGroup is one (engine, action, severity) bucket count for the
// summary cards; the UI pivots these into per-dimension totals.
type ShieldEventGroup struct {
	Engine   string `json:"engine"`
	Action   string `json:"action"`
	Severity string `json:"severity"`
	Count    int64  `json:"count"`
}

// ShieldEventTimeBucket is one time-bucketed, per-action count for the chart.
type ShieldEventTimeBucket struct {
	Bucket time.Time `json:"bucket"`
	Action string    `json:"action"`
	Count  int64     `json:"count"`
}

// ShieldEventsSummary is the aggregate view backing the dashboard cards + chart.
type ShieldEventsSummary struct {
	Total  int64                   `json:"total"`
	Groups []ShieldEventGroup      `json:"groups"`
	Series []ShieldEventTimeBucket `json:"series"`
}

// maxSummaryBuckets caps how many time buckets the summary series may span, so a
// small bucket over a large window can never produce an unbounded result set.
const maxSummaryBuckets = 2000

// clampBucketSeconds enforces a minimum bucket size (60s) and widens the bucket
// when the window would otherwise exceed maxSummaryBuckets buckets. Pure +
// table-tested so the defensive cap can't silently regress.
func clampBucketSeconds(bucketSeconds int, window time.Duration) int {
	if bucketSeconds < 60 {
		bucketSeconds = 60
	}
	windowSec := int(window.Seconds())
	if windowSec > 0 && windowSec/bucketSeconds > maxSummaryBuckets {
		bucketSeconds = windowSec/maxSummaryBuckets + 1
	}
	return bucketSeconds
}

// QueryShieldEventsSummary returns aggregate counts (grouped by
// engine/action/severity) and a per-action time series for the filter window.
// bucketSeconds controls the chart granularity (the handler derives it from the
// window; clamped to >= 60s here).
func (c *Client) QueryShieldEventsSummary(ctx context.Context, f ShieldEventsFilter, bucketSeconds int) (ShieldEventsSummary, error) {
	var sum ShieldEventsSummary
	if c == nil || c.conn == nil {
		return sum, fmt.Errorf("clickhouse client not initialised")
	}
	if f.ProjectID == "" {
		return sum, fmt.Errorf("project_id is required")
	}
	if f.From.IsZero() || f.To.IsZero() || !f.To.After(f.From) {
		return sum, fmt.Errorf("from/to time window invalid")
	}
	bucketSeconds = clampBucketSeconds(bucketSeconds, f.To.Sub(f.From))

	// Read the per-minute rollup (countMerge over `bucket`) when the operator has
	// opted in and the filter uses only rollup dimensions; otherwise scan raw
	// (count() over `ts`). Both produce identical result shapes.
	table := c.shieldQualifiedTable()
	whereSQL, args := buildShieldWhere(f)
	countExpr, timeCol := "toInt64(count())", "ts"
	if c.shieldRollupUsable(f) {
		table = c.shieldRollupQualifiedTable()
		whereSQL, args = buildShieldRollupWhere(f)
		countExpr, timeCol = "toInt64(countMerge(events_count))", "bucket"
	}
	qctx, cancel := c.withTimeout(ctx)
	defer cancel()

	var (
		groups []ShieldEventGroup
		total  int64
		series []ShieldEventTimeBucket
	)
	g, gctx := errgroup.WithContext(qctx)

	// Breakdown by (engine, action, severity). Cardinality is naturally bounded
	// (engines × actions × severities), but keep a backstop LIMIT.
	g.Go(func() error {
		groupSQL := fmt.Sprintf(
			`SELECT engine, action, severity, %s AS cnt
			FROM %s %s
			GROUP BY engine, action, severity
			ORDER BY cnt DESC
			LIMIT 1000`,
			countExpr, table, whereSQL,
		)
		grows, err := c.conn.Query(gctx, groupSQL, args...)
		if err != nil {
			return fmt.Errorf("shield summary groups: %w", err)
		}
		defer grows.Close()
		var t int64
		out := make([]ShieldEventGroup, 0, 64)
		for grows.Next() {
			var gr ShieldEventGroup
			if err := grows.Scan(&gr.Engine, &gr.Action, &gr.Severity, &gr.Count); err != nil {
				return fmt.Errorf("scan shield summary group: %w", err)
			}
			out = append(out, gr)
			t += gr.Count
		}
		if err := grows.Err(); err != nil {
			return fmt.Errorf("shield summary groups rows: %w", err)
		}
		groups, total = out, t
		return nil
	})

	// Per-action time series for the chart (bucket count bounded above).
	g.Go(func() error {
		seriesSQL := fmt.Sprintf(
			`SELECT toStartOfInterval(%s, INTERVAL %d SECOND) AS bkt, action, %s AS cnt
			FROM %s %s
			GROUP BY bkt, action
			ORDER BY bkt
			LIMIT %d`,
			timeCol, bucketSeconds, countExpr, table, whereSQL, maxSummaryBuckets*8,
		)
		srows, err := c.conn.Query(gctx, seriesSQL, args...)
		if err != nil {
			return fmt.Errorf("shield summary series: %w", err)
		}
		defer srows.Close()
		out := make([]ShieldEventTimeBucket, 0, 256)
		for srows.Next() {
			var b ShieldEventTimeBucket
			if err := srows.Scan(&b.Bucket, &b.Action, &b.Count); err != nil {
				return fmt.Errorf("scan shield summary series: %w", err)
			}
			b.Bucket = b.Bucket.UTC()
			out = append(out, b)
		}
		if err := srows.Err(); err != nil {
			return fmt.Errorf("shield summary series rows: %w", err)
		}
		series = out
		return nil
	})

	if err := g.Wait(); err != nil {
		if isUnknownTable(err) {
			return ShieldEventsSummary{}, nil
		}
		return ShieldEventsSummary{}, err
	}
	sum.Groups, sum.Total, sum.Series = groups, total, series
	return sum, nil
}
