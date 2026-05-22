package clickhouse

import "time"

// RawEvent is one row from `api_events_raw`. Field set follows the
// stable columns documented in
// elchi-collector/docs/schema.md#stable-columns. JSON tags shape the
// inventory_detail HTTP response; renames here are a contract change
// for the UI.
type RawEvent struct {
	EventID          string    `json:"event_id" ch:"event_id"`
	Ts               time.Time `json:"ts" ch:"ts"`
	CreatedAt        time.Time `json:"created_at" ch:"created_at"`
	NodeID           string    `json:"node_id" ch:"node_id"`
	ListenerName     string    `json:"listener_name" ch:"listener_name"`
	ProjectID        string    `json:"project_id" ch:"project_id"`
	ListenerIP       string    `json:"listener_ip" ch:"listener_ip"`
	InstanceID       string    `json:"instance_id" ch:"instance_id"`
	StreamID         string    `json:"stream_id" ch:"stream_id"`
	Protocol         string    `json:"protocol" ch:"protocol"`
	Method           string    `json:"method" ch:"method"`
	NormalizedPath   string    `json:"normalized_path" ch:"normalized_path"`
	Host             string    `json:"host" ch:"host"`
	GRPCService      string    `json:"grpc_service" ch:"grpc_service"`
	GRPCMethod       string    `json:"grpc_method" ch:"grpc_method"`
	StatusCode       uint16    `json:"status_code" ch:"status_code"`
	GRPCStatus       *int32    `json:"grpc_status,omitempty" ch:"grpc_status"`
	GRPCMessage      string    `json:"grpc_message,omitempty" ch:"grpc_message"`
	RequestID        string    `json:"request_id" ch:"request_id"`
	RedirectLocation string    `json:"redirect_location,omitempty" ch:"redirect_location"`
	DurationMs       float64   `json:"duration_ms" ch:"duration_ms"`
	RequestBytes     uint64    `json:"request_bytes" ch:"request_bytes"`
	ResponseBytes    uint64    `json:"response_bytes" ch:"response_bytes"`
	Cluster          string    `json:"cluster,omitempty" ch:"cluster"`
	RouteName        string    `json:"route_name,omitempty" ch:"route_name"`
	ContentType      string    `json:"content_type,omitempty" ch:"content_type"`
	UserAgentHash    string    `json:"user_agent_hash,omitempty" ch:"user_agent_hash"`
	SourceIPHash     string    `json:"source_ip_hash,omitempty" ch:"source_ip_hash"`
	// Raw IP / UA shipped by the collector starting from migration 005.
	// Both columns are populated only when the collector's policy
	// flags (`Policy.StoreRawSourceIP`, `Policy.StoreRawUserAgent`)
	// are on (default-on, nil-treated-as-on). Operators flip them off
	// for compliance — in that case the values arrive as empty
	// strings and the JSON keys drop out via omitempty.
	SourceIP       string            `json:"source_ip,omitempty" ch:"source_ip"`
	UserAgent      string            `json:"user_agent,omitempty" ch:"user_agent"`
	ConsumerHash   string            `json:"consumer_hash,omitempty" ch:"consumer_hash"`
	AuthObserved   uint8             `json:"auth_observed" ch:"auth_observed"`
	RiskFlags      []string          `json:"risk_flags,omitempty" ch:"risk_flags"`
	PIICategories  []string          `json:"pii_categories,omitempty" ch:"pii_categories"`
	EndpointCats   []string          `json:"endpoint_categories,omitempty" ch:"endpoint_categories"`
	TLSVersion     string            `json:"tls_version,omitempty" ch:"tls_version"`
	TLSSNI         string            `json:"tls_sni,omitempty" ch:"tls_sni"`
	TLSPeerSubject string            `json:"tls_peer_subject,omitempty" ch:"tls_peer_subject"`
	TLSPeerIssuer  string            `json:"tls_peer_issuer,omitempty" ch:"tls_peer_issuer"`
	Headers        map[string]string `json:"headers,omitempty" ch:"headers"`
	RiskScore      uint8             `json:"risk_score" ch:"risk_score"`
	Tags           map[string]string `json:"tags,omitempty" ch:"tags"`
}

