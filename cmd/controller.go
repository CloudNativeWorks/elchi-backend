package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/CloudNativeWorks/elchi-backend/controller/api/middleware"
	"github.com/CloudNativeWorks/elchi-backend/controller/api/router"
	"github.com/CloudNativeWorks/elchi-backend/controller/api/settings"
	"github.com/CloudNativeWorks/elchi-backend/controller/bridge"
	"github.com/CloudNativeWorks/elchi-backend/controller/client"
	"github.com/CloudNativeWorks/elchi-backend/controller/client/services"
	"github.com/CloudNativeWorks/elchi-backend/controller/cloud/openstack"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/custom"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/extension"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/scenario"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/controller/dependency"
	"github.com/CloudNativeWorks/elchi-backend/controller/discovery"
	"github.com/CloudNativeWorks/elchi-backend/controller/handlers"
	"github.com/CloudNativeWorks/elchi-backend/controller/routemap"
	"github.com/CloudNativeWorks/elchi-backend/controller/service"
	"github.com/CloudNativeWorks/elchi-backend/pkg/acme"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async"
	"github.com/CloudNativeWorks/elchi-backend/pkg/audit"
	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	pkgGslb "github.com/CloudNativeWorks/elchi-backend/pkg/gslb"
	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	server "github.com/CloudNativeWorks/elchi-backend/pkg/httpserver"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/registry"
)

// acmeJobAdapter adapts async.AsyncJobSystem to acme.AsyncJobCreator interface
// This avoids circular dependency between pkg/async and pkg/acme
type acmeJobAdapter struct {
	asyncSystem async.AsyncJobSystem
}

