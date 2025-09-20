package client

import (
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/CloudNativeWorks/elchi-backend/controller/client/grpc"
	"github.com/CloudNativeWorks/elchi-backend/controller/client/handlers"
	"github.com/CloudNativeWorks/elchi-backend/controller/client/services"
	"github.com/CloudNativeWorks/elchi-backend/controller/cloud/openstack"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/registry"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
)

const (
	grpcPort = ":50051"
)

type AppHandler struct {
	Service        *services.ClientService
	Handler        *handlers.Client
	Logger         *logger.Logger
	XDSHandler     *xds.AppHandler
	RegistryClient *registry.RegistryClient
}

func NewClientHandler(context *db.AppContext, xdsHandler *xds.AppHandler, openStackHandler *openstack.Handler, clientService *services.ClientService) *AppHandler {
	return &AppHandler{
		Service:    clientService,
		Handler:    handlers.NewClientHandler(context, xdsHandler, clientService, openStackHandler),
		Logger:     logger.NewLogger("controller/client"),
		XDSHandler: xdsHandler,
	}
}

// SetRegistryClient sets the registry client for this handler
func (h *AppHandler) SetRegistryClient(client *registry.RegistryClient) {
	h.RegistryClient = client
	h.Service.SetRegistryClient(client)
	h.Handler.SetRegistryClient(client)
}

func (h *AppHandler) Start(appConfig *config.AppConfig) {
	// Start gRPC server
	lis, err := net.Listen("tcp", grpcPort)
	if err != nil {
		h.Logger.Fatalf("Failed to listen on gRPC port: %v", err)
	}

	opts := []grpclib.ServerOption{
		grpclib.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle:     0,                // Never expire connection
			Time:                  30 * time.Second, // Health check every 30 seconds (1 minute -> 30 seconds)
			Timeout:               5 * time.Second,  // Health check timeout (10 -> 5 seconds)
			MaxConnectionAge:      0,                // Infinite connection age
			MaxConnectionAgeGrace: 0,                // Infinite grace period
		}),
		grpclib.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second, // Minimum ping interval (30s -> 10s)
			PermitWithoutStream: true,             // Allow ping without stream (false -> true)
		}),
		// Connection management settings
		grpclib.MaxConcurrentStreams(1000),             // Maximum concurrent streams
		grpclib.InitialWindowSize(1 * 1024 * 1024),     // 1MB initial window size
		grpclib.InitialConnWindowSize(1 * 1024 * 1024), // 1MB initial connection window size
		grpclib.MaxHeaderListSize(32 * 1024),           // 32KB maximum header size
		grpclib.WriteBufferSize(64 * 1024),             // 64KB write buffer
		grpclib.ReadBufferSize(64 * 1024),              // 64KB read buffer
	}

	grpcServer := grpclib.NewServer(opts...)

	// Register command service
	pb.RegisterCommandServiceServer(grpcServer, grpc.NewServer(h.Service, appConfig))

	// Wait group for graceful shutdown
	wg := sync.WaitGroup{}

	// Start gRPC server
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.Logger.Infof("controller gRPC server started: %s", grpcPort)
		if err := grpcServer.Serve(lis); err != nil {
			h.Logger.Errorf("gRPC server error: %v", err)
		}
	}()

	// Signal catcher
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Wait for signal
	<-sigChan
	h.Logger.Info("Shutdown signal received...")

	// Immediately close servers and cleanup resources
	go func() {
		// Stop client handler background goroutines
		h.Handler.Stop()

		// Stop registry client background processes
		if h.RegistryClient != nil {
			h.RegistryClient.Stop()
		}

		// Force stop gRPC server
		grpcServer.Stop()

		// Clean up all connections
		h.Service.DisconnectAllClients()
	}()

	// Wait maximum 3 seconds
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		h.Logger.Info("Server shutdown completed successfully.")
	case <-time.After(3 * time.Second):
		h.Logger.Info("Server force shutdown.")
	}
}
