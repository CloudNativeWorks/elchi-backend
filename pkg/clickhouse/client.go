// Package clickhouse provides a thin read-only client over the
// elchi-collector's ClickHouse tables (`api_events_raw` and the
// `api_events_1{m,h,d}` rollups). Schema is pinned by
// elchi-collector/docs/schema.md as the stable interface; only stable
// columns documented there are queried here.
//
// The client is initialised at controller startup. When CLICKHOUSE_URI
// is empty, callers are expected to skip initialisation entirely — the
// inventory_detail HTTP handlers respond 503 in that case so the
// controller can still boot in environments without a collector.
package clickhouse

import (
	"context"
	"fmt"
	"time"

	chgo "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

// Client wraps the long-lived driver.Conn together with the database
// and table identifiers used in every query. Construct once at startup
// and share by reference; callers must NOT close it until shutdown.
type Client struct {
	conn            driver.Conn
	database        string
	rawTable        string
	shieldTable     string
	shieldUseRollup bool
	rollup1m        string
	rollup1h        string
	rollup1d        string
	queryTimeout    time.Duration
	logger          *logger.Logger
	// discoveryCache memoises DiscoverInventoryKeys by filter — see
	// discovery_cache.go. Always non-nil after Open(); the TTL alone
	// decides whether reads/writes do real work.
	discoveryCache *discoveryCache
}

// Open dials ClickHouse using the URI/DSN from cfg and returns a
// ready-to-use Client. chgo.Open builds a *lazy* connection pool: it does
// not actually dial until the first query, so the returned client
// self-heals once ClickHouse becomes reachable.
//
// The startup Ping is therefore only a *probe*, NOT a gate: when it fails
// (e.g. ClickHouse hasn't finished booting yet — common under Docker
// Swarm / k8s where the controller starts before the CH cluster forms),
// we log a warning and return the client anyway. The next real query
// (storage-stats, inventory_detail) dials a fresh connection from the
// pool and succeeds — no controller restart required. Returning nil here
// would instead disable ClickHouse permanently until the next restart.
//
// A non-nil error is returned only for unrecoverable config problems
// (empty URI, unparseable DSN, pool construction failure) where retrying
// the same client could never succeed.
func Open(cfg *config.AppConfig, log *logger.Logger) (*Client, error) {
	if cfg.ClickhouseURI == "" {
		return nil, fmt.Errorf("CLICKHOUSE_URI is empty")
	}

	opts, err := chgo.ParseDSN(cfg.ClickhouseURI)
	if err != nil {
		return nil, fmt.Errorf("parse CLICKHOUSE_URI: %w", err)
	}
	// Defensive: applyDefaults sets ClickhouseConnectTimeoutSec to 5s
	// when missing, but a hand-edited config that explicitly leaves it
	// at 0 would otherwise produce a `context.WithTimeout(ctx, 0)` at
	// Ping time — that context is *already* cancelled, so the Ping
	// returns DeadlineExceeded before a TCP handshake is even
	// attempted, making the controller unable to bring up the
	// ClickHouse client even when the server is healthy.
	connectTimeout := time.Duration(cfg.ClickhouseConnectTimeoutSec) * time.Second
	if connectTimeout <= 0 {
		connectTimeout = 5 * time.Second
	}
	opts.DialTimeout = connectTimeout
	// Override the driver's built-in pool caps. Defaults (idle=5,
	// open=10) starve inventory's geo dashboard, which fires up to
	// seven parallel scans per request. Settings come from
	// applyDefaults so the values are always populated; we still
	// guard against a hand-edited config that left them at 0.
	if cfg.ClickhouseMaxIdleConns > 0 {
		opts.MaxIdleConns = cfg.ClickhouseMaxIdleConns
	}
	if cfg.ClickhouseMaxOpenConns > 0 {
		opts.MaxOpenConns = cfg.ClickhouseMaxOpenConns
	}
	if cfg.ClickhouseConnMaxLifetimeMin > 0 {
		opts.ConnMaxLifetime = time.Duration(cfg.ClickhouseConnMaxLifetimeMin) * time.Minute
	}

	conn, err := chgo.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open clickhouse: %w", err)
	}

	// Probe the connection, but do NOT fail on a ping error: chgo.Open's
	// pool is lazy and reconnects on the next query, so a controller that
	// boots ahead of ClickHouse self-heals instead of staying disabled
	// until restart. We keep the client and only log the transient miss.
	pingCtx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	if err := conn.Ping(pingCtx); err != nil {
		log.Warnf("clickhouse ping failed at startup (will reconnect on first query): %v", err)
	}

	c := &Client{
		conn:            conn,
		database:        cfg.ClickhouseDatabase,
		rawTable:        cfg.ClickhouseTable,
		shieldTable:     cfg.ClickhouseShieldTable,
		shieldUseRollup: cfg.ClickhouseShieldUseRollup,
		rollup1m:        cfg.ClickhouseRollup1m,
		rollup1h:        cfg.ClickhouseRollup1h,
		rollup1d:        cfg.ClickhouseRollup1d,
		queryTimeout:    time.Duration(cfg.ClickhouseQueryTimeoutSec) * time.Second,
		logger:          log,
		// 60s TTL is the upper bound on staleness an operator filtering
		// the inventory list by country/ASN/IP/UA will tolerate; the
		// underlying raw events stream lives at sub-minute freshness
		// so a fresh dimension shows up on the next refresh anyway.
		// 256 entries is well above the realistic combination ceiling
		// per controller and bounds the cache memory at a few MB.
		discoveryCache: newDiscoveryCache(60*time.Second, 256),
	}
	log.Infof("clickhouse client ready: db=%s raw=%s pool(open=%d idle=%d lifetime=%s)",
		c.database, c.rawTable, opts.MaxOpenConns, opts.MaxIdleConns, opts.ConnMaxLifetime)
	return c, nil
}

