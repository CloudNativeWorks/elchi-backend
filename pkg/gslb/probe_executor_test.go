package gslb

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TestNewDefaultProbeExecutor tests probe executor creation
func TestNewDefaultProbeExecutor(t *testing.T) {
	log := setupTestLogger(t)

	executor := NewDefaultProbeExecutor(log)
	defer executor.Close()

	assert.NotNil(t, executor, "Executor should be created")
	assert.NotNil(t, executor.httpClient, "HTTP client should be configured")
	assert.NotNil(t, executor.logger, "Logger should be set")
}

// TestExecuteProbe_NilIPHealth tests nil ipHealth handling
func TestExecuteProbe_NilIPHealth(t *testing.T) {
	log := setupTestLogger(t)
	executor := NewDefaultProbeExecutor(log)
	defer executor.Close()

	ctx := context.Background()
	probe := &models.GSLBProbe{
		Type: "http",
		Port: 80,
		Path: "/",
	}

	result := executor.ExecuteProbe(ctx, nil, probe)

	assert.False(t, result.Success, "Probe should fail with nil ipHealth")
	assert.NotNil(t, result.Error, "Error should be set")
	assert.Contains(t, result.Error.Error(), "ipHealth is nil", "Error message should mention nil ipHealth")
}

// TestExecuteProbe_NilProbe tests nil probe handling
func TestExecuteProbe_NilProbe(t *testing.T) {
	log := setupTestLogger(t)
	executor := NewDefaultProbeExecutor(log)
	defer executor.Close()

	ctx := context.Background()
	ipHealth := &models.GSLBIPHealth{
		RecordID: primitive.NewObjectID(),
		IP:       "1.2.3.4",
	}

	result := executor.ExecuteProbe(ctx, ipHealth, nil)

	assert.False(t, result.Success, "Probe should fail with nil probe")
	assert.NotNil(t, result.Error, "Error should be set")
	assert.Contains(t, result.Error.Error(), "probe config is nil", "Error message should mention nil probe")
	assert.Equal(t, "1.2.3.4", result.IP, "IP should be set from ipHealth")
}

// TestExecuteProbe_UnsupportedType tests unsupported probe type
func TestExecuteProbe_UnsupportedType(t *testing.T) {
	log := setupTestLogger(t)
	executor := NewDefaultProbeExecutor(log)
	defer executor.Close()

	ctx := context.Background()
	ipHealth := &models.GSLBIPHealth{
		RecordID: primitive.NewObjectID(),
		IP:       "1.2.3.4",
	}
	probe := &models.GSLBProbe{
		Type: "grpc", // Unsupported type
		Port: 50051,
	}

	result := executor.ExecuteProbe(ctx, ipHealth, probe)

	assert.False(t, result.Success, "Probe should fail with unsupported type")
	assert.NotNil(t, result.Error, "Error should be set")
	assert.Contains(t, result.Error.Error(), "unsupported probe type", "Error should mention unsupported type")
}

// TestExecuteHTTPProbe_Success tests successful HTTP probe
func TestExecuteHTTPProbe_Success(t *testing.T) {
	log := setupTestLogger(t)
	executor := NewDefaultProbeExecutor(log)
	defer executor.Close()

	// Create test HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/health", r.URL.Path, "Request path should match")
		assert.Equal(t, "elchi-gslb-health-checker/1.0", r.UserAgent(), "User-Agent should be set")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}))
	defer server.Close()

	// Parse server address to get IP and port
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())

	ctx := context.Background()
	ipHealth := &models.GSLBIPHealth{
		RecordID: primitive.NewObjectID(),
		IP:       "127.0.0.1",
	}
	probe := &models.GSLBProbe{
		Type:                "http",
		Port:                mustParseInt(port),
		Path:                "/health",
		Timeout:             5.0,
		ExpectedStatusCodes: []string{"200"}, // Only accept 200
	}

	result := executor.ExecuteProbe(ctx, ipHealth, probe)

	assert.True(t, result.Success, "HTTP probe should succeed")
	assert.Equal(t, 200, result.ResponseCode, "Response code should be 200")
	assert.Nil(t, result.Error, "Error should be nil")
	assert.Greater(t, result.ResponseTime, 0.0, "Response time should be measured")
	assert.Equal(t, probe, result.Probe, "Probe config should be in result")
}

