package grpc

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/controller/client/services"
	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
	"go.mongodb.org/mongo-driver/bson"
)

// Server represents the gRPC server
type Server struct {
	pb.UnimplementedCommandServiceServer
	clientService *services.ClientService
	logger        *logger.Logger
}

// NewServer creates a new gRPC server
func NewServer(clientService *services.ClientService, appConfig *config.AppConfig) *Server {
	// Initialize logger
	if err := logger.Init(logger.Config{
		Level:      appConfig.Logging.Level,
		Format:     appConfig.Logging.Format,
		OutputPath: appConfig.Logging.OutputPath,
		Module:     "client_service_server",
	}); err != nil {
		log.Printf("Logger could not be initialized: %v\n", err)
		panic(err)
	}

	return &Server{
		clientService: clientService,
		logger:        logger.NewLogger("controller/clientServer"),
	}
}

// Register handles client registration
func (s *Server) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	_, sessionToken, err := s.clientService.RegisterClient(req)
	if err != nil {
		return &pb.RegisterResponse{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	return &pb.RegisterResponse{
		Success:      true,
		SessionToken: sessionToken,
	}, nil
}

// Unregister handles client unregistration
func (s *Server) Unregister(ctx context.Context, req *pb.UnregisterRequest) (*pb.UnregisterResponse, error) {
	err := s.clientService.UnregisterClient(req.GetIdentity().GetClientId())
	if err != nil {
		return &pb.UnregisterResponse{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	return &pb.UnregisterResponse{
		Success: true,
	}, nil
}

// Ping handles ping requests for connectivity testing
func (s *Server) Ping(ctx context.Context, req *pb.PingRequest) (*pb.PingResponse, error) {
	s.logger.Debugf("Ping request received from client: %s at %d", req.ClientId, req.Timestamp)

	// Validate client ID
	if req.ClientId == "" {
		s.logger.Warn("Ping request with empty client ID")
		return &pb.PingResponse{
			ClientTimestamp: req.Timestamp,
			ServerTimestamp: time.Now().Unix(),
			Message:         "client_id is required",
			Success:         false,
		}, nil
	}

	// Check if client is registered
	client, err := s.clientService.GetClient(req.ClientId)
	if err != nil {
		s.logger.Warnf("Ping from unregistered client: %s", req.ClientId)
		return &pb.PingResponse{
			ClientTimestamp: req.Timestamp,
			ServerTimestamp: time.Now().Unix(),
			Message:         "client not registered",
			Success:         false,
		}, nil
	}

	// Update client's last seen time (in-memory)
	client.UpdateLastSeen()

	// Also update in database asynchronously
	go func(clientID string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		filter := bson.M{"client_id": clientID}
		update := bson.M{"$set": bson.M{"last_seen": time.Now()}}
		if _, err := s.clientService.Context.Client.Collection("clients").UpdateOne(ctx, filter, update); err != nil {
			s.logger.Errorf("Failed to update LastSeen in DB for client %s: %v", clientID, err)
		}
	}(req.ClientId)

	// Calculate latency if timestamp provided
	latencyMsg := "pong"
	if req.Timestamp > 0 {
		latency := time.Now().Unix() - req.Timestamp
		latencyMsg = fmt.Sprintf("pong (latency: %ds)", latency)
	}

	s.logger.Debugf("Ping response sent to client: %s", req.ClientId)

	return &pb.PingResponse{
		ClientTimestamp: req.Timestamp,
		ServerTimestamp: time.Now().Unix(),
		Message:         latencyMsg,
		Success:         true,
	}, nil
}

// CommandStream manages the client command stream
func (s *Server) CommandStream(stream pb.CommandService_CommandStreamServer) error {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Errorf("Panic in CommandStream: %v", r)
		}
	}()

	// Wait for the initial message
	initialResp, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("failed to receive initial message: %v", err)
	}

	clientID := initialResp.GetIdentity().GetClientId()
	sessionToken := initialResp.GetIdentity().GetSessionToken()
	if sessionToken == "" {
		return fmt.Errorf("session token missing")
	}

	if err := s.clientService.ValidateSession(clientID, sessionToken); err != nil {
		s.logger.Errorf("Session validation error (Client ID: %s, Session Token: %s): %v", clientID, sessionToken, err)
		return fmt.Errorf("session validation error: %v", err)
	}

	if err := s.clientService.UpdateClientStream(clientID, stream); err != nil {
		s.logger.Errorf("Stream update error (Client ID: %s): %v", clientID, err)
		return fmt.Errorf("stream update error: %v", err)
	}

	s.logger.Infof("Client stream connection successful (Client ID: %s)", clientID)

	client, err := s.clientService.GetClient(clientID)
	if err != nil {
		s.logger.Errorf("Failed to get client after stream update (Client ID: %s): %v", clientID, err)
		return fmt.Errorf("client not found after stream update: %v", err)
	}

	// Keepalive ticker removed - client will send pings via separate connection

	for {
		select {
		case <-stream.Context().Done():
			s.clientService.DisconnectClient(clientID)
			s.logger.Infof("Client stream context cancelled (Client ID: %s): %v", clientID, stream.Context().Err())
			return stream.Context().Err()
		case <-client.Context.Done():
			s.clientService.DisconnectClient(clientID)
			s.logger.Infof("Client context cancelled (Client ID: %s): %v", clientID, client.Context.Err())
			return client.Context.Err()
		default:
			// Removed keepalive ticker - client sends pings via separate connection
			resp, err := stream.Recv()
			if err != nil {
				if isGracefulDisconnection(err) {
					s.clientService.DisconnectClient(clientID)
					s.logger.Infof("Client gracefully disconnected (Client ID: %s)", clientID)
					return nil
				}

				if isTemporaryError(err) {
					s.logger.Warnf("Temporary stream error (Client ID: %s): %v - retrying", clientID, err)
					time.Sleep(1 * time.Second)
					continue
				}

				s.clientService.DisconnectClient(clientID)
				s.logger.Warnf("Stream closed (Client ID: %s): %v", clientID, err)
				return fmt.Errorf("stream error: %v", err)
			}

			// Message validation
			if resp == nil {
				s.logger.Warnf("Received nil message (Client ID: %s)", clientID)
				continue
			}

			if resp.GetCommandId() == "" {
				s.logger.Warnf("Invalid message: commandId is empty (Client ID: %s)", clientID)
				continue
			}

			// Update last seen
			if client, err := s.clientService.GetClient(clientID); err == nil {
				client.UpdateLastSeen()
			}

			s.logger.Infof("Command response received (Client ID: %s, Command ID: %s)", clientID, resp.GetCommandId())
			s.clientService.HandleCommandResponse(resp)
		}
	}
}

// isGracefulDisconnection checks if the error indicates a graceful disconnection
func isGracefulDisconnection(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return errStr == "EOF" || errStr == "io: read/write on closed pipe"
}

// isTemporaryError example function (can be improved)
func isTemporaryError(err error) bool {
	if err == nil {
		return false
	}
	// Context timeout is temporary, but cancellation might not be
	if err == context.DeadlineExceeded {
		return true
	}
	// Only treat context.Canceled as temporary in specific cases
	if err == context.Canceled {
		return false // Let the upper level handle this
	}
	// net.Error is temporary if Timeout is true
	if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
		return true
	}
	// String check for some gRPC errors (example)
	errStr := err.Error()
	if errStr == "transport is closing" || errStr == "connection reset by peer" {
		return true
	}
	return false
}
