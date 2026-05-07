package waf

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// WAFConfig represents a WAF configuration document in MongoDB.
type WAFConfig struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id,omitempty"`
	Name      string             `bson:"name" json:"name"`
	CreatedAt time.Time          `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time          `bson:"updated_at" json:"updated_at"`
	Project   string             `bson:"project" json:"project"`
	Data      WAFConfigData      `bson:"data" json:"data"`
}

// DirectiveSet is a named, ordered list of ModSecurity directives.
// It is the canonical in-memory representation of a single configuration set.
type DirectiveSet struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Directives  []string `json:"directives"`
}

// WAFConfigData is the canonical in-memory shape for a WAF configuration.
//
// IMPORTANT: this struct does not carry json/bson tags. Marshalling is
// handled by custom (Un)Marshal methods in data_marshalling.go that mediate
// between this canonical form and two physical wire formats:
//
//   - HTTP/JSON API uses the **modern** shape (sets, default_set).
//   - MongoDB BSON disk format uses the **legacy** shape
//     (directives_map, default_directives) — and stays legacy forever for
//     rolling-deploy interop. Do not change MarshalBSON without a coordinated
//     fleet restart and migration plan.
//   - WASM-injection JSON uses the **legacy** shape (consumed by the Coraza
//     WASM plugin). See wasm_injector.go.
type WAFConfigData struct {
	Sets                   []DirectiveSet
	DefaultSet             string
	MetricLabels           map[string]string
	PerAuthorityDirectives map[string]string
}

// WAFConfigRequest represents the request body for creating/updating WAF config.
type WAFConfigRequest struct {
	Name    string        `json:"name" binding:"required"`
	Project string        `json:"project" binding:"required"`
	Data    WAFConfigData `json:"data" binding:"required"`
}

// WAFConfigResponse represents the API response for WAF config operations.
type WAFConfigResponse struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	Project   string        `json:"project"`
	Data      WAFConfigData `json:"data"`
}

// ToResponse converts WAFConfig to WAFConfigResponse.
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
