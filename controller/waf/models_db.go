package waf

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// WAFConfig represents a WAF configuration document in MongoDB
type WAFConfig struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id,omitempty"`
	Name      string             `bson:"name" json:"name"`
	CreatedAt time.Time          `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time          `bson:"updated_at" json:"updated_at"`
	Project   string             `bson:"project" json:"project"`
	Data      WAFConfigData      `bson:"data" json:"data"`
}

// WAFConfigData contains the actual WAF configuration
type WAFConfigData struct {
	DirectivesMap          map[string][]string `bson:"directives_map" json:"directives_map"`
	DefaultDirectives      string              `bson:"default_directives" json:"default_directives"`
	MetricLabels           map[string]string   `bson:"metric_labels" json:"metric_labels"`
	PerAuthorityDirectives map[string]string   `bson:"per_authority_directives" json:"per_authority_directives"`
}

// WAFConfigRequest represents the request body for creating/updating WAF config
type WAFConfigRequest struct {
	Name    string        `json:"name" binding:"required"`
	Project string        `json:"project" binding:"required"`
	Data    WAFConfigData `json:"data" binding:"required"`
}

// WAFConfigResponse represents the API response for WAF config operations
type WAFConfigResponse struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	Project   string        `json:"project"`
	Data      WAFConfigData `json:"data"`
}

// ToResponse converts WAFConfig to WAFConfigResponse
func (w *WAFConfig) ToResponse() WAFConfigResponse {
	return WAFConfigResponse{
		ID:        w.ID.Hex(),
		Name:      w.Name,
		CreatedAt: w.CreatedAt,
		UpdatedAt: w.UpdatedAt,
		Project:   w.Project,
		Data:      w.Data,
	}
}