// TestExecuteHTTPProbe_UnexpectedStatusCode tests HTTP probe with unexpected status code
func TestExecuteHTTPProbe_UnexpectedStatusCode(t *testing.T) {
	log := setupTestLogger(t)
	executor := NewDefaultProbeExecutor(log)
	defer executor.Close()

	// Create test HTTP server returning 404
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Not Found"))
	}))
	defer server.Close()

	// Parse server address
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())

	ctx := context.Background()
	ipHealth := &models.GSLBIPHealth{
		RecordID: primitive.NewObjectID(),
		IP:       "127.0.0.1",
	}
	probe := &models.GSLBProbe{
		Type:                "http",
		Port:                mustParseInt(port),
		Path:                "/",
		Timeout:             5.0,
		ExpectedStatusCodes: []string{"200"}, // Expect 200
	}

	result := executor.ExecuteProbe(ctx, ipHealth, probe)

	assert.False(t, result.Success, "HTTP probe should fail with unexpected status code")
	assert.Equal(t, 404, result.ResponseCode, "Response code should be 404")
	assert.NotNil(t, result.Error, "Error should be set")
	assert.Contains(t, result.Error.Error(), "unexpected status code 404", "Error should mention status code")
}

// TestExecuteHTTPProbe_WithHostHeader tests HTTP probe with custom Host header
func TestExecuteHTTPProbe_WithHostHeader(t *testing.T) {
	log := setupTestLogger(t)
	executor := NewDefaultProbeExecutor(log)
	defer executor.Close()

	// Create test HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "example.com", r.Host, "Host header should be set")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Parse server address
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())

	ctx := context.Background()
	ipHealth := &models.GSLBIPHealth{
		RecordID: primitive.NewObjectID(),
		IP:       "127.0.0.1",
	}
	probe := &models.GSLBProbe{
		Type:                "http",
		Port:                mustParseInt(port),
		Path:                "/",
		Timeout:             5.0,
		ExpectedStatusCodes: []string{"200"},
		HostHeader:          "example.com", // Custom Host header
	}

	result := executor.ExecuteProbe(ctx, ipHealth, probe)

	assert.True(t, result.Success, "HTTP probe should succeed with Host header")
	assert.Equal(t, 200, result.ResponseCode, "Response code should be 200")
}

// TestExecuteHTTPProbe_ContextCancellation tests context cancellation during HTTP probe
func TestExecuteHTTPProbe_ContextCancellation(t *testing.T) {
	log := setupTestLogger(t)
	executor := NewDefaultProbeExecutor(log)
	defer executor.Close()

	// Create test HTTP server with delay
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second) // Delay to allow cancellation
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Parse server address
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())

	// Create context with short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	ipHealth := &models.GSLBIPHealth{
		RecordID: primitive.NewObjectID(),
		IP:       "127.0.0.1",
	}
	probe := &models.GSLBProbe{
		Type:                "http",
		Port:                mustParseInt(port),
		Path:                "/",
		Timeout:             5.0,
		ExpectedStatusCodes: []string{"200"},
	}

	result := executor.ExecuteProbe(ctx, ipHealth, probe)

	assert.False(t, result.Success, "HTTP probe should fail on context cancellation")
	assert.NotNil(t, result.Error, "Error should be set")
}

