package models

import (
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// ResourceTemplate represents a template for XDS resources
type ResourceTemplate struct {
	ID       primitive.ObjectID `json:"_id,omitempty" bson:"_id,omitempty"`
	GType    string             `json:"gtype" bson:"gtype"`
	Version  string             `json:"version" bson:"version"`
	Project  string             `json:"project" bson:"project"`
	General  *TemplateGeneral   `json:"general,omitempty" bson:"general,omitempty"`
	Resource any                `json:"resource,omitempty" bson:"resource,omitempty"`
}

// TemplateGeneral represents the general section of a template
type TemplateGeneral struct {
	ConfigDiscovery []*ConfigDiscovery `json:"config_discovery,omitempty" bson:"config_discovery,omitempty"`
	TypedConfig     []*TypedConfig     `json:"typed_config,omitempty" bson:"typed_config,omitempty"`
	ElchiDiscovery  []*ElchiDiscovery  `json:"elchi_discovery,omitempty" bson:"elchi_discovery,omitempty"`
}

// ResourceTemplateRequest represents the request body for template creation/update
type ResourceTemplateRequest struct {
	General  *TemplateGeneral `json:"general,omitempty"`
	Resource any              `json:"resource,omitempty"`
}

// ResourceTemplateResponse represents the response body for template read operations (without ID)
type ResourceTemplateResponse struct {
	GType    string           `json:"gtype"`
	Version  string           `json:"version"`
	Project  string           `json:"project"`
	General  *TemplateGeneral `json:"general,omitempty"`
	Resource any              `json:"resource,omitempty"`
}

// ResourceTemplateExistsResponse represents the response for template existence check
type ResourceTemplateExistsResponse struct {
	Exists bool `json:"exists"`
}