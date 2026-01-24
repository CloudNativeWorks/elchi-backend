// Package models provides data structures for the registry service
// including controller and control-plane information.
package models

import "time"

// ControllerInfo represents a controller instance
type ControllerInfo struct {
	ID          string    `json:"controller_id" bson:"controller_id"`
	Version     string    `json:"version" bson:"version"` // v1.2.1
	HTTPAddress string    `json:"http_address" bson:"http_address"`
	LastSeen    time.Time `json:"last_seen" bson:"last_seen"`
}

// ClientMapping represents client to controller mapping (similar to NodeMapping in control-plane)
type ClientMapping struct {
	ClientID     string    `json:"client_id" bson:"client_id"`
	Version      string    `json:"version" bson:"version"` // v1.2.1
	ControllerID string    `json:"controller_id" bson:"controller_id"`
	LastSeen     time.Time `json:"last_seen" bson:"last_seen"`
}

// ClientInfo represents information about a connected client
type ClientInfo struct {
	ClientID string    `json:"client_id"`
	Version  string    `json:"version"`
	LastSeen time.Time `json:"last_seen"`
}

// ControllerRegistryData represents all controller data in the registry for reporting
type ControllerRegistryData struct {
	Controllers         []*ControllerInfo        `json:"controllers"`
	ClientsByController map[string][]*ClientInfo `json:"clients_by_controller"`
}
