// Package metrics provides metrics collection functionality
// for monitoring controller and client statistics.
package metrics

import (
	"context"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

// MetricsPusher interface for pushing metrics to Registry
type MetricsPusher interface {
	Push(ctx context.Context, metrics []*bridge.Metric) error
}

// Collector collects and immediately pushes metrics from Controller
type Collector struct {
	db           *db.AppContext
	controllerID string
	namespace    string
	pusher       MetricsPusher
	logger       *logger.Logger
}

// NewCollector creates a new controller metrics collector
func NewCollector(appContext *db.AppContext, controllerID, namespace string, pusher MetricsPusher) *Collector {
	return &Collector{
		db:           appContext,
		controllerID: controllerID,
		namespace:    namespace,
		pusher:       pusher,
		logger:       logger.NewLogger("controller/metrics"),
	}
}

// RecordHealthCheck records and immediately pushes a health check metric
func (c *Collector) RecordHealthCheck(shardID string, success bool, duration time.Duration) {
	if c.pusher == nil {
		return
	}

	result := "failure"
	if success {
		result = "success"
	}

	metrics := []*bridge.Metric{
		{
			Name:  "elchi_gslb_health_checks_total",
			Value: 1.0, // Increment counter
			Labels: map[string]string{
				"shard_id":      shardID,
				"result":        result,
				"controller_id": c.controllerID,
			},
		},
		{
			Name:  "elchi_gslb_health_check_duration_seconds_sum",
			Value: duration.Seconds(), // Sum of durations (will be accumulated)
			Labels: map[string]string{
				"shard_id":      shardID,
				"controller_id": c.controllerID,
			},
		},
		{
			Name:  "elchi_gslb_health_check_duration_seconds_count",
			Value: 1.0, // Count of checks (will be accumulated)
			Labels: map[string]string{
				"shard_id":      shardID,
				"controller_id": c.controllerID,
			},
		},
	}

	// Fire-and-forget push to Registry
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	if err := c.pusher.Push(ctx, metrics); err != nil {
		// Log error but don't block (fire-and-forget)
		c.logger.Debugf("Failed to push health check metrics: %v", err)
	}
}

// RecordStatusTransition records and immediately pushes a status transition metric
func (c *Collector) RecordStatusTransition(fqdn, ip, from, to string) {
	if c.pusher == nil {
		return
	}

	metrics := []*bridge.Metric{
		{
			Name:  "elchi_gslb_status_transitions_total",
			Value: 1.0, // Increment counter
			Labels: map[string]string{
				"from":          from,
				"to":            to,
				"controller_id": c.controllerID,
			},
		},
	}

	// Fire-and-forget push to Registry
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	if err := c.pusher.Push(ctx, metrics); err != nil {
		// Log error but don't block (fire-and-forget)
		c.logger.Debugf("Failed to push status transition metrics: %v", err)
	}
}
