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

	// Channel cleanup
	defer func() {
		s.pendingMux.Lock()
		delete(s.pendingResponses, commandID)
		s.pendingMux.Unlock()
		close(respChan)
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
		
		return nil, fmt.Errorf("failed to send command to client %s: %v", clientID, err)
	}

	// Wait for response
	select {
	case resp := <-respChan:
		return resp, nil
	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("command timed out after 15 seconds - client %s may be unresponsive", clientID)
	}
}
// HandleCommandResponse processes command response
func (s *ClientService) HandleCommandResponse(resp *pb.CommandResponse) {
	s.pendingMux.RLock()
	respChan, exists := s.pendingResponses[resp.GetCommandId()]
	s.pendingMux.RUnlock()

	if exists {
		select {
		case respChan <- resp:
			s.logger.Debugf("Response forwarded: %s", resp.GetCommandId())
		default:
			s.logger.Debugf("Failed to forward response (channel full): %s", resp.GetCommandId())
		}
	} else {
		s.logger.Debugf("Unexpected response received: %s", resp.GetCommandId())
	}
}

