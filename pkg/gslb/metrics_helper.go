package gslb

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// startMetricsPusher starts periodic system-level metrics reporting (every 30s)
// Runs in background, reports aggregated stats across Time Wheel
func (hc *HealthChecker) startMetricsPusher() {
	if hc.metricsPusher == nil {
		hc.logger.Debug("Metrics pusher not configured, skipping periodic metrics")
		return
	}

	hc.wg.Add(1)
	ticker := time.NewTicker(30 * time.Second)
	go func() {
		defer hc.wg.Done()
		defer ticker.Stop()
		hc.logger.Info("System metrics pusher started (30s interval)")

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
		// Time Wheel metrics
		{
			Name:  "elchi_gslb_timewheel_current_slot",
			Value: float64(stats.TimeWheelStats.CurrentSlot),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
		{
			Name:  "elchi_gslb_timewheel_scheduled_total",
			Value: float64(stats.TimeWheelStats.Scheduled),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
		{
			Name:  "elchi_gslb_timewheel_executed_total",
			Value: float64(stats.TimeWheelStats.Executed),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
		{
			Name:  "elchi_gslb_timewheel_current_load",
			Value: float64(stats.TimeWheelStats.CurrentLoad),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
		// Worker Pool metrics
		{
			Name:  "elchi_gslb_workers_current",
			Value: float64(stats.WorkerPoolStats.CurrentWorkers),
			Labels: map[string]string{
				"controller": hc.shardManager.controllerID,
			},
		},
		{
			Name:  "elchi_gslb_workers_queue_depth",
			Value: float64(stats.WorkerPoolStats.QueueDepth),
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
	minMicros := atomic.LoadInt64(&hc.probeLatencyMin)
	// Convert microseconds to seconds
	return float64(minMicros) / 1_000_000.0
}

// getMaxProbeLatency returns maximum probe latency in seconds
func (hc *HealthChecker) getMaxProbeLatency() float64 {
	maxMicros := atomic.LoadInt64(&hc.probeLatencyMax)
	// Convert microseconds to seconds
	return float64(maxMicros) / 1_000_000.0
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