// RawEventsFilter narrows the api_events_raw query. Project, Listener
// and time window are required (the inventory_detail handler enforces
// this); the rest are optional refinements.
type RawEventsFilter struct {
	ProjectID      string
	ListenerName   string
	NormalizedPath string // optional — exact match (order key prefix); empty = any
	Method         string // optional
	StatusMin      uint16 // optional; 0 = unbounded
	StatusMax      uint16 // optional; 0 = unbounded
	RequestID      string // optional
	// EventIDs pins the query to an explicit set of `event_id`
	// values — used by the inventory-detail "sample events"
	// drill-down, which feeds the `api_inventory.sample_event_ids[]`
	// list straight into the raw-events reader. When non-empty the
	// time window still applies (callers pass a wide enough one);
	// empty means "no event_id filter".
	EventIDs []string
	// RiskFlags / PIICategories filter to events carrying at least one
	// of the listed values (hasAny semantics). MinRiskScore keeps only
	// events with risk_score >= the threshold (0 = no filter).
	RiskFlags     []string
	PIICategories []string
	MinRiskScore  uint8
	From          time.Time
	To            time.Time
	Limit         int
	Offset        int
	// IncludeTotal toggles the extra SELECT count() roundtrip used to
	// populate pagination metadata. Off by default: count() over
	// `api_events_raw` walks every part inside the time window and is
	// orders of magnitude slower than the LIMITed data query on busy
	// listeners. Handlers opt in when the caller asks for a total.
	IncludeTotal bool
	// Fields picks the projection mode. "core" (default) drops the
	// large headers/tags maps and TLS metadata that the inventory list
	// UI does not render — a 500-event page shrinks from ~2 MB to a
	// few hundred KB. "full" returns every documented column. Empty
	// string is treated as "core" so legacy callers benefit without an
	// API change.
	Fields string
	// SortBy / SortOrder drive the ORDER BY. SortBy is validated
	// against a fixed whitelist (ts / duration_ms / status_code /
	// risk_score) inside the query builder — an unknown value falls
	// back to ts, so a caller can never inject an arbitrary column.
	// SortOrder is "asc" or "desc" (default desc).
	SortBy    string
	SortOrder string
}

// RollupBucket is one row from any of `api_events_1{m,h,d}` after the
// `-Merge` combinators are applied to the AggregateFunction columns.
// `BucketSize` is encoded as a string ("1m" / "1h" / "1d") so the UI
// can render the granularity without parsing the timestamp difference.
type RollupBucket struct {
	BucketSize       string    `json:"bucket_size"`
	TsBucket         time.Time `json:"ts_bucket"`
	ProjectID        string    `json:"project_id"`
	ListenerName     string    `json:"listener_name"`
	Method           string    `json:"method,omitempty"`
	NormalizedPath   string    `json:"normalized_path,omitempty"`
	StatusClass      uint8     `json:"status_class"` // 1..5 → 1xx..5xx
	EventsCount      uint64    `json:"events_count"`
	DurationP50      float64   `json:"duration_p50_ms"`
	DurationP95      float64   `json:"duration_p95_ms"`
	DurationP99      float64   `json:"duration_p99_ms"`
	DurationAvg      float64   `json:"duration_avg_ms"`
	ResponseBytesSum uint64    `json:"response_bytes_sum"`
	ResponseBytesMax uint64    `json:"response_bytes_max"`
	MaxRiskScore     uint8     `json:"max_risk_score"`
	UniqueConsumers  uint64    `json:"unique_consumers"`
	UniqueSourceIPs  uint64    `json:"unique_source_ips"`
	ErrorCount       uint64    `json:"error_count"`
	ClientErrorCount uint64    `json:"client_error_count"`
}

// GeoFilter narrows the geo / operator aggregation queries. Project +
// time window are required; ListenerName / NormalizedPath narrow the
// scope to a specific listener or endpoint. Method is optional. Top
// caps the per-dimension cardinality returned to the UI.
type GeoFilter struct {
	ProjectID      string
	ListenerName   string
	NormalizedPath string
	Method         string
	From           time.Time
	To             time.Time
	Top            int
	// IncludeRawDims toggles the two raw-string aggregates
	// (top_user_agents / top_source_ips). Off by default — each is its
	// own ClickHouse scan and the default dashboard does not render
	// them, so opting in cuts the summary's parallel-query count from
	// seven to five.
	IncludeRawDims bool
}

// GeoSummary is the response envelope of /api/v3/inventory/geo. Every
// nested struct/field name matches the JSON contract the UI binds to.
type GeoSummary struct {
	TotalEvents        int64            `json:"total_events"`
	InternalVsExternal GeoKindBreakdown `json:"internal_vs_external"`
	TopCountries       []GeoCountry     `json:"top_countries"`
	TopASNs            []GeoASN         `json:"top_asns"`
	TopCities          []GeoCity        `json:"top_cities"`
	UserAgents         UASummary        `json:"user_agents"`
	ThreatIntel        TIHit            `json:"threat_intel"`
	// Top-N raw IPs / UAs the collector has been told to persist
	// (migration 005). Empty arrays when the collector toggle is off
	// for compliance — the hashed equivalents (`*_hash`) are always
	// present on the per-event payload.
	TopUserAgents []RawUserAgent `json:"top_user_agents"`
	TopSourceIPs  []RawSourceIP  `json:"top_source_ips"`
	TimeSeries    *GeoTimeSeries `json:"time_series,omitempty"`
}

