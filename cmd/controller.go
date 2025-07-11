package cmd

import (
	"log"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/CloudNativeWorks/elchi-backend/controller/api/auth"
	"github.com/CloudNativeWorks/elchi-backend/controller/api/router"
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

		// Initialize registry client
		registryConfig := registry.Config{
			RegistryAddress: getRegistryAddress(appConfig),
			ControllerID:    getControllerID(appConfig),
			GRPCAddress:     getControllerGRPCAddress(appConfig),
		}

		rootLogger := logger.NewLogger("controller")
		registryClient, err := registry.NewRegistryClient(registryConfig, rootLogger)
		if err != nil {
			rootLogger.Fatalf("Failed to create registry client: %v", err)
		}

		// Connect to registry and register controller
		if err := registryClient.Connect(); err != nil {
			rootLogger.Errorf("Failed to connect to registry: %v", err)
		} else {
			if err := registryClient.RegisterController(); err != nil {
				rootLogger.Errorf("Failed to register controller: %v", err)
			}
		}

		appContext := db.NewMongoDB(appConfig, false)
		xdsHandler := xds.NewXDSHandler(appContext)
		extensionHandler := extension.NewExtensionHandler(appContext)
		scenarioHandler := scenario.NewScenarioHandler(appContext)
		customHandler := custom.NewCustomHandler(appContext)
		bridgeHandler := bridge.NewBridgeHandler(appContext)
		userHandler := auth.NewUserHandler(appContext)
		dependencyHandler := dependency.NewDependencyHandler(appContext)

		serviceHandler := service.NewServiceHandler(appContext)
		clientHandler := client.NewClientHandler(appContext, xdsHandler)
		
		// Pass registry client to client handler
		clientHandler.SetRegistryClient(registryClient)
		
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

// getRegistryAddress returns registry address from config or environment
func getRegistryAddress(config *config.AppConfig) string {
	if addr := os.Getenv("REGISTRY_ADDRESS"); addr != "" {
		return addr
	}
	// Default registry address
	return "localhost:9090"
}

// getControllerID returns controller ID from config or hostname
func getControllerID(config *config.AppConfig) string {
	if id := os.Getenv("CONTROLLER_ID"); id != "" {
		return id
	}
	// Will auto-detect from hostname in registry client
	return ""
}

// getControllerGRPCAddress returns controller gRPC address
func getControllerGRPCAddress(config *config.AppConfig) string {
	if addr := os.Getenv("CONTROLLER_GRPC_ADDRESS"); addr != "" {
		return addr
	}
	// Will auto-detect from hostname:8080 in registry client
	return ""
}

func init() {
	rootCmd.AddCommand(restCmd)
}
