package models

import (
	"log"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type KnownTYPES string

const (
	EDS        KnownTYPES = "endpoint"
	CDS        KnownTYPES = "cluster"
	LDS        KnownTYPES = "listener"
	ROUTE      KnownTYPES = "route"
	EXTENSIONS KnownTYPES = "extensions"
	FILTERS    KnownTYPES = "filters"
	ACCESSLOG  KnownTYPES = "access_log"
)

type ScenarioBody map[string]any

type ResourceClass interface {
	GetGeneral() General
	GetGtype() GTypes
	SetGeneral(general *General)
	GetResource() any
	SetResource(resource any)
	GetVersion() any
	GetConfigDiscovery() []*ConfigDiscovery
	GetTypedConfig() []*TypedConfig
	SetTypedConfig(typedConfig []*TypedConfig)
	SetVersion(versionRaw any)
	SetPermissions(permissions *Permissions)
	SetManaged(managed bool)
	SetID(id primitive.ObjectID)
	GetID() string
	GetManaged() bool

	SetBootstrapClusters(clusters []any)
	SetBootstrapAccessLoggers(accessLoggers []any)
	SetBootstrapStatSinks(statSinks []any)
}

type General struct {
	Name            string             `json:"name" bson:"name"`
	Version         string             `json:"version" bson:"version"`
	Type            KnownTYPES         `json:"type" bson:"type"`
	GType           GTypes             `json:"gtype" bson:"gtype"`
	Project         string             `json:"project" bson:"project"`
	Collection      string             `json:"collection" bson:"collection"`
	CanonicalName   string             `json:"canonical_name" bson:"canonical_name"`
	Category        string             `json:"category" bson:"category"`
	Managed         bool               `json:"managed,omitempty" bson:"managed,omitempty"`
	Metadata        map[string]any     `json:"metadata" bson:"metadata"`
	Permissions     Permissions        `json:"permissions" bson:"permissions"`
	ConfigDiscovery []*ConfigDiscovery `json:"config_discovery,omitempty" bson:"config_discovery,omitempty"`
	TypedConfig     []*TypedConfig     `json:"typed_config,omitempty" bson:"typed_config,omitempty"`
	CreatedAt       primitive.DateTime `json:"created_at,omitempty" bson:"created_at,omitempty"`
	UpdatedAt       primitive.DateTime `json:"updated_at,omitempty" bson:"updated_at,omitempty"`
}

type Permissions struct {
	Users  []string `json:"users" bson:"users"`
	Groups []string `json:"groups" bson:"groups"`
}

type ConfigDiscovery struct {
	ParentName    string `json:"parent_name,omitempty" bson:"parent_name,omitempty"`
	GType         GTypes `json:"gtype" bson:"gtype"`
	Name          string `json:"name" bson:"name"`
	Priority      int    `json:"priority" bson:"priority"`
	Category      string `json:"category" bson:"category"`
	CanonicalName string `json:"canonical_name" bson:"canonical_name"`
}

type TypedConfig struct {
	Name          string `json:"name" bson:"name"`
	CanonicalName string `json:"canonical_name" bson:"canonical_name"`
	Gtype         GTypes `json:"gtype" bson:"gtype"`
	Type          string `json:"type" bson:"type"`
	Category      string `json:"category" bson:"category"`
	Collection    string `json:"collection" bson:"collection"`
	Disabled      bool   `json:"disabled" bson:"disabled"`
	Priority      int    `json:"priority" bson:"priority"`
	ParentName    string `json:"parent_name" bson:"parent_name"`
}

type TC struct {
	Name        string         `json:"name" bson:"name"`
	TypedConfig map[string]any `json:"typed_config" bson:"typed_config"`
}

type DBResource struct {
	ID       primitive.ObjectID `json:"id,omitempty" bson:"_id,omitempty"`
	General  General            `json:"general" bson:"general"`
	Resource Resource           `json:"resource" bson:"resource"`
}

type Resource struct {
	Version  string `json:"version" bson:"version"`
	Resource any    `json:"resource" bson:"resource"`
}

func (d *DBResource) GetGeneral() General {
	return d.General
}

func (d *DBResource) GetGtype() GTypes {
	return d.General.GType
}

func (d *DBResource) GetResource() any {
	return d.Resource.Resource
}

func (d *DBResource) GetVersion() any {
	return d.Resource.Version
}

func (d *DBResource) GetConfigDiscovery() []*ConfigDiscovery {
	return d.General.ConfigDiscovery
}

func (d *DBResource) GetTypedConfig() []*TypedConfig {
	return d.General.TypedConfig
}

func (d *DBResource) GetID() string {
	return d.ID.Hex()
}

func (d *DBResource) SetID(id primitive.ObjectID) {
	d.ID = id
}

func (d *DBResource) GetManaged() bool {
	return d.General.Managed
}

func (d *DBResource) SetTypedConfig(typedConfig []*TypedConfig) {
	d.General.TypedConfig = typedConfig
}

func (d *DBResource) SetVersion(versionRaw any) {
	version, ok := versionRaw.(string)
	if !ok {
		d.Resource.Version = "0"
		return
	}
	d.Resource.Version = version
}

func (d *DBResource) SetResource(resource any) {
	d.Resource.Resource = resource
}

func (d *DBResource) SetBootstrapClusters(clusters []any) {
	resourceMap, ok := d.Resource.Resource.(primitive.M)
	if !ok {
		log.Printf("failed to parse Resource.Resource as map[string]any, got type: %T\n", d.Resource.Resource)
		return
	}

	staticResources, ok := resourceMap["static_resources"].(primitive.M)
	if !ok || staticResources == nil {
		staticResources = make(primitive.M)
	}

	staticResources["clusters"] = clusters
	resourceMap["static_resources"] = staticResources
	d.Resource.Resource = resourceMap
}

func (d *DBResource) SetBootstrapAccessLoggers(accessLoggers []any) {
	resourceMap, ok := d.Resource.Resource.(primitive.M)
	if !ok {
		log.Printf("failed to parse Resource.Resource as map[string]any, got type: %T\n", d.Resource.Resource)
		return
	}

	admin, ok := resourceMap["admin"].(primitive.M)
	if !ok || admin == nil {
		admin = make(primitive.M)
	}

	admin["access_log"] = accessLoggers
	resourceMap["admin"] = admin
	d.Resource.Resource = resourceMap
}

func (d *DBResource) SetBootstrapStatSinks(statSinks []any) {
	resourceMap, ok := d.Resource.Resource.(primitive.M)
	if !ok {
		log.Printf("failed to parse Resource.Resource as map[string]any, got type: %T\n", d.Resource.Resource)
		return
	}

	resourceMap["stats_sinks"] = statSinks
	d.Resource.Resource = resourceMap
}

func (d *DBResource) SetGeneral(general *General) {
	d.General = *general
}

func (d *DBResource) SetPermissions(permissions *Permissions) {
	d.General.Permissions = *permissions
}

func (d *DBResource) SetManaged(managed bool) {
	d.General.Managed = managed
}