// RawUserAgent is one entry in top_user_agents. Value is the raw UA
// header verbatim (no truncation — collector classifies/hashes in
// parallel). Percentage is computed backend-side from count /
// total_events.
type RawUserAgent struct {
	Value      string  `json:"value"`
	Count      int64   `json:"count"`
	Percentage float64 `json:"percentage"`
}

// RawSourceIP is one entry in top_source_ips. Value is the textual
// IPv4 / IPv6 the collector recorded (or empty if compliance toggle
// is off).
type RawSourceIP struct {
	Value      string  `json:"value"`
	Count      int64   `json:"count"`
	Percentage float64 `json:"percentage"`
}

// GeoKindBreakdown sums the three buckets the collector emits in
// `tags['geo.kind']`. "unknown" catches empty/missing values and
// anything outside the documented internal/external pair.
type GeoKindBreakdown struct {
	Internal int64 `json:"internal"`
	External int64 `json:"external"`
	Unknown  int64 `json:"unknown"`
}

// GeoCountry is one row of top_countries. Code is the ISO-3166-1 α-2
// value the collector wrote; Name is the English label (also from
// the collector). Percentage is computed backend-side from
// count/total_events and clamped to one decimal.
type GeoCountry struct {
	Code       string  `json:"code"`
	Name       string  `json:"name"`
	Count      int64   `json:"count"`
	Percentage float64 `json:"percentage"`
}

// GeoASN is one row of top_asns. The collector emits ASN as a
// string-decoded value (`AS9121` style) plus the organisation name.
type GeoASN struct {
	ASN        string  `json:"asn"`
	Org        string  `json:"org"`
	Count      int64   `json:"count"`
	Percentage float64 `json:"percentage"`
}

// GeoCity collapses (city, country) so the UI can disambiguate
// "Springfield" across regions.
type GeoCity struct {
	City       string  `json:"city"`
	Country    string  `json:"country"`
	Count      int64   `json:"count"`
	Percentage float64 `json:"percentage"`
}

// UASummary mirrors the collector's `ua.kind` taxonomy plus a top-N
// of `ua.family` (the actual tool/agent name when classifiable).
type UASummary struct {
	Kinds       map[string]int64 `json:"kinds"`
	TopFamilies []UAFamily       `json:"top_families"`
}

// UAFamily is one entry in user_agents.top_families. Kind is the
// classification bucket the collector assigned to this family — pinned
// alongside the family value so the UI doesn't need a second lookup
// to color-code rows.
type UAFamily struct {
	Family string `json:"family"`
	Kind   string `json:"kind"`
	Count  int64  `json:"count"`
}

// TIHit summarises threat-intel hits. TopSources splits the
// comma-joined `ti.source` collector value into the individual feed
// names so the UI can render a per-feed bar.
type TIHit struct {
	TotalHits  int64      `json:"total_hits"`
	Percentage float64    `json:"percentage"`
	TopSources []TISource `json:"top_sources"`
}

// TISource is one row of threat_intel.top_sources.
type TISource struct {
	Source string `json:"source"`
	Count  int64  `json:"count"`
}

// GeoTimeSeries carries bucketed trends for the geo dashboard. Layout
// is "X axis once + parallel Y series" so charting libraries can plug
// straight in without per-bucket flattening. Top-N country / UA-kind
// series come from the same Top-N picked for the snapshot listings;
// everything outside the Top-N gets folded into the "other" series so
// the totals still add up to the bucket total.
type GeoTimeSeries struct {
	Granularity   string              `json:"granularity"`
	Buckets       []time.Time         `json:"buckets"`
	CountrySeries []TimeSeriesByLabel `json:"country_series"`
	UAKindSeries  []TimeSeriesByLabel `json:"ua_kind_series"`
	TIHitsSeries  []int64             `json:"ti_hits_series"`
}

// TimeSeriesByLabel pairs a dimension value with its per-bucket count
// vector. Values has the same length as GeoTimeSeries.Buckets — a
// missing bucket is encoded as 0 so the UI can plot directly.
type TimeSeriesByLabel struct {
	Label  string  `json:"label"`          // e.g. country code "TR" or ua.kind "browser"
	Name   string  `json:"name,omitempty"` // optional friendly name (country_name)
	Values []int64 `json:"values"`
}

