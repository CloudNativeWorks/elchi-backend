package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// ResourceSnippet represents a saved configuration snippet for reuse
type ResourceSnippet struct {
	// Identification
	ID   primitive.ObjectID `json:"id" bson:"_id,omitempty"`
	Name string             `json:"name" bson:"name"`

	// Auto-discovered metadata
	ComponentType string `json:"component_type" bson:"component_type"` // "HealthCheck", "OutlierDetection", etc.
	GType         string `json:"gtype" bson:"gtype"`                   // Full GType enum value: "envoy.config.cluster.v3.Cluster"
	FieldPath     string `json:"field_path" bson:"field_path"`         // "health_checks", "outlier_detection"
	IsArray       bool   `json:"is_array" bson:"is_array"`

	// Version & Project
	Version string `json:"version" bson:"version"` // "v1.34.2"
	Project string `json:"project" bson:"project"`

	// Snippet data
	SnippetData any    `json:"snippet_data" bson:"snippet_data"`
	DataHash    string `json:"data_hash" bson:"data_hash"`

	// Timestamps
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt time.Time `json:"updated_at" bson:"updated_at"`
	CreatedBy string    `json:"created_by" bson:"created_by"`
}

// SnippetCreateRequest represents the request to create a new snippet
type SnippetCreateRequest struct {
	Name          string `json:"name" binding:"required"`
	ComponentType string `json:"component_type" binding:"required"`
	GType         string `json:"gtype" binding:"required"`
	FieldPath     string `json:"field_path" binding:"required"`
	IsArray       bool   `json:"is_array"`
	Version       string `json:"version" binding:"required"`
	Project       string `json:"project" binding:"required"`
	SnippetData   any    `json:"snippet_data" binding:"required"`
}

// SnippetUpdateRequest represents the request to update an existing snippet
type SnippetUpdateRequest struct {
	Name        *string `json:"name,omitempty"`
	SnippetData *any    `json:"snippet_data,omitempty"`
}

// SnippetListRequest represents query parameters for listing snippets
type SnippetListRequest struct {
	Project       string `form:"project"`
	Version       string `form:"version"`
	ComponentType string `form:"component_type"`
	GType         string `form:"gtype"`
	Search        string `form:"search"`
	Limit         int    `form:"limit"`
	Offset        int    `form:"offset"`
}

// SnippetBatchCreateRequest represents the request to create multiple snippets
type SnippetBatchCreateRequest struct {
	Snippets []SnippetCreateRequest `json:"snippets" binding:"required,min=1,max=50"`
}

// SnippetBatchDeleteRequest represents the request to delete multiple snippets
type SnippetBatchDeleteRequest struct {
	SnippetIDs []string `json:"snippet_ids" binding:"required,min=1,max=50"`
}

// SnippetListResponse represents the paginated list response
type SnippetListResponse struct {
	Snippets   []ResourceSnippet `json:"snippets"`
	Total      int64             `json:"total"`
	Limit      int               `json:"limit"`
	Offset     int               `json:"offset"`
	HasMore    bool              `json:"has_more"`
	Page       int               `json:"page"`
	TotalPages int               `json:"total_pages"`
}

// SnippetBatchCreateResponse represents the response for batch create
type SnippetBatchCreateResponse struct {
	Created []ResourceSnippet `json:"created"`
	Failed  []SnippetError    `json:"failed,omitempty"`
}

// SnippetBatchDeleteResponse represents the response for batch delete
type SnippetBatchDeleteResponse struct {
	Deleted []string       `json:"deleted"`
	Failed  []SnippetError `json:"failed,omitempty"`
}

// SnippetError represents an error during batch operations
type SnippetError struct {
	Index   int    `json:"index,omitempty"`
	ID      string `json:"id,omitempty"`
	Error   string `json:"error"`
	Message string `json:"message"`
}

// SnippetStats represents statistics about snippets
type SnippetStats struct {
	TotalSnippets      int64                 `json:"total_snippets"`
	SnippetsByProject  map[string]int64      `json:"snippets_by_project"`
	SnippetsByGType    map[string]int64      `json:"snippets_by_gtype"`
	SnippetsByVersion  map[string]int64      `json:"snippets_by_version"`
	MostUsedComponents map[string]int64      `json:"most_used_components"`
	RecentActivity     []SnippetActivityItem `json:"recent_activity"`
}

// SnippetActivityItem represents a recent activity item
type SnippetActivityItem struct {
	Action    string    `json:"action"` // "created", "updated", "deleted"
	SnippetID string    `json:"snippet_id"`
	Name      string    `json:"name"`
	User      string    `json:"user"`
	Timestamp time.Time `json:"timestamp"`
}

// Default pagination values
const (
	DefaultSnippetLimit  = 50
	MaxSnippetLimit      = 100
	DefaultSnippetOffset = 0
)
