package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type Service struct {
	Name      string           `json:"name" bson:"name"`
	Project   string           `json:"project" bson:"project"`
	AdminPort uint32           `json:"admin_port" bson:"admin_port"`
	Clients   []ListenerClient `json:"clients" bson:"clients"`
}

type ListenerClient struct {
	DownstreamAddress string `json:"downstream_address,omitempty" bson:"downstream_address,omitempty"`
	ClientID          string `json:"client_id,omitempty" bson:"client_id,omitempty"`
}

type Envoys struct {
	ID      primitive.ObjectID `bson:"_id,omitempty"        json:"_id"`
	Name    string             `bson:"name"                 json:"name"`
	Project string             `bson:"project"              json:"project"`
	Errors  []ErrorItem        `bson:"errors,omitempty"     json:"errors,omitempty"`
	Envoys  []EnvoyInfo        `bson:"envoys,omitempty"     json:"envoys,omitempty"`
	Status  string             `bson:"status,omitempty"     json:"status,omitempty"`
}

type ErrorItem struct {
	Message       string    `bson:"message"         json:"message"`
	Type          string    `bson:"type"            json:"type"`
	ResponseNonce string    `bson:"response_nonce"  json:"response_nonce"`
	Timestamp     time.Time `bson:"timestamp"       json:"timestamp"`
	NodeID        string    `bson:"nodeid"          json:"nodeid"`
}

type EnvoyInfo struct {
	LastSync       int64  `bson:"lastSync"         json:"lastSync"`
	DownstreamAddr string `bson:"downstream_address" json:"downstream_address"`
	Version        string `bson:"version"          json:"version"`
	ClientName     string `bson:"client_name"      json:"client_name"`
	Connected      bool   `bson:"connected"        json:"connected"`
	NodeID         string `bson:"nodeid"           json:"nodeid"`
	SourceAddr     string `bson:"source_address"   json:"source_address"`
	Connections    int    `bson:"connections"      json:"connections"`
}
