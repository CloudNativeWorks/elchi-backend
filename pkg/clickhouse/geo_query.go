package clickhouse

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// QueryGeoSummary runs six aggregate sub-queries in parallel against
// `api_events_raw.tags` and returns the geo + UA + threat-intel
// summary that backs the API Discovery dashboard.
//
// Rollups don't carry the `tags` map (collector schema 1m/1h/1d
// dimensions exclude geo/UA/TI), so every query reads from raw —
// hence the caller-enforced 7-day time-window cap. Each sub-query
// runs on its own goroutine; ClickHouse's internal thread pool keeps
// total wall-time close to the slowest single aggregation rather than
// the sum of six.
//
// Fail-fast contract: the first sub-query to error cancels the rest
// via the shared context. Partial results are intentionally not
// emitted — a half-populated dashboard is worse than a clear failure.
func (c *Client) QueryGeoSummary(ctx context.Context, f GeoFilter) (*GeoSummary, error) {
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("clickhouse client not initialised")
	}
	if err := validateGeoFilter(f); err != nil {
		return nil, err
	}
	whereSQL, args := buildGeoWhere(f)
	top := normaliseTop(f.Top)

	qctx, cancel := c.withTimeout(ctx)
	defer cancel()

	// errgroup gives proper fail-fast: the first sub-query error
	// cancels the derived context, the remaining ClickHouse calls
	// abort instead of running to completion and wasting cycles on
	// a response we're going to discard.
	g, gctx := errgroup.WithContext(qctx)
	var (
		mu      sync.Mutex
		summary GeoSummary
	)

	// Initialise nullable slice/map fields non-nil so the JSON
	// contract always emits `[]` / `{}` and the UI's `.length` /
	// `.entries()` calls don't crash on null. Raw-dim Top-N stay
	// empty when IncludeRawDims is off; the UserAgents.Kinds map and
	// ThreatIntel.TotalHits are populated by the totals scan; the
	// dedicated families / TI-sources sub-queries fill the remaining
	// slices.
	summary.TopUserAgents = []RawUserAgent{}
	summary.TopSourceIPs = []RawSourceIP{}
	summary.ThreatIntel = TIHit{TopSources: []TISource{}}
	summary.UserAgents = UASummary{Kinds: map[string]int64{}, TopFamilies: []UAFamily{}}

	g.Go(func() error {
		// queryGeoBundle is the single ClickHouse scan that replaces
		// what used to be five paralleled aggregates: totals + the
		// three `geo.kind` buckets + ti.hits + ua.kind histogram +
		// Top-N country / ASN / city / family. The high-cardinality
		// dimensions arrive as `Map(String, UInt64)` payloads with
		// compound keys ("value\tcompanion"); topN* helpers split &
		// rank backend-side so the SQL stays one pass over the table.
		res, err := c.queryGeoBundle(gctx, whereSQL, args)
		if err != nil {
			return fmt.Errorf("geo bundle: %w", err)
		}
		// Compute the Top-N slices BEFORE taking the mutex. Each
		// topN* call walks the dimension map and runs sort.Slice;
		// at 100K+ city cardinality that's measurable (~5 ms × 4
		// dimensions). Holding the mutex during that window would
		// serialise the TI-sources goroutine writing to
		// summary.ThreatIntel.TopSources unnecessarily.
		topCountries := topNCountries(res.CountryData, top)
		topASNs := topNASNs(res.ASNData, top)
		topCities := topNCities(res.CityData, top)
		topFamilies := topNFamilies(res.FamilyData, top)

		mu.Lock()
		summary.TotalEvents = res.TotalEvents
		summary.InternalVsExternal = res.Kinds
		summary.ThreatIntel.TotalHits = res.TIHits
		summary.UserAgents.Kinds = res.UAKinds
		summary.TopCountries = topCountries
		summary.TopASNs = topASNs
		summary.TopCities = topCities
		summary.UserAgents.TopFamilies = topFamilies
		mu.Unlock()
		return nil
	})
	g.Go(func() error {
		// TI top_sources only — the total_hits count is folded into
		// queryGeoTotals above to save one full table scan.
		sources, err := c.queryTISources(gctx, whereSQL, args, top)
		if err != nil {
			return fmt.Errorf("threat intel sources: %w", err)
		}
		mu.Lock()
		summary.ThreatIntel.TopSources = sources
		mu.Unlock()
		return nil
	})

	// Raw User-Agent / source-IP Top-N are opt-in via
	// GeoFilter.IncludeRawDims — each is its own full `api_events_raw`
	// scan, and the default dashboard does not render them. Opting in
	// brings the parallel query count back to seven; with it off we
	// run five.
	if f.IncludeRawDims {
		g.Go(func() error {
			rows, err := c.queryTopUserAgents(gctx, whereSQL, args, top)
			if err != nil {
				return fmt.Errorf("top user agents: %w", err)
			}
			mu.Lock()
			summary.TopUserAgents = rows
			mu.Unlock()
			return nil
		})

		g.Go(func() error {
			rows, err := c.queryTopSourceIPs(gctx, whereSQL, args, top)
			if err != nil {
				return fmt.Errorf("top source ips: %w", err)
			}
			mu.Lock()
			summary.TopSourceIPs = rows
			mu.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Backend-side percentage computation: keeps the 1-decimal
	// formatting consistent across dimensions, and avoids leaking
	// floating divide-by-zero into the JSON when total_events == 0
	// (legitimate scenario for projects with no traffic yet).
	applyPercentages(&summary)
	return &summary, nil
}

// QueryGeoTimeSeries returns bucketed trends for the geo dashboard.
// Two parallel aggregates: country counts per bucket (with the same
// Top-N selection algorithm the summary uses) and UA-kind counts per
// bucket. TI hits are emitted as a simple per-bucket sum.
//
// `topCountries` parameter lets the caller pass the already-computed
// Top-N list from QueryGeoSummary so the series labels match the
// dashboard's snapshot table exactly. Anything outside that set is
// folded into a single "other" series (same behaviour for UA kinds is
// not needed — the kind taxonomy is small and finite).
func (c *Client) QueryGeoTimeSeries(ctx context.Context, f GeoFilter, granularity string, topCountries []string) (*GeoTimeSeries, error) {
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("clickhouse client not initialised")
	}
	if err := validateGeoFilter(f); err != nil {
		return nil, err
	}
	bucketFn, ok := bucketFunction(granularity)
	if !ok {
		return nil, fmt.Errorf("unsupported granularity: %s (expected 1m, 1h, 1d)", granularity)
	}
	whereSQL, args := buildGeoWhere(f)
	qctx, cancel := c.withTimeout(ctx)
	defer cancel()

	// Compute the full bucket list once so the parallel queries fan
	// into the same X axis; missing buckets get zeros at merge time.
	//
	// Bucket keys are int64 unix-second offsets — NOT time.Time —
	// because ClickHouse's DateTime returns a Go time.Time stamped
	// with the server's local zone (e.g. Asia/Istanbul) while we
	// build the bucket list from UTC inputs. Two time.Time values
	// representing the same instant in different zones compare
	// unequal under `==` / map lookup (Location pointer is part of
	// the struct), so a naive map[time.Time]int would silently miss
	// every row and yield empty series. Unix() collapses both to
	// the same key. parseTimeWindow already canonicalises f.From /
	// f.To to UTC so expandBuckets produces UTC buckets directly.
	buckets := expandBuckets(f.From, f.To, granularity)
	bucketIdx := make(map[int64]int, len(buckets))
	for i, b := range buckets {
		bucketIdx[b.Unix()] = i
	}

	// Containment set for "other" folding; small (≤ 50) so a map is fine.
	topSet := make(map[string]struct{}, len(topCountries))
	for _, c := range topCountries {
		if c != "" {
			topSet[c] = struct{}{}
		}
	}

	g, gctx := errgroup.WithContext(qctx)
	var (
		mu  sync.Mutex
		out = GeoTimeSeries{
			Granularity:  granularity,
			Buckets:      buckets,
			TIHitsSeries: make([]int64, len(buckets)),
		}
	)

	g.Go(func() error {
		series, err := c.queryCountrySeries(gctx, whereSQL, args, bucketFn, bucketIdx, topSet, len(buckets))
		if err != nil {
			return fmt.Errorf("country series: %w", err)
		}
		mu.Lock()
		out.CountrySeries = series
		mu.Unlock()
		return nil
	})
	g.Go(func() error {
		series, err := c.queryUAKindSeries(gctx, whereSQL, args, bucketFn, bucketIdx, len(buckets))
		if err != nil {
			return fmt.Errorf("ua kind series: %w", err)
		}
		mu.Lock()
		out.UAKindSeries = series
		mu.Unlock()
		return nil
	})
	g.Go(func() error {
		ti, err := c.queryTIHitsSeries(gctx, whereSQL, args, bucketFn, bucketIdx, len(buckets))
		if err != nil {
			return fmt.Errorf("ti hits series: %w", err)
		}
		mu.Lock()
		out.TIHitsSeries = ti
		mu.Unlock()
		return nil
	})

	if err := g.Wait(); err != nil {
		return nil, err
	}
	return &out, nil
}

// DiscoverPathsByGeo returns the distinct (listener_name,
// normalized_path) pairs that match the geo / operator filter inside
// the given time window. The handler uses this to pre-filter the
// MongoDB `api_inventory` list when an operator passes `?country=…`
// or `?asn=…`.
//
// Returned as a flat slice of "<listener>::<path>" composite keys so
// the caller can build a MongoDB `$or` array without rebuilding the
// shape on every iteration.
type GeoInventoryKey struct {
	ListenerName   string `ch:"listener_name"`
	NormalizedPath string `ch:"normalized_path"`
}

// InventoryDiscoveryFilter narrows DiscoverInventoryKeys to the
// (listener, path) tuples whose traffic in the given window matches
// any combination of geo / operator / raw-IP / raw-UA constraints.
// All non-empty fields combine with AND; at least one of the four
// dimensions must be set (caller responsibility, defensively
// re-validated here).
type InventoryDiscoveryFilter struct {
	ProjectID string
	Country   string
	ASN       string
	SourceIP  string
	UserAgent string
	From      time.Time
	To        time.Time
	Limit     int
}

// DiscoverInventoryKeys reads distinct (listener_name,
// normalized_path) tuples matching the InventoryDiscoveryFilter
// constraints. Used by the inventory list endpoint to translate a
// CH-side dimension (country / ASN / IP / UA) into a MongoDB `$or`
// clause over `api_inventory`.
func (c *Client) DiscoverInventoryKeys(ctx context.Context, f InventoryDiscoveryFilter) ([]GeoInventoryKey, error) {
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("clickhouse client not initialised")
	}
	if f.ProjectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}
	if f.Country == "" && f.ASN == "" && f.SourceIP == "" && f.UserAgent == "" {
		return nil, fmt.Errorf("at least one of country / asn / source_ip / user_agent must be provided")
	}
	if f.From.IsZero() || f.To.IsZero() || !f.To.After(f.From) {
		return nil, fmt.Errorf("from/to time window invalid")
	}
	// Cap the discovery set hard. The result feeds a MongoDB `$or` of
	// `(listener_name, normalized_path)` pairs — at 1000 entries the
	// planner still picks an index intersection; past a few thousand
	// it falls back to a collection scan that defeats the whole
	// pre-filter. UI list endpoints paginate at 100 anyway, so 1000
	// candidates is two orders of magnitude over typical use.
	limit := f.Limit
	if limit <= 0 || limit > 2000 {
		limit = 1000
	}
	f.Limit = limit // normalise so the cache key reflects the actual cap

	// Cache lookup. Same filter inside the TTL window skips the full
	// SELECT DISTINCT scan — the inventory dashboard hits this for
	// every refresh, and the underlying SCAN cost dominates a
	// page-load otherwise.
	cacheKey := discoveryCacheKey(f)
	if hit, ok := c.discoveryCache.Get(cacheKey); ok {
		return hit, nil
	}

	clauses := []string{
		"project_id = ?",
		"ts >= ?",
		"ts <  ?",
		"normalized_path != ''",
	}
	args := []any{f.ProjectID, f.From, f.To}
	if f.Country != "" {
		clauses = append(clauses, "tags['geo.country'] = ?")
		args = append(args, f.Country)
	}
	if f.ASN != "" {
		clauses = append(clauses, "tags['geo.asn'] = ?")
		args = append(args, f.ASN)
	}
	if f.SourceIP != "" {
		clauses = append(clauses, "source_ip = ?")
		args = append(args, f.SourceIP)
	}
	if f.UserAgent != "" {
		clauses = append(clauses, "user_agent = ?")
		args = append(args, f.UserAgent)
	}
	sql := fmt.Sprintf(
		`SELECT DISTINCT listener_name, normalized_path
        FROM %s
        WHERE %s
        LIMIT %d`,
		c.rawQualifiedTable(),
		strings.Join(clauses, " AND "),
		limit,
	)
	qctx, cancel := c.withTimeout(ctx)
	defer cancel()
	rows, err := c.conn.Query(qctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("discover paths by geo: %w", err)
	}
	defer rows.Close()
	out := make([]GeoInventoryKey, 0, 256)
	for rows.Next() {
		var k GeoInventoryKey
		if err := rows.ScanStruct(&k); err != nil {
			return nil, fmt.Errorf("scan inventory key: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inventory keys: %w", err)
	}
	// Cache success path only (empty set is still a valid result and
	// is cached). Errors are not cached so a transient failure
	// retries on the next call.
	c.discoveryCache.Set(cacheKey, out)
	return out, nil
}

