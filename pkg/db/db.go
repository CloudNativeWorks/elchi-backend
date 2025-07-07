package db

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"

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
	"users":         {Keys: bson.M{"username": 1}, Options: options.Index().SetUnique(true).SetName("username_1").SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"groups":        {Keys: bson.D{{Key: "groupname", Value: 1}, {Key: "project", Value: 1}}, Options: options.Index().SetUnique(true).SetName("groupname_project_1").SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"services":      {Keys: bson.D{{Key: "name", Value: 1}, {Key: "project", Value: 1}}, Options: options.Index().SetUnique(true).SetName("name_project_1").SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"clusters":      {Keys: bson.D{{Key: generalName, Value: 1}, {Key: generalVersion, Value: 1}, {Key: generalProject, Value: 1}}, Options: options.Index().SetUnique(true).SetName(generalNameProject).SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"listeners":     {Keys: bson.D{{Key: generalName, Value: 1}, {Key: generalVersion, Value: 1}, {Key: generalProject, Value: 1}}, Options: options.Index().SetUnique(true).SetName(generalNameProject).SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"endpoints":     {Keys: bson.D{{Key: generalName, Value: 1}, {Key: generalVersion, Value: 1}, {Key: generalProject, Value: 1}}, Options: options.Index().SetUnique(true).SetName(generalNameProject).SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"routes":        {Keys: bson.D{{Key: generalName, Value: 1}, {Key: generalVersion, Value: 1}, {Key: generalProject, Value: 1}}, Options: options.Index().SetUnique(true).SetName(generalNameProject).SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"virtual_hosts": {Keys: bson.D{{Key: generalName, Value: 1}, {Key: generalVersion, Value: 1}, {Key: generalProject, Value: 1}}, Options: options.Index().SetUnique(true).SetName(generalNameProject).SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"filters":       {Keys: bson.D{{Key: generalName, Value: 1}, {Key: generalVersion, Value: 1}, {Key: generalProject, Value: 1}}, Options: options.Index().SetUnique(true).SetName(generalNameProject).SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"secrets":       {Keys: bson.D{{Key: generalName, Value: 1}, {Key: generalVersion, Value: 1}, {Key: generalProject, Value: 1}}, Options: options.Index().SetUnique(true).SetName(generalNameProject).SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"extensions":    {Keys: bson.D{{Key: generalName, Value: 1}, {Key: generalVersion, Value: 1}, {Key: generalProject, Value: 1}}, Options: options.Index().SetUnique(true).SetName(generalNameProject).SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"bootstrap":     {Keys: bson.D{{Key: generalName, Value: 1}, {Key: generalVersion, Value: 1}, {Key: generalProject, Value: 1}}, Options: options.Index().SetUnique(true).SetName(generalNameProject).SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"tls":           {Keys: bson.D{{Key: generalName, Value: 1}, {Key: generalVersion, Value: 1}, {Key: generalProject, Value: 1}}, Options: options.Index().SetUnique(true).SetName(generalNameProject).SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"envoys":        {Keys: bson.D{{Key: "name", Value: 1}, {Key: "project", Value: 1}}, Options: options.Index().SetUnique(true).SetName("name_project_1").SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"projects":      {Keys: bson.M{"projectname": 1}, Options: options.Index().SetUnique(true).SetName("projectname_1").SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"clients":       {Keys: bson.M{"client_id": 1}, Options: options.Index().SetUnique(true).SetName("client_id_1").SetCollation(&options.Collation{Locale: "en", Strength: 2})},
	"settings":      {Keys: bson.M{"project": 1}, Options: options.Index().SetUnique(true).SetName("project_name_1").SetCollation(&options.Collation{Locale: "en", Strength: 2})},
}

func buildMongoDBConnectionString(config *config.AppConfig) string {
	u := &url.URL{
		Scheme: config.MongodbScheme,
		Host:   config.MongodbHosts,
		Path:   config.MongodbDatabase,
	}

	if config.MongodbUsername != "" && config.MongodbPassword != "" {
		u.User = url.UserPassword(config.MongodbUsername, config.MongodbPassword)
	}

	if config.MongodbScheme != "mongodb+srv" {
		if !strings.Contains(config.MongodbHosts, ":") && config.MongodbPort != "" {
			u.Host = fmt.Sprintf("%s:%s", config.MongodbHosts, config.MongodbPort)
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

	query.Add("retryWrites", "true")
	query.Add("w", "majority")
	query.Add("tls", config.MongodbTLSEnabled)

	u.RawQuery = query.Encode()

	return u.String()
}

func NewMongoDB(config *config.AppConfig, createDefaultResources bool) *AppContext {
	logger := logger.NewLogger("database")
	connectionString := buildMongoDBConnectionString(config)
	tM := reflect.TypeOf(bson.M{})
	reg := bson.NewRegistry()
	reg.RegisterTypeMapEntry(bson.TypeEmbeddedDocument, tM)

	ctx := context.Background()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(connectionString).SetRegistry(reg))
	if err != nil {
		logger.Fatal("MongoDB connection error:", err)
	}

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
		createDefaults(ctx, context, logger.Logger)
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
	for collectionName, index := range Indices {
		collection := database.Collection(collectionName)
		indexName := getIndexName(index)
		if err := createIndex(ctx, collection, index, indexName); err != nil {
			logger.Fatal("Failed to create index for", collectionName, ":", err)
			return err
		}
	}

	return nil
}

func createIndex(ctx context.Context, collection *mongo.Collection, index mongo.IndexModel, indexName string) error {
	if indexName == "" {
		return errstr.ErrInvalidIndexName
	}
	exists, err := indexExists(ctx, collection, indexName)
	if err != nil {
		return fmt.Errorf("could not check for index existence: %w", err)
	}

	if !exists {
		_, err = collection.Indexes().CreateOne(ctx, index)
		if err != nil {
			return fmt.Errorf("could not create index for %v on collection %v: %w", index.Keys, collection.Name(), err)
		}
	}
	return nil
}

func getIndexName(index mongo.IndexModel) string {
	var nameParts []string

	switch keys := index.Keys.(type) {
	case bson.M:
		for key, val := range keys {
			if nestedKeys, ok := val.(bson.M); ok {
				for nestedKey := range nestedKeys {
					nameParts = append(nameParts, key+"."+nestedKey+"_1")
				}
			} else {
				nameParts = append(nameParts, key+"_1")
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
				nameParts = append(nameParts, key+"_1")
			}
		}
	default:
		return ""
	}

	return strings.Join(nameParts, "_")
}
