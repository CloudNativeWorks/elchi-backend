package cmd

import (
	"context"
	"fmt"
	"log"

	"github.com/CloudNativeWorks/versioned-go-control-plane/pkg/server/v3"
	"github.com/spf13/cobra"

	"github.com/CloudNativeWorks/elchi-backend/control-plane/envoys"
	grpcserver "github.com/CloudNativeWorks/elchi-backend/control-plane/server"
	"github.com/CloudNativeWorks/elchi-backend/control-plane/server/bridge"
	"github.com/CloudNativeWorks/elchi-backend/control-plane/server/snapshot"
	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/registry"
	"github.com/CloudNativeWorks/elchi-backend/pkg/version"
)

var (
	port     uint
	location string
)

// grpcCmd represents the command for starting the gRPC server.
// It initializes the server, sets up the necessary services, and starts listening for incoming gRPC requests.
// Parameters:
// - none
// Returns:
// - *cobra.Command: a Cobra command instance for the gRPC server
var grpcCmd = &cobra.Command{
	Use:   "elchi-control-plane",
	Short: "Start Elchi Control Plane",
	Long:  `Start Elchi Control Plane`,
	Run: func(cmd *cobra.Command, _ []string) {
		appConfig := config.Read(cfgFile)
		// Initialize logger with default config
		if err := logger.Init(logger.Config{
			Level:      appConfig.Logging.Level,
			Format:     appConfig.Logging.Format,
			OutputPath: appConfig.Logging.OutputPath,
			Module:     "root",
		}); err != nil {
			log.Fatalf("Fatal: Logger could not be initialized: %v", err)
		}

		// Port resolution: --port flag > CONTROL_PLANE_PORT in config > default 18000
		if !cmd.Flags().Changed("port") && appConfig.ControlPlanePort > 0 {
			port = appConfig.ControlPlanePort
		}

		appContext := db.NewMongoDB(appConfig, true)
		ctxCache := snapshot.GetContext()
		pokeService := bridge.NewPokeService(ctxCache, appContext)
		envoyConnTracker := envoys.NewEnvoyConnTracker()

		// Combine RegistryAddress and RegistryPort
		registryAddress := appConfig.RegistryAddress
		if registryAddress == "" {
			registryAddress = "localhost"
		}
		fullAddress := fmt.Sprintf("%s:%d", registryAddress, appConfig.RegistryPort)

		// Create routing config and manager FIRST
		routingConfig := registry.NewControlPlaneConfig(fullAddress, version.GetVersion())
		routingManager, err := registry.NewControlPlaneManager(routingConfig, logger.NewLogger("control-plane/server"), ctxCache)
		if err != nil {
			log.Fatalf("Failed to create routing manager: %v", err)
		}

		// Start routing manager
		if err := routingManager.Start(); err != nil {
			log.Fatalf("Failed to start routing manager: %v", err)
		}
		defer func() {
			if err := routingManager.Stop(); err != nil {
				log.Printf("Failed to stop routing manager: %v", err)
			}
		}()

		// NOW create callbacks with routing manager
		callbacks := grpcserver.NewCallbacks(pokeService, ctxCache, appContext, envoyConnTracker, routingManager)
		srv := server.NewServer(context.Background(), ctxCache.Cache.Cache, callbacks)

		grpcServer := grpcserver.NewServer(srv, port, ctxCache, envoyConnTracker)

		// Start heartbeat service to keep lastSync fresh for active envoys
		// This runs in background and updates lastSync every 30 seconds
		heartbeatCtx, heartbeatCancel := context.WithCancel(context.Background())
		defer heartbeatCancel()
		go envoyConnTracker.StartHeartbeat(heartbeatCtx, appContext.Client, logger.NewLogger("control-plane/heartbeat"))

		grpcServer.Run(appContext)
	},
}

func init() {
	rootCmd.AddCommand(grpcCmd)
	grpcCmd.PersistentFlags().UintVar(&port, "port", 18000, "xDS management server port")
	grpcCmd.PersistentFlags().StringVar(&location, "location", "dc1", "Server Location")
}
