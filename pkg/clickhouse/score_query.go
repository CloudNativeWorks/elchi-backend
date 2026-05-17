package clickhouse

import (
	"context"
	"fmt"
)

// QuerySecurityMetrics runs the single ClickHouse scan behind the API
// Security Score. Every figure is a `countIf` / quantile over the
// same `api_events_raw` window, so the cost is one pass — the handler
// pairs this with a MongoDB endpoint-count aggregate to compute the
// final grade.
//
// risk_score is a UInt8; quantile() returns Float64, scanned into the
// struct's float64 P95RiskScore directly. A window with zero events
// yields an all-zero struct (the handler treats that as "no traffic →
// N/A grade" rather than a perfect score).
func (c *Client) QuerySecurityMetrics(ctx context.Context, f SecurityScoreFilter) (SecurityMetrics, error) {
	if c == nil || c.conn == nil {
		return SecurityMetrics{}, fmt.Errorf("clickhouse client not initialised")
	}
	if f.ProjectID == "" {
		return SecurityMetrics{}, fmt.Errorf("project_id is required")
	}
	if f.From.IsZero() || f.To.IsZero() || !f.To.After(f.From) {
		return SecurityMetrics{}, fmt.Errorf("from/to time window invalid")
	}

	sql := fmt.Sprintf(
		`SELECT
            toInt64(count())                                                     AS total,
            toInt64(countIf(arrayExists(x -> x IN ('weak_tls_version','plain_text_transport'), risk_flags))) AS weak_transport,
            toInt64(countIf(has(risk_flags, 'legacy_protocol')))                 AS legacy_proto,
            toInt64(countIf(risk_score >= 10))                                   AS critical_events,
            toInt64(countIf(tags['ua.kind'] IN ('scanner','bot')))               AS scanner_bot,
            toInt64(countIf(has(risk_flags, 'threat_intel_hit')))                AS ti_hits,
            toFloat64(quantile(0.95)(risk_score))                                AS p95_risk
        FROM %s
        WHERE project_id = ? AND ts >= ? AND ts < ?`,
		c.rawQualifiedTable(),
	)

	qctx, cancel := c.withTimeout(ctx)
	defer cancel()

	var m SecurityMetrics
	if err := c.conn.QueryRow(qctx, sql, f.ProjectID, f.From, f.To).Scan(
		&m.TotalEvents, &m.WeakTransport, &m.LegacyProtocol,
		&m.CriticalEvents, &m.ScannerBot, &m.TIHits, &m.P95RiskScore,
	); err != nil {
		return SecurityMetrics{}, fmt.Errorf("query security metrics: %w", err)
	}
	return m, nil
}
