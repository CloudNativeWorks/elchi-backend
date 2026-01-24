// Package metrics provides metrics aggregation for the registry service
// collecting and exposing control-plane and controller statistics.
package metrics

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

// MetricEntry represents a single metric data point with metadata
type MetricEntry struct {
	Name      string
	Value     float64
	Labels    map[string]string
	SourceID  string
	Timestamp time.Time
}

// Aggregator collects and stores metrics from all components
type Aggregator struct {
	bridge.UnimplementedMetricsServiceServer
	mu      sync.RWMutex
	metrics map[string]*MetricEntry // key: "metric_name::label1=val1,label2=val2"
	logger  *logger.Logger
}

// NewAggregator creates a new metrics aggregator instance
func NewAggregator(logger *logger.Logger) *Aggregator {
	return &Aggregator{
		metrics: make(map[string]*MetricEntry),
		logger:  logger,
	}
}

// PushMetrics implements MetricsServiceServer (gRPC handler)
// This is the fire-and-forget endpoint where all components push their metrics
func (a *Aggregator) PushMetrics(ctx context.Context, req *bridge.PushMetricsRequest) (*bridge.PushMetricsResponse, error) {
	if req == nil {
		return &bridge.PushMetricsResponse{Accepted: false}, fmt.Errorf("request cannot be nil")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now()

	// Store each metric from the push request
	for _, metric := range req.Metrics {
		// Generate unique key from name + labels
		key := a.generateKey(metric.Name, metric.Labels)

		// Check if this is a counter-like metric (suffix "_total", "_sum", or "_count")
		isCounter := (len(metric.Name) > 6 && metric.Name[len(metric.Name)-6:] == "_total") ||
			(len(metric.Name) > 4 && metric.Name[len(metric.Name)-4:] == "_sum") ||
			(len(metric.Name) > 6 && metric.Name[len(metric.Name)-6:] == "_count")

		if isCounter {
			// Counter/Histogram: accumulate values
			if existing, exists := a.metrics[key]; exists {
				a.metrics[key] = &MetricEntry{
					Name:      metric.Name,
					Value:     existing.Value + metric.Value, // Accumulate
					Labels:    metric.Labels,
					SourceID:  req.SourceId,
					Timestamp: now,
				}
			} else {
				// First time seeing this counter/histogram metric
				a.metrics[key] = &MetricEntry{
					Name:      metric.Name,
					Value:     metric.Value,
					Labels:    metric.Labels,
					SourceID:  req.SourceId,
					Timestamp: now,
				}
			}
		} else {
			// Gauge: replace value
			a.metrics[key] = &MetricEntry{
				Name:      metric.Name,
				Value:     metric.Value,
				Labels:    metric.Labels,
				SourceID:  req.SourceId,
				Timestamp: now,
			}
		}
	}

	a.logger.Debugf("Received %d metrics from %s (%s)", len(req.Metrics), req.SourceId, req.SourceType)

	// Fire-and-forget: Immediate response
	return &bridge.PushMetricsResponse{Accepted: true}, nil
}

// generateKey creates a unique key from metric name and labels
// Format: "metric_name::label1=val1,label2=val2" (sorted labels for consistency)
func (a *Aggregator) generateKey(name string, labels map[string]string) string {
	if len(labels) == 0 {
		return name
	}

	// Sort label keys for consistent key generation
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Build label string
	labelStr := ""
	for i, k := range keys {
		if i > 0 {
			labelStr += ","
		}
		labelStr += fmt.Sprintf("%s=%s", k, labels[k])
	}

	return fmt.Sprintf("%s::%s", name, labelStr)
}

// GetPrometheusMetrics returns all metrics in Prometheus text format
func (a *Aggregator) GetPrometheusMetrics() string {
	a.mu.RLock()
	defer a.mu.RUnlock()

	return a.formatPrometheus(a.metrics)
}

// formatPrometheus converts internal metrics map to Prometheus text format
func (a *Aggregator) formatPrometheus(metrics map[string]*MetricEntry) string {
	if len(metrics) == 0 {
		return "# No metrics available\n"
	}

	// Group metrics by base name (strip _sum/_count suffixes for histograms)
	metricsByBaseName := make(map[string]map[string][]*MetricEntry) // base_name -> metric_name -> entries
	histogramBases := make(map[string]bool)                         // Track which base names are histograms

	for _, entry := range metrics {
		baseName := entry.Name
		metricName := entry.Name

		// Detect histogram pattern (_sum and _count suffixes)
		if len(entry.Name) > 4 && entry.Name[len(entry.Name)-4:] == "_sum" {
			baseName = entry.Name[:len(entry.Name)-4]
			histogramBases[baseName] = true
		} else if len(entry.Name) > 6 && entry.Name[len(entry.Name)-6:] == "_count" {
			baseName = entry.Name[:len(entry.Name)-6]
			histogramBases[baseName] = true
		}

		if metricsByBaseName[baseName] == nil {
			metricsByBaseName[baseName] = make(map[string][]*MetricEntry)
		}
		metricsByBaseName[baseName][metricName] = append(metricsByBaseName[baseName][metricName], entry)
	}

	// Sort base names for consistent output
	baseNames := make([]string, 0, len(metricsByBaseName))
	for baseName := range metricsByBaseName {
		baseNames = append(baseNames, baseName)
	}
	sort.Strings(baseNames)

	// Build Prometheus text format output
	output := ""
	for _, baseName := range baseNames {
		metricsForBase := metricsByBaseName[baseName]

		// Determine metric type
		isHistogram := histogramBases[baseName]
		var metricType string
		switch {
		case isHistogram:
			metricType = "histogram"
		case len(baseName) > 6 && baseName[len(baseName)-6:] == "_total":
			metricType = "counter"
		default:
			metricType = "gauge"
		}

		// Add TYPE comment
		output += fmt.Sprintf("# TYPE %s %s\n", baseName, metricType)

		// Output all metrics for this base name
		for metricName, entries := range metricsForBase {
			for _, entry := range entries {
				if len(entry.Labels) == 0 {
					output += fmt.Sprintf("%s %f\n", metricName, entry.Value)
				} else {
					// Format labels
					labelStr := ""
					keys := make([]string, 0, len(entry.Labels))
					for k := range entry.Labels {
						keys = append(keys, k)
					}
					sort.Strings(keys)

					for i, k := range keys {
						if i > 0 {
							labelStr += ","
						}
						labelStr += fmt.Sprintf("%s=\"%s\"", k, entry.Labels[k])
					}

					output += fmt.Sprintf("%s{%s} %f\n", metricName, labelStr, entry.Value)
				}
			}
		}

		output += "\n"
	}

	return output
}

// CleanupStaleMetrics removes metrics older than the specified duration
func (a *Aggregator) CleanupStaleMetrics(maxAge time.Duration) int {
	a.mu.Lock()
	defer a.mu.Unlock()

	threshold := time.Now().Add(-maxAge)
	removedCount := 0

	for key, entry := range a.metrics {
		if entry.Timestamp.Before(threshold) {
			delete(a.metrics, key)
			removedCount++
		}
	}

	if removedCount > 0 {
		a.logger.Debugf("Cleaned up %d stale metrics (older than %v)", removedCount, maxAge)
	}

	return removedCount
}

// GetMetricCount returns the current number of stored metrics
func (a *Aggregator) GetMetricCount() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.metrics)
}
