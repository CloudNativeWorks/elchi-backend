package clickhouse

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/sync/errgroup"
)

// QueryRawEvents reads from `api_events_raw` for the given filter.
// Project / Listener / time window are required; the helper trusts the
// caller to validate inputs (the inventory_detail handler enforces a
// max time-range guard before calling here).
//
// SQL plan: WHERE prefix exploits the order key
// (project_id, normalized_path, ts) — supplying normalized_path turns
// the predicate into a primary-key range scan; without it we still
// limit the partition fan-out via project_id + ts BETWEEN.
//
// Total count is only computed when f.IncludeTotal is set; a
// `SELECT count()` over the raw table walks the whole window's data
// parts and can dwarf the LIMITed data query on busy listeners. UI
// pagination that doesn't render an exact total can leave it off.
// When skipped, the returned total is -1 (sentinel).
func (c *Client) QueryRawEvents(ctx context.Context, f RawEventsFilter) ([]RawEvent, int64, error) {
	if c == nil || c.conn == nil {
		return nil, 0, fmt.Errorf("clickhouse client not initialised")
	}
	if f.ProjectID == "" || f.ListenerName == "" {
		return nil, 0, fmt.Errorf("project_id and listener_name are required")
	}
	if f.From.IsZero() || f.To.IsZero() || !f.To.After(f.From) {
		return nil, 0, fmt.Errorf("from/to time window invalid")
	}

	whereSQL, args := buildRawEventsWhere(f)

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	selectList := rawEventSelectList(f.Fields)
	dataSQL := fmt.Sprintf(
		`SELECT %s
        FROM %s
        %s
        ORDER BY %s
        LIMIT %d OFFSET %d`,
		selectList,
		c.rawQualifiedTable(),
		whereSQL,
		rawEventsOrderBy(f.SortBy, f.SortOrder),
		limit, offset,
	)

	qctx, cancel := c.withTimeout(ctx)
	defer cancel()

	// Count and data are independent scans. Running them in series
	// makes the count() its own round-trip on top of an already-slow
	// raw-events read; doing them in parallel keeps wall-time at
	// max(count, data). errgroup propagates the first error to cancel
	// the sibling so a stalled leg can't outrun its budget.
	var (
		total int64 = -1
		out   []RawEvent
	)
	g, gctx := errgroup.WithContext(qctx)
	if f.IncludeTotal {
		g.Go(func() error {
			countSQL := fmt.Sprintf(`SELECT count() FROM %s %s`, c.rawQualifiedTable(), whereSQL)
			var totalU uint64
			if err := c.conn.QueryRow(gctx, countSQL, args...).Scan(&totalU); err != nil {
				return fmt.Errorf("count raw events: %w", err)
			}
			total = int64(totalU)
			return nil
		})
	}
	g.Go(func() error {
		rows, err := c.conn.Query(gctx, dataSQL, args...)
		if err != nil {
			return fmt.Errorf("query raw events: %w", err)
		}
		defer rows.Close()
		out = make([]RawEvent, 0, limit)
		for rows.Next() {
			var ev RawEvent
			if err := rows.ScanStruct(&ev); err != nil {
				return fmt.Errorf("scan raw event: %w", err)
			}
			// CH `DateTime`/`DateTime64` are stored without timezone
			// and the driver decodes them in the server's local zone
			// (e.g. Europe/Istanbul). Normalise to UTC so the JSON
			// response always emits `Z` timestamps — UI consumers
			// expect a single canonical zone for charting / ISO parse.
			ev.Ts = ev.Ts.UTC()
			ev.CreatedAt = ev.CreatedAt.UTC()
			out = append(out, ev)
		}
		return rows.Err()
	})
	if err := g.Wait(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// QueryRollup reads from one of `api_events_1{m,h,d}` and applies the
// `-Merge` combinators on the AggregateFunction columns to materialise
// the public contract described in elchi-collector/docs/schema.md.
// Granularity must be one of {"1m","1h","1d"}; "1d" silently drops the
// normalized_path dimension because that table doesn't store it.
func (c *Client) QueryRollup(ctx context.Context, f RollupFilter) ([]RollupBucket, error) {
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("clickhouse client not initialised")
	}
	if f.ProjectID == "" || f.ListenerName == "" {
		return nil, fmt.Errorf("project_id and listener_name are required")
	}
	if f.From.IsZero() || f.To.IsZero() || !f.To.After(f.From) {
		return nil, fmt.Errorf("from/to time window invalid")
	}
	table := c.rollupQualifiedTable(f.Granularity)
	if table == "" {
		return nil, fmt.Errorf("unsupported granularity: %s", f.Granularity)
	}

	// Build WHERE — `1d` rollups have no normalized_path column, so
	// the path filter is ignored at this level. The HTTP layer
	// rejects 1d+path combinations up front, so reaching this code
	// path with NormalizedPath set under 1d would mean a programming
	// error; we still strip it defensively to keep the query valid.
	includePath := f.Granularity != "1d" && f.NormalizedPath != ""
	includeMethod := f.Method != ""

	// Rollup tables call the time-bucket column `bucket` (DateTime).
	// We alias it to `ts_bucket` in the outer SELECT so the JSON
	// response shape — and the UI contract — keeps the more
	// descriptive name. Schema doc (`elchi-collector/docs/schema.md`)
	// shipped with `ts_bucket` in the prose; the materialised tables
	// use `bucket`. Don't trust the doc, trust the DESCRIBE.
	var clauses []string
	args := []any{f.ProjectID, f.ListenerName, f.From, f.To}
	clauses = append(clauses,
		"project_id = ?",
		"listener_name = ?",
		"bucket >= ?",
		"bucket <  ?",
	)
	if includePath {
		clauses = append(clauses, "normalized_path = ?")
		args = append(args, f.NormalizedPath)
	}
	if includeMethod {
		clauses = append(clauses, "method = ?")
		args = append(args, f.Method)
	}

	pathCol := "''"
	pathGroup := ""
	if f.Granularity != "1d" {
		pathCol = "normalized_path"
		pathGroup = ", normalized_path"
	}

	// Wrap the aggregation in a subquery so the t-digest merge runs
	// EXACTLY ONCE per (bucket, project, listener, method, status,
	// optional path) tuple. Repeating quantilesTDigestMerge(...) three
	// times in the outer SELECT would force ClickHouse to recompute
	// the digest for every percentile column — measurable cost on
	// long time windows.
	// quantilesTDigestMerge returns Float32 for each percentile;
	// clickhouse-go/v2 won't auto-widen to float64 on Scan, so we
	// explicitly cast (and do the same for the avg) to keep the Go
	// struct fields at the conventional float64 width.
	sql := fmt.Sprintf(
		`SELECT
            ts_bucket,
            project_id,
            listener_name,
            method,
            normalized_path,
            status_class,
            events_count,
            toFloat64(arrayElement(quantiles, 1)) AS duration_p50,
            toFloat64(arrayElement(quantiles, 2)) AS duration_p95,
            toFloat64(arrayElement(quantiles, 3)) AS duration_p99,
            toFloat64(duration_avg) AS duration_avg,
            response_bytes_sum,
            response_bytes_max,
            max_risk_score,
            unique_consumers,
            unique_source_ips,
            error_count,
            client_error_count
        FROM (
            SELECT
                bucket AS ts_bucket,
                project_id,
                listener_name,
                method,
                %s AS normalized_path,
                status_class,
                countMerge(events_count) AS events_count,
                quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_quantiles) AS quantiles,
                avgMerge(duration_avg) AS duration_avg,
                sumMerge(response_bytes_sum) AS response_bytes_sum,
                maxMerge(response_bytes_max) AS response_bytes_max,
                maxMerge(max_risk_score) AS max_risk_score,
                uniqHLL12Merge(unique_consumers) AS unique_consumers,
                uniqHLL12Merge(unique_source_ips) AS unique_source_ips,
                countIfMerge(error_count) AS error_count,
                countIfMerge(client_error_count) AS client_error_count
            FROM %s
            WHERE %s
            GROUP BY bucket, project_id, listener_name, method, status_class%s
        )
        ORDER BY ts_bucket ASC`,
		pathCol,
		table,
		strings.Join(clauses, " AND "),
		pathGroup,
	)

	qctx, cancel := c.withTimeout(ctx)
	defer cancel()

	rows, err := c.conn.Query(qctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query rollup %s: %w", f.Granularity, err)
	}
	defer rows.Close()

	out := []RollupBucket{}
	for rows.Next() {
		var b RollupBucket
		b.BucketSize = f.Granularity
		if err := rows.Scan(
			&b.TsBucket,
			&b.ProjectID,
			&b.ListenerName,
			&b.Method,
			&b.NormalizedPath,
			&b.StatusClass,
			&b.EventsCount,
			&b.DurationP50,
			&b.DurationP95,
			&b.DurationP99,
			&b.DurationAvg,
			&b.ResponseBytesSum,
			&b.ResponseBytesMax,
			&b.MaxRiskScore,
			&b.UniqueConsumers,
			&b.UniqueSourceIPs,
			&b.ErrorCount,
			&b.ClientErrorCount,
		); err != nil {
			return nil, fmt.Errorf("scan rollup row: %w", err)
		}
		// See QueryRawEvents — normalise bucket to UTC so the JSON
		// response is timezone-canonical (`Z` suffix). The rollup
		// `bucket` column is naked DateTime, so the driver hands us
		// back the server's local zone otherwise.
		b.TsBucket = b.TsBucket.UTC()
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rollup rows: %w", err)
	}
	return out, nil
}

// rawEventSelectList picks the SELECT projection for QueryRawEvents.
// `core` (default) skips the wide map columns (headers/tags) plus the
// rarely-rendered TLS / gRPC / instance metadata — a 500-row page
// shrinks from megabytes to a few hundred KB on busy listeners.
// `full` returns every documented column. Unknown values fall back to
// `core` so a typo doesn't surface a partial response with confusing
// gaps.
//
// SELECT column order must stay in sync with the corresponding
// ScanStruct on the row — clickhouse-go/v2 binds by index against the
// column metadata, so the struct's `ch:"…"` tags decide which fields
// the columns land on. Adding a new column means appending it here AND
// adding a matching `ch:` tag on RawEvent.
func rawEventSelectList(mode string) string {
	switch mode {
	case "full":
		return `event_id, ts, created_at, node_id, listener_name, project_id, listener_ip,
            instance_id, stream_id, protocol, method, normalized_path, host,
            grpc_service, grpc_method, status_code, grpc_status, grpc_message,
            request_id, redirect_location, duration_ms, request_bytes, response_bytes,
            cluster, route_name, content_type,
            user_agent_hash, source_ip_hash, consumer_hash, auth_observed,
            risk_flags, pii_categories, endpoint_categories,
            tls_version, tls_sni, tls_peer_subject, tls_peer_issuer,
            headers, risk_score, tags,
            source_ip, user_agent`
	default:
		// "core" — drops headers/tags (Map<String,String> can be huge),
		// TLS subject/issuer (rarely shown in the list), gRPC service /
		// method (only relevant on grpc-routed listeners), and
		// instance/stream IDs (debug-only). UI's per-row detail view
		// requests `fields=full` when the operator expands a row.
		return `event_id, ts, node_id, listener_name, project_id,
            protocol, method, normalized_path, host,
            status_code, request_id, redirect_location,
            duration_ms, request_bytes, response_bytes,
            cluster, route_name, content_type,
            user_agent_hash, source_ip_hash, consumer_hash, auth_observed,
            risk_flags, pii_categories, endpoint_categories,
            risk_score,
            source_ip, user_agent`
	}
}

// buildRawEventsWhere assembles the parameterised WHERE clause for
// raw event queries. Returns the SQL fragment and the matching args
// slice so QueryRow / Query can share the same plan.
func buildRawEventsWhere(f RawEventsFilter) (string, []any) {
	clauses := []string{"project_id = ?", "listener_name = ?", "ts >= ?", "ts < ?"}
	args := []any{f.ProjectID, f.ListenerName, f.From, f.To}

	if f.NormalizedPath != "" {
		clauses = append(clauses, "normalized_path = ?")
		args = append(args, f.NormalizedPath)
	}
	if f.Method != "" {
		clauses = append(clauses, "method = ?")
		args = append(args, f.Method)
	}
	if f.StatusMin > 0 {
		clauses = append(clauses, "status_code >= ?")
		args = append(args, f.StatusMin)
	}
	if f.StatusMax > 0 {
		clauses = append(clauses, "status_code <= ?")
		args = append(args, f.StatusMax)
	}
	if f.RequestID != "" {
		clauses = append(clauses, "request_id = ?")
		args = append(args, f.RequestID)
	}
	if len(f.EventIDs) > 0 {
		// clickhouse-go/v2 expands a []string bound to `IN ?` into
		// the proper `IN (?, ?, …)` set — same idiom geo_query.go's
		// country-series filter relies on.
		clauses = append(clauses, "event_id IN ?")
		args = append(args, f.EventIDs)
	}
	if len(f.RiskFlags) > 0 {
		// hasAny: keep events carrying at least one of the requested
		// flags. The []string binds to the array argument directly.
		clauses = append(clauses, "hasAny(risk_flags, ?)")
		args = append(args, f.RiskFlags)
	}
	if len(f.PIICategories) > 0 {
		clauses = append(clauses, "hasAny(pii_categories, ?)")
		args = append(args, f.PIICategories)
	}
	if f.MinRiskScore > 0 {
		clauses = append(clauses, "risk_score >= ?")
		args = append(args, f.MinRiskScore)
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

// rawEventsOrderBy maps the public sort_by / sort_order knobs to a
// safe ORDER BY fragment. SortBy is matched against a fixed switch —
// an unknown value (or empty) silently falls back to `ts`, so no
// caller-supplied string ever reaches the SQL as a column name.
// Default direction is DESC (newest / worst first).
//
// Only `ts` aligns with the table's order key
// (project_id, normalized_path, ts); the other columns force an
// in-memory sort. That is acceptable here — the events query is
// already narrowed to one (project, path) inside a bounded time
// window and LIMITed, so the sort runs over a small windowed set.
func rawEventsOrderBy(sortBy, sortOrder string) string {
	col := "ts"
	switch sortBy {
	case "duration_ms":
		col = "duration_ms"
	case "status_code":
		col = "status_code"
	case "risk_score":
		col = "risk_score"
	}
	dir := "DESC"
	if sortOrder == "asc" {
		dir = "ASC"
	}
	return col + " " + dir
}
