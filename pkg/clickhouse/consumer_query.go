package clickhouse

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// QueryConsumerTop returns the consumer leaderboard for a project over a
// time window: the top identities by event volume plus the anonymous
// bucket reported separately.
//
// Anonymous events (empty consumer_hash) have no stable identity to
// pivot on, so they are excluded from the GROUP BY and surfaced as a
// single aggregate count instead of polluting the leaderboard. The
// percentage on each row is share of *named* events (total − anonymous)
// so the leaderboard percentages reason about identifiable traffic only.
//
// Reads from raw — consumer_hash / source_ip_hash / risk_score live only
// on the per-event payload, not the rollups — hence the caller-enforced
// 7-day window cap. The counts scan and the leaderboard scan run in
// parallel; ClickHouse's thread pool keeps wall-time near the slower of
// the two.
func (c *Client) QueryConsumerTop(ctx context.Context, f ConsumerFilter) (*ConsumerSummary, error) {
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("clickhouse client not initialised")
	}
	if err := validateConsumerFilter(f); err != nil {
		return nil, err
	}
	whereSQL, args := buildConsumerWhere(f)
	top := normaliseTop(f.Top)

	qctx, cancel := c.withTimeout(ctx)
	defer cancel()

	g, gctx := errgroup.WithContext(qctx)
	var (
		mu       sync.Mutex
		summary  = ConsumerSummary{TopConsumers: []ConsumerAggregation{}}
		anonHits int64
	)

	// Window totals: all events + the anonymous slice in one scan. Used
	// to derive named-event totals for the percentage column.
	g.Go(func() error {
		countSQL := fmt.Sprintf(
			`SELECT
                toInt64(count())                        AS total,
                toInt64(countIf(consumer_hash = ''))    AS anon
            FROM %s
            %s`,
			c.rawQualifiedTable(), whereSQL,
		)
		var total, anon int64
		if err := c.conn.QueryRow(gctx, countSQL, args...).Scan(&total, &anon); err != nil {
			return fmt.Errorf("consumer totals: %w", err)
		}
		mu.Lock()
		summary.TotalEvents = total
		anonHits = anon
		mu.Unlock()
		return nil
	})

	// Leaderboard: named consumers ranked by volume.
	g.Go(func() error {
		rows, err := c.queryConsumerLeaderboard(gctx, whereSQL, args, top)
		if err != nil {
			return fmt.Errorf("consumer leaderboard: %w", err)
		}
		mu.Lock()
		summary.TopConsumers = rows
		mu.Unlock()
		return nil
	})

	if err := g.Wait(); err != nil {
		return nil, err
	}

	summary.AnonymousEvents = anonHits

	// Percentage is share of named (identifiable) events. Guard the
	// divide-by-zero when a project has only anonymous traffic.
	named := summary.TotalEvents - summary.AnonymousEvents
	if named > 0 {
		for i := range summary.TopConsumers {
			summary.TopConsumers[i].Percentage =
				floor1(float64(summary.TopConsumers[i].Events) / float64(named) * 100)
		}
	}
	return &summary, nil
}

