package cmd

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/registry/server"
	"github.com/CloudNativeWorks/elchi-backend/registry/service"
	"github.com/CloudNativeWorks/elchi-backend/registry/storage"
)

var (
	registryPort uint
)

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
	Run: func(_ *cobra.Command, _ []string) {
		appConfig := config.Read(cfgFile)

		// Initialize logger with default config
		if err := logger.Init(logger.Config{
			Level:      appConfig.Logging.Level,
			Format:     appConfig.Logging.Format,
			OutputPath: appConfig.Logging.OutputPath,
			Module:     "registry",
		}); err != nil {
			log.Fatalf("Fatal: Logger could not be initialized: %v", err)
			os.Exit(1)
		}

		registryPort = appConfig.RegistryPort

		rootLogger := logger.NewLogger("registry")
		rootLogger.Infof("Starting Elchi Registry Service")

		// Initialize in-memory storage
		rootLogger.Info("Initializing in-memory storage...")
		storageInstance := storage.NewInMemoryStorage()
		routingStorageInstance := storage.NewInMemoryRoutingStorage()

		// Initialize services
		rootLogger.Info("Initializing registry service...")
		registryService := service.NewRegistryService(storageInstance, rootLogger)

		rootLogger.Info("Initializing routing service...")
		routingService := service.NewRoutingService(routingStorageInstance, rootLogger)

		// Start cleanup goroutine for stale data (every 10 minutes)
		go func() {
			ticker := time.NewTicker(10 * time.Minute)
			defer ticker.Stop()

			for range ticker.C {
				// Clean up data that hasn't been updated for 2 minutes
				if err := routingService.CleanupStaleData(context.Background(), 2*time.Minute); err != nil {
					rootLogger.WithError(err).Error("Routing cleanup failed")
				} else {
					rootLogger.Debug("Routing stale data cleanup completed")
				}
			}
		}()

		// Start gRPC server
		rootLogger.WithField("port", appConfig.RegistryPort).Info("Starting gRPC server")
		if err := server.StartGRPCServer(int(appConfig.RegistryPort), registryService, routingService, rootLogger); err != nil {
			rootLogger.WithError(err).Fatal("gRPC server error")
		}
	},
}

func init() {
	rootCmd.AddCommand(registryCmd)
	registryCmd.PersistentFlags().UintVar(&registryPort, "port", 9090, "Registry service port")
}
