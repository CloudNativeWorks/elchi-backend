package clickhouse

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/sync/errgroup"
)

// transportIssueFlags are the risk_flags that count as a transport
// posture problem — used to derive the per-row `Issues` list.
var transportIssueFlags = map[string]struct{}{
	"plain_text_transport": {},
	"weak_tls_version":     {},
	"legacy_protocol":      {},
}

// QueryTransportPosture returns the TLS-version + protocol histograms
// for a project plus the per-endpoint list of weak-transport
// offenders. The two halves run as parallel ClickHouse scans
// (errgroup); the summary is fixed-taxonomy `countIf`s (one pass) and
// the offender list is a GROUP BY restricted to non-ideal transport.
func (c *Client) QueryTransportPosture(ctx context.Context, f TransportFilter) (TransportPostureResult, error) {
	if c == nil || c.conn == nil {
		return TransportPostureResult{}, fmt.Errorf("clickhouse client not initialised")
	}
	if f.ProjectID == "" {
		return TransportPostureResult{}, fmt.Errorf("project_id is required")
	}
	if f.From.IsZero() || f.To.IsZero() || !f.To.After(f.From) {
		return TransportPostureResult{}, fmt.Errorf("from/to time window invalid")
	}
	top := f.Top
	switch {
	case top <= 0:
		top = 100
	case top > 500:
		top = 500
	}

	clauses := []string{"project_id = ?", "ts >= ?", "ts <  ?"}
	args := []any{f.ProjectID, f.From, f.To}
	if f.ListenerName != "" {
		clauses = append(clauses, "listener_name = ?")
		args = append(args, f.ListenerName)
	}
	whereSQL := "WHERE " + strings.Join(clauses, " AND ")

	qctx, cancel := c.withTimeout(ctx)
	defer cancel()

	var out TransportPostureResult
	g, gctx := errgroup.WithContext(qctx)

	g.Go(func() error {
		summary, err := c.queryTransportSummary(gctx, whereSQL, args)
		if err != nil {
			return fmt.Errorf("transport summary: %w", err)
		}
		out.TotalEvents = summary.TotalEvents
		out.TLSVersions = summary.TLSVersions
		out.Protocols = summary.Protocols
		return nil
	})
	g.Go(func() error {
		rows, err := c.queryWeakTransport(gctx, whereSQL, args, top)
		if err != nil {
			return fmt.Errorf("weak transport list: %w", err)
		}
		out.WeakTransport = rows
		return nil
	})

	if err := g.Wait(); err != nil {
		return TransportPostureResult{}, err
	}
	return out, nil
}

// queryTransportSummary is the fixed-taxonomy single-scan half: TLS
// version and protocol histograms via countIf.
func (c *Client) queryTransportSummary(ctx context.Context, whereSQL string, args []any) (TransportPostureResult, error) {
	sql := fmt.Sprintf(
		`SELECT
            toInt64(count())                                       AS total,
            toInt64(countIf(tls_version = 'TLSv1_3'))              AS tls13,
            toInt64(countIf(tls_version = 'TLSv1_2'))              AS tls12,
            toInt64(countIf(tls_version IN ('TLSv1','TLSv1_1')))   AS tls_old,
            toInt64(countIf(tls_version = ''))                     AS plaintext,
            toInt64(countIf(protocol IN ('http/1.0','http/1.1')))  AS http1,
            toInt64(countIf(protocol = 'http/2'))                  AS http2,
            toInt64(countIf(protocol = 'http/3'))                  AS http3,
            toInt64(countIf(protocol = 'tcp'))                     AS tcp
        FROM %s %s`,
		c.rawQualifiedTable(), whereSQL,
	)
	var (
		total, tls13, tls12, tlsOld, plaintext int64
		http1, http2, http3, tcp               int64
	)
	if err := c.conn.QueryRow(ctx, sql, args...).Scan(
		&total, &tls13, &tls12, &tlsOld, &plaintext,
		&http1, &http2, &http3, &tcp,
	); err != nil {
		return TransportPostureResult{}, err
	}
	return TransportPostureResult{
		TotalEvents: total,
		// Stable keys — emit zero buckets so the UI donut doesn't have
		// to special-case missing slices.
		TLSVersions: map[string]int64{
			"TLSv1_3":     tls13,
			"TLSv1_2":     tls12,
			"TLSv1_0/1_1": tlsOld,
			"plaintext":   plaintext,
		},
		Protocols: map[string]int64{
			"http/1.x": http1,
			"http/2":   http2,
			"http/3":   http3,
			"tcp":      tcp,
		},
	}, nil
}

// queryWeakTransport lists the endpoints observed on anything below
// TLSv1.3 or carrying a transport risk flag. `any()` picks a
// representative host / tls_version / protocol per (listener, path);
// groupUniqArray flattens every risk flag seen so the handler can
// keep only the transport-relevant ones.
//
// The SELECT aliases are deliberately NOT the column names
// (`sample_tls`, not `tls_version`): ClickHouse resolves a WHERE
// identifier against SELECT aliases first, so an `AS tls_version`
// would make the `tls_version != 'TLSv1_3'` predicate bind to the
// `any(tls_version)` aggregate — illegal in WHERE (error 184).
func (c *Client) queryWeakTransport(ctx context.Context, whereSQL string, args []any, top int) ([]WeakTransportRow, error) {
	sql := fmt.Sprintf(
		`SELECT
            listener_name,
            normalized_path,
            any(host)                          AS sample_host,
            any(tls_version)                   AS sample_tls,
            any(protocol)                      AS sample_protocol,
            groupUniqArray(arrayJoin(risk_flags)) AS flag_set,
            toInt64(count())                   AS event_count
        FROM %s
        %s AND ( tls_version != 'TLSv1_3'
                 OR arrayExists(x -> x IN ('plain_text_transport','weak_tls_version','legacy_protocol'), risk_flags) )
        GROUP BY listener_name, normalized_path
        ORDER BY event_count DESC
        LIMIT %d`,
		c.rawQualifiedTable(), whereSQL, top,
	)
	rows, err := c.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]WeakTransportRow, 0, top)
	for rows.Next() {
		var (
			r     WeakTransportRow
			flags []string
		)
		if err := rows.Scan(&r.ListenerName, &r.NormalizedPath, &r.Host,
			&r.TLSVersion, &r.Protocol, &flags, &r.EventCount); err != nil {
			return nil, err
		}
		// Keep only the transport-relevant flags as the row's issues.
		r.Issues = make([]string, 0, len(flags))
		for _, f := range flags {
			if _, ok := transportIssueFlags[f]; ok {
				r.Issues = append(r.Issues, f)
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