// ---------------------------------------------------------------------
// internal helpers — single-purpose sub-queries
// ---------------------------------------------------------------------

// geoBundleResult is the single-scan aggregate returned by
// queryGeoBundle. Pairs every count/bucket aggregate with four
// `sumMap`-collected dimension maps (country / asn / city / family),
// removing four otherwise-distinct full scans of `api_events_raw`.
//
// Compound keys: country/asn/city dimensions encode `value\tcompanion`
// (companion = country_name / asn_org / country code respectively) so
// the Top-N helper can split them post-scan without a second lookup.
// The TAB separator is safe because none of these companion fields
// contain TABs in practice; the SQL builder defensively replaces TAB
// with a space before concat in case future data does.
type geoBundleResult struct {
	TotalEvents int64
	Kinds       GeoKindBreakdown
	TIHits      int64
	UAKinds     map[string]int64

	// High-cardinality dimensions, captured as Map(String, UInt64).
	// Key encoding documented above; backend Top-N + companion split
	// happens in topNCountries / topNASNs / topNCities.
	CountryData map[string]uint64 // "TR\tTurkey" → count
	ASNData     map[string]uint64 // "12735\tTurkNet…" → count
	CityData    map[string]uint64 // "Istanbul\tTR" → count
	FamilyData  map[string]uint64 // "chrome" → count (no companion)
}

