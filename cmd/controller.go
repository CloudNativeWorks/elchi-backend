package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/CloudNativeWorks/elchi-backend/controller/api/router"
	"github.com/CloudNativeWorks/elchi-backend/controller/api/settings"
	"github.com/CloudNativeWorks/elchi-backend/controller/bridge"
	"github.com/CloudNativeWorks/elchi-backend/controller/client"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/custom"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/extension"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/scenario"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/controller/dependency"
	"github.com/CloudNativeWorks/elchi-backend/controller/handlers"
	"github.com/CloudNativeWorks/elchi-backend/controller/service"
	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	server "github.com/CloudNativeWorks/elchi-backend/pkg/httpserver"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/registry"
)

// restCmd represents the command for starting the REST API server.
// It initializes the server, sets up routes, and starts listening for incoming HTTP requests.
// Parameters:
// - none
// Returns:
// - *cobra.Command: a Cobra command instance for the REST API server
var restCmd = &cobra.Command{
	Use:   "elchi-controller",
	Short: "Start Elchi Controller",
	Long:  `Start Elchi Controller`,
	Run: func(_ *cobra.Command, _ []string) {
		appConfig := config.Read(cfgFile)

		// Initialize logger with default config
		if err := logger.Init(logger.Config{
			Level:      appConfig.Logging.Level,
			Format:     appConfig.Logging.Format,
			OutputPath: appConfig.Logging.OutputPath,
			Module:     "root",
		}); err != nil {
			log.Fatalf("Fatal: Logger could not be initialized: %v\n", err)
			os.Exit(1)
		}

		rootLogger := logger.NewLogger("controller")
		// Combine RegistryAddress and RegistryPort
		registryAddress := appConfig.RegistryAddress
		if registryAddress == "" {
			registryAddress = "localhost"
		}
		fullAddress := fmt.Sprintf("%s:%d", registryAddress, appConfig.RegistryPort)

		rootLogger.Infof("Controller starting with registry at: %s (port: %d)", registryAddress, appConfig.RegistryPort)

		registryClient, err := registry.NewRegistryClient(fullAddress, rootLogger)
		if err != nil {
			rootLogger.Fatalf("Failed to create registry client: %v", err)
		}

		// Start registry connection and registration in background with retry
		go func() {
			// Create context for registry operations
			registryCtx, registryCancel := context.WithCancel(context.Background())
			defer registryCancel()

			rootLogger.Infof("Starting registry connection and registration process...")

			// Connect to registry with retry
			if err := registryClient.ConnectWithRetry(registryCtx); err != nil {
				rootLogger.Errorf("Failed to connect to registry: %v", err)
				return
			}

			// Register controller with retry
			if err := registryClient.RegisterControllerWithRetry(registryCtx); err != nil {
				rootLogger.Errorf("Failed to register controller: %v", err)
				return
			}

			rootLogger.Infof("Successfully connected to registry and registered controller")
		}()

		appContext := db.NewMongoDB(appConfig, false)
		xdsHandler := xds.NewXDSHandler(appContext)
		extensionHandler := extension.NewExtensionHandler(appContext)
		scenarioHandler := scenario.NewScenarioHandler(appContext)
		customHandler := custom.NewCustomHandler(appContext)
		bridgeHandler := bridge.NewBridgeHandler(appContext)
		userHandler := settings.NewUserHandler(appContext)
		dependencyHandler := dependency.NewDependencyHandler(appContext)

		serviceHandler := service.NewServiceHandler(appContext)
		clientHandler := client.NewClientHandler(appContext, xdsHandler)

		// Pass registry client to client handler (even before connection is established)
		clientHandler.SetRegistryClient(registryClient)

		// Sync all existing clients with registry after client handler is set up
		go func() {
			// Wait a bit for everything to be initialized
			time.Sleep(2 * time.Second)
			
			rootLogger.Infof("Starting initial client sync with registry...")
			getAllClients := func() []string {
				return clientHandler.Service.GetConnectedClientIDs()
			}
			
			syncCtx, syncCancel := context.WithTimeout(context.Background(), 60*time.Second)
			if err := registryClient.SyncAllClientsWithRegistry(syncCtx, getAllClients); err != nil {
				rootLogger.Errorf("Failed to sync existing clients with registry: %v", err)
			} else {
				rootLogger.Infof("Initial client sync completed successfully")
			}
			syncCancel()
		}()

		// Start health monitor for registry connection recovery
		registryClient.StartHealthMonitor(func() []string {
			return clientHandler.Service.GetConnectedClientIDs()
		})

		go clientHandler.Start(appConfig)

		dependencyHandler.StartCacheCleanup(1 * time.Minute)

		h := handlers.NewHandler(
			xdsHandler,
			extensionHandler,
			customHandler,
			userHandler,
			dependencyHandler,
			bridgeHandler,
			scenarioHandler,
			clientHandler,
			serviceHandler,
		)

		r := router.InitRouter(h)

		if err := server.NewHTTPServer(r).Run(appConfig, rootLogger.Logger); err != nil {
			rootLogger.Fatalf("Server failed to run: %v", err)
		}
	},
}

func init() {
	rootCmd.AddCommand(restCmd)
}
