package clickhouse

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	currentPostureDefaultWindowDays = 7
	// Bounded by raw retention (~7d) — current posture is read from raw
	// (not the rollup) so it can be host/protocol/grpc precise, and raw is
	// the 7d-retained table. A larger window would silently return only the
	// retained slice, so the cap stays at the raw-retention horizon.
	currentPostureMaxWindowDays = 7
)

// clampWindowDays normalises a requested window to [1, max] with a default.
func clampWindowDays(windowDays int) int {
	switch {
	case windowDays <= 0:
		return currentPostureDefaultWindowDays
	case windowDays > currentPostureMaxWindowDays:
		return currentPostureMaxWindowDays
	}
	return windowDays
}

// QueryCurrentPosture computes one operation's recent ("current") posture
// from ClickHouse — the counterpart to the monotonic "_ever" values on the
// inventory doc. It answers "is this endpoint STILL bad, or only
// historically?".
//
// Read from RAW with the FULL operation key (project, listener, host,
// protocol, method, normalized_path, grpc_service, grpc_method). Rationale:
//   - Host precision / NO false positives. The rollup tables key only on
//     (listener, method, normalized_path) — they drop host/protocol/grpc, so
//     a rollup lookup would return the MAX across every virtual host sharing
//     that path and mis-attribute a sibling host's risk to a clean endpoint.
//     Raw carries the full key, so the match is exactly this operation.
//   - Sampling-safe anyway. The collector's raw sampling only sheds BENIGN
//     events (risky ones are always written), so max(risk_score) over raw is
//     exact for the risk signal — the rollup held no advantage here (the MV
//     is materialised FROM raw).
//
// There is deliberately NO current EXPOSURE score: the collector does not yet
// ship posture_score to ClickHouse, so posture stays "_ever" only. Active is
// false when no raw events fell in the window (dormant); the caller surfaces
// that as a null `current` so the UI distinguishes "clean because quiet" from
// "clean because remediated".
func (c *Client) QueryCurrentPosture(ctx context.Context, f CurrentPostureFilter) (*CurrentPosture, error) {
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("clickhouse client not initialised")
	}
	if f.ProjectID == "" {
		return nil, fmt.Errorf("project_id is required")
	}

	windowDays := clampWindowDays(f.WindowDays)
	windowStart := time.Now().UTC().AddDate(0, 0, -windowDays)

	out := &CurrentPosture{
		WindowDays:    windowDays,
		RiskFlags:     []string{},
		PIICategories: []string{},
	}

	sql := fmt.Sprintf(
		`SELECT
            toInt64(count())                     AS n,
            toInt64(maxOrDefault(risk_score))    AS max_risk,
            groupUniqArrayArray(risk_flags)      AS flags,
            groupUniqArrayArray(pii_categories)  AS pii,
            toUInt8(maxOrDefault(auth_observed)) AS any_auth,
            toUInt8(minOrDefault(auth_observed)) AS min_auth,
            maxOrNull(ts)                        AS last_active
         FROM %s
         WHERE project_id = ? AND listener_name = ? AND host = ? AND protocol = ?
           AND method = ? AND normalized_path = ? AND grpc_service = ? AND grpc_method = ?
           AND ts >= ?`,
		c.rawQualifiedTable(),
	)

	qctx, cancel := c.withTimeout(ctx)
	defer cancel()

	var (
		n          int64
		maxRisk    int64
		flags      []string
		pii        []string
		anyAuth    uint8
		minAuth    uint8
		lastActive *time.Time
	)
	if err := c.conn.QueryRow(qctx, sql,
		f.ProjectID, f.ListenerName, f.Host, f.Protocol,
		f.Method, f.NormalizedPath, f.GRPCService, f.GRPCMethod,
		windowStart,
	).Scan(&n, &maxRisk, &flags, &pii, &anyAuth, &minAuth, &lastActive); err != nil {
		return nil, fmt.Errorf("current posture: %w", err)
	}

	out.EventCount = n
	out.Active = n > 0
	if flags != nil {
		out.RiskFlags = flags
	}
	if pii != nil {
		out.PIICategories = pii
	}
	// Score + auth signals are only meaningful when the window has traffic.
	// A dormant operation's aggregates default to 0 and would otherwise read
	// as "clean & unauthenticated" — the caller nulls `current` when !Active.
	if n > 0 {
		out.MaxRiskScore = maxRisk
		out.AuthObserved = anyAuth == 1
		out.NoauthObserved = minAuth == 0
		if lastActive != nil {
			out.LastActive = *lastActive
		}
	}
	return out, nil
}

