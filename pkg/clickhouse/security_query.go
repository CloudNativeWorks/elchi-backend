package clickhouse

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SchemaMigration is the latest applied row from the collector's
// `_collector_migrations` tracker — the read API uses it to discover
// which schema generation the ClickHouse side is currently on.
type SchemaMigration struct {
	ID        uint32    `json:"id"`
	Name      string    `json:"name"`
	AppliedAt time.Time `json:"applied_at"`
}

// SchemaGeneration returns the highest-id row from
// `<db>._collector_migrations`. The collector writes one row per
// applied migration; the max id is the current ClickHouse schema
// generation. A missing table (collector never ran a migration here)
// surfaces as an error the caller can treat as "unknown".
func (c *Client) SchemaGeneration(ctx context.Context) (SchemaMigration, error) {
	if c == nil || c.conn == nil {
		return SchemaMigration{}, fmt.Errorf("clickhouse client not initialised")
	}
	sql := fmt.Sprintf(
		`SELECT id, name, applied_at
        FROM %s._collector_migrations
        ORDER BY id DESC
        LIMIT 1`,
		c.database,
	)
	qctx, cancel := c.withTimeout(ctx)
	defer cancel()
	var m SchemaMigration
	if err := c.conn.QueryRow(qctx, sql).Scan(&m.ID, &m.Name, &m.AppliedAt); err != nil {
		return SchemaMigration{}, fmt.Errorf("query ch schema generation: %w", err)
	}
	m.AppliedAt = m.AppliedAt.UTC()
	return m, nil
}

// QueryBotScannerHeatmap returns one row per
// (normalized_path, ua.family, ua.kind) tuple where `ua.kind`
// classifies as bot or scanner, ordered by count desc and capped at
// `Top` (default 50, max 200). Backend for the bot/scanner heatmap
// in the security dashboard.
//
// Plan: `tags['ua.kind'] IN (...)` predicate filters early; the
// remaining GROUP BY runs on a small subset of rows (bots/scanners
// usually < 1% of total traffic). normalized_path is part of the
// primary key, so even when the filter doesn't help the partition
// pruning on (project_id, ts) keeps the scan bounded to the window.
func (c *Client) QueryBotScannerHeatmap(ctx context.Context, f BotScannerFilter) ([]BotScannerHit, error) {
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("clickhouse client not initialised")
	}
	if f.ProjectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	if f.From.IsZero() || f.To.IsZero() || !f.To.After(f.From) {
		return nil, fmt.Errorf("from/to time window invalid")
	}
	top := f.Top
	switch {
	case top <= 0:
		top = 50
	case top > 200:
		top = 200
	}

	clauses := []string{
		"project_id = ?",
		"ts >= ?",
		"ts <  ?",
		"tags['ua.kind'] IN ('bot','scanner')",
		"normalized_path != ''",
	}
	args := []any{f.ProjectID, f.From, f.To}
	if f.ListenerName != "" {
		clauses = append(clauses, "listener_name = ?")
		args = append(args, f.ListenerName)
	}

	sql := fmt.Sprintf(
		`SELECT
            normalized_path,
            tags['ua.family'] AS family,
            tags['ua.kind']   AS kind,
            toInt64(count())  AS count
        FROM %s
        WHERE %s
        GROUP BY normalized_path, family, kind
        ORDER BY count DESC
        LIMIT %d`,
		c.rawQualifiedTable(),
		strings.Join(clauses, " AND "),
		top,
	)

	qctx, cancel := c.withTimeout(ctx)
	defer cancel()
	rows, err := c.conn.Query(qctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("bot scanner heatmap: %w", err)
	}
	defer rows.Close()

	out := make([]BotScannerHit, 0, top)
	for rows.Next() {
		var h BotScannerHit
		if err := rows.Scan(&h.NormalizedPath, &h.Family, &h.Kind, &h.Count); err != nil {
			return nil, fmt.Errorf("scan bot/scanner row: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
