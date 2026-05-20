package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/registry/leader"
	"github.com/CloudNativeWorks/elchi-backend/registry/metrics"
	"github.com/CloudNativeWorks/elchi-backend/registry/server"
	"github.com/CloudNativeWorks/elchi-backend/registry/service"
	"github.com/CloudNativeWorks/elchi-backend/registry/snapshot"
	"github.com/CloudNativeWorks/elchi-backend/registry/storage"
)

// parseDurationOr parses s as a Go duration; returns d on empty/invalid input.
func parseDurationOr(s string, d time.Duration) time.Duration {
	if s == "" {
		return d
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return d
	}
	return v
}

var registryPort uint

// registryCmd represents the command for starting the registry service.
// It initializes the registry server, sets up the necessary services, and starts listening for incoming gRPC requests.
// Parameters:
// - none
// Returns:
// - *cobra.Command: a Cobra command instance for the registry server
var registryCmd = &cobra.Command{
	Use:   "elchi-registry",
	Short: "Start Elchi Registry Service",
	Long:  `Start Elchi Registry Service`,
	Run: func(cmd *cobra.Command, _ []string) {
		appConfig := config.Read(cfgFile)

		// Initialize logger with default config
		if err := logger.Init(logger.Config{
			Level:      appConfig.Logging.Level,
			Format:     appConfig.Logging.Format,
			OutputPath: appConfig.Logging.OutputPath,
			Module:     "registry",
		}); err != nil {
			log.Fatalf("Fatal: Logger could not be initialized: %v", err)
		}

		// Port resolution: --port flag > REGISTRY_PORT in config > default 9090
		if !cmd.Flags().Changed("port") && appConfig.RegistryPort > 0 {
			registryPort = appConfig.RegistryPort
		}

		rootLogger := logger.NewLogger("registry")
		rootLogger.Infof("Starting Elchi Registry Service on port %d", registryPort)

		// Combine RegistryAddress and RegistryPort
		registryAddress := "0.0.0.0"
		fullAddress := fmt.Sprintf("%s:%d", registryAddress, registryPort)

		// MongoDB is required for HA (leader election + state snapshot).
		// In single-node deployments this is still safe — the registry will
		// just acquire its own lock and run as the sole leader.
		rootLogger.Info("Connecting to MongoDB for leader election + snapshot...")
		appContext := db.NewMongoDB(appConfig, true)

		// Initialize in-memory storage
		rootLogger.Info("Initializing in-memory storage...")
		controllerStorage := storage.NewInMemoryStorage()          // For controller routing
		controlPlaneStorage := storage.NewInMemoryRoutingStorage() // For control-plane routing

		// Initialize services
		rootLogger.Info("Initializing controller routing service...")
		controllerRoutingService := service.NewControllerRoutingService(controllerStorage, rootLogger)

		rootLogger.Info("Initializing control-plane routing service...")
		controlPlaneRoutingService := service.NewRoutingService(controlPlaneStorage, rootLogger)

		// Initialize metrics aggregator
		rootLogger.Info("Initializing metrics aggregator...")
		metricsLogger := logger.NewLogger("registry/metrics")
		metricsAggregator := metrics.NewAggregator(metricsLogger)

		// Leader election + snapshot mirror.
		leaderLogger := logger.NewLogger("registry/leader")
		ttl := parseDurationOr(appConfig.RegistryLeaderLockTTL, leader.DefaultTTL)
		renew := parseDurationOr(appConfig.RegistryLeaderRenewalInterval, leader.DefaultRenewInterval)
		election := leader.New(appContext.Client, ttl, renew, leaderLogger)

		snapshotLogger := logger.NewLogger("registry/snapshot")
		snapshotter := snapshot.New(appContext.Client, controllerStorage, controlPlaneStorage, election.HolderID(), snapshotLogger)
		writeInterval := parseDurationOr(appConfig.RegistrySnapshotInterval, snapshot.DefaultWriteInterval)
		pollInterval := parseDurationOr(appConfig.RegistrySnapshotPollInterval, snapshot.DefaultPollInterval)

		// signal.NotifyContext wires SIGINT/SIGTERM into ctx cancellation.
		// Both `election.Run` and `StartGRPCServer` watch this ctx and run
		// their graceful-shutdown paths (lock release, Mongo snapshot save,
		// gRPC GracefulStop) before the process exits. Without this Go's
		// default signal behaviour terminates the process immediately,
		// deferred releases never fire, and a restarted registry sits as
		// standby for the full TTL window (~30s) rejecting RPCs.
		electionCtx, electionCancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer electionCancel()

		// Wire leadership transitions: leader writes snapshots, standby reads them.
		// Health status is flipped by registry/server.NewHealthServer (registered below).
		//
		// IMPORTANT — callback runs OFF the election loop goroutine (via `go`).
		// `leader.go:104` documents "Callbacks must not block. They run in the
		// election loop goroutine." Snapshotter.Load and StartReader's initial
		// Load both do blocking Mongo FindOne; running them inline would stall
		// `tickOnce`, push the next `renewInterval` past `ttl`, and cause the
		// leader to lose its lease whenever Mongo lags (e.g. PRIMARY election
		// during a rolling upgrade). Detaching to a goroutine keeps the
		// election renewal cadence independent of Mongo latency.
		//
		// Ordering note: StopReader/StopWriter are non-blocking (close a chan),
		// so the order inside the goroutine is preserved. The election loop
		// itself only mutates atomic state + fires callbacks, so concurrent
		// callbacks from rapid back-to-back transitions are safe — the last
		// one wins and lifecycle is idempotent.
		election.OnLeadershipChange(func(isLeader bool) {
			go func() {
				if isLeader {
					snapshotter.StopReader()
					// Pull the latest snapshot once before promotion so a freshly elected
					// leader does not start with completely empty state.
					if err := snapshotter.Load(electionCtx); err != nil {
						snapshotLogger.Warnf("snapshot load on promotion failed: %v", err)
					}
					snapshotter.StartWriter(electionCtx, writeInterval)
					return
				}
				snapshotter.StopWriter()
				snapshotter.StartReader(electionCtx, pollInterval)
			}()
		})

		// Start as a snapshot reader BEFORE the election loop runs.
		//
		// The election callback only fires on a leadership *transition*
		// (atomic.Bool CompareAndSwap in registry/leader.tickOnce). An
		// instance that joins while a leader already holds the lease
		// stays at the initial not-leader state forever — no transition,
		// no callback, no StartReader → its in-memory store would never
		// be hydrated from the snapshot doc and would drift indefinitely
		// from the leader's view.
		//
		// Pre-starting the reader makes "standby" the correct default
		// regardless of whether the initial-state callback ever fires.
		// StartReader is documented idempotent and the promotion branch
		// above calls StopReader before StartWriter, so the lifecycle
		// stays correct on every later transition.
		snapshotter.StartReader(electionCtx, pollInterval)

		// Drive elections in the background.
		go election.Run(electionCtx)

		// Start HTTP metrics server (port 9091)
		httpMetricsServer := server.NewHTTPMetricsServer(metricsAggregator, 9091, metricsLogger)
		go func() {
			if err := httpMetricsServer.Start(); err != nil {
				metricsLogger.Errorf("HTTP metrics server failed: %v", err)
			}
		}()

		// Start cleanup goroutine for stale data (every 5 minutes).
		// Only the leader cleans up — standbys must not delete entries that
		// the leader is still tracking.
		go func() {
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()

			for range ticker.C {
				if !election.IsLeader() {
					continue
				}

				// Clean up control-plane data that hasn't been updated for 2 minutes
				if err := controlPlaneRoutingService.CleanupStaleData(context.Background(), 2*time.Minute); err != nil {
					rootLogger.WithError(err).Error("Control-plane routing cleanup failed")
				} else {
					rootLogger.Debug("Control-plane stale data cleanup completed")
				}

				// Clean up controller data that hasn't been updated for 2 minutes
				if err := controllerRoutingService.CleanupStaleData(context.Background(), 2*time.Minute); err != nil {
					rootLogger.WithError(err).Error("Controller routing cleanup failed")
				} else {
					rootLogger.Debug("Controller stale data cleanup completed")
				}

				// Clean up stale metrics
				metricsAggregator.CleanupStaleMetrics(2 * time.Minute)
			}
		}()

		// Start gRPC server. electionCtx propagates SIGINT/SIGTERM so the
		// server can GracefulStop, which in turn lets the election loop
		// finish releasing the Mongo lock before the binary exits.
		rootLogger.WithField("address", fullAddress).Info("Starting gRPC server")
		if err := server.StartGRPCServer(electionCtx, fullAddress, controllerRoutingService, controlPlaneRoutingService, metricsAggregator, rootLogger, appConfig, election, election); err != nil {
			rootLogger.WithError(err).Fatal("gRPC server error")
		}
	},
}


func init() {
	rootCmd.AddCommand(registryCmd)
	registryCmd.PersistentFlags().UintVar(&registryPort, "port", 9090, "Registry service port")
}
