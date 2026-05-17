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
	conn         driver.Conn
	database     string
	rawTable     string
	rollup1m     string
	rollup1h     string
	rollup1d     string
	queryTimeout time.Duration
	logger       *logger.Logger
	// discoveryCache memoises DiscoverInventoryKeys by filter — see
	// discovery_cache.go. Always non-nil after Open(); the TTL alone
	// decides whether reads/writes do real work.
	discoveryCache *discoveryCache
}

// Open dials ClickHouse using the URI/DSN from cfg, validates the
// connection with a Ping, and returns a ready-to-use Client. The Ping
// failure is fatal — without a working connection the inventory_detail
// endpoints would return 5xx on every request, so we surface the error
// to the caller (cmd/controller.go) which logs and proceeds without a
// ClickHouse client (graceful degradation).
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

	pingCtx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	if err := conn.Ping(pingCtx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping clickhouse: %w", err)
	}

	c := &Client{
		conn:         conn,
		database:     cfg.ClickhouseDatabase,
		rawTable:     cfg.ClickhouseTable,
		rollup1m:     cfg.ClickhouseRollup1m,
		rollup1h:     cfg.ClickhouseRollup1h,
		rollup1d:     cfg.ClickhouseRollup1d,
		queryTimeout: time.Duration(cfg.ClickhouseQueryTimeoutSec) * time.Second,
		logger:       log,
		// 60s TTL is the upper bound on staleness an operator filtering
		// the inventory list by country/ASN/IP/UA will tolerate; the
		// underlying raw events stream lives at sub-minute freshness
		// so a fresh dimension shows up on the next refresh anyway.
		// 256 entries is well above the realistic combination ceiling
		// per controller and bounds the cache memory at a few MB.
		discoveryCache: newDiscoveryCache(60*time.Second, 256),
	}
	log.Infof("clickhouse connected: db=%s raw=%s pool(open=%d idle=%d lifetime=%s)",
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
