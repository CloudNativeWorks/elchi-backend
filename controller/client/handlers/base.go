package handlers

import (
	"context"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/controller/client/processor"
	"github.com/CloudNativeWorks/elchi-backend/controller/client/responser"
	"github.com/CloudNativeWorks/elchi-backend/controller/client/services"
	"github.com/CloudNativeWorks/elchi-backend/controller/cloud/openstack"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/registry"
)

type Client struct {
	Context        *db.AppContext
	Service        *services.ClientService
	logger         *logger.Logger
	cmdFactory     *processor.CommandProcessorFactory
	responser      *responser.CommandResponserFactory
	registryClient *registry.RegistryClient

	// Graceful shutdown context
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc

	// Client-level request serialization to prevent concurrent gRPC calls
	clientMutexes sync.Map // clientID -> *sync.Mutex mapping
	cleanupTicker *time.Ticker
}

func NewClientHandler(dbContext *db.AppContext, xdsHandler *xds.AppHandler, clientService *services.ClientService, openStackHandler *openstack.Handler) *Client {
	// Create shutdown context
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())

	h := &Client{
		Context:        dbContext,
		Service:        clientService,
		logger:         logger.NewLogger("controller/client/handler"),
		responser:      responser.NewCommandResponserFactory(),
		cmdFactory:     processor.NewCommandProcessorFactory(),
		shutdownCtx:    shutdownCtx,
		shutdownCancel: shutdownCancel,
	}

	processorLogger := logger.NewLogger("controller/client/processor")
	responserLogger := logger.NewLogger("controller/client/responser")

	// Processor Register
	h.cmdFactory.RegisterProcessor("DEPLOY", &processor.DeployProcessor{XDSHandler: xdsHandler, Logger: processorLogger, Service: clientService, OpenStackHandler: openStackHandler})
	h.cmdFactory.RegisterProcessor("SERVICE", &processor.ServiceProcessor{XDSHandler: xdsHandler, Logger: processorLogger})
	h.cmdFactory.RegisterProcessor("UPDATE_BOOTSTRAP", &processor.BootstrapProcessor{XDSHandler: xdsHandler, Logger: processorLogger, Service: clientService})
	h.cmdFactory.RegisterProcessor("UNDEPLOY", &processor.UnDeployProcessor{XDSHandler: xdsHandler, Logger: processorLogger})
	h.cmdFactory.RegisterProcessor("PROXY", &processor.ProxyProcessor{XDSHandler: xdsHandler, Logger: processorLogger})
	h.cmdFactory.RegisterProcessor("CLIENT_LOGS", &processor.GeneralLogProcessor{Logger: processorLogger})
	h.cmdFactory.RegisterProcessor("CLIENT_STATS", &processor.ClientStatsProcessor{Logger: processorLogger})
	h.cmdFactory.RegisterProcessor("NETWORK", &processor.NetworkProcessor{Logger: processorLogger})
	h.cmdFactory.RegisterProcessor("FRR", &processor.FRRProcessor{Logger: processorLogger})
	h.cmdFactory.RegisterProcessor("FRR_LOGS", &processor.GeneralLogProcessor{Logger: processorLogger})
	h.cmdFactory.RegisterProcessor("ENVOY_VERSION", &processor.EnvoyVersionProcessor{Logger: processorLogger})

	// Responser Register
	h.responser.RegisterResponser("DEPLOY", &responser.DeployResponser{XDSHandler: xdsHandler, Logger: responserLogger, Service: clientService, OpenStackHandler: openStackHandler})
	h.responser.RegisterResponser("SERVICE", &responser.ServiceResponser{})
	h.responser.RegisterResponser("UPDATE_BOOTSTRAP", &responser.BootstrapResponser{})
	h.responser.RegisterResponser("UNDEPLOY", &responser.UnDeployResponser{XDSHandler: xdsHandler, Logger: responserLogger, Service: clientService, OpenStackHandler: openStackHandler})
	h.responser.RegisterResponser("PROXY", &responser.ProxyResponser{})
	h.responser.RegisterResponser("CLIENT_LOGS", &responser.GeneralLogResponser{})
	h.responser.RegisterResponser("CLIENT_STATS", &responser.ClientStatsResponser{})
	h.responser.RegisterResponser("NETWORK", &responser.NetworkResponser{})
	h.responser.RegisterResponser("FRR", &responser.FRRResponser{})
	h.responser.RegisterResponser("FRR_LOGS", &responser.GeneralLogResponser{})
	h.responser.RegisterResponser("ENVOY_VERSION", &responser.EnvoyVersionResponser{})

	// Start mutex cleanup routine
	h.startMutexCleanup()

	return h
}

// SetRegistryClient sets the registry client for command routing
func (h *Client) SetRegistryClient(registryClient *registry.RegistryClient) {
	h.registryClient = registryClient
}

// getClientMutex returns or creates a mutex for the given client ID
func (h *Client) getClientMutex(clientID string) *sync.Mutex {
	mutex, _ := h.clientMutexes.LoadOrStore(clientID, &sync.Mutex{})
	return mutex.(*sync.Mutex)
}

// startMutexCleanup starts a background goroutine to cleanup unused mutexes
func (h *Client) startMutexCleanup() {
	h.cleanupTicker = time.NewTicker(5 * time.Minute)
	go func() {
		defer h.logger.Info("🔄 Mutex cleanup goroutine terminated")
		for {
			select {
			case <-h.cleanupTicker.C:
				h.cleanupUnusedMutexes()
			case <-h.shutdownCtx.Done():
				h.logger.Info("🔄 Mutex cleanup goroutine received shutdown signal")
				return
			}
		}
	}()
}

// cleanupUnusedMutexes removes mutexes for disconnected clients to prevent memory leaks
func (h *Client) cleanupUnusedMutexes() {
	connectedClients := make(map[string]bool)
	for _, clientID := range h.Service.GetConnectedClientIDs() {
		connectedClients[clientID] = true
	}

	// Remove mutexes for disconnected clients
	h.clientMutexes.Range(func(key, value any) bool {
		clientID := key.(string)
		if !connectedClients[clientID] {
			h.clientMutexes.Delete(clientID)
			h.logger.Debugf("Cleaned up mutex for disconnected client: %s", clientID)
		}
		return true
	})
}

// Stop gracefully shuts down the client handler
func (h *Client) Stop() {
	h.logger.Info("🔄 Stopping client handler and cleaning up resources...")

	// Signal shutdown to all background goroutines
	if h.shutdownCancel != nil {
		h.shutdownCancel()
	}

	if h.cleanupTicker != nil {
		h.cleanupTicker.Stop()
	}

	// Force cleanup all mutexes to prevent shutdown deadlock
	h.clientMutexes.Range(func(key, value any) bool {
		clientID := key.(string)
		h.logger.Debugf("🔄 Cleaning up mutex for client: %s", clientID)
		h.clientMutexes.Delete(clientID)
		return true
	})

	h.logger.Info("🔄 Client handler stopped successfully")
}
