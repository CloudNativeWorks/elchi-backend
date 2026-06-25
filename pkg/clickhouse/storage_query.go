package clickhouse

import (
	"context"
	"fmt"
)

// CHDisk is the ClickHouse data disk's space (the `default` disk).
type CHDisk struct {
	FreeBytes     uint64 `json:"free_bytes"`
	TotalBytes    uint64 `json:"total_bytes"`
	KeepFreeBytes uint64 `json:"keep_free_bytes"`
}

// CHTableStat is one table's on-disk footprint plus, for the per-insert tables
// (api_events_raw, elchi_shield_audit), the recent write volume.
type CHTableStat struct {
	Name string `json:"name"`
	// Kind: discovery | discovery_rollup | security | security_rollup | other.
	Kind             string `json:"kind"`
	Bytes            uint64 `json:"bytes"`
	Rows             uint64 `json:"rows"`
	LastHourRows     uint64 `json:"last_hour_rows"`
	Last24hRows      uint64 `json:"last_24h_rows"`
	LastHourBytesEst uint64 `json:"last_hour_bytes_est"`
	Last24hBytesEst  uint64 `json:"last_24h_bytes_est"`
}

// CHStorage is the live ClickHouse storage picture for the settings dashboard.
type CHStorage struct {
	Disk           CHDisk        `json:"disk"`
	Tables         []CHTableStat `json:"tables"`
	TotalBytes     uint64        `json:"total_bytes"`
	DiscoveryBytes uint64        `json:"discovery_bytes"`
	SecurityBytes  uint64        `json:"security_bytes"`
	// Write-rate projection. PerHour/PerDay are from the last 1h; the 24h-avg
	// variants smooth out spikes. Projected7d is the steady-state size the
	// 7-day-TTL tables converge to at the current rate.
	PerHourBytes      uint64  `json:"per_hour_bytes"`
	PerDayBytes       uint64  `json:"per_day_bytes"`
	PerDayBytes24hAvg uint64  `json:"per_day_bytes_24h_avg"`
	Projected7dBytes  uint64  `json:"projected_7d_bytes"`
	RetentionDays     int     `json:"retention_days"`
	HeadroomBytes     int64   `json:"headroom_bytes"`  // (current + free) - projected_7d
	DaysUntilFull     float64 `json:"days_until_full"` // free / per_day (worst case, no TTL); -1 if idle
}

