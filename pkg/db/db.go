// Package db provides MongoDB database connectivity and context management
// including connection handling, default resources, and scenario initialization.
package db

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/errstr"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

type AppContext struct {
	Client *mongo.Database
	Logger *logger.Logger
	Config *config.AppConfig
}

var (
	adminUser                      = "admin"
	adminEmail                     = "admin@elchi.io"
	adminRole          models.Role = "owner"
	adminActive                    = true
	adminBaseGroup                 = ""
	generalProject                 = "general.project"
	generalName                    = "general.name"
	generalVersion                 = "general.version"
	generalNameProject             = "general_name_version_project_1"
)

var Indices = map[string]mongo.IndexModel{
	"users":              {Keys: bson.M{"username": 1}, Options: options.Index().SetUnique(true).SetName("username_1").SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"groups":             {Keys: bson.D{{Key: "groupname", Value: 1}, {Key: "project", Value: 1}}, Options: options.Index().SetUnique(true).SetName("groupname_project_1").SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"services":           {Keys: bson.D{{Key: "name", Value: 1}, {Key: "project", Value: 1}}, Options: options.Index().SetUnique(true).SetName("name_project_1").SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"clusters":           {Keys: bson.D{{Key: generalName, Value: 1}, {Key: generalVersion, Value: 1}, {Key: generalProject, Value: 1}}, Options: options.Index().SetUnique(true).SetName(generalNameProject).SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"listeners":          {Keys: bson.D{{Key: generalName, Value: 1}, {Key: generalProject, Value: 1}}, Options: options.Index().SetUnique(true).SetName("general_name_project_1").SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"endpoints":          {Keys: bson.D{{Key: generalName, Value: 1}, {Key: generalVersion, Value: 1}, {Key: generalProject, Value: 1}}, Options: options.Index().SetUnique(true).SetName(generalNameProject).SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"routes":             {Keys: bson.D{{Key: generalName, Value: 1}, {Key: generalVersion, Value: 1}, {Key: generalProject, Value: 1}}, Options: options.Index().SetUnique(true).SetName(generalNameProject).SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"virtual_hosts":      {Keys: bson.D{{Key: generalName, Value: 1}, {Key: generalVersion, Value: 1}, {Key: generalProject, Value: 1}}, Options: options.Index().SetUnique(true).SetName(generalNameProject).SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"filters":            {Keys: bson.D{{Key: generalName, Value: 1}, {Key: generalVersion, Value: 1}, {Key: generalProject, Value: 1}}, Options: options.Index().SetUnique(true).SetName(generalNameProject).SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"secrets":            {Keys: bson.D{{Key: generalName, Value: 1}, {Key: generalVersion, Value: 1}, {Key: generalProject, Value: 1}}, Options: options.Index().SetUnique(true).SetName(generalNameProject).SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"extensions":         {Keys: bson.D{{Key: generalName, Value: 1}, {Key: generalVersion, Value: 1}, {Key: generalProject, Value: 1}}, Options: options.Index().SetUnique(true).SetName(generalNameProject).SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"bootstrap":          {Keys: bson.D{{Key: generalName, Value: 1}, {Key: generalVersion, Value: 1}, {Key: generalProject, Value: 1}}, Options: options.Index().SetUnique(true).SetName(generalNameProject).SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"tls":                {Keys: bson.D{{Key: generalName, Value: 1}, {Key: generalVersion, Value: 1}, {Key: generalProject, Value: 1}}, Options: options.Index().SetUnique(true).SetName(generalNameProject).SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"envoys":             {Keys: bson.D{{Key: "name", Value: 1}, {Key: "project", Value: 1}}, Options: options.Index().SetUnique(true).SetName("name_project_1").SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"projects":           {Keys: bson.M{"projectname": 1}, Options: options.Index().SetUnique(true).SetName("projectname_1").SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"clients":            {Keys: bson.M{"client_id": 1}, Options: options.Index().SetUnique(true).SetName("client_id_1").SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"settings":           {Keys: bson.M{"project": 1}, Options: options.Index().SetUnique(true).SetName("project_name_1").SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"scenarios":          {Keys: bson.M{"scenario_id": 1}, Options: options.Index().SetUnique(true).SetName("scenario_id_1").SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"audit_logs":         {Keys: bson.M{"id": 1}, Options: options.Index().SetUnique(true).SetName("id_1").SetSparse(true)},
	"resource_templates": {Keys: bson.D{{Key: "gtype", Value: 1}, {Key: "version", Value: 1}, {Key: "project", Value: 1}}, Options: options.Index().SetUnique(true).SetName("gtype_version_project_1").SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"snippets":           {Keys: bson.D{{Key: "name", Value: 1}, {Key: "project", Value: 1}, {Key: "gtype", Value: 1}}, Options: options.Index().SetUnique(true).SetName("name_project_gtype_1").SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"admin_ports":        {Keys: bson.D{{Key: "name", Value: 1}, {Key: "project", Value: 1}, {Key: "version", Value: 1}}, Options: options.Index().SetUnique(true).SetName("name_project_version_1").SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	// ACME certificate management collections (supports multiple CAs: Let's Encrypt, Google Trust Services, etc.)
	// NOTE: One certificate can be used by multiple Envoy versions (stored in secret_versions array)
	"acme_certificates":    {Keys: bson.D{{Key: "secret_name", Value: 1}, {Key: "project", Value: 1}}, Options: options.Index().SetUnique(true).SetName("secret_name_project_1").SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"acme_dns_credentials": {Keys: bson.D{{Key: "name", Value: 1}, {Key: "project", Value: 1}}, Options: options.Index().SetUnique(true).SetName("name_project_1").SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"acme_temp_keys":       {Keys: bson.M{"cert_request_id": 1}, Options: options.Index().SetUnique(true).SetName("cert_request_id_1")},
	"acme_accounts":        {Keys: bson.D{{Key: "email", Value: 1}, {Key: "ca_provider", Value: 1}, {Key: "environment", Value: 1}, {Key: "project", Value: 1}}, Options: options.Index().SetUnique(true).SetName("email_ca_provider_environment_project_1").SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	// GSLB records collection - Global Server Load Balancing DNS records
	"gslb_records": {Keys: bson.D{{Key: "project", Value: 1}, {Key: "fqdn", Value: 1}}, Options: options.Index().SetUnique(true).SetName("project_fqdn_1")},
	// GSLB shards collection - Shard ownership for distributed health checking
	"gslb_shards": {Keys: bson.D{{Key: "shard_id", Value: 1}}, Options: options.Index().SetUnique(true).SetName("shard_id_1")},
	// GSLB IP health collection - IP health state tracking separate from records
	"gslb_ip_health": {Keys: bson.D{{Key: "record_id", Value: 1}, {Key: "ip", Value: 1}}, Options: options.Index().SetUnique(true).SetName("record_id_ip_1")},
	// GSLB nodes collection - Tracks elchi-gslb instances that fetch DNS records
	"gslb_nodes": {Keys: bson.D{{Key: "node_ip", Value: 1}, {Key: "zone", Value: 1}}, Options: options.Index().SetUnique(true).SetName("node_ip_zone_1")},
}

// TextSearchIndices defines text search indexes for secure search functionality
var TextSearchIndices = map[string]mongo.IndexModel{
	// Text search indexes for XDS resources - secure alternative to regex search
	"clusters_text": {
		Keys: bson.D{
			{Key: "general.name", Value: "text"},
			{Key: "general.canonical_name", Value: "text"},
		},
		Options: options.Index().SetName("clusters_text_search").SetDefaultLanguage("english"),
	},
	"listeners_text": {
		Keys: bson.D{
			{Key: "general.name", Value: "text"},
			{Key: "general.canonical_name", Value: "text"},
		},
		Options: options.Index().SetName("listeners_text_search").SetDefaultLanguage("english"),
	},
	"endpoints_text": {
		Keys: bson.D{
			{Key: "general.name", Value: "text"},
			{Key: "general.canonical_name", Value: "text"},
		},
		Options: options.Index().SetName("endpoints_text_search").SetDefaultLanguage("english"),
	},
	"routes_text": {
		Keys: bson.D{
			{Key: "general.name", Value: "text"},
			{Key: "general.canonical_name", Value: "text"},
		},
		Options: options.Index().SetName("routes_text_search").SetDefaultLanguage("english"),
	},
	"virtual_hosts_text": {
		Keys: bson.D{
			{Key: "general.name", Value: "text"},
			{Key: "general.canonical_name", Value: "text"},
		},
		Options: options.Index().SetName("virtual_hosts_text_search").SetDefaultLanguage("english"),
	},
	"filters_text": {
		Keys: bson.D{
			{Key: "general.name", Value: "text"},
			{Key: "general.canonical_name", Value: "text"},
		},
		Options: options.Index().SetName("filters_text_search").SetDefaultLanguage("english"),
	},
	"secrets_text": {
		Keys: bson.D{
			{Key: "general.name", Value: "text"},
			{Key: "general.canonical_name", Value: "text"},
		},
		Options: options.Index().SetName("secrets_text_search").SetDefaultLanguage("english"),
	},
	"extensions_text": {
		Keys: bson.D{
			{Key: "general.name", Value: "text"},
			{Key: "general.canonical_name", Value: "text"},
		},
		Options: options.Index().SetName("extensions_text_search").SetDefaultLanguage("english"),
	},
	"bootstrap_text": {
		Keys: bson.D{
			{Key: "general.name", Value: "text"},
			{Key: "general.canonical_name", Value: "text"},
		},
		Options: options.Index().SetName("bootstrap_text_search").SetDefaultLanguage("english"),
	},
	"tls_text": {
		Keys: bson.D{
			{Key: "general.name", Value: "text"},
			{Key: "general.canonical_name", Value: "text"},
		},
		Options: options.Index().SetName("tls_text_search").SetDefaultLanguage("english"),
	},
	"scenarios_text": {
		Keys: bson.D{
			{Key: "name", Value: "text"},
			{Key: "description", Value: "text"},
		},
		Options: options.Index().SetName("scenarios_text_search").SetDefaultLanguage("english"),
	},
	"snippets_text": {
		Keys: bson.D{
			{Key: "name", Value: "text"},
			{Key: "component_type", Value: "text"},
			{Key: "field_path", Value: "text"},
		},
		Options: options.Index().SetName("snippets_text_search").SetDefaultLanguage("english"),
	},
}

// AuditIndices defines specialized indexes for audit_logs collection
var AuditIndices = map[string]mongo.IndexModel{
	// Primary query index - timestamp desc for recent entries first
	"audit_timestamp": {
		Keys:    bson.D{{Key: "timestamp", Value: -1}},
		Options: options.Index(),
	},

	// User activity queries - most common filter
	"audit_user_timestamp": {
		Keys:    bson.D{{Key: "user_id", Value: 1}, {Key: "timestamp", Value: -1}},
		Options: options.Index(),
	},

	// Username search queries (case-insensitive)
	"audit_username": {
		Keys:    bson.M{"username": 1},
		Options: options.Index().SetCollation(&options.Collation{Locale: "en", Strength: 2}),
	},

	// Action-based queries for security monitoring
	"audit_action_timestamp": {
		Keys:    bson.D{{Key: "action", Value: 1}, {Key: "timestamp", Value: -1}},
		Options: options.Index(),
	},

	// Resource-specific audit trails
	"audit_resource": {
		Keys: bson.D{
			{Key: "resource_type", Value: 1},
			{Key: "resource_id", Value: 1},
			{Key: "timestamp", Value: -1},
		},
		Options: options.Index(),
	},

	// Project-based queries for multi-tenant filtering
	"audit_project_timestamp": {
		Keys:    bson.D{{Key: "project", Value: 1}, {Key: "timestamp", Value: -1}},
		Options: options.Index(),
	},

	// Error monitoring - failed operations
	"audit_errors": {
		Keys:    bson.D{{Key: "success", Value: 1}, {Key: "timestamp", Value: -1}},
		Options: options.Index(),
	},

	// API type filtering (REST vs CLIENT_COMMAND)
	"audit_api_type": {
		Keys:    bson.D{{Key: "api_type", Value: 1}, {Key: "timestamp", Value: -1}},
		Options: options.Index(),
	},

	// Compound index for dashboard statistics queries
	"audit_stats": {
		Keys: bson.D{
			{Key: "project", Value: 1},
			{Key: "resource_type", Value: 1},
			{Key: "timestamp", Value: -1},
		},
		Options: options.Index(),
	},

	// Performance index for cleanup operations
	"audit_cleanup": {
		Keys:    bson.D{{Key: "timestamp", Value: 1}},
		Options: options.Index(),
	},

	// Request ID for debugging and tracing
	"audit_request_id": {
		Keys:    bson.M{"request_id": 1},
		Options: options.Index(),
	},
}

// GSLBIndices defines specialized indexes for GSLB collections
var GSLBIndices = map[string][]mongo.IndexModel{
	"gslb_records": {
		// Shard-based queries (CRITICAL for distributed health checking)
		{
			Keys:    bson.D{{Key: "shard_id", Value: 1}, {Key: "enabled", Value: 1}},
			Options: options.Index().SetName("shard_id_enabled_1"),
		},
		// Time Wheel queries records with matching interval: GetRecordsByShards(shards, interval)
		{
			Keys: bson.D{
				{Key: "shard_id", Value: 1},
				{Key: "sub_shard_id", Value: 1},
				{Key: "probe.interval", Value: 1},
			},
			Options: options.Index().SetName("idx_shard_interval"),
		},
		{
			Keys:    bson.D{{Key: "probe.enabled", Value: 1}},
			Options: options.Index().SetName("idx_probe_enabled").SetSparse(true),
		},
		// Service reference queries (for auto-created records)
		{
			Keys:    bson.D{{Key: "service_id", Value: 1}, {Key: "project", Value: 1}, {Key: "version", Value: 1}},
			Options: options.Index().SetName("service_id_project_version_1"),
		},
		// Zone-based queries (CRITICAL for DNS API query performance)
		{
			Keys:    bson.D{{Key: "zone", Value: 1}, {Key: "project", Value: 1}, {Key: "enabled", Value: 1}},
			Options: options.Index().SetName("zone_project_enabled_1"),
		},
	},
	"gslb_shards": {
		// Lease expiry queries (for shard acquisition)
		{
			Keys:    bson.M{"lease_expiry": 1},
			Options: options.Index().SetName("lease_expiry_1"),
		},
		// Controller-based queries (for shard management)
		{
			Keys:    bson.M{"controller_id": 1},
			Options: options.Index().SetName("controller_id_1"),
		},

		// Query: Find shards with (lease_expiry < now OR controller_id = "")
		// This index covers both conditions efficiently
		{
			Keys:    bson.D{{Key: "lease_expiry", Value: 1}, {Key: "controller_id", Value: 1}},
			Options: options.Index().SetName("idx_lease_controller"),
		},
	},
	"gslb_ip_health": {
		// Primary lookup: Get all IPs for a record
		{
			Keys:    bson.D{{Key: "record_id", Value: 1}},
			Options: options.Index().SetName("record_id_1"),
		},
		// Health checker query: Get IPs for owned shards (CRITICAL for distributed health checking)
		{
			Keys:    bson.D{{Key: "shard_id", Value: 1}, {Key: "sub_shard_id", Value: 1}},
			Options: options.Index().SetName("shard_id_sub_shard_id_1"),
		},
		// Query: shard_id + sub_shard_id + backoff_until check (filters out IPs in circuit breaker)
		// This optimizes the $or condition for backoff expiry checks (2-3x faster)
		{
			Keys: bson.D{
				{Key: "shard_id", Value: 1},
				{Key: "sub_shard_id", Value: 1},
				{Key: "backoff_until", Value: 1},
			},
			Options: options.Index().SetName("idx_shard_backoff"),
		},
		// Query: GetWarningIPs(shards) -> finds IPs in warning state for fast-fail detection
		{
			Keys: bson.D{
				{Key: "health_state", Value: 1},
				{Key: "shard_id", Value: 1},
				{Key: "sub_shard_id", Value: 1},
			},
			Options: options.Index().
				SetName("idx_warning_state").
				SetPartialFilterExpression(bson.D{{Key: "health_state", Value: "warning"}}),
		},
		// Query: Filter IPs where backoff expired (backoff_until <= now)
		{
			Keys: bson.D{
				{Key: "backoff_until", Value: 1},
				{Key: "health_state", Value: 1},
			},
			Options: options.Index().
				SetName("idx_backoff_expiry").
				SetPartialFilterExpression(bson.D{
					{Key: "health_state", Value: "critical"},
					{Key: "backoff_until", Value: bson.D{{Key: "$exists", Value: true}}},
				}),
		},
		// IP uniqueness per record (removed from here - already in Indices as unique)
		// Circuit breaker queries (filter out backed-off IPs)
		{
			Keys:    bson.D{{Key: "backoff_until", Value: 1}},
			Options: options.Index().SetName("backoff_until_1"),
		},
		// DNS API query: Get healthy IPs by FQDN (CRITICAL for DNS performance)
		{
			Keys:    bson.D{{Key: "fqdn", Value: 1}, {Key: "health_state", Value: 1}},
			Options: options.Index().SetName("fqdn_health_state_1"),
		},
		{
			Keys:    bson.D{{Key: "fqdn", Value: 1}, {Key: "healthy", Value: 1}},
			Options: options.Index().SetName("idx_fqdn_healthy"),
		},
		// Project-based filtering (for API list operations)
		{
			Keys:    bson.D{{Key: "fqdn", Value: 1}},
			Options: options.Index().SetName("fqdn_1"),
		},
		// Client-based queries (for client undeploy operations)
		{
			Keys:    bson.D{{Key: "client_id", Value: 1}},
			Options: options.Index().SetName("client_id_1"),
		},
	},
}

// ACMEIndices defines specialized indexes for ACME collections
var ACMEIndices = map[string][]mongo.IndexModel{
	"acme_certificates": {
		// Project-based queries (CRITICAL for project isolation)
		{
			Keys:    bson.M{"project": 1},
			Options: options.Index().SetName("project_1"),
		},
		// Status-based queries for renewal scheduler
		{
			Keys:    bson.M{"status": 1},
			Options: options.Index().SetName("status_1"),
		},
		// Renewal window queries
		{
			Keys:    bson.D{{Key: "renewal_starts_at", Value: 1}, {Key: "auto_renew", Value: 1}},
			Options: options.Index().SetName("renewal_starts_at_auto_renew_1"),
		},
		// Permission-based queries
		{
			Keys:    bson.M{"permissions.groups": 1},
			Options: options.Index().SetName("permissions_groups_1"),
		},
		{
			Keys:    bson.M{"permissions.users": 1},
			Options: options.Index().SetName("permissions_users_1"),
		},
		// Expiration monitoring
		{
			Keys:    bson.M{"expires_at": 1},
			Options: options.Index().SetName("expires_at_1"),
		},
		// DNS credential lookup
		{
			Keys:    bson.M{"dns_verification.dns_credential_id": 1},
			Options: options.Index().SetName("dns_credential_id_1").SetSparse(true),
		},
	},
	"acme_accounts": {
		// Project-based queries
		{
			Keys:    bson.M{"project": 1},
			Options: options.Index().SetName("project_1"),
		},
		// Query optimization for account listing
		{
			Keys: bson.D{
				{Key: "project", Value: 1},
				{Key: "ca_provider", Value: 1},
				{Key: "status", Value: 1},
			},
			Options: options.Index().SetName("project_ca_provider_status_1"),
		},
		// Permission-based filtering
		{
			Keys:    bson.M{"permissions.groups": 1},
			Options: options.Index().SetName("permissions_groups_1"),
		},
		{
			Keys:    bson.M{"permissions.users": 1},
			Options: options.Index().SetName("permissions_users_1"),
		},
		// Certificate count tracking
		{
			Keys: bson.D{
				{Key: "project", Value: 1},
				{Key: "certificate_count", Value: 1},
			},
			Options: options.Index().SetName("project_certificate_count_1"),
		},
	},
	"acme_dns_credentials": {
		// Project-based queries (CRITICAL for project isolation)
		{
			Keys:    bson.M{"project": 1},
			Options: options.Index().SetName("project_1"),
		},
		// Provider filtering
		{
			Keys:    bson.M{"provider": 1},
			Options: options.Index().SetName("provider_1"),
		},
		// Status monitoring
		{
			Keys:    bson.M{"status": 1},
			Options: options.Index().SetName("status_1"),
		},
		// Permission-based queries
		{
			Keys:    bson.M{"permissions.groups": 1},
			Options: options.Index().SetName("permissions_groups_1"),
		},
		{
			Keys:    bson.M{"permissions.users": 1},
			Options: options.Index().SetName("permissions_users_1"),
		},
	},
	"acme_temp_keys": {
		// Project-based queries
		{
			Keys:    bson.M{"project": 1},
			Options: options.Index().SetName("project_1"),
		},
		// Status filtering
		{
			Keys:    bson.M{"status": 1},
			Options: options.Index().SetName("status_1"),
		},
		// Regular index for sorting by expiration (not TTL - temp keys only deleted on success)
		{
			Keys:    bson.M{"expires_at": 1},
			Options: options.Index().SetName("expires_at_1"),
		},
	},
}

func buildMongoDBConnectionString(config *config.AppConfig) string {
	// Default scheme if not provided
	scheme := config.MongodbScheme
	if scheme == "" {
		scheme = "mongodb"
	}

	u := &url.URL{
		Scheme: scheme,
		Host:   config.MongodbHosts,
	}

	if config.MongodbUsername != "" && config.MongodbPassword != "" {
		u.User = url.UserPassword(config.MongodbUsername, config.MongodbPassword)
	}

	// Handle multiple hosts and ports
	if scheme != "mongodb+srv" {
		hosts := strings.Split(config.MongodbHosts, ",")
		var processedHosts []string

		for _, host := range hosts {
			host = strings.TrimSpace(host)
			if host == "" {
				continue
			}

			// Check if host already has port
			if strings.Contains(host, ":") {
				processedHosts = append(processedHosts, host)
			} else {
				// Add port from config or default 27017
				port := config.MongodbPort
				if port == "" {
					port = "27017"
				}
				processedHosts = append(processedHosts, fmt.Sprintf("%s:%s", host, port))
			}
		}

		if len(processedHosts) > 0 {
			u.Host = strings.Join(processedHosts, ",")
		}
	}

	query := url.Values{}
	if config.MongodbReplicaSet != "" {
		query.Add("replicaSet", config.MongodbReplicaSet)
	}
	if config.MongodbTimeoutMs != "" {
		query.Add("connectTimeoutMS", config.MongodbTimeoutMs)
	}
	if config.MongodbAuthSource != "" {
		query.Add("authSource", config.MongodbAuthSource)
	}
	if config.MongodbAuthMechanism != "" {
		query.Add("authMechanism", config.MongodbAuthMechanism)
	}

	// Handle TLS properly
	if config.MongodbTLSEnabled != "" {
		query.Add("tls", config.MongodbTLSEnabled)
	}

	// Add database name to path before query parameters
	if config.MongodbDatabase != "" {
		u.Path = "/" + config.MongodbDatabase
	}

	u.RawQuery = query.Encode()

	return u.String()
}

func NewMongoDB(config *config.AppConfig, createDefaultResources bool) *AppContext {
	logger := logger.NewLogger("database")
	connectionString := buildMongoDBConnectionString(config)

	// Log MongoDB configuration details (without password for security)
	logger.Infof("MongoDB Configuration Details:")
	logger.Infof("  - Hosts: %s", config.MongodbHosts)
	logger.Infof("  - Database: %s", config.MongodbDatabase)
	logger.Infof("  - Username: %s", config.MongodbUsername)
	logger.Infof("  - Scheme: %s", config.MongodbScheme)
	logger.Infof("  - Port: %s", config.MongodbPort)
	logger.Infof("  - Replica Set: %s", config.MongodbReplicaSet)
	logger.Infof("  - Timeout: %s ms", config.MongodbTimeoutMs)
	logger.Infof("  - TLS Enabled: %s", config.MongodbTLSEnabled)
	logger.Infof("  - Auth Source: %s", config.MongodbAuthSource)
	logger.Infof("  - Auth Mechanism: %s", config.MongodbAuthMechanism)

	// Mask password in connection string for logging
	maskedConnectionString := strings.ReplaceAll(connectionString, config.MongodbPassword, "***MASKED***")
	logger.Infof("Connection String: %s", maskedConnectionString)

	tM := reflect.TypeOf(bson.M{})
	reg := bson.NewRegistry()
	reg.RegisterTypeMapEntry(bson.TypeEmbeddedDocument, tM)

	ctx := context.Background()
	logger.Infof("Attempting MongoDB connection...")

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(connectionString).SetRegistry(reg))
	if err != nil {
		logger.Errorf("MongoDB connection failed with detailed error:")
		logger.Errorf("  - Error Type: %T", err)
		logger.Errorf("  - Error Message: %v", err)

		// Check for common connection issues
		errorStr := err.Error()
		if strings.Contains(errorStr, "connection failed") {
			logger.Errorf("  - Issue: Network connectivity problem")
			logger.Errorf("  - Check: Verify hosts are reachable and ports are open")
		}
		if strings.Contains(errorStr, "authentication failed") {
			logger.Errorf("  - Issue: Authentication credentials problem")
			logger.Errorf("  - Check: Verify username, password, authSource, and authMechanism")
		}
		if strings.Contains(errorStr, "timeout") {
			logger.Errorf("  - Issue: Connection timeout (%s ms)", config.MongodbTimeoutMs)
			logger.Errorf("  - Check: Consider increasing timeoutMs value (current: %s)", config.MongodbTimeoutMs)
		}
		if strings.Contains(errorStr, "replica set") {
			logger.Errorf("  - Issue: Replica set configuration problem")
			logger.Errorf("  - Check: Verify replica set name: %s", config.MongodbReplicaSet)
		}
		if strings.Contains(errorStr, "tls") || strings.Contains(errorStr, "ssl") {
			logger.Errorf("  - Issue: TLS/SSL configuration problem")
			logger.Errorf("  - Check: TLS setting is: %s", config.MongodbTLSEnabled)
		}

		logger.Fatal("MongoDB connection error:", err)
	}

	logger.Infof("MongoDB connection successful!")

	// Test the connection with a ping
	pingCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := client.Ping(pingCtx, nil); err != nil {
		logger.Errorf("MongoDB ping failed after connection: %v", err)
		logger.Fatal("MongoDB ping error:", err)
	}

	logger.Infof("MongoDB ping successful!")

	database := client.Database(config.MongodbDatabase)
	err = collectCreateIndex(ctx, database, logger)
	if err != nil {
		logger.Fatal("Index creation error:", err)
	}

	context := &AppContext{
		Client: database,
		Logger: logger,
		Config: config,
	}

	if createDefaultResources {
		createDefaults(ctx, context, logger)
	}

	return context
}

func (db *AppContext) GetGenerals(ctx context.Context, collectionName string) (*mongo.Cursor, error) {
	collection := db.Client.Collection(collectionName)
	findOptions := options.Find()
	findOptions.SetProjection(bson.D{{Key: "general", Value: 1}})

	cur, err := collection.Find(ctx, bson.D{{}}, findOptions)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, errstr.ErrListenerNotFound
		}
		return nil, errstr.ErrUnknownDBError
	}

	return cur, err
}

func indexExists(ctx context.Context, collection *mongo.Collection, indexName string) (bool, error) {
	cursor, err := collection.Indexes().List(ctx)
	if err != nil {
		return false, fmt.Errorf("indexExists: %w", err)
	}
	defer cursor.Close(ctx)
	var indexes []bson.M
	if err = cursor.All(ctx, &indexes); err != nil {
		return false, err
	}
	for _, index := range indexes {
		if index["name"] == indexName {
			return true, nil
		}
	}
	return false, nil
}

func collectCreateIndex(ctx context.Context, database *mongo.Database, logger *logger.Logger) error {
	// Create standard collection indexes
	for collectionName, index := range Indices {
		collection := database.Collection(collectionName)
		indexName := getIndexName(index)

		// Check if index exists first
		exists, err := indexExists(ctx, collection, indexName)
		if err != nil {
			logger.Errorf("Failed to check index existence for %s.%s: %v", collectionName, indexName, err)
			continue
		}

		if !exists {
			if err := createIndex(ctx, collection, index, indexName); err != nil {
				logger.Fatal("Failed to create index for", collectionName, ":", err)
				return err
			}
			logger.Infof("Created index: %s.%s", collectionName, indexName)
		} else {
			logger.Debugf("Index already exists: %s.%s", collectionName, indexName)
		}
	}

	// Create text search indexes for secure search functionality
	for collectionName, index := range TextSearchIndices {
		// Extract collection name from index key (e.g., "clusters_text" -> "clusters")
		actualCollectionName := strings.Split(collectionName, "_")[0]
		collection := database.Collection(actualCollectionName)
		indexName := getIndexName(index)

		// Check if index exists first
		exists, err := indexExists(ctx, collection, indexName)
		if err != nil {
			logger.Errorf("Failed to check text search index for %s.%s: %v", actualCollectionName, indexName, err)
			continue
		}

		if !exists {
			if err := createIndex(ctx, collection, index, indexName); err != nil {
				logger.Errorf("Failed to create text search index for %s: %v", actualCollectionName, err)
				// Don't return error for text indexes - they're optional
				continue
			}
			logger.Infof("Created text search index: %s.%s", actualCollectionName, indexName)
		} else {
			logger.Debugf("Text search index already exists: %s.%s", actualCollectionName, indexName)
		}
	}

	// Create specialized audit indexes
	auditCollection := database.Collection("audit_logs")
	for indexKey, index := range AuditIndices {
		indexName := getIndexName(index)

		// Check if index exists first
		exists, err := indexExists(ctx, auditCollection, indexName)
		if err != nil {
			logger.Errorf("Failed to check audit index %s: %v", indexKey, err)
			continue
		}

		if !exists {
			if err := createIndex(ctx, auditCollection, index, indexName); err != nil {
				logger.Errorf("Failed to create audit index %s: %v", indexKey, err)
				return err
			}
			logger.Infof("Created audit index: %s (%s)", indexKey, indexName)
		} else {
			logger.Debugf("Audit index already exists: %s (%s)", indexKey, indexName)
		}
	}

	// Create GSLB specialized indexes
	for collectionName, indexes := range GSLBIndices {
		collection := database.Collection(collectionName)
		for _, index := range indexes {
			indexName := getIndexName(index)

			// Check if index exists first
			exists, err := indexExists(ctx, collection, indexName)
			if err != nil {
				logger.Errorf("Failed to check GSLB index for %s.%s: %v", collectionName, indexName, err)
				continue
			}

			if !exists {
				if err := createIndex(ctx, collection, index, indexName); err != nil {
					logger.Errorf("Failed to create GSLB index for %s.%s: %v", collectionName, indexName, err)
					// Don't fail on GSLB index creation - continue with others
					continue
				}
				logger.Debugf("Created GSLB index: %s.%s", collectionName, indexName)
			} else {
				logger.Debugf("GSLB index already exists: %s.%s", collectionName, indexName)
			}
		}
	}

	// Create ACME specialized indexes
	for collectionName, indexes := range ACMEIndices {
		collection := database.Collection(collectionName)
		for _, index := range indexes {
			indexName := getIndexName(index)

			// Check if index exists first
			exists, err := indexExists(ctx, collection, indexName)
			if err != nil {
				logger.Errorf("Failed to check ACME index for %s.%s: %v", collectionName, indexName, err)
				continue
			}

			if !exists {
				if err := createIndex(ctx, collection, index, indexName); err != nil {
					logger.Errorf("Failed to create ACME index for %s.%s: %v", collectionName, indexName, err)
					// Don't fail on ACME index creation - continue with others
					continue
				}
				logger.Infof("Created ACME index: %s.%s", collectionName, indexName)
			} else {
				logger.Debugf("ACME index already exists: %s.%s", collectionName, indexName)
			}
		}
	}

	// Create zone unique index for settings collection (CRITICAL for GSLB zone uniqueness)
	settingsCollection := database.Collection("settings")
	zoneUniqueIndex := mongo.IndexModel{
		Keys: bson.D{{Key: "gslb_config.zone", Value: 1}},
		Options: options.Index().
			SetUnique(true).
			SetName("gslb_config_zone_unique_1").
			SetPartialFilterExpression(bson.M{
				// MongoDB partial filter doesn't support $ne operator
				// Only check existence and type=string (empty strings will be caught at validation layer)
				"gslb_config.zone": bson.M{
					"$exists": true,
					"$type":   "string",
				},
			}),
	}
	zoneIndexName := getIndexName(zoneUniqueIndex)
	exists, err := indexExists(ctx, settingsCollection, zoneIndexName)
	switch {
	case err != nil:
		logger.Errorf("Failed to check zone unique index: %v", err)
	case !exists:
		if err := createIndex(ctx, settingsCollection, zoneUniqueIndex, zoneIndexName); err != nil {
			logger.Errorf("Failed to create zone unique index: %v", err)
		} else {
			logger.Debugf("Created GSLB zone unique index on settings collection")
		}
	default:
		logger.Debugf("GSLB zone unique index already exists on settings collection")
	}

	return nil
}

// createIndex creates the index without existence check (caller should check first)
func createIndex(ctx context.Context, collection *mongo.Collection, index mongo.IndexModel, indexName string) error {
	if indexName == "" {
		return errstr.ErrInvalidIndexName
	}

	_, err := collection.Indexes().CreateOne(ctx, index)
	if err != nil {
		return fmt.Errorf("could not create index for %v on collection %v: %w", index.Keys, collection.Name(), err)
	}

	return nil
}

func getIndexName(index mongo.IndexModel) string {
	// If a custom name is set, use it
	if index.Options != nil {
		if nameOption := index.Options.Name; nameOption != nil {
			return *nameOption
		}
	}

	// Otherwise generate name from keys
	var nameParts []string

	switch keys := index.Keys.(type) {
	case bson.M:
		for key, val := range keys {
			if nestedKeys, ok := val.(bson.M); ok {
				for nestedKey := range nestedKeys {
					nameParts = append(nameParts, key+"."+nestedKey+"_1")
				}
			} else {
				// Handle different sort orders
				sortOrder := "_1"
				if intVal, ok := val.(int); ok && intVal == -1 {
					sortOrder = "_-1"
				} else if int32Val, ok := val.(int32); ok && int32Val == -1 {
					sortOrder = "_-1"
				}
				nameParts = append(nameParts, key+sortOrder)
			}
		}
	case bson.D:
		for _, keyVal := range keys {
			key := keyVal.Key
			if nestedKeys, ok := keyVal.Value.(bson.D); ok {
				for _, nestedKeyVal := range nestedKeys {
					nestedKey := nestedKeyVal.Key
					nameParts = append(nameParts, key+"."+nestedKey+"_1")
				}
			} else {
				// Handle different sort orders
				sortOrder := "_1"
				if intVal, ok := keyVal.Value.(int); ok && intVal == -1 {
					sortOrder = "_-1"
				} else if int32Val, ok := keyVal.Value.(int32); ok && int32Val == -1 {
					sortOrder = "_-1"
				}
				nameParts = append(nameParts, key+sortOrder)
			}
		}
	default:
		return ""
	}

	return strings.Join(nameParts, "_")
}