// Close shuts the underlying connection pool down. Errors are returned
// for observability but cannot be acted on at shutdown time.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Ping verifies the connection is still usable. Used by /healthz and by
// the inventory_detail handlers' fast-fail path before a query.
func (c *Client) Ping(ctx context.Context) error {
	if c == nil || c.conn == nil {
		return fmt.Errorf("clickhouse client not initialised")
	}
	return c.conn.Ping(ctx)
}

// rawQualifiedTable returns "<db>.<raw_events_table>" for use in SQL.
func (c *Client) rawQualifiedTable() string {
	return c.database + "." + c.rawTable
}

// shieldQualifiedTable returns "<db>.<shield_audit_table>" for use in SQL. This
// is the table elchi-shield's ClickHouse audit exporter writes to directly.
func (c *Client) shieldQualifiedTable() string {
	return c.database + "." + c.shieldTable
}

// shieldRollupQualifiedTable returns "<db>.<shield_table>_1m" — the per-minute
// AggregatingMergeTree rollup shield maintains off the audit table. The `_1m`
// suffix is hardcoded to exactly match what shield's exporter creates (there is no
// separate config knob, so the read side can never drift from the write side).
func (c *Client) shieldRollupQualifiedTable() string {
	return c.database + "." + c.shieldTable + "_1m"
}

// rollupQualifiedTable returns the qualified rollup name for the given
// granularity. An unknown granularity yields an empty string so callers
// can fail-closed.
func (c *Client) rollupQualifiedTable(granularity string) string {
	var t string
	switch granularity {
	case "1m":
		t = c.rollup1m
	case "1h":
		t = c.rollup1h
	case "1d":
		t = c.rollup1d
	default:
		return ""
	}
	return c.database + "." + t
}

// withTimeout wraps the provided context with the configured query
// timeout when one is set; otherwise it returns the context unchanged
// alongside a no-op cancel.
func (c *Client) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.queryTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.queryTimeout)
}
