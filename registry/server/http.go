package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/registry/metrics"
)

// HTTPMetricsServer serves Prometheus metrics over HTTP
type HTTPMetricsServer struct {
	aggregator *metrics.Aggregator
	port       int
	logger     *logger.Logger
	server     *http.Server
}

// NewHTTPMetricsServer creates a new HTTP server for Prometheus metrics
func NewHTTPMetricsServer(agg *metrics.Aggregator, port int, logger *logger.Logger) *HTTPMetricsServer {
	return &HTTPMetricsServer{
		aggregator: agg,
		port:       port,
		logger:     logger,
	}
}

// Start starts the HTTP metrics server
func (s *HTTPMetricsServer) Start() error {
	mux := http.NewServeMux()

	// Prometheus scrape endpoint
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.WriteHeader(http.StatusOK)

		metricsOutput := s.aggregator.GetPrometheusMetrics()
		if _, err := w.Write([]byte(metricsOutput)); err != nil {
			s.logger.Errorf("Failed to write metrics response: %v", err)
		}

		s.logger.Debugf("Served metrics endpoint: %d metrics", s.aggregator.GetMetricCount())
	})

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{"status":"healthy","service":"registry-metrics"}`)); err != nil {
			s.logger.Errorf("Failed to write health check response: %v", err)
		}
	})

	// Metrics statistics endpoint (for debugging)
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		stats := fmt.Sprintf(`{"metric_count":%d,"service":"registry-metrics"}`, s.aggregator.GetMetricCount())
		if _, err := w.Write([]byte(stats)); err != nil {
			s.logger.Errorf("Failed to write stats response: %v", err)
		}
	})

	s.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", s.port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	s.logger.Infof("HTTP metrics server starting on port %d", s.port)
	s.logger.Infof("Prometheus endpoint: http://:%d/metrics", s.port)

	return s.server.ListenAndServe()
}

// Stop gracefully shuts down the HTTP server
func (s *HTTPMetricsServer) Stop(ctx context.Context) error {
	if s.server == nil {
		return nil
	}

	s.logger.Infof("Shutting down HTTP metrics server...")

	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := s.server.Shutdown(shutdownCtx); err != nil {
		s.logger.Errorf("HTTP metrics server shutdown error: %v", err)
		return err
	}

	s.logger.Infof("HTTP metrics server stopped")
	return nil
}
