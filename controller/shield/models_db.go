// Package shield manages elchi-shield config policies: a project-scoped Mongo
// store of config bundles plus the deploy path that renders a policy into a
// protobuf ShieldConfig and pushes it to edge clients over the command stream.
package shield

import (
	"strconv"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// ShieldPolicyCollection is the Mongo collection holding shield config policies.
const ShieldPolicyCollection = "shield_policies"

// ShieldPolicy is a stored elchi-shield config bundle. Files are opaque
// (path + content/download + mode); sha256 is derived at deploy time.
type ShieldPolicy struct {
	ID        primitive.ObjectID      `bson:"_id,omitempty" json:"id,omitempty"`
	Name      string                  `bson:"name" json:"name"`
	Project   string                  `bson:"project" json:"project"`
	FullSync  bool                    `bson:"full_sync" json:"full_sync"`
	Files     []models.ShieldFileJSON `bson:"files" json:"files"`
	Version   int                     `bson:"version" json:"version"`
	CreatedAt time.Time               `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time               `bson:"updated_at" json:"updated_at"`
}

// ShieldPolicyRequest is the create/update request body.
type ShieldPolicyRequest struct {
	Name     string `json:"name" binding:"required"`
	Project  string `json:"project" binding:"required"`
	FullSync bool   `json:"full_sync"`
	// Files may be empty when full_sync is set (a clearing bundle); validate()
	// enforces the rule, so no binding:"required" here.
	Files []models.ShieldFileJSON `json:"files"`
}

// ShieldPolicyResponse is the API response shape.
type ShieldPolicyResponse struct {
	ID        string                  `json:"id"`
	Name      string                  `json:"name"`
	Project   string                  `json:"project"`
	FullSync  bool                    `json:"full_sync"`
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
		FullSync:  p.FullSync,
		Files:     p.Files,
		Version:   p.Version,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
}

// ToConfigJSON renders the policy into the deployable config bundle (the bundle
// version is the policy's monotonically-increasing Version, so the edge's
// active_config_version reflects exactly which policy revision it runs).
func (p *ShieldPolicy) ToConfigJSON() models.ShieldConfigJSON {
	return models.ShieldConfigJSON{
		Files:    p.Files,
		FullSync: p.FullSync,
		Version:  strconv.Itoa(p.Version),
	}
}
