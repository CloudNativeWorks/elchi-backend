package models

import "time"

// ControlPlane represents a registered control plane
type ControlPlane struct {
	ID          string    `json:"id"`           // cp-v1-2-1-abc123
	Version     string    `json:"version"`      // v1.2.1
	LastSeen    time.Time `json:"last_seen"`
}

// NodeMapping represents node to control-plane mapping
type NodeMapping struct {
	NodeID         string    `json:"node_id"`         // envoy-node-123
	Version        string    `json:"version"`         // v1.2.1
	ControlPlaneID string    `json:"control_plane_id"` // cp-v1-2-1-abc123
	LastSeen       time.Time `json:"last_seen"`
}

// NodeInfo represents a node connected to a control plane
type NodeInfo struct {
	NodeID   string    `json:"node_id"`
	Version  string    `json:"version"`
	LastSeen time.Time `json:"last_seen"`
} 