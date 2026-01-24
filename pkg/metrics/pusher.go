// Package metrics provides metrics collection and pushing functionality
// for monitoring system performance via gRPC.
package metrics

import (
	"context"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// MetricsPusher handles pushing metrics to Registry via gRPC
type MetricsPusher struct {
	client     bridge.MetricsServiceClient
	conn       *grpc.ClientConn
	sourceID   string
	sourceType string
	version    string // Optional (for control-plane)
	logger     *logger.Logger
}

// NewMetricsPusher creates a new metrics pusher instance
func NewMetricsPusher(registryAddr, sourceID, sourceType, version string, logger *logger.Logger) (*MetricsPusher, error) {
	conn, err := grpc.NewClient(
		registryAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(1024*1024*10), // 10MB
			grpc.MaxCallSendMsgSize(1024*1024*10), // 10MB
		),
	)
	if err != nil {
		return nil, err
	}

	return &MetricsPusher{
		client:     bridge.NewMetricsServiceClient(conn),
		conn:       conn,
		sourceID:   sourceID,
		sourceType: sourceType,
		version:    version,
		logger:     logger,
	}, nil
}

// Push sends metrics to Registry
func (mp *MetricsPusher) Push(ctx context.Context, metrics []*bridge.Metric) error {
	pushCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()

	req := &bridge.PushMetricsRequest{
		SourceId:   mp.sourceID,
		SourceType: mp.sourceType,
		Version:    mp.version,
		Timestamp:  time.Now().Unix(),
		Metrics:    metrics,
	}

	// Don't wait for response processing
	// If call fails, log and continue (non-blocking)
	_, err := mp.client.PushMetrics(pushCtx, req)
	if err != nil {
		mp.logger.Debugf("Failed to push metrics: %v", err)
		return err
	}

	return nil
}

// StartPeriodicPush starts background goroutine for periodic metric push
func (mp *MetricsPusher) StartPeriodicPush(ctx context.Context, interval time.Duration, collectFunc func() []*bridge.Metric) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	mp.logger.Infof("Starting periodic metrics push (interval: %v)", interval)

	for {
		select {
		case <-ctx.Done():
			mp.logger.Infof("Stopping periodic metrics push")
			return
		case <-ticker.C:
			metrics := collectFunc()
			if len(metrics) == 0 {
				mp.logger.Debugf("No metrics to push")
				continue
			}

			if err := mp.Push(ctx, metrics); err != nil {
				// Error already logged in Push(), continue
				continue
			}
		}
	}
}

// Close closes the gRPC connection
func (mp *MetricsPusher) Close() error {
	if mp.conn != nil {
		return mp.conn.Close()
	}
	return nil
}