// QueryStorageStats reads the live storage picture from ClickHouse system tables.
// All numbers are measured (system.parts/system.disks + recent-row counts), not
// guessed — the per-window byte estimates use the table's own current
// bytes/row ratio so they track the real compression.
func (c *Client) QueryStorageStats(ctx context.Context, retentionDays int) (CHStorage, error) {
	var out CHStorage
	if c == nil || c.conn == nil {
		return out, fmt.Errorf("clickhouse client not initialised")
	}
	if retentionDays <= 0 {
		retentionDays = 7
	}
	out.RetentionDays = retentionDays

	qctx, cancel := c.withTimeout(ctx)
	defer cancel()

	// 1) Per-table on-disk size + rows for every table in the database.
	sizes := map[string]struct{ bytes, rows uint64 }{}
	srows, err := c.conn.Query(qctx, `
		SELECT table, sum(bytes_on_disk) AS bytes, sum(rows) AS rows
		FROM system.parts
		WHERE active AND database = ?
		GROUP BY table`, c.database)
	if err != nil {
		return out, fmt.Errorf("storage: system.parts: %w", err)
	}
	for srows.Next() {
		var t string
		var b, r uint64
		if err := srows.Scan(&t, &b, &r); err != nil {
			srows.Close()
			return out, fmt.Errorf("storage: scan parts: %w", err)
		}
		sizes[t] = struct{ bytes, rows uint64 }{b, r}
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return out, fmt.Errorf("storage: parts rows: %w", err)
	}

	// 2) The data disk's free/total space (and the configured reserve).
	if err := c.conn.QueryRow(qctx, `
		SELECT free_space, total_space, keep_free_space
		FROM system.disks WHERE name = 'default' LIMIT 1`).
		Scan(&out.Disk.FreeBytes, &out.Disk.TotalBytes, &out.Disk.KeepFreeBytes); err != nil {
		// Non-fatal: some setups name the disk differently; fall back to any disk.
		_ = c.conn.QueryRow(qctx, `
			SELECT free_space, total_space, keep_free_space
			FROM system.disks ORDER BY total_space DESC LIMIT 1`).
			Scan(&out.Disk.FreeBytes, &out.Disk.TotalBytes, &out.Disk.KeepFreeBytes)
	}

	// 3) Recent write volume for the two per-insert sources. Prefer the per-minute
	//    rollups (countMerge over ~60× less data) and fall back to a raw scan.
	rawH1, rawH24 := c.rateCounts(qctx, c.rollup1m, c.rawTable)
	audH1, audH24 := c.rateCounts(qctx, c.shieldTable+"_1m", c.shieldTable)

	// 4) Build the table list, classify, and attach window data to raw + audit.
	kindOf := func(t string) string {
		switch t {
		case c.rawTable:
			return "discovery"
		case c.rollup1m, c.rollup1h, c.rollup1d:
			return "discovery_rollup"
		case c.shieldTable:
			return "security"
		case c.shieldTable + "_1m":
			return "security_rollup"
		default:
			return "other"
		}
	}
	avg := func(bytes, rows uint64) uint64 {
		if rows == 0 {
			return 0
		}
		return bytes / rows
	}
	rawAvg := avg(sizes[c.rawTable].bytes, sizes[c.rawTable].rows)
	audAvg := avg(sizes[c.shieldTable].bytes, sizes[c.shieldTable].rows)

	for t, s := range sizes {
		kind := kindOf(t)
		ts := CHTableStat{Name: t, Kind: kind, Bytes: s.bytes, Rows: s.rows}
		switch t {
		case c.rawTable:
			ts.LastHourRows, ts.Last24hRows = rawH1, rawH24
			ts.LastHourBytesEst, ts.Last24hBytesEst = rawH1*rawAvg, rawH24*rawAvg
		case c.shieldTable:
			ts.LastHourRows, ts.Last24hRows = audH1, audH24
			ts.LastHourBytesEst, ts.Last24hBytesEst = audH1*audAvg, audH24*audAvg
		}
		out.Tables = append(out.Tables, ts)
		out.TotalBytes += s.bytes
		switch kind {
		case "discovery", "discovery_rollup":
			out.DiscoveryBytes += s.bytes
		case "security", "security_rollup":
			out.SecurityBytes += s.bytes
		}
	}

	// 5) Projection. The per-insert rate is raw+audit; scale by the family's
	//    current rollup overhead so the projection includes the rollups' growth.
	insertBytes := sizes[c.rawTable].bytes + sizes[c.shieldTable].bytes
	familyBytes := out.DiscoveryBytes + out.SecurityBytes
	rollupFactor := 1.0
	if insertBytes > 0 && familyBytes > insertBytes {
		rollupFactor = float64(familyBytes) / float64(insertBytes)
	}
	out.PerHourBytes = uint64(float64(rawH1*rawAvg+audH1*audAvg) * rollupFactor)
	out.PerDayBytes = out.PerHourBytes * 24
	out.PerDayBytes24hAvg = uint64(float64(rawH24*rawAvg+audH24*audAvg) * rollupFactor)
	out.Projected7dBytes = out.PerDayBytes24hAvg * uint64(retentionDays)
	if out.Projected7dBytes == 0 {
		// No 24h data yet (fresh deploy) — fall back to the 1h-derived daily rate.
		out.Projected7dBytes = out.PerDayBytes * uint64(retentionDays)
	}

	// 6) Headroom: does the steady-state fit on disk? + a worst-case fill estimate.
	out.HeadroomBytes = int64(out.TotalBytes) + int64(out.Disk.FreeBytes) - int64(out.Projected7dBytes)
	out.DaysUntilFull = -1
	if out.PerDayBytes > 0 {
		out.DaysUntilFull = float64(out.Disk.FreeBytes) / float64(out.PerDayBytes)
	}
	return out, nil
}

// rateCounts returns (last1h, last24h) row counts for a per-insert source. It
// prefers the source's per-minute rollup — countMerge over aggregated minute
// buckets reads ~60× less data than the raw table and is exact — and falls back
// to a partition-pruned raw scan when the rollup is absent. Best-effort: zeros on
// error. Both rollups expose the same `events_count AggregateFunction(count)`.
func (c *Client) rateCounts(ctx context.Context, rollupTable, rawTable string) (h1, h24 uint64) {
	if rollupTable != "" {
		var r1, r24 uint64
		q := fmt.Sprintf(`
			SELECT
				toUInt64(countMergeIf(events_count, bucket >= now() - INTERVAL 1 HOUR)) AS h1,
				toUInt64(countMerge(events_count)) AS h24
			FROM %s.%s
			WHERE bucket >= now() - INTERVAL 24 HOUR`, c.database, rollupTable)
		if err := c.conn.QueryRow(ctx, q).Scan(&r1, &r24); err == nil {
			return r1, r24
		}
	}
	if rawTable != "" {
		var w1, w24 uint64
		q := fmt.Sprintf(`
			SELECT countIf(ts >= now() - INTERVAL 1 HOUR) AS h1, count() AS h24
			FROM %s.%s
			WHERE ts >= now() - INTERVAL 24 HOUR`, c.database, rawTable)
		_ = c.conn.QueryRow(ctx, q).Scan(&w1, &w24)
		return w1, w24
	}
	return 0, 0
}
