package models

import (
	"time"
)

// AuditAPIType represents the type of API being audited
type AuditAPIType string

const (
	AuditAPITypeREST          AuditAPIType = "REST"
	AuditAPITypeClientCommand AuditAPIType = "CLIENT_COMMAND"
)

// AuditEntry represents an audit log entry that flows through request lifecycle
type AuditEntry struct {
	ID        string    `json:"id" bson:"_id,omitempty"`
	Timestamp time.Time `json:"timestamp" bson:"timestamp"`

	// User Context - populated from auth middleware
	UserID   string   `json:"user_id" bson:"user_id"`
	Username string   `json:"username" bson:"username"`
	UserRole string   `json:"user_role" bson:"user_role"`
	Groups   []string `json:"groups,omitempty" bson:"groups,omitempty"`

	// Request Context - populated in audit middleware
	ClientIP  string `json:"client_ip" bson:"client_ip"`
	UserAgent string `json:"user_agent" bson:"user_agent"`
	RequestID string `json:"request_id" bson:"request_id"` // Unique request ID

	// API Context - populated in audit middleware
	APIType AuditAPIType `json:"api_type" bson:"api_type"`
	Method  string       `json:"method" bson:"method"`
	Path    string       `json:"path" bson:"path"`

	// Business Context - populated in handlers
	Action       string `json:"action" bson:"action"`
	ResourceType string `json:"resource_type" bson:"resource_type"`
	ResourceID   string `json:"resource_id" bson:"resource_id"`
	ResourceName string `json:"resource_name" bson:"resource_name"`
	Project      string `json:"project" bson:"project"`

	// Client Command Specific - populated for client commands from body
	// Stores the entire command body including type, subtype, clients, command details
	Command map[string]any `json:"command,omitempty" bson:"command,omitempty"`

	// Request/Response - populated at the end of request
	ResponseStatus int   `json:"response_status" bson:"response_status"`
	Duration       int64 `json:"duration_ms" bson:"duration_ms"`

	// Changes for PUT requests - populated in handlers
	Changes map[string]interface{} `json:"changes,omitempty" bson:"changes,omitempty"`

	// Result tracking
	Success      bool   `json:"success" bson:"success"`
	ErrorMessage string `json:"error_message,omitempty" bson:"error_message,omitempty"`
}