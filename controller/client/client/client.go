package client

import (
	"context"
	"sync"
	"time"

	pb "github.com/CloudNativeWorks/elchi-proto/client"
)

// ClientInfo represents a connected client
type ClientInfo struct {
	ID           string                                `json:"id" bson:"_id"`
	ClientID     string                                `json:"client_id" bson:"client_id"`
	Version      string                                `json:"version" bson:"version"`
	Hostname     string                                `json:"hostname" bson:"hostname"`
	Name         string                                `json:"name" bson:"name"`
	OS           string                                `json:"os" bson:"os"`
	Arch         string                                `json:"arch" bson:"arch"`
	Kernel       string                                `json:"kernel" bson:"kernel"`
	Connected    bool                                  `json:"connected" bson:"connected"`
	LastSeen     time.Time                             `json:"last_seen" bson:"last_seen"`
	SessionToken string                                `json:"session_token" bson:"session_token"`
	Metadata     map[string]string                     `json:"metadata" bson:"metadata"`
	Projects     []string                              `json:"projects" bson:"projects"`
	AccessTokens string                                `json:"access_token" bson:"access_token"`
	Stream       pb.CommandService_CommandStreamServer `json:"-" bson:"-"`
	Context      context.Context                       `json:"-" bson:"-"`
	CancelFunc   context.CancelFunc                    `json:"-" bson:"-"`
	mu           sync.RWMutex                          `json:"-" bson:"-"`
}

// NewClientInfo creates a new client info from register request
func NewClientInfo(req *pb.RegisterRequest, sessionToken string) *ClientInfo {
	return &ClientInfo{
		ClientID:     req.GetClientId(),
		Version:      req.GetVersion(),
		Hostname:     req.GetHostname(),
		Name:         req.GetName(),
		OS:           req.GetOs(),
		Arch:         req.GetArch(),
		Kernel:       req.GetKernel(),
		Connected:    true,
		LastSeen:     time.Now(),
		SessionToken: sessionToken,
		Metadata:     req.GetMetadata(),
	}
}

// UpdateLastSeen updates the last seen timestamp
func (c *ClientInfo) UpdateLastSeen() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.LastSeen = time.Now()
}

// IsConnected returns true if client is connected
func (c *ClientInfo) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Connected && c.Stream != nil
}
