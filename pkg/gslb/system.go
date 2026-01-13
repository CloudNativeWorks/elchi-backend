package gslb

import (
	"context"
	"fmt"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/metrics"
	"go.mongodb.org/mongo-driver/mongo"
)

// System manages the complete GSLB health checking system
// This is the main entry point for starting/stopping all GSLB components
type System struct {
	// Core components
	shardManager    *ShardManager
	ipHealthManager *IPHealthManager
	writeBuffer     *WriteBuffer
	healthChecker   *HealthChecker

	// Metrics (fire-and-forget to Registry)
	metricsPusher *metrics.MetricsPusher

	// Configuration
	controllerID string
	db           *mongo.Database
	logger       *logger.Logger

	// Context for cancellation propagation
	ctx    context.Context
	cancel context.CancelFunc

	// State
	started bool
}

// NewSystem creates a new GSLB system instance
func NewSystem(appContext *db.AppContext, controllerID string) (*System, error) {
	log := logger.NewLogger("gslb/system")

	// Initialize MongoDB indexes
	log.Infof("Initializing GSLB system for controller: %s", controllerID)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create shard documents if they don't exist
	if err := InitializeShards(ctx, appContext.Client, log); err != nil {
		return nil, fmt.Errorf("failed to initialize shards: %w", err)
	}

	// Create shard manager
	shardManager := NewShardManager(appContext, controllerID)

	// Create IP health manager
	ipHealthManager := NewIPHealthManager(appContext.Client, log)

	// Create system context for cancellation propagation
	systemCtx, cancel := context.WithCancel(context.Background())

	// Create write buffer (100 updates/batch, 5s flush interval)
	writeBuffer := NewWriteBuffer(systemCtx, appContext.Client, log, 100, 5*time.Second)

	// Initialize metrics pusher (connects to Registry)
	registryAddr := fmt.Sprintf("%s:%d", appContext.Config.RegistryAddress, appContext.Config.RegistryPort)
	metricsPusher, err := metrics.NewMetricsPusher(
		registryAddr,
		controllerID,        // source_id
		"gslb-health-check", // source_type
		"",                  // version (not applicable for GSLB)
		log,
	)
	if err != nil {
		log.Warnf("Failed to initialize metrics pusher: %v (metrics will not be sent)", err)
		metricsPusher = nil // Continue without metrics
	}

	// Create health checker (integrates all components + metrics pusher)
	healthChecker := NewHealthChecker(appContext, shardManager, ipHealthManager, writeBuffer, metricsPusher, log)

	return &System{
		shardManager:    shardManager,
		ipHealthManager: ipHealthManager,
		writeBuffer:     writeBuffer,
		healthChecker:   healthChecker,
		metricsPusher:   metricsPusher,
		controllerID:    controllerID,
		db:              appContext.Client,
		logger:          log,
		ctx:             systemCtx,
		cancel:          cancel,
		started:         false,
	}, nil
}

// Start starts all GSLB components in the correct order
func (s *System) Start() error {
	if s.started {
		return fmt.Errorf("GSLB system already started")
	}

	s.logger.Infof("🚀 Starting GSLB System for controller: %s", s.controllerID)

	// Start shard manager (acquires shards and starts lease renewal)
	go s.shardManager.Start()

	// Wait for initial shard acquisition with proper synchronization
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := s.shardManager.WaitReady(ctx); err != nil {
		return fmt.Errorf("shard manager failed to initialize: %w", err)
	}

	ownedShards := s.shardManager.GetOwnedShards()
	s.logger.Infof("Shard manager ready with %d logical shards", len(ownedShards))

	// Start health checker if we have shards (starts bucket scheduler and all timer buckets)
	// If initial acquisition returned 0 shards, health checker will wait in standby mode
	if len(ownedShards) > 0 {
		if err := s.healthChecker.Start(); err != nil {
			return fmt.Errorf("failed to start health checker: %w", err)
		}
	} else {
		s.logger.Infof("⏸️  Health checker in standby mode (no shards owned yet, waiting for rebalancing)")
	}

	// Start background goroutine to listen for shard acquisition events
	// When shards are acquired (via rebalancing), restart health checker
	go s.listenForShardAcquisition()

	s.started = true
	s.logger.Infof("✅ GSLB System started successfully")

	return nil
}

