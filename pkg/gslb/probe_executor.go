package gslb

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// ProbeTask represents a single probe task for health checking
type ProbeTask struct {
	IPHealth  *models.GSLBIPHealth
	Probe     *models.GSLBProbe
	RecordIDs []primitive.ObjectID // Records using this IP+config (for fan-out)
}

// ProbeResult represents the result of a health check probe
type ProbeResult struct {
	IP           string
	Success      bool
	ResponseCode int
	ResponseTime float64
	Error        error                 // Error if probe failed
	Probe        *models.GSLBProbe     // Probe config used (for threshold lookups)
	Context      context.Context       // Contains ProbeTask for fan-out to multiple records
}

// ProbeExecutor interface for executing health check probes
type ProbeExecutor interface {
	ExecuteProbe(ctx context.Context, ipHealth *models.GSLBIPHealth, probe *models.GSLBProbe) ProbeResult
	Close()
}

// DefaultProbeExecutor is the default implementation of ProbeExecutor interface
// Executes HTTP, HTTPS, and TCP health checks
type DefaultProbeExecutor struct {
	logger *logger.Logger

	// HTTP client with connection pooling
	httpClient *http.Client

	// Shared HTTP client pool for custom configurations (prevents connection leak)
	// Key: "ssl_skip" or "no_redirect" or "ssl_skip_no_redirect"
	// Value: Pre-configured HTTP client with pooled transport
	clientPool map[string]*http.Client
	poolMu     sync.RWMutex
}

// NewDefaultProbeExecutor creates a new default probe executor
func NewDefaultProbeExecutor(logger *logger.Logger) *DefaultProbeExecutor {
	// Create HTTP client with connection pooling and timeouts
	// Note: DialContext timeout will be set dynamically per probe config
	httpClient := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
			DisableKeepAlives:   false,
			// DialContext will be configured per-request in executeHTTPProbe
			// to use probe timeout + 100ms (prevents hanging on unreachable IPs)
		},
		// Timeout will be set per-request based on probe config
		Timeout: 0,
	}

	return &DefaultProbeExecutor{
		logger:     logger,
		httpClient: httpClient,
		clientPool: make(map[string]*http.Client),
	}
}

// ExecuteProbe executes a health check probe based on probe type
// Returns ProbeResult with success status, response code, and response time
func (pe *DefaultProbeExecutor) ExecuteProbe(ctx context.Context, ipHealth *models.GSLBIPHealth, probe *models.GSLBProbe) ProbeResult {
	startTime := time.Now()

	var result ProbeResult

	// CRITICAL: Check for nil ipHealth to prevent panic
	if ipHealth == nil {
		result.Success = false
		result.Error = fmt.Errorf("ipHealth is nil")
		result.Probe = probe
		result.ResponseTime = time.Since(startTime).Seconds()
		return result
	}

	result.IP = ipHealth.IP
	result.Probe = probe // Pass probe config in result (eliminates cache lookup)

	// CRITICAL: Check for nil probe to prevent panic
	if probe == nil {
		result.Success = false
		result.Error = fmt.Errorf("probe config is nil")
		result.ResponseTime = time.Since(startTime).Seconds()
		return result
	}

	switch probe.Type {
	case "http", "https":
		result = pe.executeHTTPProbe(ctx, ipHealth, probe)
	case "tcp":
		result = pe.executeTCPProbe(ctx, ipHealth, probe)
	default:
		result.Success = false
		result.Error = fmt.Errorf("unsupported probe type: %s", probe.Type)
	}

	// Calculate response time
	result.ResponseTime = time.Since(startTime).Seconds()

	// Ensure probe is set in result (in case sub-function creates new result struct)
	result.Probe = probe

	return result
}

// executeHTTPProbe performs HTTP/HTTPS health check
func (pe *DefaultProbeExecutor) executeHTTPProbe(ctx context.Context, ipHealth *models.GSLBIPHealth, probe *models.GSLBProbe) ProbeResult {
	result := ProbeResult{IP: ipHealth.IP}

	// Build URL
	scheme := probe.Type // "http" or "https"
	host := ipHealth.IP
	port := probe.Port
	path := probe.Path
	if path == "" {
		path = "/"
	}

	url := fmt.Sprintf("%s://%s:%d%s", scheme, host, port, path)

	// Create HTTP request with context (timeout managed by context)
	// Reuse shared HTTP client instead of creating new one per probe
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		result.Success = false
		result.Error = fmt.Errorf("failed to create request: %w", err)
		return result
	}

	// Set Host header if specified (for virtual hosting)
	if probe.HostHeader != "" {
		req.Host = probe.HostHeader
	}

	// Set User-Agent
	req.Header.Set("User-Agent", "elchi-gslb-health-checker/1.0")

	// Check if we need custom client configuration
	needsCustomTransport := probe.Type == "https" && probe.SkipSSLVerify != nil && *probe.SkipSSLVerify
	needsCustomRedirect := probe.FollowRedirects != nil && !*probe.FollowRedirects

	// Calculate probe timeout for connection-level timeouts
	probeTimeout := time.Duration(probe.Timeout * float64(time.Second))

	// ALWAYS use custom client to get proper DialContext timeout (probe timeout + 100ms)
	// This prevents TCP connections from hanging on unreachable IPs
	// Build pool key for this client configuration (includes timeout for proper pooling)
	poolKey := fmt.Sprintf("timeout_%dms", int(probeTimeout.Milliseconds()))
	if needsCustomTransport {
		poolKey += "_ssl_skip"
	}
	if needsCustomRedirect {
		poolKey += "_no_redirect"
	}

	// Get or create pooled HTTP client with dynamic DialContext timeout
	// (PERFORMANCE FIX: prevents connection leak + CRITICAL FIX: prevents hanging connections)
	client := pe.getOrCreateClient(poolKey, needsCustomTransport, needsCustomRedirect, probeTimeout)

	// Execute request using appropriate client
	// Timeout is handled by context (set in worker pool)
	resp, err := client.Do(req)
	if err != nil {
		result.Success = false
		result.ResponseCode = 0

		// Build detailed error message including Host header if present
		// This helps troubleshooting when virtual hosting is used
		if probe.HostHeader != "" {
			result.Error = fmt.Errorf("HTTP request failed (Host: %s): %w", probe.HostHeader, err)
		} else {
			result.Error = fmt.Errorf("HTTP request failed: %w", err)
		}
		return result
	}
	defer resp.Body.Close()

	// Drain and discard response body to enable connection reuse
	_, _ = io.Copy(io.Discard, resp.Body)

	result.ResponseCode = resp.StatusCode

	// Check if status code matches expected codes
	if probe.MatchesStatusCode(resp.StatusCode) {
		result.Success = true
		result.Error = nil
	} else {
		result.Success = false
		result.Error = fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	return result
}

