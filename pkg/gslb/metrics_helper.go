package gslb

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// startMetricsPusher starts periodic system-level metrics reporting (every 30s)
// Runs in background, reports aggregated stats across all buckets
func (hc *HealthChecker) startMetricsPusher() {
	if hc.metricsPusher == nil {
		hc.logger.Debug("Metrics pusher not configured, skipping periodic metrics")
		return
	}

	ticker := time.NewTicker(30 * time.Second)
	go func() {
		defer ticker.Stop()
		hc.logger.Info("📊 System metrics pusher started (30s interval)")

		for {
			select {
			case <-ticker.C:
				hc.pushSystemMetrics()
			case <-hc.ctx.Done():
				hc.logger.Info("System metrics pusher stopped")
				return
			}
		}
	}()
}

// pushSystemMetrics pushes aggregated system-level metrics to Registry
// Called every 30 seconds - reports overall GSLB health
func (hc *HealthChecker) pushSystemMetrics() {
	if hc.metricsPusher == nil {
		return
	}

	stats, err := hc.GetStats()
	if err != nil {
		hc.logger.Debugf("Failed to get stats for metrics: %v", err)
		return
	}

	metricsToSend := []*bridge.Metric{
		// Shard ownership
		{
			Name:  "elchi_gslb_owned_shards",
			Value: float64(stats.OwnedShards),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
		// IP health states
		{
			Name:  "elchi_gslb_total_ips",
			Value: float64(stats.TotalIPs),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
		{
			Name:  "elchi_gslb_healthy_ips",
			Value: float64(stats.HealthyIPs),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
				"state":      "passing",
			},
		},
		{
			Name:  "elchi_gslb_warning_ips",
			Value: float64(stats.WarningIPs),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
				"state":      "warning",
			},
		},
		{
			Name:  "elchi_gslb_critical_ips",
			Value: float64(stats.CriticalIPs),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
				"state":      "critical",
			},
		},
		{
			Name:  "elchi_gslb_backoff_active_ips",
			Value: float64(stats.BackoffActiveIPs),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
		// Aggregated bucket metrics
		{
			Name:  "elchi_gslb_total_buckets",
			Value: float64(stats.BucketStats.TotalBuckets),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
		{
			Name:  "elchi_gslb_total_records",
			Value: float64(stats.BucketStats.TotalRecords),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
		{
			Name:  "elchi_gslb_total_workers",
			Value: float64(stats.BucketStats.TotalWorkers),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
		// Result queue metrics
		{
			Name:  "elchi_gslb_result_queue_depth",
			Value: float64(stats.ResultQueueDepth),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
		{
			Name: "elchi_gslb_result_queue_capacity_pct",
			Value: func() float64 {
				if stats.ResultQueueCap > 0 {
					return float64(stats.ResultQueueDepth) / float64(stats.ResultQueueCap) * 100
				}
				return 0.0
			}(),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
		// Write buffer metrics
		{
			Name:  "elchi_gslb_write_buffer_size",
			Value: float64(stats.WriteBufferStats.CurrentSize),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
		{
			Name:  "elchi_gslb_write_buffer_capacity_pct",
			Value: stats.WriteBufferStats.Capacity,
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
		{
			Name:  "elchi_gslb_write_buffer_flush_total",
			Value: float64(stats.WriteBufferStats.FlushCount),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
		{
			Name:  "elchi_gslb_write_buffer_updates_total",
			Value: float64(stats.WriteBufferStats.TotalUpdates),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
		{
			Name:  "elchi_gslb_write_buffer_flush_errors_total",
			Value: float64(stats.WriteBufferStats.FlushErrors),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
		{
			Name:  "elchi_gslb_write_buffer_avg_flush_duration_seconds",
			Value: stats.WriteBufferStats.AvgFlushDuration,
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
		// Probe success/failure metrics (HIGH PRIORITY)
		{
			Name:  "elchi_gslb_probes_total",
			Value: float64(atomic.LoadInt64(&hc.probeSuccessCount)),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
				"result":     "success",
			},
		},
		{
			Name:  "elchi_gslb_probes_total",
			Value: float64(atomic.LoadInt64(&hc.probeFailureCount)),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
				"result":     "failure",
			},
		},
		// Probe success rate (SLI metric)
		{
			Name:  "elchi_gslb_probe_success_rate_percent",
			Value: hc.calculateProbeSuccessRate(),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
		// Probe latency metrics (HIGH PRIORITY)
		{
			Name:  "elchi_gslb_probe_latency_avg_seconds",
			Value: hc.calculateAvgProbeLatency(),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
		{
			Name:  "elchi_gslb_probe_latency_min_seconds",
			Value: hc.getMinProbeLatency(),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
		{
			Name:  "elchi_gslb_probe_latency_max_seconds",
			Value: hc.getMaxProbeLatency(),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
	}

	// Add error type metrics
	hc.probeErrorCounts.Range(func(key, value any) bool {
		errorType := key.(string)
		count := atomic.LoadInt64(value.(*int64))

		metricsToSend = append(metricsToSend, &bridge.Metric{
			Name:  "elchi_gslb_probe_errors_total",
			Value: float64(count),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
				"error_type": errorType,
			},
		})
		return true
	})

	// Fire-and-forget push
	go func() {
		pushCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = hc.metricsPusher.Push(pushCtx, metricsToSend)
	}()
}

// calculateProbeSuccessRate calculates probe success rate percentage (0-100)
func (hc *HealthChecker) calculateProbeSuccessRate() float64 {
	successCount := atomic.LoadInt64(&hc.probeSuccessCount)
	failureCount := atomic.LoadInt64(&hc.probeFailureCount)
	totalProbes := successCount + failureCount

	if totalProbes == 0 {
		return 100.0 // No probes yet = 100% (optimistic default)
	}

	return float64(successCount) / float64(totalProbes) * 100.0
}

// calculateAvgProbeLatency calculates average probe latency in seconds
func (hc *HealthChecker) calculateAvgProbeLatency() float64 {
	sum := atomic.LoadInt64(&hc.probeLatencySum)
	count := atomic.LoadInt64(&hc.probeLatencyCount)

	if count == 0 {
		return 0.0
	}

	// Convert microseconds to seconds
	avgMicros := float64(sum) / float64(count)
	return avgMicros / 1_000_000.0
}

// getMinProbeLatency returns minimum probe latency in seconds
func (hc *HealthChecker) getMinProbeLatency() float64 {
	hc.probeLatencyMu.Lock()
	minMicros := hc.probeLatencyMin
	hc.probeLatencyMu.Unlock()

	// Convert microseconds to seconds
	return float64(minMicros) / 1_000_000.0
}

// getMaxProbeLatency returns maximum probe latency in seconds
func (hc *HealthChecker) getMaxProbeLatency() float64 {
	hc.probeLatencyMu.Lock()
	maxMicros := hc.probeLatencyMax
	hc.probeLatencyMu.Unlock()

	// Convert microseconds to seconds
	return float64(maxMicros) / 1_000_000.0
}

// pushBucketCycleMetrics pushes per-bucket cycle metrics to Registry (fire-and-forget)
// Called at the end of each bucket cycle - high frequency, per-bucket granularity
func (tb *TimerBucket) pushBucketCycleMetrics(
	recordCount int,
	totalIPs int,
	probedIPs int,
	skippedIPs int,
	cycleLatency time.Duration,
	completionPercent float64,
) {
	// Skip if no metrics pusher or controller ID available
	if tb.metricsPusher == nil || tb.controllerID == "" {
		return
	}

	workerStats := tb.workerPool.GetStats()

	metricsToSend := []*bridge.Metric{
		// Bucket cycle metrics
		{
			Name:  "elchi_gslb_bucket_cycle_latency_seconds",
			Value: cycleLatency.Seconds(),
			Labels: map[string]string{
				"controller":  tb.controllerID,
				"interval":    formatInterval(tb.interval),
				"bucket_type": string(tb.bucketType),
			},
		},
		{
			Name:  "elchi_gslb_bucket_completion_percent",
			Value: completionPercent,
			Labels: map[string]string{
				"controller": tb.controllerID,
				"interval":   formatInterval(tb.interval),
			},
		},
		// Bucket record/IP metrics
		{
			Name:  "elchi_gslb_bucket_records",
			Value: float64(recordCount),
			Labels: map[string]string{
				"controller": tb.controllerID,
				"interval":   formatInterval(tb.interval),
			},
		},
		{
			Name:  "elchi_gslb_bucket_total_ips",
			Value: float64(totalIPs),
			Labels: map[string]string{
				"controller": tb.controllerID,
				"interval":   formatInterval(tb.interval),
			},
		},
		{
			Name:  "elchi_gslb_bucket_probed_ips",
			Value: float64(probedIPs),
			Labels: map[string]string{
				"controller": tb.controllerID,
				"interval":   formatInterval(tb.interval),
			},
		},
		{
			Name:  "elchi_gslb_bucket_skipped_ips",
			Value: float64(skippedIPs),
			Labels: map[string]string{
				"controller": tb.controllerID,
				"interval":   formatInterval(tb.interval),
			},
		},
		// Worker pool metrics
		{
			Name:  "elchi_gslb_bucket_workers_active",
			Value: float64(workerStats.CurrentWorkers),
			Labels: map[string]string{
				"controller": tb.controllerID,
				"interval":   formatInterval(tb.interval),
			},
		},
		{
			Name:  "elchi_gslb_bucket_queue_depth",
			Value: float64(workerStats.QueueDepth),
			Labels: map[string]string{
				"controller": tb.controllerID,
				"interval":   formatInterval(tb.interval),
			},
		},
		{
			Name: "elchi_gslb_bucket_queue_capacity_pct",
			Value: func() float64 {
				if workerStats.QueueCapacity > 0 {
					return float64(workerStats.QueueDepth) / float64(workerStats.QueueCapacity) * 100
				}
				return 0.0
			}(),
			Labels: map[string]string{
				"controller": tb.controllerID,
				"interval":   formatInterval(tb.interval),
			},
		},
	}

	// Fire-and-forget push
	go func() {
		pushCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		_ = tb.metricsPusher.Push(pushCtx, metricsToSend)
	}()
}

// pushStateTransitionMetric pushes a state transition event to Registry (fire-and-forget)
// Called on every state change - captures transition events for alerting
func (hc *HealthChecker) pushStateTransitionMetric(oldState, newState models.HealthState) {
	if hc.metricsPusher == nil {
		return
	}

	metric := &bridge.Metric{
		Name:  "elchi_gslb_state_transitions_total",
		Value: 1.0, // Counter increment
		Labels: map[string]string{
			"controller": hc.shardManager.controllerID,
			"from_state": oldState.String(),
			"to_state":   newState.String(),
		},
	}

	// Fire-and-forget push
	go func() {
		pushCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		_ = hc.metricsPusher.Push(pushCtx, []*bridge.Metric{metric})
	}()
}

// formatInterval converts interval (seconds) to readable string for labels
// CRITICAL FIX (Bug #7): Always use seconds to prevent label collision
// Problem: 90/60 = 1 (integer division) caused 90s and 60s to share "1m" label
// Solution: Always use "%ds" format for all intervals
func formatInterval(interval int) string {
	return fmt.Sprintf("%ds", interval)
}
