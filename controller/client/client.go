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
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
)

const (
	grpcPort = ":50051"
)

type AppHandler struct {
	Service    *services.ClientService
	Handler    *handlers.Client
	Logger     *logger.Logger
	XDSHandler *xds.AppHandler
}

func NewClientHandler(context *db.AppContext, xdsHandler *xds.AppHandler) *AppHandler {
	clientService := services.NewClientService(context)
	return &AppHandler{
		Service:    clientService,
		Handler:    handlers.NewClientHandler(context, xdsHandler, clientService),
		Logger:     logger.NewLogger("controller/client"),
		XDSHandler: xdsHandler,
	}
}

func (h *AppHandler) Start(appConfig *config.AppConfig) {
	// Start gRPC server
	lis, err := net.Listen("tcp", grpcPort)
	if err != nil {
		h.Logger.Fatalf("Failed to listen on gRPC port: %v", err)
	}

	opts := []grpclib.ServerOption{
		grpclib.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle: 0,                // Never expire connection
			Time:              1 * time.Minute,  // Health check every minute
			Timeout:           10 * time.Second, // Health check timeout
		}),
		grpclib.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             5 * time.Second, // Minimum ping interval
			PermitWithoutStream: true,            // Allow ping without stream
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
	pb.RegisterCommandServiceServer(grpcServer, grpc.NewServer(h.Service, appConfig))

	// Wait group for graceful shutdown
	wg := sync.WaitGroup{}

	// Start gRPC server
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.Logger.Infof("gRPC server started: %s", grpcPort)
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

	// Immediately close servers
	go func() {
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