// listenForShardAcquisition listens for shard acquisition events from ShardManager
// When shards are acquired (especially after initial 0-shard startup), start health checker
// This is the PERMANENT solution for multi-controller environments where initial acquisition
// may return 0 shards (all owned by other controllers), but rebalancing acquires them later
func (s *System) listenForShardAcquisition() {
	s.logger.Infof("🎧 Listening for shard acquisition events...")

	// Get shard acquisition event channel from ShardManager
	shardAcquiredChan := s.shardManager.GetShardAcquisitionChannel()

	for shardCount := range shardAcquiredChan {
		s.logger.Infof("🔔 Shard acquisition event received: %d shards owned", shardCount)

		// Check if health checker is already running
		if s.healthChecker.IsRunning() {
			s.logger.Debugf("Health checker already running, no restart needed")
			continue
		}

		// Health checker not running - start it now that we have shards
		s.logger.Infof("🚀 Starting health checker (triggered by shard acquisition)")
		if err := s.healthChecker.Start(); err != nil {
			s.logger.Errorf("Failed to start health checker after shard acquisition: %v", err)
			// Don't panic - will retry on next acquisition event
		} else {
			s.logger.Infof("✅ Health checker started successfully after acquiring %d shards", shardCount)
		}
	}

	s.logger.Infof("Shard acquisition listener stopped")
}

// Stop gracefully stops all GSLB components in reverse order with timeout protection
func (s *System) Stop() error {
	if !s.started {
		return nil
	}

	s.logger.Infof("Stopping GSLB System...")

	// Create a timeout context for graceful shutdown (30 seconds max)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	// Cancel system context first to propagate shutdown signal to all components
	s.cancel()

	// Channel to track completion of each component shutdown
	done := make(chan struct{})

	go func() {
		// Stop health checker first (stops probing)
		s.healthChecker.Stop()

		// Flush and stop write buffer (ensure no data loss)
		s.logger.Infof("Flushing write buffer before shutdown...")
		s.writeBuffer.Stop()

		// Stop shard manager (releases shards)
		s.shardManager.Stop()

		close(done)
	}()

	// Wait for graceful shutdown or timeout
	select {
	case <-done:
		s.logger.Infof("✅ GSLB System stopped successfully")
	case <-shutdownCtx.Done():
		s.logger.Errorf("❌ GSLB System shutdown timed out after 30 seconds")
		return fmt.Errorf("shutdown timeout exceeded")
	}

	s.started = false
	return nil
}

// GetIPHealthManager returns the IP health manager for external use
func (s *System) GetIPHealthManager() *IPHealthManager {
	return s.ipHealthManager
}

// GetHealthChecker returns the health checker for monitoring
func (s *System) GetHealthChecker() *HealthChecker {
	return s.healthChecker
}

// GetShardManager returns the shard manager for monitoring
func (s *System) GetShardManager() *ShardManager {
	return s.shardManager
}

// ReloadAllRecords forces immediate reload of all bucket records from database
// Call this after GSLB record create/update/delete operations
func (s *System) ReloadAllRecords() error {
	if !s.started {
		return fmt.Errorf("GSLB system not started")
	}
	return s.healthChecker.ReloadAllRecords()
}

// GetStats returns comprehensive system statistics
type SystemStats struct {
	ControllerID     string
	Started          bool
	OwnedShards      []ShardOwnership
	HealthCheckerStats *HealthCheckerStats
	WriteBufferStats BufferStats
}

// GetStats returns current GSLB system statistics
func (s *System) GetStats() (*SystemStats, error) {
	healthStats, err := s.healthChecker.GetStats()
	if err != nil {
		return nil, fmt.Errorf("failed to get health checker stats: %w", err)
	}

	return &SystemStats{
		ControllerID:       s.controllerID,
		Started:            s.started,
		OwnedShards:        s.shardManager.GetOwnedShards(),
		HealthCheckerStats: healthStats,
		WriteBufferStats:   s.writeBuffer.GetStats(),
	}, nil
}
