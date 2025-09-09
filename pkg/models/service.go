package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type Service struct {
	Name        string           `json:"name" bson:"name"`
	Project     string           `json:"project" bson:"project"`
	Version     string           `json:"version" bson:"version"`
	AdminPort   uint32           `json:"admin_port" bson:"admin_port"`
	Clients     []ServiceClients `json:"clients" bson:"clients"`
	Permissions Permissions      `json:"permissions" bson:"permissions"`
}

type Envoys struct {
	ID             primitive.ObjectID   `bson:"_id,omitempty"        json:"_id"`
	Name           string               `bson:"name"                 json:"name"`
	Project        string               `bson:"project"              json:"project"`
	EnhancedErrors []EnhancedErrorEntry `bson:"enhanced_errors,omitempty" json:"enhanced_errors,omitempty"` // Enhanced errors only
	Envoys         []EnvoyInfo          `bson:"envoys,omitempty"     json:"envoys,omitempty"`
	Status         string               `bson:"status,omitempty"     json:"status,omitempty"`
}

// Enhanced error entry with rich metadata
type EnhancedErrorEntry struct {
	ID               string     `bson:"_id" json:"id"`
	Message          string     `bson:"message" json:"message"`
	UserFriendlyMsg  string     `bson:"user_friendly_message" json:"userFriendlyMessage"`
	ResourceType     string     `bson:"resource_type" json:"resourceType"`
	ResourceName     string     `bson:"resource_name" json:"resourceName"`
	ResponseNonce    string     `bson:"response_nonce" json:"responseNonce"`
	NodeID           string     `bson:"nodeid" json:"nodeId"`
	Severity         string     `bson:"severity" json:"severity"`
	Status           string     `bson:"status" json:"status"`
	Timestamp        time.Time  `bson:"timestamp" json:"timestamp"`
	ResolvedAt       *time.Time `bson:"resolved_at,omitempty" json:"resolvedAt,omitempty"`
	ResolvedBy       string     `bson:"resolved_by,omitempty" json:"resolvedBy,omitempty"`
	FirstOccurred    time.Time  `bson:"first_occurred" json:"firstOccurred"`
	OccurrenceCount  int        `bson:"occurrence_count" json:"occurrenceCount"`
	LastOccurred     time.Time  `bson:"last_occurred" json:"lastOccurred"`
	AffectedServices []string   `bson:"affected_services" json:"affectedServices"`
	SuggestedFix     string     `bson:"suggested_fix,omitempty" json:"suggestedFix,omitempty"`
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