// queryConsumerLeaderboard runs the GROUP BY consumer_hash aggregate.
func (c *Client) queryConsumerLeaderboard(ctx context.Context, whereSQL string, args []any, top int) ([]ConsumerAggregation, error) {
	out := []ConsumerAggregation{}
	sql := fmt.Sprintf(
		`SELECT
            consumer_hash                                       AS consumer_hash,
            toInt64(count())                                    AS events,
            toInt64(max(risk_score))                            AS max_risk,
            toInt64(uniq(source_ip_hash))                       AS distinct_ips,
            toInt64(uniq((listener_name, normalized_path)))     AS distinct_endpoints,
            toInt64(countIf(tags['ti.hit'] = 'true'))           AS ti_hits,
            min(ts)                                             AS first_seen,
            max(ts)                                             AS last_seen
        FROM %s
        %s AND consumer_hash != ''
        GROUP BY consumer_hash
        ORDER BY events DESC
        LIMIT %d`,
		c.rawQualifiedTable(), whereSQL, top,
	)
	rows, err := c.conn.Query(ctx, sql, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var a ConsumerAggregation
		if err := rows.Scan(
			&a.ConsumerHash, &a.Events, &a.MaxRiskScore,
			&a.DistinctIPs, &a.DistinctEndpoints, &a.TIHits,
			&a.FirstSeen, &a.LastSeen,
		); err != nil {
			return out, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// QueryConsumerDetail returns the full behavioural profile of one
// consumer: aggregate summary + geo snapshot, method/status histograms,
// and the operations it touched most. This is the "who is doing this"
// investigation pivot.
//
// The caller MUST pass a validated hash (regex-checked, 64 hex chars at
// the handler boundary). The hash is bound as a parameter (`?`), never
// concatenated, so even an unvalidated value cannot break out of the
// WHERE clause.
//
// raw events carry no risk_flags (those are an inventory-only
// enrichment), so the threat signal here is max risk_score +
// critical_events (risk_score>=10) + ti_hits rather than a flag list.
func (c *Client) QueryConsumerDetail(ctx context.Context, hash string, f ConsumerFilter) (*ConsumerDetail, error) {
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("clickhouse client not initialised")
	}
	if err := validateConsumerFilter(f); err != nil {
		return nil, err
	}
	if hash == "" {
		return nil, fmt.Errorf("consumer hash is required")
	}
	top := normaliseTop(f.Top)

	// Window WHERE + the consumer_hash predicate appended as a bound
	// parameter (injection-safe).
	whereSQL, args := buildConsumerWhere(f)
	whereSQL += " AND consumer_hash = ?"
	args = append(args, hash)

	qctx, cancel := c.withTimeout(ctx)
	defer cancel()

	g, gctx := errgroup.WithContext(qctx)
	var (
		mu     sync.Mutex
		detail = ConsumerDetail{
			ConsumerHash: hash,
			MethodDist:   map[string]int64{},
			StatusDist:   map[string]int64{},
			TopEndpoints: []ConsumerEndpoint{},
			TopSourceIPs: []ConsumerSourceIP{},
		}
	)

	// Summary + geo snapshot — single row.
	g.Go(func() error {
		sql := fmt.Sprintf(
			`SELECT
                toInt64(count())                                AS total,
                toInt64(uniq((listener_name, normalized_path))) AS endpoints,
                toInt64(uniq(source_ip_hash))                   AS ips,
                toInt64(max(risk_score))                        AS max_risk,
                toInt64(countIf(risk_score >= 10))              AS critical,
                toInt64(countIf(tags['ti.hit'] = 'true'))       AS ti_hits,
                min(ts)                                         AS first_seen,
                max(ts)                                         AS last_seen,
                any(tags['geo.country'])                        AS geo_country,
                any(tags['geo.asn'])                            AS geo_asn,
                any(tags['geo.asn_org'])                        AS geo_asn_org
            FROM %s
            %s`,
			c.rawQualifiedTable(), whereSQL,
		)
		var (
			total, endpoints, ips, maxRisk, critical, tiHits int64
			firstSeen, lastSeen                              time.Time
			country, asn, asnOrg                             string
		)
		if err := c.conn.QueryRow(gctx, sql, args...).Scan(
			&total, &endpoints, &ips, &maxRisk, &critical, &tiHits,
			&firstSeen, &lastSeen, &country, &asn, &asnOrg,
		); err != nil {
			return fmt.Errorf("consumer summary: %w", err)
		}
		mu.Lock()
		detail.TotalEvents = total
		detail.DistinctEndpoints = endpoints
		detail.DistinctIPs = ips
		detail.MaxRiskScore = maxRisk
		detail.CriticalEvents = critical
		detail.TIHits = tiHits
		detail.FirstSeen = firstSeen
		detail.LastSeen = lastSeen
		detail.GeoCountry = country
		detail.GeoASN = asn
		detail.GeoASNOrg = asnOrg
		mu.Unlock()
		return nil
	})

	// Method distribution.
	g.Go(func() error {
		dist, err := c.queryConsumerKeyCount(gctx, "method", whereSQL, args)
		if err != nil {
			return fmt.Errorf("consumer method dist: %w", err)
		}
		mu.Lock()
		detail.MethodDist = dist
		mu.Unlock()
		return nil
	})

	// Status-code distribution (textual keys).
	g.Go(func() error {
		dist, err := c.queryConsumerKeyCount(gctx, "toString(status_code)", whereSQL, args)
		if err != nil {
			return fmt.Errorf("consumer status dist: %w", err)
		}
		mu.Lock()
		detail.StatusDist = dist
		mu.Unlock()
		return nil
	})

	// Top endpoints this consumer touched.
	g.Go(func() error {
		rows, err := c.queryConsumerEndpoints(gctx, whereSQL, args, top)
		if err != nil {
			return fmt.Errorf("consumer endpoints: %w", err)
		}
		mu.Lock()
		detail.TopEndpoints = rows
		mu.Unlock()
		return nil
	})

	// Source addresses this consumer was seen from.
	g.Go(func() error {
		rows, err := c.queryConsumerSourceIPs(gctx, whereSQL, args, top)
		if err != nil {
			return fmt.Errorf("consumer source ips: %w", err)
		}
		mu.Lock()
		detail.TopSourceIPs = rows
		mu.Unlock()
		return nil
	})

	if err := g.Wait(); err != nil {
		return nil, err
	}
	return &detail, nil
}

// queryConsumerSourceIPs lists the source addresses the consumer was
// observed from, ranked by volume. source_ip_hash is always populated;
// the raw source_ip is included via any() and is empty unless the
// collector persists raw IPs (Policy.StoreRawSourceIP).
func (c *Client) queryConsumerSourceIPs(ctx context.Context, whereSQL string, args []any, top int) ([]ConsumerSourceIP, error) {
	out := []ConsumerSourceIP{}
	sql := fmt.Sprintf(
		`SELECT
            source_ip_hash   AS hash,
            any(source_ip)   AS ip,
            toInt64(count()) AS count
        FROM %s
        %s AND source_ip_hash != ''
        GROUP BY source_ip_hash
        ORDER BY count DESC
        LIMIT %d`,
		c.rawQualifiedTable(), whereSQL, top,
	)
	rows, err := c.conn.Query(ctx, sql, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var s ConsumerSourceIP
		if err := rows.Scan(&s.Hash, &s.IP, &s.Count); err != nil {
			return out, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// queryConsumerKeyCount runs a GROUP BY over a single dimension and
// returns it as a {value: count} map. keyExpr is a fixed SQL expression
// (column name or wrapped function) chosen by the caller — never user
// input — so it is safe to interpolate.
func (c *Client) queryConsumerKeyCount(ctx context.Context, keyExpr, whereSQL string, args []any) (map[string]int64, error) {
	out := map[string]int64{}
	sql := fmt.Sprintf(
		`SELECT %s AS k, toInt64(count()) AS n
        FROM %s
        %s
        GROUP BY k`,
		keyExpr, c.rawQualifiedTable(), whereSQL,
	)
	rows, err := c.conn.Query(ctx, sql, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			k string
			n int64
		)
		if err := rows.Scan(&k, &n); err != nil {
			return out, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// queryConsumerEndpoints lists the operations the consumer hit most,
// with per-endpoint volume and worst observed risk score.
func (c *Client) queryConsumerEndpoints(ctx context.Context, whereSQL string, args []any, top int) ([]ConsumerEndpoint, error) {
	out := []ConsumerEndpoint{}
	sql := fmt.Sprintf(
		`SELECT
            listener_name              AS listener_name,
            normalized_path            AS normalized_path,
            method                     AS method,
            toInt64(count())           AS count,
            toInt64(max(risk_score))   AS max_risk
        FROM %s
        %s
        GROUP BY listener_name, normalized_path, method
        ORDER BY count DESC
        LIMIT %d`,
		c.rawQualifiedTable(), whereSQL, top,
	)
	rows, err := c.conn.Query(ctx, sql, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var e ConsumerEndpoint
		if err := rows.Scan(
			&e.ListenerName, &e.NormalizedPath, &e.Method, &e.Count, &e.MaxRiskScore,
		); err != nil {
			return out, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// buildConsumerWhere builds the shared window predicate for consumer
// queries. Mirrors buildGeoWhere: parametrized project_id + half-open
// [from, to) range. Callers append consumer_hash predicates as needed.
func buildConsumerWhere(f ConsumerFilter) (string, []any) {
	clauses := "WHERE project_id = ? AND ts >= ? AND ts < ?"
	args := []any{f.ProjectID, f.From, f.To}
	return clauses, args
}

// validateConsumerFilter mirrors validateGeoFilter: project + a valid
// half-open time window are required.
func validateConsumerFilter(f ConsumerFilter) error {
	if f.ProjectID == "" {
		return fmt.Errorf("project_id is required")
	}
	if f.From.IsZero() || f.To.IsZero() || !f.To.After(f.From) {
		return fmt.Errorf("from/to time window invalid")
	}
	return nil
}