// CurrentRiskKey identifies one operation for the batch current-risk lookup
// (the list-enrichment counterpart to QueryCurrentPosture). It is the FULL
// operation key — host / protocol / grpc_* included — so a row is matched to
// exactly its own traffic (the rollup tables drop host and would
// mis-attribute a sibling virtual host's risk to a clean endpoint → FP).
type CurrentRiskKey struct {
	ListenerName   string
	Host           string
	Protocol       string
	Method         string
	NormalizedPath string
	GRPCService    string
	GRPCMethod     string
}

// QueryCurrentRiskBatch returns the current (windowed) max risk score per
// FULL operation key for the supplied keys, in a single ClickHouse
// round-trip. Backs catalog list enrichment ("show CURRENT risk, not stale
// ever-max") for ListInventory / ListInventoryOperations pages.
//
// Read from RAW (same host-precise / sampling-safe rationale as
// QueryCurrentPosture). The query prunes on `normalized_path IN (...)` — the
// raw table's primary key is (project_id, normalized_path, ts), so the page's
// distinct paths drive an indexed seek; it then GROUPs BY the full key so
// each operation is attributed exactly. Result keys ABSENT from the map had
// no traffic in the window → the caller renders them dormant.
//
// Empty key set returns an empty map (enrichment is a best-effort overlay,
// never an error that blocks the list).
//
// posture-current is a deliberate follow-up: once the collector ships
// posture_score to ClickHouse (raw), add max(posture_score) to the same
// SELECT / GROUP BY and merge — the catalog then shows current EXPOSURE too.
func (c *Client) QueryCurrentRiskBatch(ctx context.Context, projectID string, keys []CurrentRiskKey, windowDays int) (map[CurrentRiskKey]int64, error) {
	result := map[CurrentRiskKey]int64{}
	if c == nil || c.conn == nil {
		return result, fmt.Errorf("clickhouse client not initialised")
	}
	if projectID == "" || len(keys) == 0 {
		return result, nil
	}

	windowStart := time.Now().UTC().AddDate(0, 0, -clampWindowDays(windowDays))

	// Distinct normalized_paths drive the primary-key prune. Values come from
	// MongoDB inventory docs, but are bound as parameters (never spliced).
	pathSet := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		pathSet[k.NormalizedPath] = struct{}{}
	}
	placeholders := make([]string, 0, len(pathSet))
	args := make([]any, 0, 2+len(pathSet))
	args = append(args, projectID, windowStart)
	for p := range pathSet {
		placeholders = append(placeholders, "?")
		args = append(args, p)
	}

	sql := fmt.Sprintf(
		`SELECT
            listener_name,
            host,
            protocol,
            method,
            normalized_path,
            grpc_service,
            grpc_method,
            toInt64(maxOrDefault(risk_score)) AS cur
         FROM %s
         WHERE project_id = ? AND ts >= ? AND normalized_path IN (%s)
         GROUP BY listener_name, host, protocol, method, normalized_path, grpc_service, grpc_method`,
		c.rawQualifiedTable(), strings.Join(placeholders, ","),
	)

	qctx, cancel := c.withTimeout(ctx)
	defer cancel()

	rows, err := c.conn.Query(qctx, sql, args...)
	if err != nil {
		return result, fmt.Errorf("current risk batch: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			k   CurrentRiskKey
			cur int64
		)
		if err := rows.Scan(
			&k.ListenerName, &k.Host, &k.Protocol, &k.Method,
			&k.NormalizedPath, &k.GRPCService, &k.GRPCMethod, &cur,
		); err != nil {
			return result, err
		}
		result[k] = cur
	}
	return result, rows.Err()
}
