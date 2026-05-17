package clickhouse

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/sync/errgroup"
)

// QueryErrorHotspots returns the per-endpoint 4xx/5xx hotspot list
// plus the project-wide error roll-up.
//
// Sourced from the `api_events_1h` rollup, NOT api_events_raw: the
// rollup already carries countIf-aggregated client_error_count (4xx)
// and error_count (5xx) per (project, listener, method, status_class,
// path, bucket), so a hotspot scan reads a few thousand rows instead
// of the project's full 7-day raw event set — measured ~100x fewer
// rows / ~100x faster on a 13M-event project. The 1h grain keeps the
// normalized_path dimension (the 1d rollup drops it).
//
// The two queries (hotspot GROUP BY + scalar summary) run in
// parallel over the same window via errgroup.
func (c *Client) QueryErrorHotspots(ctx context.Context, f ErrorFilter) ([]ErrorHotspot, ErrorSummary, error) {
	if c == nil || c.conn == nil {
		return nil, ErrorSummary{}, fmt.Errorf("clickhouse client not initialised")
	}
	if f.ProjectID == "" {
		return nil, ErrorSummary{}, fmt.Errorf("project_id is required")
	}
	if f.From.IsZero() || f.To.IsZero() || !f.To.After(f.From) {
		return nil, ErrorSummary{}, fmt.Errorf("from/to time window invalid")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	minRate := f.MinErrorRate
	if minRate < 0 {
		minRate = 0
	}
	if minRate > 100 {
		minRate = 100
	}

	// 1h rollup table. From/To filter the `bucket` column; the rollup
	// is hour-grained so a sub-hour window edge rounds to the bucket.
	table := c.rollupQualifiedTable("1h")
	if table == "" {
		return nil, ErrorSummary{}, fmt.Errorf("1h rollup table not configured")
	}
	clauses := []string{"project_id = ?", "bucket >= ?", "bucket <  ?"}
	args := []any{f.ProjectID, f.From, f.To}
	if f.ListenerName != "" {
		clauses = append(clauses, "listener_name = ?")
		args = append(args, f.ListenerName)
	}
	whereSQL := "WHERE " + strings.Join(clauses, " AND ")

	qctx, cancel := c.withTimeout(ctx)
	defer cancel()

	var (
		hotspots []ErrorHotspot
		summary  ErrorSummary
	)
	g, gctx := errgroup.WithContext(qctx)

	g.Go(func() error {
		rows, err := c.queryErrorHotspotRows(gctx, table, whereSQL, args, minRate, limit, offset)
		if err != nil {
			return fmt.Errorf("error hotspots: %w", err)
		}
		hotspots = rows
		return nil
	})
	g.Go(func() error {
		s, err := c.queryErrorSummary(gctx, table, whereSQL, args)
		if err != nil {
			return fmt.Errorf("error summary: %w", err)
		}
		summary = s
		return nil
	})

	if err := g.Wait(); err != nil {
		return nil, ErrorSummary{}, err
	}
	return hotspots, summary, nil
}

func (c *Client) queryErrorHotspotRows(
	ctx context.Context, table, whereSQL string, args []any, minRate float64, limit, offset int,
) ([]ErrorHotspot, error) {
	// Rollup AggregateFunction columns merged with the -Merge
	// combinators: events_count → total, client_error_count → 4xx,
	// error_count → 5xx. The HAVING clause drops error-free endpoints
	// and applies the minimum error-rate threshold.
	sql := fmt.Sprintf(
		`SELECT
            listener_name,
            normalized_path,
            method,
            toInt64(countMerge(events_count))         AS total,
            toInt64(countIfMerge(client_error_count)) AS e4xx,
            toInt64(countIfMerge(error_count))        AS e5xx
        FROM %s
        %s
        GROUP BY listener_name, normalized_path, method
        HAVING (e4xx + e5xx) > 0 AND total > 0 AND (e4xx + e5xx) / total * 100 >= %s
        ORDER BY (e4xx + e5xx) DESC
        LIMIT %d OFFSET %d`,
		table,
		whereSQL,
		strconv.FormatFloat(minRate, 'f', -1, 64),
		limit, offset,
	)
	rows, err := c.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ErrorHotspot, 0, limit)
	for rows.Next() {
		var h ErrorHotspot
		if err := rows.Scan(&h.ListenerName, &h.NormalizedPath, &h.Method,
			&h.TotalEvents, &h.Error4xx, &h.Error5xx); err != nil {
			return nil, err
		}
		if h.TotalEvents > 0 {
			h.ErrorRate = floor1(float64(h.Error4xx+h.Error5xx) / float64(h.TotalEvents) * 100.0)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (c *Client) queryErrorSummary(ctx context.Context, table, whereSQL string, args []any) (ErrorSummary, error) {
	sql := fmt.Sprintf(
		`SELECT
            toInt64(countMerge(events_count))         AS total,
            toInt64(countIfMerge(client_error_count)) AS e4xx,
            toInt64(countIfMerge(error_count))        AS e5xx
        FROM %s %s`,
		table, whereSQL,
	)
	var s ErrorSummary
	if err := c.conn.QueryRow(ctx, sql, args...).Scan(&s.TotalEvents, &s.Total4xx, &s.Total5xx); err != nil {
		return ErrorSummary{}, err
	}
	if s.TotalEvents > 0 {
		s.OverallErrorRate = floor1(float64(s.Total4xx+s.Total5xx) / float64(s.TotalEvents) * 100.0)
	}
	return s, nil
}

// QueryErrorSeries returns the bucketed 4xx/5xx time series from the
// rollup tables. Granularity selects the 1m / 1h / 1d table; the
// rollups store error_count (5xx) and client_error_count (4xx) as
// AggregateFunction columns, merged here with the -Merge combinators.
func (c *Client) QueryErrorSeries(ctx context.Context, f ErrorFilter, granularity string) ([]ErrorSeriesPoint, error) {
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("clickhouse client not initialised")
	}
	table := c.rollupQualifiedTable(granularity)
	if table == "" {
		return nil, fmt.Errorf("unsupported granularity: %s", granularity)
	}
	if f.ProjectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	if f.From.IsZero() || f.To.IsZero() || !f.To.After(f.From) {
		return nil, fmt.Errorf("from/to time window invalid")
	}

	clauses := []string{"project_id = ?", "bucket >= ?", "bucket <  ?"}
	args := []any{f.ProjectID, f.From, f.To}
	if f.ListenerName != "" {
		clauses = append(clauses, "listener_name = ?")
		args = append(args, f.ListenerName)
	}

	sql := fmt.Sprintf(
		`SELECT
            bucket,
            toInt64(countMerge(events_count))         AS total,
            toInt64(countIfMerge(client_error_count)) AS e4xx,
            toInt64(countIfMerge(error_count))        AS e5xx
        FROM %s
        WHERE %s
        GROUP BY bucket
        ORDER BY bucket ASC`,
		table, strings.Join(clauses, " AND "),
	)

	qctx, cancel := c.withTimeout(ctx)
	defer cancel()
	rows, err := c.conn.Query(qctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query error series: %w", err)
	}
	defer rows.Close()

	out := []ErrorSeriesPoint{}
	for rows.Next() {
		var p ErrorSeriesPoint
		if err := rows.Scan(&p.Bucket, &p.Total, &p.Error4xx, &p.Error5xx); err != nil {
			return nil, fmt.Errorf("scan error series row: %w", err)
		}
		// Rollup `bucket` is naked DateTime — normalise to UTC so the
		// JSON response is timezone-canonical (see QueryRollup).
		p.Bucket = p.Bucket.UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}
