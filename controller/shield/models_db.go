// Package shield manages elchi-shield config policies: a project-scoped Mongo
// store of config bundles plus the deploy path that MERGES all of a project's
// policies into one full-sync bundle and pushes it to the project's connected
// edge clients over the command stream, on every policy create/update/delete.
package shield

import (
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// ShieldPolicyCollection is the Mongo collection holding shield config policies.
const ShieldPolicyCollection = "shield_policies"

// ShieldPolicy is a stored elchi-shield config bundle. Files are opaque
// (path + content/download + mode); sha256 is derived at deploy time. A project's
// policies are merged (file paths must be unique across them) into one full-sync
// desired-state bundle, so there is no per-policy full_sync flag.
type ShieldPolicy struct {
	ID        primitive.ObjectID      `bson:"_id,omitempty" json:"id,omitempty"`
	Name      string                  `bson:"name" json:"name"`
	Project   string                  `bson:"project" json:"project"`
	Files     []models.ShieldFileJSON `bson:"files" json:"files"`
	Version   int                     `bson:"version" json:"version"`
	CreatedAt time.Time               `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time               `bson:"updated_at" json:"updated_at"`
}

// ShieldPolicyRequest is the create/update request body.
type ShieldPolicyRequest struct {
	Name    string                  `json:"name" binding:"required"`
	Project string                  `json:"project" binding:"required"`
	Files   []models.ShieldFileJSON `json:"files" binding:"required"`
}

// ShieldPolicyResponse is the API response shape.
type ShieldPolicyResponse struct {
	ID        string                  `json:"id"`
	Name      string                  `json:"name"`
	Project   string                  `json:"project"`
	Files     []models.ShieldFileJSON `json:"files"`
	Version   int                     `json:"version"`
	CreatedAt time.Time               `json:"created_at"`
	UpdatedAt time.Time               `json:"updated_at"`
}

// ToResponse converts a stored policy to its API representation.
func (p *ShieldPolicy) ToResponse() ShieldPolicyResponse {
	return ShieldPolicyResponse{
		ID:        p.ID.Hex(),
		Name:      p.Name,
		Project:   p.Project,
		Files:     p.Files,
		Version:   p.Version,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
}