// executeTCPProbe performs TCP connection health check
func (pe *DefaultProbeExecutor) executeTCPProbe(ctx context.Context, ipHealth *models.GSLBIPHealth, probe *models.GSLBProbe) ProbeResult {
	result := ProbeResult{IP: ipHealth.IP}

	// Build target address
	target := fmt.Sprintf("%s:%d", ipHealth.IP, probe.Port)

	// Create dialer with timeout from probe config
	dialer := &net.Dialer{
		Timeout: time.Duration(probe.Timeout * float64(time.Second)),
	}

	// Attempt TCP connection
	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		result.Success = false
		result.ResponseCode = 0
		result.Error = fmt.Errorf("TCP connection failed: %w", err)
		return result
	}
	defer conn.Close()

	// Connection successful
	result.Success = true
	result.ResponseCode = 200 // Use 200 for successful TCP connections
	result.Error = nil

	return result
}

// getOrCreateClient retrieves or creates a pooled HTTP client for custom configurations
// This prevents connection leak by reusing both clients and transports instead of creating new ones per probe
func (pe *DefaultProbeExecutor) getOrCreateClient(poolKey string, skipSSLVerify, noRedirect bool, probeTimeout time.Duration) *http.Client {
	// Try read lock first (fast path - most common case)
	pe.poolMu.RLock()
	client, exists := pe.clientPool[poolKey]
	pe.poolMu.RUnlock()

	if exists {
		return client
	}

	// Need to create - acquire write lock
	pe.poolMu.Lock()
	defer pe.poolMu.Unlock()

	// Double-check after acquiring write lock (another goroutine might have created it)
	if client, exists := pe.clientPool[poolKey]; exists {
		return client
	}

	// Clone base transport
	baseTransport, ok := pe.httpClient.Transport.(*http.Transport)
	if !ok {
		// Fallback if base transport is not *http.Transport
		baseTransport = &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
			DisableKeepAlives:   false,
		}
	}

	transport := baseTransport.Clone()

	// ✅ CRITICAL FIX: Configure DialContext with probe timeout + 100ms
	// This prevents TCP connections from hanging on unreachable IPs
	// User requirement: Use probe timeout + 100ms, not fixed timeout
	dialTimeout := probeTimeout + (100 * time.Millisecond)
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialer := &net.Dialer{
			Timeout:   dialTimeout,
			KeepAlive: 30 * time.Second,
		}
		return dialer.DialContext(ctx, network, addr)
	}

	// ✅ Set TLS handshake timeout (same as dial timeout)
	transport.TLSHandshakeTimeout = dialTimeout

	// ✅ Set response header timeout (same as dial timeout)
	transport.ResponseHeaderTimeout = dialTimeout

	// Apply SSL skip if requested
	if skipSSLVerify {
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{}
		}
		transport.TLSClientConfig.InsecureSkipVerify = true // #nosec G402 - User explicitly requested to skip SSL verification
	}

	// Handle redirect behavior
	var checkRedirect func(req *http.Request, via []*http.Request) error
	if noRedirect {
		checkRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // Don't follow redirects
		}
	}

	// Create HTTP client with pooled transport
	client = &http.Client{
		Transport:     transport,
		Timeout:       pe.httpClient.Timeout,
		CheckRedirect: checkRedirect,
	}

	// Store in pool for reuse
	pe.clientPool[poolKey] = client
	pe.logger.Debugf("Created pooled HTTP client: %s (timeout: %v, total: %d)", poolKey, dialTimeout, len(pe.clientPool))

	return client
}

// Close cleans up resources used by the probe executor
func (pe *DefaultProbeExecutor) Close() {
	// Close idle connections on base client
	pe.httpClient.CloseIdleConnections()

	// Close idle connections on all pooled clients
	pe.poolMu.Lock()
	defer pe.poolMu.Unlock()

	for poolKey, client := range pe.clientPool {
		client.CloseIdleConnections()
		pe.logger.Debugf("Closed pooled HTTP client: %s", poolKey)
	}

	// Clear the pool
	pe.clientPool = make(map[string]*http.Client)
}