// queryGeoBundle is the one-scan dashboard summary. It replaces the
// previous five-goroutine fan-out (totals + 4 separate GROUP BY
// scans) with a single ClickHouse query that emits 12 scalar
// aggregates plus 4 `Map(String, UInt64)` payloads.
//
// Why one scan beats five paralleled:
//   - CH still parses, plans, and binds five queries; pool budget
//     drops accordingly under multi-user load.
//   - The same `tags` Map column is decoded once instead of five
//     times. Disk I/O stays roughly the same (all scans hit the same
//     parts), but the per-query CPU & network round-trips collapse.
//   - Reduces summary's parallel scan count from five to two
//     (this + the TI-sources micro-scan), freeing pool slots for the
//     time-series / discovery / raw events queries that share the
//     same client.
//
// Wall-time is comparable to the previous slowest-of-five pattern
// (~52 ms vs ~50 ms in our test harness on 35 K rows) — the win is
// in resource cost, not latency.
func (c *Client) queryGeoBundle(ctx context.Context, whereSQL string, args []any) (geoBundleResult, error) {
	// `replaceAll(.., '\t', ' ')` defends the compound encoding: a
	// rogue TAB in a country_name / asn_org would otherwise corrupt
	// the post-scan split. CH evaluates the function once per row.
	sql := fmt.Sprintf(
		`SELECT
            toInt64(count())                                                   AS total_events,
            toInt64(countIf(tags['geo.kind'] = 'internal'))                    AS internal_count,
            toInt64(countIf(tags['geo.kind'] = 'external'))                    AS external_count,
            toInt64(countIf(tags['geo.kind'] NOT IN ('internal','external')))  AS unknown_count,
            toInt64(countIf(tags['ti.hit'] = 'true'))                          AS ti_hits,
            toInt64(countIf(tags['ua.kind'] = 'browser'))                      AS ua_browser,
            toInt64(countIf(tags['ua.kind'] = 'bot'))                          AS ua_bot,
            toInt64(countIf(tags['ua.kind'] = 'scanner'))                      AS ua_scanner,
            toInt64(countIf(tags['ua.kind'] = 'sdk'))                          AS ua_sdk,
            toInt64(countIf(tags['ua.kind'] = 'cli'))                          AS ua_cli,
            toInt64(countIf(tags['ua.kind'] = 'monitor'))                      AS ua_monitor,
            toInt64(countIf(tags['ua.kind'] = 'empty' OR tags['ua.kind'] = '')) AS ua_empty,
            toInt64(countIf(tags['ua.kind'] NOT IN ('browser','bot','scanner','sdk','cli','monitor','empty',''))) AS ua_unknown,
            sumMap(map(
                concat(tags['geo.country'], '\t', replaceAll(tags['geo.country_name'], '\t', ' ')),
                toUInt64(1)
            )) AS country_data,
            sumMap(map(
                concat(tags['geo.asn'], '\t', replaceAll(tags['geo.asn_org'], '\t', ' ')),
                toUInt64(1)
            )) AS asn_data,
            sumMap(map(
                concat(tags['geo.city'], '\t', tags['geo.country']),
                toUInt64(1)
            )) AS city_data,
            sumMap(map(tags['ua.family'], toUInt64(1))) AS family_data
        FROM %s %s`,
		c.rawQualifiedTable(), whereSQL,
	)
	var (
		total       int64
		internal    int64
		external    int64
		unknown     int64
		tiHits      int64
		uaBrowser   int64
		uaBot       int64
		uaScanner   int64
		uaSDK       int64
		uaCLI       int64
		uaMonitor   int64
		uaEmpty     int64
		uaUnknown   int64
		countryData map[string]uint64
		asnData     map[string]uint64
		cityData    map[string]uint64
		familyData  map[string]uint64
	)
	if err := c.conn.QueryRow(ctx, sql, args...).Scan(
		&total, &internal, &external, &unknown, &tiHits,
		&uaBrowser, &uaBot, &uaScanner, &uaSDK, &uaCLI, &uaMonitor, &uaEmpty, &uaUnknown,
		&countryData, &asnData, &cityData, &familyData,
	); err != nil {
		return geoBundleResult{}, err
	}
	return geoBundleResult{
		TotalEvents: total,
		Kinds:       GeoKindBreakdown{Internal: internal, External: external, Unknown: unknown},
		TIHits:      tiHits,
		// UI relies on a stable JSON shape — emit zero-valued buckets
		// rather than dropping keys, so the donut chart doesn't have
		// to special-case missing kinds for low-traffic windows.
		UAKinds: map[string]int64{
			"browser": uaBrowser,
			"bot":     uaBot,
			"scanner": uaScanner,
			"sdk":     uaSDK,
			"cli":     uaCLI,
			"monitor": uaMonitor,
			"empty":   uaEmpty,
			"unknown": uaUnknown,
		},
		CountryData: countryData,
		ASNData:     asnData,
		CityData:    cityData,
		FamilyData:  familyData,
	}, nil
}

