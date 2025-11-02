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
	"listeners":          {Keys: bson.D{{Key: generalName, Value: 1}, {Key: generalVersion, Value: 1}, {Key: generalProject, Value: 1}}, Options: options.Index().SetUnique(true).SetName(generalNameProject).SetCollation(&options.Collation{Locale: "en", Strength: 2})},
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
