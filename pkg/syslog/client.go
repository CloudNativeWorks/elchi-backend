// Package syslog provides a thin RFC5424 syslog client and a non-blocking
// audit-log forwarder used to optionally ship audit entries to an external
// SIEM. The pipeline runs in parallel to MongoDB persistence — SIEM downtime
// must never affect audit_logs writes.
package syslog

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"sync"
	"time"
)

// Protocol values accepted in Config.Protocol.
const (
	ProtocolUDP    = "udp"
	ProtocolTCP    = "tcp"
	ProtocolTCPTLS = "tcp+tls"
)

const (
	defaultConnectTimeout = 5 * time.Second
	defaultWriteTimeout   = 10 * time.Second
)

// Config carries the connection parameters for a syslog client.
// Cert/key fields are PEM-encoded and only consulted when Protocol == "tcp+tls".
type Config struct {
	Protocol         string
	Host             string
	Port             int
	CACert           string
	ClientCert       string
	ClientKey        string
	ConnectTimeoutMs int
	WriteTimeoutMs   int
}

func (c Config) addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

func (c Config) connectTimeout() time.Duration {
	if c.ConnectTimeoutMs > 0 {
		return time.Duration(c.ConnectTimeoutMs) * time.Millisecond
	}
	return defaultConnectTimeout
}

func (c Config) writeTimeout() time.Duration {
	if c.WriteTimeoutMs > 0 {
		return time.Duration(c.WriteTimeoutMs) * time.Millisecond
	}
	return defaultWriteTimeout
}

// Client is a single-target syslog connection. Safe for concurrent use.
// On Send error it closes the conn so the next Send re-dials.
type Client struct {
	cfg  Config
	mu   sync.Mutex
	conn net.Conn
}

// NewClient returns an unconnected Client. Call Connect before Send.
func NewClient(cfg Config) *Client {
	return &Client{cfg: cfg}
}

// Connect establishes the underlying connection.
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connectLocked()
}

func (c *Client) connectLocked() error {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}

	addr := c.cfg.addr()
	timeout := c.cfg.connectTimeout()

	switch c.cfg.Protocol {
	case ProtocolUDP:
		conn, err := net.DialTimeout("udp", addr, timeout)
		if err != nil {
			return fmt.Errorf("syslog udp dial %s: %w", addr, err)
		}
		c.conn = conn

	case ProtocolTCP:
		conn, err := net.DialTimeout("tcp", addr, timeout)
		if err != nil {
			return fmt.Errorf("syslog tcp dial %s: %w", addr, err)
		}
		c.conn = conn

	case ProtocolTCPTLS:
		tlsCfg, err := c.buildTLSConfig()
		if err != nil {
			return fmt.Errorf("syslog tls config: %w", err)
		}
		dialer := &net.Dialer{Timeout: timeout}
		conn, err := tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
		if err != nil {
			return fmt.Errorf("syslog tls dial %s: %w", addr, err)
		}
		c.conn = conn

	default:
		return fmt.Errorf("syslog: unsupported protocol %q", c.cfg.Protocol)
	}

	return nil
}

func (c *Client) buildTLSConfig() (*tls.Config, error) {
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: c.cfg.Host,
	}

	if c.cfg.CACert != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(c.cfg.CACert)) {
			return nil, fmt.Errorf("invalid CA certificate")
		}
		tlsCfg.RootCAs = pool
	}

	if c.cfg.ClientCert != "" && c.cfg.ClientKey != "" {
		cert, err := tls.X509KeyPair([]byte(c.cfg.ClientCert), []byte(c.cfg.ClientKey))
		if err != nil {
			return nil, fmt.Errorf("loading client certificate: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	return tlsCfg, nil
}

// Send writes a single syslog frame. On error the underlying connection is
// closed; the next Send will reconnect on demand. Send does NOT retry — that
// is the caller's responsibility.
func (c *Client) Send(payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		if err := c.connectLocked(); err != nil {
			return err
		}
	}

	if c.cfg.Protocol != ProtocolUDP {
		_ = c.conn.SetWriteDeadline(time.Now().Add(c.cfg.writeTimeout()))
	}

	if _, err := c.conn.Write(payload); err != nil {
		_ = c.conn.Close()
		c.conn = nil
		return fmt.Errorf("syslog send: %w", err)
	}

	if c.cfg.Protocol != ProtocolUDP {
		// Newline framing for TCP/TLS receivers (rsyslog, syslog-ng).
		if _, err := c.conn.Write([]byte{'\n'}); err != nil {
			_ = c.conn.Close()
			c.conn = nil
			return fmt.Errorf("syslog send (delim): %w", err)
		}
	}

	return nil
}

// Close releases the underlying connection.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

// TestConnection opens a short-lived connection with the given config, sends
// a synthetic RFC5424 test frame, and returns any error encountered. Safe to
// call against a separate Client — does not interfere with a forwarder's
// long-lived connection.
func TestConnection(cfg Config) error {
	tmp := NewClient(cfg)
	if err := tmp.Connect(); err != nil {
		return err
	}
	defer tmp.Close()

	payload := EncodeTestMessage(cfg)
	return tmp.Send(payload)
}