// parseCompoundKey splits "value\tcompanion" into its two parts.
// Used by topN* helpers to extract the companion field
// (country_name, asn_org, country code) that the queryGeoBundle SQL
// packs into the sumMap key alongside the primary value. A missing
// TAB returns the original string with an empty companion — defends
// against any future code path that puts a non-compound value into
// the same map.
func parseCompoundKey(s string) (value, companion string) {
	i := strings.IndexByte(s, '\t')
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i+1:]
}

// topNCountries / topNASNs / topNCities / topNFamilies select the
// Top-N entries from the corresponding sumMap payload returned by
// queryGeoBundle. Single sort.Slice is O(n log n); given typical
// cardinalities (≤ a few thousand per dimension) the cost is microseconds.
//
// Empty-key rows are dropped — they represent events the collector
// could not enrich (e.g. private-IP traffic with no country lookup)
// and would surface as a nameless slice in the UI's donut chart.
//
// Pre-allocating the typed output slice once we know the trimmed
// size avoids the grow-and-copy loop that `append` would otherwise do
// on every iteration; the inner kv struct stays scoped to the
// helper to keep the map pipeline clean.
func topNCountries(data map[string]uint64, top int) []GeoCountry {
	type kv struct {
		code, name string
		count      uint64
	}
	items := make([]kv, 0, len(data))
	for k, v := range data {
		if v == 0 {
			continue
		}
		code, name := parseCompoundKey(k)
		if code == "" {
			continue
		}
		items = append(items, kv{code, name, v})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].count > items[j].count })
	if len(items) > top {
		items = items[:top]
	}
	out := make([]GeoCountry, 0, len(items))
	for _, it := range items {
		out = append(out, GeoCountry{Code: it.code, Name: it.name, Count: int64(it.count)})
	}
	return out
}

