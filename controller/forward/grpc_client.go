package forward

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ForwardClient handles gRPC connections to other controllers
type ForwardClient struct {
	connections map[string]*grpc.ClientConn
	clients     map[string]pb.ControllerServiceClient
	mutex       sync.RWMutex
	logger      *logger.Logger
}

// NewForwardClient creates a new forward client
func NewForwardClient() *ForwardClient {
	return &ForwardClient{
		connections: make(map[string]*grpc.ClientConn),
		clients:     make(map[string]pb.ControllerServiceClient),
		mutex:       sync.RWMutex{},
		logger:      logger.NewLogger("controller/forward"),
	}
}

// getOrCreateClient gets or creates a gRPC client for target controller
func (f *ForwardClient) getOrCreateClient(targetAddress string) (pb.ControllerServiceClient, error) {
	f.mutex.RLock()
	if client, exists := f.clients[targetAddress]; exists {
		f.mutex.RUnlock()
		return client, nil
	}
	f.mutex.RUnlock()

	f.mutex.Lock()
	defer f.mutex.Unlock()

	// Double-check after acquiring write lock
	if client, exists := f.clients[targetAddress]; exists {
		return client, nil
	}

	// Create new connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, targetAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to controller %s: %v", targetAddress, err)
	}

	client := pb.NewControllerServiceClient(conn)
	f.connections[targetAddress] = conn
	f.clients[targetAddress] = client

	f.logger.Debugf("Created gRPC connection to controller: %s", targetAddress)
	return client, nil
}

// ForwardCommand forwards a command to another controller
func (f *ForwardClient) ForwardCommand(targetAddress string, command *pb.Command) (*pb.CommandResponse, error) {
	client, err := f.getOrCreateClient(targetAddress)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req := &pb.ForwardCommandRequest{
		Command: command,
	}

	resp, err := client.ForwardCommand(ctx, req)
	if err != nil {
		f.logger.Errorf("Forward command failed to %s: %v", targetAddress, err)
		// Connection might be broken, remove it
		f.removeConnection(targetAddress)
		return nil, fmt.Errorf("forward command failed: %v", err)
	}

	if !resp.Success {
		return nil, fmt.Errorf("forward command error: %s", resp.Error)
	}

	return resp.Response, nil
}

// removeConnection removes a connection from cache
func (f *ForwardClient) removeConnection(address string) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	if conn, exists := f.connections[address]; exists {
		conn.Close()
		delete(f.connections, address)
		delete(f.clients, address)
		f.logger.Debugf("Removed connection to controller: %s", address)
	}
}

// Close closes all connections
func (f *ForwardClient) Close() {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	for address, conn := range f.connections {
		conn.Close()
		f.logger.Debugf("Closed connection to controller: %s", address)
	}

	f.connections = make(map[string]*grpc.ClientConn)
	f.clients = make(map[string]pb.ControllerServiceClient)
} 