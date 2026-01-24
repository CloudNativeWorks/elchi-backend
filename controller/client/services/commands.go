package services

import (
	"fmt"
	"time"

	pb "github.com/CloudNativeWorks/elchi-proto/client"
	"github.com/google/uuid"
)

// SendCommand sends command to client and waits for response
func (s *ClientService) SendCommand(clientID string, cmdType pb.CommandType, subType pb.SubCommandType, payload any) (*pb.CommandResponse, error) {
	s.clientsMux.RLock()
	client, exists := s.clients[clientID]
	if !exists {
		s.clientsMux.RUnlock()
		return nil, fmt.Errorf("client not found/live: %s", clientID)
	}

	sessionToken := client.SessionToken
	identity := &pb.Identity{SessionToken: sessionToken, ClientId: clientID, ClientName: client.Name}
	commandID := uuid.New().String()

	command, err := NewCommandWithPayload(commandID, cmdType, subType, identity, payload)
	if err != nil {
		return nil, err
	}

	if !client.IsConnected() || client.Stream == nil {
		s.clientsMux.RUnlock()
		return nil, fmt.Errorf("client %s is not connected or stream is nil (check if client is running and connected)", clientID)
	}

	stream := client.Stream
	s.clientsMux.RUnlock()

	// Create response channel
	respChan := make(chan *pb.CommandResponse, 1)
	s.pendingMux.Lock()
	s.pendingResponses[commandID] = respChan
	s.pendingMux.Unlock()

	// Channel cleanup with safe close
	defer func() {
		s.pendingMux.Lock()
		delete(s.pendingResponses, commandID)
		s.pendingMux.Unlock()

		// Safe channel close with panic recovery
		func() {
			defer func() {
				if r := recover(); r != nil {
					s.logger.Debugf("Channel already closed (Command ID: %s): %v", commandID, r)
				}
			}()
			close(respChan)
		}()
	}()

	// Send command
	if err := stream.Send(command); err != nil {
		// Check for EOF and connection issues
		if err.Error() == "EOF" {
			// Client disconnected, mark as disconnected
			s.clientsMux.Lock()
			if client, exists := s.clients[clientID]; exists {
				client.Connected = false
			}
			s.clientsMux.Unlock()
			return nil, fmt.Errorf("client %s disconnected (EOF - connection lost)", clientID)
		}

		if err.Error() == "rpc error: code = Unavailable desc = connection closed" {
			return nil, fmt.Errorf("client %s connection closed", clientID)
		}

		return nil, fmt.Errorf("failed to send command to client %s: %w", clientID, err)
	}

	// Set timeout based on command type
	timeout := getCommandTimeout(cmdType)

	// Wait for response
	select {
	case resp := <-respChan:
		return resp, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("command timed out after %v - client %s may be unresponsive", timeout, clientID)
	}
}

// getCommandTimeout returns appropriate timeout for different command types
func getCommandTimeout(cmdType pb.CommandType) time.Duration {
	switch cmdType {
	case pb.CommandType_ENVOY_VERSION:
		// Envoy version commands can take longer due to download operations
		return 120 * time.Second // 2 minutes for downloads
	case pb.CommandType_DEPLOY, pb.CommandType_UNDEPLOY:
		// Deploy operations can be resource intensive
		return 60 * time.Second // 1 minute
	case pb.CommandType_UPDATE_BOOTSTRAP:
		// Bootstrap updates might take some time
		return 30 * time.Second // 30 seconds
	case pb.CommandType_UPGRADE_LISTENER:
		// Listener upgrades can take time for graceful draining and restart
		return 90 * time.Second // 1.5 minutes (includes drain time + restart)
	case pb.CommandType_NETWORK:
		// Network operations might need more time for changes to take effect
		return 45 * time.Second // 45 seconds
	default:
		// Default timeout for other commands
		return 15 * time.Second // 15 seconds
	}
}

// HandleCommandResponse processes command response
func (s *ClientService) HandleCommandResponse(resp *pb.CommandResponse) {
	s.pendingMux.RLock()
	respChan, exists := s.pendingResponses[resp.GetCommandId()]
	s.pendingMux.RUnlock()

	if exists {
		// Safe channel write with panic recovery
		func() {
			defer func() {
				if r := recover(); r != nil {
					s.logger.Debugf("Channel closed during write (Command ID: %s): %v", resp.GetCommandId(), r)
				}
			}()

			select {
			case respChan <- resp:
				s.logger.Debugf("Response forwarded: %s", resp.GetCommandId())
			default:
				s.logger.Debugf("Failed to forward response (channel full): %s", resp.GetCommandId())
			}
		}()
	} else {
		s.logger.Debugf("Unexpected response received: %s", resp.GetCommandId())
	}
}