func topNASNs(data map[string]uint64, top int) []GeoASN {
	type kv struct {
		asn, org string
		count    uint64
	}
	items := make([]kv, 0, len(data))
	for k, v := range data {
		if v == 0 {
			continue
		}
		asn, org := parseCompoundKey(k)
		if asn == "" {
			continue
		}
		items = append(items, kv{asn, org, v})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].count > items[j].count })
	if len(items) > top {
		items = items[:top]
	}
	out := make([]GeoASN, 0, len(items))
	for _, it := range items {
		out = append(out, GeoASN{ASN: it.asn, Org: it.org, Count: int64(it.count)})
	}
	return out
}

func topNCities(data map[string]uint64, top int) []GeoCity {
	type kv struct {
		city, country string
		count         uint64
	}
	items := make([]kv, 0, len(data))
	for k, v := range data {
		if v == 0 {
			continue
		}
		city, country := parseCompoundKey(k)
		if city == "" {
			continue
		}
		items = append(items, kv{city, country, v})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].count > items[j].count })
	if len(items) > top {
		items = items[:top]
	}
	out := make([]GeoCity, 0, len(items))
	for _, it := range items {
		out = append(out, GeoCity{City: it.city, Country: it.country, Count: int64(it.count)})
	}
	return out
}

// topNFamilies has no companion field — the UA family map keys are
// plain strings ("chrome", "firefox", …). Kind is intentionally not
// populated here; the histogram bucket already lives in
// summary.UserAgents.Kinds and the per-family kind isn't a stable
// contract anyway (one family can produce events tagged with
// multiple kinds during a window).
func topNFamilies(data map[string]uint64, top int) []UAFamily {
	type kv struct {
		family string
		count  uint64
	}
	items := make([]kv, 0, len(data))
	for k, v := range data {
		if v == 0 || k == "" {
			continue
		}
		items = append(items, kv{k, v})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].count > items[j].count })
	if len(items) > top {
		items = items[:top]
	}
	out := make([]UAFamily, 0, len(items))
	for _, it := range items {
		out = append(out, UAFamily{Family: it.family, Count: int64(it.count)})
	}
	return out
}