// RollupFilter narrows a rollup query. Granularity is one of
// {"1m","1h","1d"}; granularity "1d" stores no normalized_path so that
// dimension is silently dropped from the WHERE clause.
type RollupFilter struct {
	Granularity    string
	ProjectID      string
	ListenerName   string
	NormalizedPath string // ignored for "1d"
	Method         string
	From           time.Time
	To             time.Time
}

// BotScannerFilter scopes the bot/scanner heatmap. Project + time
// window are required; ListenerName narrows to a single listener
// when set. Top caps the per-axis cardinality returned.
type BotScannerFilter struct {
	ProjectID    string
	ListenerName string
	From         time.Time
	To           time.Time
	Top          int
}

// BotScannerHit is one cell of the bot/scanner × endpoint heatmap.
// `NormalizedPath` is the row axis, `Family` × `Kind` the column —
// the UI renders rows of paths with per-family columns.
type BotScannerHit struct {
	NormalizedPath string `json:"normalized_path"`
	Family         string `json:"family"`
	Kind           string `json:"kind"` // "bot" or "scanner"
	Count          int64  `json:"count"`
}

// ---------------------------------------------------------------------
// Phase 2 — security score / error analysis / transport posture
// ---------------------------------------------------------------------

// SecurityScoreFilter scopes the API Security Score event scan.
type SecurityScoreFilter struct {
	ProjectID string
	From      time.Time
	To        time.Time
}

// SecurityMetrics is the single-scan event-level aggregate behind the
// API Security Score. Endpoint-level counts (auth coverage, attack
// surface) come from MongoDB separately — this struct carries only
// the ClickHouse half.
type SecurityMetrics struct {
	TotalEvents    int64
	WeakTransport  int64   // weak_tls_version OR plain_text_transport
	LegacyProtocol int64   // legacy_protocol risk flag
	CriticalEvents int64   // risk_score >= 10
	ScannerBot     int64   // ua.kind in (scanner, bot)
	TIHits         int64   // threat_intel_hit risk flag
	P95RiskScore   float64 // quantile(0.95)(risk_score)
}

// TransportFilter scopes the TLS / transport posture queries.
type TransportFilter struct {
	ProjectID    string
	ListenerName string
	From         time.Time
	To           time.Time
	Top          int
}

// TransportPostureResult is the full payload of QueryTransportPosture:
// TLS-version + protocol histograms plus the per-endpoint list of
// weak-transport offenders.
type TransportPostureResult struct {
	TotalEvents   int64
	TLSVersions   map[string]int64
	Protocols     map[string]int64
	WeakTransport []WeakTransportRow
}

// WeakTransportRow is one endpoint observed on a non-ideal transport
// (anything below TLSv1.3, plain text, or a legacy protocol).
type WeakTransportRow struct {
	ListenerName   string   `json:"listener_name"`
	NormalizedPath string   `json:"normalized_path"`
	Host           string   `json:"host"`
	TLSVersion     string   `json:"tls_version"`
	Protocol       string   `json:"protocol"`
	Issues         []string `json:"issues"`
	EventCount     int64    `json:"event_count"`
}

// ErrorFilter scopes the error-analysis hotspot query.
type ErrorFilter struct {
	ProjectID    string
	ListenerName string
	From         time.Time
	To           time.Time
	MinErrorRate float64 // 0..100; rows below this rate are dropped CH-side
	Limit        int
	Offset       int
}

// ErrorHotspot is one (listener, path, method) row of the error
// hotspot list. Sourced from the api_events_1h rollup — error_4xx /
// error_5xx already give the 4xx/5xx split, so a per-status-code
// breakdown is intentionally not carried here: the UI drills into
// /inventory/:id/events?status_min=400 for exact codes.
type ErrorHotspot struct {
	ListenerName   string  `json:"listener_name"`
	NormalizedPath string  `json:"normalized_path"`
	Method         string  `json:"method"`
	TotalEvents    int64   `json:"total_events"`
	Error4xx       int64   `json:"error_4xx"`
	Error5xx       int64   `json:"error_5xx"`
	ErrorRate      float64 `json:"error_rate"`
}

// ErrorSummary is the project-wide error roll-up returned alongside
// the hotspot list.
type ErrorSummary struct {
	TotalEvents      int64   `json:"total_events"`
	Total4xx         int64   `json:"total_4xx"`
	Total5xx         int64   `json:"total_5xx"`
	OverallErrorRate float64 `json:"overall_error_rate"`
}

