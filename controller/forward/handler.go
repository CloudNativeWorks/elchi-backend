package forward

import (
	"context"
	"fmt"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/controller/client/services"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
)

// ForwardHandler handles incoming forward requests from other controllers
type ForwardHandler struct {
	clientService *services.ClientService
	logger        *logger.Logger
}

// NewForwardHandler creates a new forward handler
func NewForwardHandler(clientService *services.ClientService) *ForwardHandler {
	return &ForwardHandler{
		clientService: clientService,
		logger:        logger.NewLogger("controller/forward-handler"),
	}
}

// HandleForwardCommand handles a forwarded command request
func (h *ForwardHandler) HandleForwardCommand(ctx context.Context, req *pb.ForwardCommandRequest) (*pb.ForwardCommandResponse, error) {
	command := req.GetCommand()
	if command == nil {
		return &pb.ForwardCommandResponse{
			Success: false,
			Error:   "command is nil",
		}, nil
	}

	clientID := command.GetIdentity().GetClientId()
	if clientID == "" {
		return &pb.ForwardCommandResponse{
			Success: false,
			Error:   "client_id is empty",
		}, nil
	}

	h.logger.Debugf("Handling forward command for client: %s, type: %s", clientID, command.GetType())

	// Get client connection directly (bypass SendCommand since we have ready command)
	client, err := h.clientService.GetClient(clientID)
	if err != nil {
		h.logger.Errorf("Failed to get client %s: %v", clientID, err)
		return &pb.ForwardCommandResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to get client: %v", err),
		}, nil
	}

	if !client.IsConnected() || client.Stream == nil {
		h.logger.Errorf("Client %s is not connected", clientID)
		return &pb.ForwardCommandResponse{
			Success: false,
			Error:   "client not connected",
		}, nil
	}

	// Create response channel for this command
	respChan := make(chan *pb.CommandResponse, 1)
	h.clientService.SetPendingResponse(command.GetCommandId(), respChan)

	// Cleanup response channel
	defer func() {
		h.clientService.RemovePendingResponse(command.GetCommandId())
		close(respChan)
	}()

	// Send command directly to client (command is already prepared)
	if err := client.Stream.Send(command); err != nil {
		h.logger.Errorf("Failed to send command to client %s: %v", clientID, err)
		return &pb.ForwardCommandResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to send command to client: %v", err),
		}, nil
	}

	// Wait for response with timeout
	select {
	case response := <-respChan:
		return &pb.ForwardCommandResponse{
			Success:  true,
			Error:    "",
			Response: response,
		}, nil
	case <-time.After(15 * time.Second):
		h.logger.Errorf("Command timeout for client %s", clientID)
		return &pb.ForwardCommandResponse{
			Success: false,
			Error:   "command timeout",
		}, nil
	}
} 