// queryTopUserAgents pulls the Top-N raw User-Agent strings the
// collector persisted (migration 005). Empty-string rows are excluded
// so a collector running with `store_raw_user_agent=false` yields
// an empty list rather than a "" placeholder row.
func (c *Client) queryTopUserAgents(ctx context.Context, whereSQL string, args []any, top int) ([]RawUserAgent, error) {
	sql := fmt.Sprintf(
		`SELECT
            user_agent       AS value,
            toInt64(count()) AS count
        FROM %s
        %s AND user_agent != ''
        GROUP BY value
        ORDER BY count DESC
        LIMIT %d`,
		c.rawQualifiedTable(), whereSQL, top,
	)
	rows, err := c.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RawUserAgent, 0, top)
	for rows.Next() {
		var r RawUserAgent
		if err := rows.Scan(&r.Value, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// queryTopSourceIPs mirrors queryTopUserAgents for the source_ip
// column. Same empty-string filter applies for the compliance
// opt-out path.
func (c *Client) queryTopSourceIPs(ctx context.Context, whereSQL string, args []any, top int) ([]RawSourceIP, error) {
	sql := fmt.Sprintf(
		`SELECT
            source_ip        AS value,
            toInt64(count()) AS count
        FROM %s
        %s AND source_ip != ''
        GROUP BY value
        ORDER BY count DESC
        LIMIT %d`,
		c.rawQualifiedTable(), whereSQL, top,
	)
	rows, err := c.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RawSourceIP, 0, top)
	for rows.Next() {
		var r RawSourceIP
		if err := rows.Scan(&r.Value, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// queryTISources returns the Top-N TI feed names contributing to
// `ti.hit = 'true'` rows in the window. TotalHits is computed inside
// queryGeoTotals so this query only emits the top-sources groupby,
// keeping it scoped to the hits-only subset (a small fraction of
// rows on healthy traffic).
func (c *Client) queryTISources(ctx context.Context, whereSQL string, args []any, top int) ([]TISource, error) {
	out := []TISource{}
	srcSQL := fmt.Sprintf(
		`SELECT
            arrayJoin(splitByChar(',', tags['ti.source'])) AS source,
            toInt64(count())                               AS count
        FROM %s
        %s AND tags['ti.hit'] = 'true' AND tags['ti.source'] != ''
        GROUP BY source
        ORDER BY count DESC
        LIMIT %d`,
		c.rawQualifiedTable(), whereSQL, top,
	)
	rows, err := c.conn.Query(ctx, srcSQL, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var s TISource
		if err := rows.Scan(&s.Source, &s.Count); err != nil {
			return out, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (c *Client) queryCountrySeries(
	ctx context.Context, whereSQL string, args []any,
	bucketFn string, bucketIdx map[int64]int, topSet map[string]struct{}, bucketCount int,
) ([]TimeSeriesByLabel, error) {
	// Empty Top-N → no series possible. Returning an empty (non-nil)
	// slice keeps the UI contract `[]` rather than null, and skips two
	// scans of `api_events_raw`. The handler builds topSet from
	// QueryGeoSummary's TopCountries; when the window has zero traffic
	// the dashboard shows an empty country chart and we have nothing
	// useful to plot anyway.
	if len(topSet) == 0 {
		return []TimeSeriesByLabel{}, nil
	}

	// Materialise the Top-N codes for the IN-clause. Composite key
	// approach keeps the SQL plan simple; ClickHouse hashes the small
	// (≤50 elements) slice into an in-memory set and uses it as a row
	// filter, so we only scan rows that contribute to one of the
	// Top-N series — a 10080-bucket × 100-country result set shrinks
	// to 10080 × 50.
	topList := make([]string, 0, len(topSet))
	for code := range topSet {
		topList = append(topList, code)
	}

	// Build two queries in parallel: (1) Top-N per-bucket series,
	// (2) `other` bucket totals — i.e. everything NOT IN the Top-N.
	// Splitting avoids exploding cardinality wire-side, which is the
	// dominant cost on long windows.
	topSQL := fmt.Sprintf(
		`SELECT
            %s AS bucket,
            tags['geo.country']           AS code,
            any(tags['geo.country_name']) AS name,
            toInt64(count())              AS count
        FROM %s
        %s AND tags['geo.country'] IN ?
        GROUP BY bucket, code
        ORDER BY bucket ASC, count DESC`,
		bucketFn, c.rawQualifiedTable(), whereSQL,
	)
	otherSQL := fmt.Sprintf(
		`SELECT
            %s AS bucket,
            toInt64(count()) AS count
        FROM %s
        %s AND tags['geo.country'] != '' AND tags['geo.country'] NOT IN ?
        GROUP BY bucket
        ORDER BY bucket ASC`,
		bucketFn, c.rawQualifiedTable(), whereSQL,
	)

	topArgs := append(append([]any{}, args...), topList)
	otherArgs := append(append([]any{}, args...), topList)

	// Top-N and `other` are independent scans — fan them out in
	// parallel so the wall-time stays at max(top, other) rather than
	// adding both. Each goroutine writes to its own variable; the
	// merge into `series` happens single-threaded after Wait().
	var (
		topRows   = []countrySeriesRow{}
		otherRows = []countryOtherRow{}
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		rows, err := c.conn.Query(gctx, topSQL, topArgs...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r countrySeriesRow
			if err := rows.Scan(&r.bucket, &r.code, &r.name, &r.count); err != nil {
				return err
			}
			topRows = append(topRows, r)
		}
		return rows.Err()
	})
	g.Go(func() error {
		rows, err := c.conn.Query(gctx, otherSQL, otherArgs...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r countryOtherRow
			if err := rows.Scan(&r.bucket, &r.count); err != nil {
				return err
			}
			otherRows = append(otherRows, r)
		}
		return rows.Err()
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Merge Top-N rows into the per-code series. Bucket index lookup
	// keys on UTC unix seconds — same idiom used in queryUAKindSeries
	// to defuse the time.Time / Location-pointer equality trap.
	indexByCode := map[string]int{}
	names := map[string]string{}
	series := []TimeSeriesByLabel{}
	for _, r := range topRows {
		idx, ok := bucketIdx[r.bucket.UTC().Unix()]
		if !ok {
			continue
		}
		i, exists := indexByCode[r.code]
		if !exists {
			series = append(series, TimeSeriesByLabel{
				Label:  r.code,
				Values: make([]int64, bucketCount),
			})
			i = len(series) - 1
			indexByCode[r.code] = i
		}
		if r.name != "" {
			names[r.code] = r.name
		}
		series[i].Values[idx] += r.count
	}
	// Backfill names (any-aggregate is non-deterministic; keep first non-empty).
	for code, name := range names {
		if i, ok := indexByCode[code]; ok {
			series[i].Name = name
		}
	}

	// `other` series — non-Top-N traffic folded into a single line.
	other := make([]int64, bucketCount)
	hasOther := false
	for _, r := range otherRows {
		idx, ok := bucketIdx[r.bucket.UTC().Unix()]
		if !ok {
			continue
		}
		other[idx] += r.count
		if r.count > 0 {
			hasOther = true
		}
	}
	if hasOther {
		series = append(series, TimeSeriesByLabel{Label: "other", Values: other})
	}
	return series, nil
}

// countrySeriesRow / countryOtherRow are the typed scan targets used
// by queryCountrySeries' parallel readers — kept named so the merge
// loop stays readable and the scanner doesn't need to allocate
// anonymous structs per row.
type countrySeriesRow struct {
	bucket time.Time
	code   string
	name   string
	count  int64
}

type countryOtherRow struct {
	bucket time.Time
	count  int64
}

func (c *Client) queryUAKindSeries(
	ctx context.Context, whereSQL string, args []any,
	bucketFn string, bucketIdx map[int64]int, bucketCount int,
) ([]TimeSeriesByLabel, error) {
	sql := fmt.Sprintf(
		`SELECT
            %s AS bucket,
            tags['ua.kind'] AS kind,
            toInt64(count()) AS count
        FROM %s
        %s
        GROUP BY bucket, kind
        ORDER BY bucket ASC`,
		bucketFn, c.rawQualifiedTable(), whereSQL,
	)
	rows, err := c.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	indexByKind := map[string]int{}
	// Non-nil empty default → JSON `[]` for the no-data case.
	series := []TimeSeriesByLabel{}
	for rows.Next() {
		var (
			b     time.Time
			kind  string
			count int64
		)
		if err := rows.Scan(&b, &kind, &count); err != nil {
			return nil, err
		}
		idx, ok := bucketIdx[b.UTC().Unix()]
		if !ok {
			continue
		}
		if kind == "" {
			kind = "empty"
		}
		i, exists := indexByKind[kind]
		if !exists {
			series = append(series, TimeSeriesByLabel{
				Label:  kind,
				Values: make([]int64, bucketCount),
			})
			i = len(series) - 1
			indexByKind[kind] = i
		}
		series[i].Values[idx] += count
	}
	return series, rows.Err()
}

func (c *Client) queryTIHitsSeries(
	ctx context.Context, whereSQL string, args []any,
	bucketFn string, bucketIdx map[int64]int, bucketCount int,
) ([]int64, error) {
	sql := fmt.Sprintf(
		`SELECT
            %s AS bucket,
            toInt64(countIf(tags['ti.hit'] = 'true')) AS hits
        FROM %s %s
        GROUP BY bucket
        ORDER BY bucket ASC`,
		bucketFn, c.rawQualifiedTable(), whereSQL,
	)
	rows, err := c.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]int64, bucketCount)
	for rows.Next() {
		var (
			b    time.Time
			hits int64
		)
		if err := rows.Scan(&b, &hits); err != nil {
			return nil, err
		}
		if idx, ok := bucketIdx[b.UTC().Unix()]; ok {
			out[idx] = hits
		}
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------
// small utilities
// ---------------------------------------------------------------------

func validateGeoFilter(f GeoFilter) error {
	if f.ProjectID == "" {
		return fmt.Errorf("project_id is required")
	}
	if f.From.IsZero() || f.To.IsZero() || !f.To.After(f.From) {
		return fmt.Errorf("from/to time window invalid")
	}
	return nil
}

func buildGeoWhere(f GeoFilter) (string, []any) {
	clauses := []string{"project_id = ?", "ts >= ?", "ts <  ?"}
	args := []any{f.ProjectID, f.From, f.To}
	if f.ListenerName != "" {
		clauses = append(clauses, "listener_name = ?")
		args = append(args, f.ListenerName)
	}
	if f.NormalizedPath != "" {
		clauses = append(clauses, "normalized_path = ?")
		args = append(args, f.NormalizedPath)
	}
	if f.Method != "" {
		clauses = append(clauses, "method = ?")
		args = append(args, f.Method)
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func normaliseTop(top int) int {
	switch {
	case top <= 0:
		return 10
	case top > 50:
		return 50
	default:
		return top
	}
}

func applyPercentages(s *GeoSummary) {
	if s == nil || s.TotalEvents <= 0 {
		return
	}
	total := float64(s.TotalEvents)
	pct := func(n int64) float64 {
		return floor1(float64(n) / total * 100.0)
	}
	for i := range s.TopCountries {
		s.TopCountries[i].Percentage = pct(s.TopCountries[i].Count)
	}
	for i := range s.TopASNs {
		s.TopASNs[i].Percentage = pct(s.TopASNs[i].Count)
	}
	for i := range s.TopCities {
		s.TopCities[i].Percentage = pct(s.TopCities[i].Count)
	}
	for i := range s.TopUserAgents {
		s.TopUserAgents[i].Percentage = pct(s.TopUserAgents[i].Count)
	}
	for i := range s.TopSourceIPs {
		s.TopSourceIPs[i].Percentage = pct(s.TopSourceIPs[i].Count)
	}
	s.ThreatIntel.Percentage = pct(s.ThreatIntel.TotalHits)
}

func floor1(v float64) float64 {
	// 1-decimal floor without importing math just for this — keeps the
	// package free of fp surprises (math.Round on .05 boundaries).
	return float64(int64(v*10)) / 10.0
}

// bucketFunction maps the public granularity tokens to the
// corresponding ClickHouse start-of-* helpers. Only those three are
// supported — anything else returns ok=false so callers 400 the
// request.
func bucketFunction(granularity string) (string, bool) {
	switch granularity {
	case "1m":
		return "toStartOfMinute(ts)", true
	case "1h":
		return "toStartOfHour(ts)", true
	case "1d":
		return "toStartOfDay(ts)", true
	default:
		return "", false
	}
}

// expandBuckets enumerates every bucket boundary inside [from, to)
// for the chosen granularity so missing buckets surface as zero
// rather than as gaps in the series. The handler clamps the window
// to 7 days so this slice stays small (max 10080 minutes at 1m
// granularity).
func expandBuckets(from, to time.Time, granularity string) []time.Time {
	var step time.Duration
	var startFn func(t time.Time) time.Time
	switch granularity {
	case "1m":
		step = time.Minute
		startFn = func(t time.Time) time.Time {
			return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, t.Location())
		}
	case "1h":
		step = time.Hour
		startFn = func(t time.Time) time.Time {
			return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location())
		}
	case "1d":
		step = 24 * time.Hour
		startFn = func(t time.Time) time.Time {
			return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
		}
	default:
		return nil
	}
	from = startFn(from)
	to = startFn(to)
	if !to.After(from) {
		return []time.Time{from}
	}
	n := int(to.Sub(from)/step) + 1
	out := make([]time.Time, 0, n)
	for t := from; !t.After(to); t = t.Add(step) {
		out = append(out, t)
	}
	return out
}