// ErrorSeriesPoint is one bucket of the error time series (sourced
// from the rollup tables).
type ErrorSeriesPoint struct {
	Bucket   time.Time `json:"bucket"`
	Total    int64     `json:"total"`
	Error4xx int64     `json:"error_4xx"`
	Error5xx int64     `json:"error_5xx"`
}

// ---------------------------------------------------------------------
// Consumer (identity) behaviour analytics
// ---------------------------------------------------------------------

// ConsumerFilter scopes the consumer aggregation over api_events_raw.
// Project + time window are required; Top caps the leaderboard size in
// QueryConsumerTop (independent of Limit/Offset which page the handler
// response).
type ConsumerFilter struct {
	ProjectID string
	From      time.Time
	To        time.Time
	Top       int
}

// ConsumerSummary is the payload of GET /inventory/consumers. The
// anonymous bucket (events whose consumer_hash is empty) is reported as
// a single aggregate count rather than mixed into TopConsumers — those
// events have no stable identity to pivot on.
type ConsumerSummary struct {
	TotalEvents     int64                 `json:"total_events"`
	AnonymousEvents int64                 `json:"anonymous_events"`
	TopConsumers    []ConsumerAggregation `json:"top_consumers"`
}

// ConsumerAggregation is one row of the consumer leaderboard. RiskScore
// is the max observed (raw events carry no risk_flags — only inventory
// does — so threat signal here is max risk_score + ti_hits). Percentage
// is share of non-anonymous events, computed backend-side.
type ConsumerAggregation struct {
	ConsumerHash      string    `json:"consumer_hash" ch:"consumer_hash"`
	Events            int64     `json:"events" ch:"events"`
	MaxRiskScore      int64     `json:"max_risk_score" ch:"max_risk"`
	DistinctIPs       int64     `json:"distinct_ips" ch:"distinct_ips"`
	DistinctEndpoints int64     `json:"distinct_endpoints" ch:"distinct_endpoints"`
	TIHits            int64     `json:"ti_hits" ch:"ti_hits"`
	FirstSeen         time.Time `json:"first_seen" ch:"first_seen"`
	LastSeen          time.Time `json:"last_seen" ch:"last_seen"`
	Percentage        float64   `json:"percentage"`
}

// ConsumerDetail is the payload of GET /inventory/consumers/:hash — the
// investigation pivot. MethodDist / StatusDist are keyed by the method
// string and the textual status code; TopEndpoints lists the operations
// this identity touched most. Geo* fields are a best-effort snapshot
// (any() over the window) for quick attribution.
type ConsumerDetail struct {
	ConsumerHash      string             `json:"consumer_hash"`
	TotalEvents       int64              `json:"total_events"`
	DistinctEndpoints int64              `json:"distinct_endpoints"`
	DistinctIPs       int64              `json:"distinct_ips"`
	MaxRiskScore      int64              `json:"max_risk_score"`
	CriticalEvents    int64              `json:"critical_events"`
	TIHits            int64              `json:"ti_hits"`
	FirstSeen         time.Time          `json:"first_seen"`
	LastSeen          time.Time          `json:"last_seen"`
	GeoCountry        string             `json:"geo_country"`
	GeoASN            string             `json:"geo_asn"`
	GeoASNOrg         string             `json:"geo_asn_org"`
	MethodDist        map[string]int64   `json:"method_dist"`
	StatusDist        map[string]int64   `json:"status_dist"`
	TopEndpoints      []ConsumerEndpoint `json:"top_endpoints"`
	TopSourceIPs      []ConsumerSourceIP `json:"top_source_ips"`
}

// ConsumerSourceIP is one source address the consumer was seen from.
// Hash (source_ip_hash) is always present; IP carries the raw textual
// address only when the collector is configured to persist it
// (Policy.StoreRawSourceIP) — otherwise IP is empty and the UI falls
// back to the hash.
type ConsumerSourceIP struct {
	IP    string `json:"ip"`
	Hash  string `json:"hash"`
	Count int64  `json:"count"`
}

// ConsumerEndpoint is one row of ConsumerDetail.TopEndpoints — an
// operation the consumer hit, with its event count and worst observed
// risk score.
type ConsumerEndpoint struct {
	ListenerName   string `json:"listener_name" ch:"listener_name"`
	NormalizedPath string `json:"normalized_path" ch:"normalized_path"`
	Method         string `json:"method" ch:"method"`
	Count          int64  `json:"count" ch:"count"`
	MaxRiskScore   int64  `json:"max_risk_score" ch:"max_risk"`
}
