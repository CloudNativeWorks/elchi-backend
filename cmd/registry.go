package cmd

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/spf13/cobra"

	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/registry/metrics"
	"github.com/CloudNativeWorks/elchi-backend/registry/server"
	"github.com/CloudNativeWorks/elchi-backend/registry/service"
	"github.com/CloudNativeWorks/elchi-backend/registry/storage"
)

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

		// Start HTTP metrics server (port 9091)
		httpMetricsServer := server.NewHTTPMetricsServer(metricsAggregator, 9091, metricsLogger)
		go func() {
			if err := httpMetricsServer.Start(); err != nil {
				metricsLogger.Errorf("HTTP metrics server failed: %v", err)
			}
		}()

		// Start cleanup goroutine for stale data (every 5 minutes)
		go func() {
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()

			for range ticker.C {
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

		// Start gRPC server
		rootLogger.WithField("address", fullAddress).Info("Starting gRPC server")
		if err := server.StartGRPCServer(fullAddress, controllerRoutingService, controlPlaneRoutingService, metricsAggregator, rootLogger, appConfig); err != nil {
			rootLogger.WithError(err).Fatal("gRPC server error")
		}
	},
}


func init() {
	rootCmd.AddCommand(registryCmd)
	registryCmd.PersistentFlags().UintVar(&registryPort, "port", 9090, "Registry service port")
}
