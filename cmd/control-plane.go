package cmd

import (
	"context"
	"log"
	"os"

	"github.com/CloudNativeWorks/versioned-go-control-plane/pkg/server/v3"
	"github.com/spf13/cobra"

	"github.com/CloudNativeWorks/elchi-backend/control-plane/envoys"
	grpcserver "github.com/CloudNativeWorks/elchi-backend/control-plane/server"
	"github.com/CloudNativeWorks/elchi-backend/control-plane/server/bridge"
	"github.com/CloudNativeWorks/elchi-backend/control-plane/server/snapshot"
	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
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
	Run: func(_ *cobra.Command, _ []string) {
		appConfig := config.Read(cfgFile)
		// Initialize logger with default config
		if err := logger.Init(logger.Config{
			Level:      appConfig.Logging.Level,
			Format:     appConfig.Logging.Format,
			OutputPath: appConfig.Logging.OutputPath,
			Module:     "root",
		}); err != nil {
			log.Fatalf("Fatal: Logger could not be initialized: %v", err)
			os.Exit(1)
		}

		appContext := db.NewMongoDB(appConfig, true)
		ctxCache := snapshot.GetContext()
		pokeService := bridge.NewPokeService(ctxCache, appContext)
		envoyConnTracker := envoys.NewEnvoyConnTracker()

		callbacks := grpcserver.NewCallbacks(pokeService, ctxCache, appContext, envoyConnTracker)
		srv := server.NewServer(context.Background(), ctxCache.Cache.Cache, callbacks)
		grpcServer := grpcserver.NewServer(srv, port, ctxCache)

		grpcServer.Run(appContext)
	},
}

func init() {
	rootCmd.AddCommand(grpcCmd)
	grpcCmd.PersistentFlags().UintVar(&port, "port", 18000, "xDS management server port")
	grpcCmd.PersistentFlags().StringVar(&location, "location", "dc1", "Server Location")
}