// CreateACMEVerificationJob converts letsencrypt request to async request and creates job
func (a *acmeJobAdapter) CreateACMEVerificationJob(ctx context.Context, req *acme.ACMEJobRequest) (*acme.ACMEJob, error) {
	// Convert to async request format
	asyncReq := &async.CreateACMEJobRequest{
		CertificateID:   req.CertificateID,
		CertificateName: req.CertificateName,
		Domains:         req.Domains,
		Environment:     req.Environment,
		DNSCredentialID: req.DNSCredentialID,
		ACMEAccountID:   req.ACMEAccountID,
		Project:         req.Project,
		Versions:        req.Versions,
		TriggerUser:     req.TriggerUser,
		IsRenewal:       req.IsRenewal,
	}

	// Create job via async system
	job, err := a.asyncSystem.CreateACMEVerificationJob(ctx, asyncReq)
	if err != nil {
		return nil, err
	}

	// Convert back to letsencrypt format
	return &acme.ACMEJob{
		JobID: job.JobID,
	}, nil
}

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

		// Initialize JWT configuration from appConfig
		if err := helper.InitializeJWTConfigFromAppConfig(
			appConfig.ElchiJWTSecret,
			appConfig.ElchiJWTAccessTokenDuration,
			appConfig.ElchiJWTRefreshTokenDuration,
		); err != nil {
			log.Fatalf("Fatal: JWT configuration error: %v\n", err)
		}

		// Initialize CORS configuration from appConfig
		middleware.SetCORSConfig(appConfig.ElchiCORSAllowedOrigins)

		// Initialize logger with default config
		if err := logger.Init(logger.Config{
			Level:      appConfig.Logging.Level,
			Format:     appConfig.Logging.Format,
			OutputPath: appConfig.Logging.OutputPath,
			Module:     "root",
		}); err != nil {
			log.Fatalf("Fatal: Logger could not be initialized: %v\n", err)
		}

		rootLogger := logger.NewLogger("controller")
		// Combine RegistryAddress and RegistryPort
		registryAddress := appConfig.RegistryAddress
		if registryAddress == "" {
			registryAddress = "localhost"
		}
		fullAddress := fmt.Sprintf("%s:%d", registryAddress, appConfig.RegistryPort)

		rootLogger.Infof("Controller starting with registry at: %s (port: %d)", registryAddress, appConfig.RegistryPort)

		registryClient, err := registry.NewRegistryClient(fullAddress, rootLogger, appConfig)
		if err != nil {
			rootLogger.Fatalf("Failed to create registry client: %v", err)
		}

		// Start initial registry connection and registration in background
		go func() {
			rootLogger.Infof("Starting initial registry connection and registration...")

			if err := registryClient.ConnectAndRegister(); err != nil {
				rootLogger.Errorf("Initial registry connection failed: %v", err)
				// Don't return - the continuous reconnect loop will handle retries
			} else {
				rootLogger.Infof("Successfully connected to registry and registered controller")
			}
		}()

		appContext := db.NewMongoDB(appConfig, false)

		// Generate controller ID for GSLB shard ownership
		// Use hostname (same as registry registration for consistency)
		controllerID, err := os.Hostname()
		if err != nil {
			rootLogger.Fatalf("Failed to get hostname for controller ID: %v", err)
		}

		// Create GSLB System (manages all GSLB components)
		gslbSystem, err := pkgGslb.NewSystem(appContext, controllerID)
		if err != nil {
			rootLogger.Fatalf("Failed to create GSLB system: %v", err)
		}

		// Start GSLB system in background
		go func() {
			if err := gslbSystem.Start(); err != nil {
				// This is expected when no GSLB records exist yet
				rootLogger.Infof("GSLB system in standby mode: %v", err)
			}
		}()

		// Ensure graceful shutdown of GSLB system
		defer func() {
			if err := gslbSystem.Stop(); err != nil {
				rootLogger.Errorf("Failed to stop GSLB system: %v", err)
			}
		}()

		rootLogger.Infof("GSLB system initialization completed")

		xdsHandler := xds.NewXDSHandler(appContext)
		extensionHandler := extension.NewExtensionHandler(appContext)
		scenarioHandler := scenario.NewScenarioHandler(appContext)
		customHandler := custom.NewCustomHandler(appContext)
		bridgeHandler := bridge.NewBridgeHandler(appContext)
		dependencyHandler := dependency.NewDependencyHandler(appContext)

		serviceHandler := service.NewServiceHandler(appContext)

		// Create a client service for openstack and client handler initialization
		clientService := services.NewClientService(appContext)
		openstackHandler := openstack.NewHandler(appContext, rootLogger, clientService)

		clientHandler := client.NewClientHandler(appContext, xdsHandler, openstackHandler, clientService)
		discoveryHandler := discovery.NewDiscoveryHandler(appContext, &bridgeHandler.Poke)
		jobHandler := handlers.NewJobHandler(appContext, &bridgeHandler.Poke, rootLogger, clientHandler.Handler)
		registryHandler := handlers.NewRegistryHandler(registryClient, rootLogger)

		// Initialize upgrade handler (will be set after jobHandler starts async system)
		var upgradeHandler *handlers.UpgradeHandler

		// Pass registry client to client handler (even before connection is established)
		clientHandler.SetRegistryClient(registryClient)

		// Start periodic client-registry synchronization
		clientHandler.Service.StartPeriodicSync(5 * time.Minute)

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

		// Start enhanced health monitor with continuous reconnect capability
		registryClient.StartHealthMonitor(func() []string {
			return clientHandler.Service.GetConnectedClientIDs()
		})

		// Ensure graceful shutdown of registry client
		defer registryClient.Stop()

		go clientHandler.Start(appConfig)

		dependencyHandler.StartCacheCleanup(1 * time.Minute)

		// Initialize audit service
		auditLogger := logger.NewLogger("controller/audit")
		auditService := audit.NewService(appContext, auditLogger)

		// Initialize settings handler
		userHandler := settings.NewUserHandler(appContext)

		// Initialize async job system workers FIRST (needed by upgrade handler)
		rootLogger.Infof("Starting async job system workers...")
		if err := jobHandler.StartAsyncSystem(&bridgeHandler.Poke); err != nil {
			rootLogger.Fatalf("Failed to start async job system: %v", err)
		}

		// Initialize all sub-handlers that will be passed to NewHandler
		asyncSystem := jobHandler.GetAsyncSystem()
		upgradeHandler = handlers.NewUpgradeHandler(appContext, asyncSystem, auditService, clientHandler.Handler)
		maintenanceHandler := handlers.NewMaintenanceHandler(appContext, rootLogger, auditService)
		templateHandler := handlers.NewResourceTemplateHandler(appContext, nil)
		routeMapHandler := routemap.NewRouteMapHandler(appContext)

		// Initialize ACME certificate manager
		acmeLogger := logger.NewLogger("controller/acme")
		acmeManager, err := acme.NewCertificateManager(
			appContext.Client,
			appConfig.ElchiJWTSecret,
			&appConfig.ACME,
			&appConfig.CAProviders,
			acmeLogger,
		)
		if err != nil {
			rootLogger.Fatalf("Failed to initialize ACME certificate manager: %v", err)
		}

		// Initialize ACME handler with async job system
		acmeHandler := handlers.NewACMEHandler(acmeManager, asyncSystem, acmeLogger)

		// Create CA providers handler
		caProvidersHandler := handlers.NewCAProvidersHandler(appConfig)

		// Create GSLB DNS handler
		dnsHandler := handlers.NewDNSHandler(appContext)

		// Create GSLB CRUD handler (with gslbSystem for reloads)
		gslbHandler := handlers.NewGSLBHandler(appContext, gslbSystem)

		// Set dependency services for snapshot updates on certificate renewal
		acmeManager.SetDependencyServices(appContext, &bridgeHandler.Poke)

		// Create renewal scheduler with constant interval (defined in pkg/acme/constants.go)
		baseScheduler := acme.NewRenewalScheduler(
			acmeManager,
			acmeLogger,
			acme.RenewalCheckInterval,
		)

		// Set async job creator after initialization (avoids import cycle)
		baseScheduler.SetAsyncJobCreator(&acmeJobAdapter{asyncSystem: asyncSystem})

		// Wrap with distributed scheduler for multi-pod safety
		distributedScheduler := acme.NewDistributedScheduler(
			baseScheduler,
			appContext.Client,
			acmeLogger,
		)

		// Start distributed scheduler in background
		ctx := context.Background()
		if err := distributedScheduler.Start(ctx); err != nil {
			rootLogger.Errorf("Failed to start distributed renewal scheduler: %v", err)
		} else {
			acmeLogger.Infof("ACME distributed renewal scheduler started (check interval: %v)", acme.RenewalCheckInterval)
			acmeLogger.Infof("Multi-pod deployment: Leader election enabled via MongoDB")
		}

		// Setup graceful shutdown for distributed scheduler
		defer func() {
			if err := distributedScheduler.Stop(); err != nil {
				acmeLogger.Errorf("Failed to stop distributed renewal scheduler: %v", err)
			}
		}()

		// Create main handler with all dependencies
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
			discoveryHandler,
			jobHandler,
			registryHandler,
			openstackHandler,
			auditService,
			templateHandler,
			routeMapHandler,
			maintenanceHandler,
			upgradeHandler,
			acmeHandler,
			caProvidersHandler,
			dnsHandler,
			gslbHandler,
		)

		// Set handler reference for audit functionality
		templateHandler.Handler = h

		r := router.InitRouter(h)

		if err := server.NewHTTPServer(r).Run(appConfig, rootLogger); err != nil {
			rootLogger.Fatalf("Server failed to run: %v", err)
		}
	},
}

func init() {
	rootCmd.AddCommand(restCmd)
}