// TestExecuteTCPProbe_Success tests successful TCP probe
func TestExecuteTCPProbe_Success(t *testing.T) {
	log := setupTestLogger(t)
	executor := NewDefaultProbeExecutor(log)
	defer executor.Close()

	// Create test TCP server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err, "Should create TCP listener")
	defer listener.Close()

	// Accept connections in background
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	// Parse listener address
	_, port, _ := net.SplitHostPort(listener.Addr().String())

	ctx := context.Background()
	ipHealth := &models.GSLBIPHealth{
		RecordID: primitive.NewObjectID(),
		IP:       "127.0.0.1",
	}
	probe := &models.GSLBProbe{
		Type:    "tcp",
		Port:    mustParseInt(port),
		Timeout: 5.0,
	}

	result := executor.ExecuteProbe(ctx, ipHealth, probe)

	assert.True(t, result.Success, "TCP probe should succeed")
	assert.Equal(t, 200, result.ResponseCode, "TCP success should use code 200")
	assert.Nil(t, result.Error, "Error should be nil")
	assert.Greater(t, result.ResponseTime, 0.0, "Response time should be measured")
}

// TestExecuteTCPProbe_ConnectionRefused tests TCP probe with connection refused
func TestExecuteTCPProbe_ConnectionRefused(t *testing.T) {
	log := setupTestLogger(t)
	executor := NewDefaultProbeExecutor(log)
	defer executor.Close()

	ctx := context.Background()
	ipHealth := &models.GSLBIPHealth{
		RecordID: primitive.NewObjectID(),
		IP:       "127.0.0.1",
	}
	probe := &models.GSLBProbe{
		Type:    "tcp",
		Port:    1, // Port 1 should be closed
		Timeout: 1.0,
	}

	result := executor.ExecuteProbe(ctx, ipHealth, probe)

	assert.False(t, result.Success, "TCP probe should fail with connection refused")
	assert.Equal(t, 0, result.ResponseCode, "Response code should be 0 on failure")
	assert.NotNil(t, result.Error, "Error should be set")
	assert.Contains(t, result.Error.Error(), "TCP connection failed", "Error should mention TCP failure")
}

// TestExecuteTCPProbe_ContextCancellation tests context cancellation during TCP probe
func TestExecuteTCPProbe_ContextCancellation(t *testing.T) {
	log := setupTestLogger(t)
	executor := NewDefaultProbeExecutor(log)
	defer executor.Close()

	// Create context with immediate cancellation
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	ipHealth := &models.GSLBIPHealth{
		RecordID: primitive.NewObjectID(),
		IP:       "127.0.0.1",
	}
	probe := &models.GSLBProbe{
		Type:    "tcp",
		Port:    80,
		Timeout: 5.0,
	}

	result := executor.ExecuteProbe(ctx, ipHealth, probe)

	assert.False(t, result.Success, "TCP probe should fail on context cancellation")
	assert.NotNil(t, result.Error, "Error should be set")
}

// TestExecuteHTTPProbe_DefaultPath tests HTTP probe with empty path (should default to "/")
func TestExecuteHTTPProbe_DefaultPath(t *testing.T) {
	log := setupTestLogger(t)
	executor := NewDefaultProbeExecutor(log)
	defer executor.Close()

	// Create test HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/", r.URL.Path, "Empty path should default to /")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Parse server address
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())

	ctx := context.Background()
	ipHealth := &models.GSLBIPHealth{
		RecordID: primitive.NewObjectID(),
		IP:       "127.0.0.1",
	}
	probe := &models.GSLBProbe{
		Type:                "http",
		Port:                mustParseInt(port),
		Path:                "", // Empty path - should default to "/"
		Timeout:             5.0,
		ExpectedStatusCodes: []string{"200"},
	}

	result := executor.ExecuteProbe(ctx, ipHealth, probe)

	assert.True(t, result.Success, "HTTP probe should succeed with default path")
}

// TestClose tests executor cleanup
func TestClose(t *testing.T) {
	log := setupTestLogger(t)
	executor := NewDefaultProbeExecutor(log)

	// Close should not panic
	executor.Close()
}

// Helper function to parse port string to int
func mustParseInt(s string) int {
	var port int
	_, err := fmt.Sscanf(s, "%d", &port)
	if err != nil {
		panic(err)
	}
	return port
}
