package db

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/scenario/generators"
	"github.com/CloudNativeWorks/elchi-backend/pkg/errstr"
	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/version"
)

// DefaultResourceMetadata represents metadata for default resources
type DefaultResourceMetadata struct {
	Name          string `json:"name"`
	GType         string `json:"gtype"`
	Type          string `json:"type"`
	CanonicalName string `json:"canonical_name"`
	Category      string `json:"category"`
	Collection    string `json:"collection"`
}

// encodeDefaultResourceMetadata encodes metadata for default resources
func encodeDefaultResourceMetadata(metadata DefaultResourceMetadata) string {
	mp := generators.NewMetadataProcessor()

	metadataMap := map[string]any{
		"name":           metadata.Name,
		"canonical_name": metadata.CanonicalName,
		"gtype":          metadata.GType,
		"type":           metadata.Type,
		"category":       metadata.Category,
		"collection":     metadata.Collection,
	}

	encoded, _ := mp.EncodeMetadata(metadataMap)
	return encoded
}

func createDefaults(ctx context.Context, context *AppContext, logger *logger.Logger) {
	vrs := version.GetVersion()

	if vrs == "" {
		logger.Infof("Version not found! Skipping default resources creation.")
		return
	}

	userID, err := createAdminUser(ctx, context)
	if err != nil {
		logger.Infof("Admin user not created: %s", err)
	}

	projectID, err := createDefaultProject(ctx, context, userID)
	if err != nil {
		logger.Infof("Default project not created: %s", err)
	}

	err = createDefaultSettings(ctx, context, projectID)
	if err != nil {
		logger.Infof("Default settings not created: %s", err)
	}

	groupID, err := createDefaultGroup(ctx, context, userID, projectID)
	if err != nil {
		logger.Infof("Admin group not created: %s", err)
	}

	if err := CreateDefaultHttpProtocolOptions(ctx, context, projectID, vrs, groupID); err != nil {
		logger.Infof("Default hpo not created: %s", err)
	}

	if err := CreateDefaultUpstreamTLS(ctx, context, projectID, vrs, groupID); err != nil {
		logger.Infof("Default upstream tls not created: %s", err)
	}

	if err := CreateDefaultCluster(ctx, context, projectID, vrs, groupID); err != nil {
		logger.Infof("Default cluster not created: %s", err)
	}

	if err := CreateDefaultStatSinks(ctx, context, projectID, vrs, groupID); err != nil {
		logger.Infof("Default stat sinks not created: %s", err)
	}

	if err := CreateDefaultRouter(ctx, context, projectID, vrs, groupID); err != nil {
		logger.Infof("Default router not created: %s", err)
	}

	if err := CreateDefaultHCM(ctx, context, projectID, vrs, groupID); err != nil {
		logger.Infof("Default hcm not created: %s", err)
	}

	// Create default scenarios (project-independent)
	if err := CreateDefaultScenarios(ctx, context); err != nil {
		logger.Infof("Default scenarios not created: %s", err)
	} else {
		logger.Info("Default scenarios created successfully")
	}
}

func createAdminUser(ctx context.Context, db *AppContext) (string, error) {
	collection := db.Client.Collection("users")
	var user models.User
	err := collection.FindOne(ctx, bson.M{"username": "admin"}).Decode(&user)

	if errors.Is(err, mongo.ErrNoDocuments) {
		// Use direct hashing for initial admin user (bypasses password policy for system setup)
		hashedPassword := helper.HashPassword("admin")
		user.Password = &hashedPassword
		authType := "local"
		now := time.Now()

		user.CreatedAt = primitive.NewDateTimeFromTime(now)
		user.UpdatedAt = primitive.NewDateTimeFromTime(now)
		user.ID = primitive.NewObjectID()
		user.UserID = user.ID.Hex()
		user.Email = &adminEmail
		user.Username = &adminUser
		user.Role = &adminRole
		user.BaseGroup = &adminBaseGroup
		user.Active = &adminActive
		user.AuthType = &authType

		token, refreshToken, _ := helper.GenerateAllTokens(user.Email, user.Username, user.UserID, nil, nil, nil, nil, user.Role)
		user.Token = &token
		user.RefreshToken = &refreshToken

		_, insertErr := collection.InsertOne(ctx, user)
		if insertErr != nil {
			if mongo.IsDuplicateKeyError(insertErr) {
				var existingUser models.User
				if findErr := collection.FindOne(ctx, bson.M{"username": "admin"}).Decode(&existingUser); findErr != nil {
					return "", fmt.Errorf("admin user not found after duplicate key error: %w", findErr)
				}
				return existingUser.UserID, nil
			}
			return "", fmt.Errorf("error creating admin user: %w", insertErr)
		}
	} else if err != nil {
		return "", fmt.Errorf("error querying admin user: %w", err)
	}

	return user.UserID, nil
}

func CreateGroup(ctx context.Context, collection *mongo.Collection, userID, projectID string) (*mongo.InsertOneResult, error) {
	var members = []string{}
	if userID != "" {
		members = []string{userID}
	}
	groupDoc := bson.M{
		"groupname":  "default",
		"members":    members,
		"project":    projectID,
		"created_at": primitive.NewDateTimeFromTime(time.Now()),
		"updated_at": primitive.NewDateTimeFromTime(time.Now()),
	}

	result, err := collection.InsertOne(ctx, groupDoc)

	return result, err
}

func createDefaultGroup(ctx context.Context, db *AppContext, userID string, projectID string) (string, error) {
	var groupID string
	if userID == "" {
		return "", errstr.ErrUserIDEmpty
	}

	collection := db.Client.Collection("groups")
	var group models.Group
	err := collection.FindOne(ctx, bson.M{"groupname": "default", "project": projectID}).Decode(&group)

	switch {
	case errors.Is(err, mongo.ErrNoDocuments):
		result, err := CreateGroup(ctx, collection, userID, projectID)
		if err != nil {
			if mongo.IsDuplicateKeyError(err) {
				db.Logger.Infof("default group already exists: %v", err)
			} else {
				return "", fmt.Errorf("failed to create default group: %w", err)
			}
		} else {
			db.Logger.Info("default group created successfully")

			groupID = result.InsertedID.(primitive.ObjectID).Hex()
			usersCollection := db.Client.Collection("users")
			userFilter := bson.M{"user_id": userID}
			userUpdate := bson.M{"$set": bson.M{"base_group": groupID}}

			_, updateErr := usersCollection.UpdateOne(ctx, userFilter, userUpdate)
			if updateErr != nil {
				db.Logger.Infof("Failed to update admin user's base group: %v", updateErr)
				return "", fmt.Errorf("failed to update admin user's base group: %w", updateErr)
			}
			db.Logger.Info("Admin user's base group updated successfully")
		}
	case err != nil:
		return "", fmt.Errorf("failed to check for default group: %w", err)
	default:
		db.Logger.Info("default group already exists")
	}

	return groupID, nil
}

func createDefaultProject(ctx context.Context, db *AppContext, userID string) (string, error) {
	projectID := ""
	if userID == "" {
		return projectID, errstr.ErrUserIDEmpty
	}

	collection := db.Client.Collection("projects")
	var project models.Project
	err := collection.FindOne(ctx, bson.M{"projectname": "default"}).Decode(&project)

	switch {
	case errors.Is(err, mongo.ErrNoDocuments):
		projectDoc := bson.M{
			"projectname": "default",
			"members":     []string{userID},
			"created_at":  primitive.NewDateTimeFromTime(time.Now()),
			"updated_at":  primitive.NewDateTimeFromTime(time.Now()),
		}

		result, err := collection.InsertOne(ctx, projectDoc)
		if err != nil {
			if mongo.IsDuplicateKeyError(err) {
				db.Logger.Infof("default project already exists: %v", err)
				projectID = project.ID.Hex()
			} else {
				return projectID, fmt.Errorf("failed to create default project: %w", err)
			}
		} else {
			db.Logger.Info("default project created successfully")
			projectID = result.InsertedID.(primitive.ObjectID).Hex()

			usersCollection := db.Client.Collection("users")
			userFilter := bson.M{"user_id": userID}
			userUpdate := bson.M{"$set": bson.M{"base_project": projectID}}

			_, updateErr := usersCollection.UpdateOne(ctx, userFilter, userUpdate)
			if updateErr != nil {
				db.Logger.Infof("Failed to update admin user's default project: %v", updateErr)
				return projectID, fmt.Errorf("failed to update admin user's default project: %w", updateErr)
			}
			db.Logger.Info("Admin user's default project updated successfully")
		}
	case err != nil:
		return projectID, fmt.Errorf("failed to check for default project: %w", err)
	default:
		db.Logger.Info("default project already exists")
		projectID = project.ID.Hex()
	}

	return projectID, nil
}

func CreateDefaultCluster(ctx context.Context, db *AppContext, projectID string, vers string, groupID string) error {
	gtypeCluster := models.Cluster
	gtypeProtocolOption := models.HTTPProtocolOptions
	gtypeUpstreamTLS := models.UpstreamTLSContext

	collection := db.Client.Collection(gtypeCluster.CollectionString())
	var cluster models.Resource
	if projectID == "" {
		return errstr.ErrProjectIDEmpty
	}
	err := collection.FindOne(ctx, bson.M{"general.name": "elchi-control-plane", "general.version": vers, "general.project": projectID}).Decode(&cluster)

	switch {
	case errors.Is(err, mongo.ErrNoDocuments):
		now := time.Now()
		createdAt := primitive.NewDateTimeFromTime(now)
		updatedAt := primitive.NewDateTimeFromTime(now)

		portValue, err := strconv.Atoi(db.Config.ElchiPort)
		if err != nil {
			return fmt.Errorf("failed to convert port to integer: %w", err)
		}

		typedConfig := []bson.M{
			{
				"name":           "elchi-control-plane-hpo",
				"canonical_name": gtypeProtocolOption.CanonicalName(),
				"gtype":          gtypeProtocolOption.String(),
				"type":           gtypeProtocolOption.Type(),
				"category":       gtypeProtocolOption.Category(),
				"collection":     gtypeProtocolOption.CollectionString(),
				"disabled":       false,
				"priority":       0,
				"parent_name":    "",
			},
		}

		if db.Config.ElchiTLSEnabled == "true" {
			typedConfig = append(typedConfig, bson.M{
				"name":           "elchi-control-plane-tls",
				"canonical_name": gtypeUpstreamTLS.CanonicalName(),
				"gtype":          gtypeUpstreamTLS.String(),
				"type":           gtypeUpstreamTLS.Type(),
				"category":       gtypeUpstreamTLS.Category(),
				"collection":     gtypeUpstreamTLS.CollectionString(),
				"disabled":       false,
				"priority":       1,
				"parent_name":    "",
			})
		}

		resourceConfig := bson.M{
			"name":            "elchi-control-plane",
			"type":            "STRICT_DNS",
			"connect_timeout": "2s",
			"typed_extension_protocol_options": bson.M{
				"envoy.extensions.upstreams.http.v3.HttpProtocolOptions": bson.M{
					"type_url": "envoy.extensions.upstreams.http.v3.HttpProtocolOptions",
					"value": encodeDefaultResourceMetadata(DefaultResourceMetadata{
						Name:          "elchi-control-plane-hpo",
						GType:         "envoy.extensions.upstreams.http.v3.HttpProtocolOptions",
						Type:          "http_protocol_options",
						CanonicalName: "envoy.upstreams.http.http_protocol_options",
						Category:      "envoy.upstreams.http.http_protocol_options",
						Collection:    "extensions",
					}),
				},
			},
			"load_assignment": bson.M{
				"cluster_name": "elchi-control-plane",
				"endpoints": []bson.M{
					{
						"lb_endpoints": []bson.M{
							{
								"endpoint": bson.M{
									"address": bson.M{
										"socket_address": bson.M{
											"address":    db.Config.ElchiAddress,
											"port_value": portValue,
											"protocol":   "TCP",
										},
									},
								},
							},
						},
					},
				},
			},
		}

		if db.Config.ElchiTLSEnabled == "true" {
			resourceConfig["transport_socket"] = bson.M{
				"name": "envoy.transport_sockets.tls",
				"typed_config": bson.M{
					"value": encodeDefaultResourceMetadata(DefaultResourceMetadata{
						Name:          "elchi-control-plane-tls",
						GType:         "envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext",
						Type:          "secret",
						CanonicalName: "envoy.transport_sockets.upstream",
						Category:      "envoy.transport_sockets.tls",
						Collection:    "tls",
					}),
					"type_url": "envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext",
				},
			}
		}

		defaultCluster := bson.M{
			"general": bson.M{
				"name":           "elchi-control-plane",
				"version":        vers,
				"type":           gtypeCluster.Type(),
				"gtype":          gtypeCluster.String(),
				"project":        projectID,
				"collection":     gtypeCluster.CollectionString(),
				"canonical_name": gtypeCluster.CanonicalName(),
				"category":       gtypeCluster.Category(),
				"metadata": bson.M{
					"is_default": true,
				},
				"permissions": bson.M{
					"users":  []string{},
					"groups": []string{groupID},
				},
				"created_at":   createdAt,
				"updated_at":   updatedAt,
				"typed_config": typedConfig,
			},
			"resource": bson.M{
				"version":  "1",
				"resource": resourceConfig,
			},
		}

		_, err = collection.InsertOne(ctx, defaultCluster)
		if err != nil {
			if mongo.IsDuplicateKeyError(err) {
				db.Logger.Infof("default cluster already exists: %v", err)
			} else {
				return fmt.Errorf("failed to create default cluster: %w", err)
			}
		} else {
			db.Logger.Info("default cluster created successfully")
		}
	case err != nil:
		return fmt.Errorf("failed to check for default cluster: %w", err)
	default:
		db.Logger.Info("default cluster already exists")
	}

	return nil
}

func CreateDefaultHttpProtocolOptions(ctx context.Context, db *AppContext, projectID string, vers string, groupID string) error {
	gtype := models.HTTPProtocolOptions
	collection := db.Client.Collection(gtype.CollectionString())
	var hpo models.Resource
	if projectID == "" {
		return errstr.ErrProjectIDEmpty
	}
	err := collection.FindOne(ctx, bson.M{"general.name": "elchi-control-plane-hpo", "general.version": vers, "general.project": projectID}).Decode(&hpo)

	switch {
	case errors.Is(err, mongo.ErrNoDocuments):
		now := time.Now()
		createdAt := primitive.NewDateTimeFromTime(now)
		updatedAt := primitive.NewDateTimeFromTime(now)

		defaultHPO := bson.M{
			"general": bson.M{
				"name":           "elchi-control-plane-hpo",
				"version":        vers,
				"type":           gtype.Type(),
				"gtype":          gtype.String(),
				"project":        projectID,
				"collection":     gtype.CollectionString(),
				"canonical_name": gtype.CanonicalName(),
				"category":       gtype.Category(),
				"metadata": bson.M{
					"is_default": true,
				},
				"permissions": bson.M{
					"users":  []string{},
					"groups": []string{groupID},
				},
				"created_at": createdAt,
				"updated_at": updatedAt,
			},
			"resource": bson.M{
				"version": "1",
				"resource": bson.M{
					"common_http_protocol_options": bson.M{
						"idle_timeout":            "0s",
						"max_connection_duration": "0s",
					},
					"explicit_http_config": bson.M{
						"http2_protocol_options": bson.M{
							"connection_keepalive": bson.M{
								"interval": "30s",
								"timeout":  "10s",
							},
						},
					},
				},
			},
		}

		_, err = collection.InsertOne(ctx, defaultHPO)
		if err != nil {
			if mongo.IsDuplicateKeyError(err) {
				db.Logger.Infof("default hpo already exists: %v", err)
			} else {
				return fmt.Errorf("failed to create default hpo: %w", err)
			}
		} else {
			db.Logger.Info("default hpo created successfully")
		}
	case err != nil:
		return fmt.Errorf("failed to check for default hpo: %w", err)
	default:
		db.Logger.Info("default hpo already exists")
	}

	return nil
}

func CreateDefaultUpstreamTLS(ctx context.Context, db *AppContext, projectID string, vers string, groupID string) error {
	gtype := models.UpstreamTLSContext
	collection := db.Client.Collection(gtype.CollectionString())
	var tls models.Resource
	if projectID == "" {
		return errstr.ErrProjectIDEmpty
	}
	err := collection.FindOne(ctx, bson.M{"general.name": "elchi-control-plane-tls", "general.version": vers, "general.project": projectID}).Decode(&tls)

	switch {
	case errors.Is(err, mongo.ErrNoDocuments):
		now := time.Now()
		createdAt := primitive.NewDateTimeFromTime(now)
		updatedAt := primitive.NewDateTimeFromTime(now)

		defaultTLS := bson.M{
			"general": bson.M{
				"name":           "elchi-control-plane-tls",
				"version":        vers,
				"type":           gtype.Type(),
				"gtype":          gtype.String(),
				"project":        projectID,
				"collection":     gtype.CollectionString(),
				"canonical_name": gtype.CanonicalName(),
				"category":       gtype.Category(),
				"metadata": bson.M{
					"is_default": true,
				},
				"permissions": bson.M{
					"users":  []string{},
					"groups": []string{groupID},
				},
				"created_at": createdAt,
				"updated_at": updatedAt,
			},
			"resource": bson.M{
				"version":  "1",
				"resource": bson.M{},
			},
		}

		_, err = collection.InsertOne(ctx, defaultTLS)
		if err != nil {
			if mongo.IsDuplicateKeyError(err) {
				db.Logger.Infof("default upstream tls already exists: %v", err)
			} else {
				return fmt.Errorf("failed to create default upstream tls: %w", err)
			}
		} else {
			db.Logger.Info("default upstream tls created successfully")
		}
	case err != nil:
		return fmt.Errorf("failed to check for default upstream tls: %w", err)
	default:
		db.Logger.Info("default upstream tls already exists")
	}

	return nil
}

func CreateDefaultStatSinks(ctx context.Context, db *AppContext, projectID string, vers string, groupID string) error {
	gtype := models.OpenTelemetry
	collection := db.Client.Collection(gtype.CollectionString())
	var sink models.Resource
	if projectID == "" {
		return errstr.ErrProjectIDEmpty
	}
	err := collection.FindOne(ctx, bson.M{"general.name": "elchi-control-plane-otel", "general.version": vers, "general.project": projectID}).Decode(&sink)

	switch {
	case errors.Is(err, mongo.ErrNoDocuments):
		now := time.Now()
		createdAt := primitive.NewDateTimeFromTime(now)
		updatedAt := primitive.NewDateTimeFromTime(now)

		defaultSink := bson.M{
			"general": bson.M{
				"name":           "elchi-control-plane-otel",
				"version":        vers,
				"type":           gtype.Type(),
				"gtype":          gtype.String(),
				"project":        projectID,
				"collection":     gtype.CollectionString(),
				"canonical_name": gtype.CanonicalName(),
				"category":       gtype.Category(),
				"metadata": bson.M{
					"is_default": true,
				},
				"permissions": bson.M{
					"users":  []string{},
					"groups": []string{groupID},
				},
				"created_at": createdAt,
				"updated_at": updatedAt,
			},
			"resource": bson.M{
				"version": "1",
				"resource": bson.M{
					"grpc_service": bson.M{
						"envoy_grpc": bson.M{
							"cluster_name": "elchi-control-plane",
						},
						"timeout": "2s",
					},
				},
			},
		}

		_, err = collection.InsertOne(ctx, defaultSink)
		if err != nil {
			if mongo.IsDuplicateKeyError(err) {
				db.Logger.Infof("default stat sink already exists: %v", err)
			} else {
				return fmt.Errorf("failed to create default stat sink: %w", err)
			}
		} else {
			db.Logger.Info("default stat sink created successfully")
		}
	case err != nil:
		return fmt.Errorf("failed to check for default stat sink: %w", err)
	default:
		db.Logger.Info("default stat sink already exists")
	}

	return nil
}

func CreateDefaultRouter(ctx context.Context, db *AppContext, projectID string, vers string, groupID string) error {
	gtype := models.Router
	collection := db.Client.Collection(gtype.CollectionString())
	var sink models.Resource
	if projectID == "" {
		return errstr.ErrProjectIDEmpty
	}
	err := collection.FindOne(ctx, bson.M{"general.name": "default-router", "general.version": vers, "general.project": projectID}).Decode(&sink)

	switch {
	case errors.Is(err, mongo.ErrNoDocuments):
		now := time.Now()
		createdAt := primitive.NewDateTimeFromTime(now)
		updatedAt := primitive.NewDateTimeFromTime(now)

		defaultRouter := bson.M{
			"general": bson.M{
				"name":           "default-router",
				"version":        vers,
				"type":           gtype.Type(),
				"gtype":          gtype.String(),
				"project":        projectID,
				"collection":     gtype.CollectionString(),
				"canonical_name": gtype.CanonicalName(),
				"category":       gtype.Category(),
				"metadata": bson.M{
					"is_default":  true,
					"http_filter": "main",
				},
				"permissions": bson.M{
					"users":  []string{},
					"groups": []string{groupID},
				},
				"created_at": createdAt,
				"updated_at": updatedAt,
			},
			"resource": bson.M{
				"version":  "1",
				"resource": bson.M{},
			},
		}

		_, err = collection.InsertOne(ctx, defaultRouter)
		if err != nil {
			if mongo.IsDuplicateKeyError(err) {
				db.Logger.Infof("default router already exists: %v", err)
			} else {
				return fmt.Errorf("failed to create default router: %w", err)
			}
		} else {
			db.Logger.Info("default router created successfully")
		}
	case err != nil:
		return fmt.Errorf("failed to check for default router: %w", err)
	default:
		db.Logger.Info("default router already exists")
	}

	return nil
}

func CreateDefaultHCM(ctx context.Context, db *AppContext, projectID string, vers string, groupID string) error {
	gtype := models.HTTPConnectionManager
	gtypeRouter := models.Router
	collection := db.Client.Collection(gtype.CollectionString())
	var sink models.Resource
	if projectID == "" {
		return errstr.ErrProjectIDEmpty
	}
	err := collection.FindOne(ctx, bson.M{"general.name": "default-httptohttps", "general.version": vers, "general.project": projectID}).Decode(&sink)

	switch {
	case errors.Is(err, mongo.ErrNoDocuments):
		now := time.Now()
		createdAt := primitive.NewDateTimeFromTime(now)
		updatedAt := primitive.NewDateTimeFromTime(now)

		configDiscovery := []bson.M{
			{
				"name":           "default-router",
				"gtype":          gtypeRouter.String(),
				"priority":       0,
				"category":       gtypeRouter.Category(),
				"canonical_name": gtypeRouter.CanonicalName(),
				"collection":     gtypeRouter.CollectionString(),
				"type":           gtypeRouter.Type(),
				"parent_name":    "default-router",
			},
		}

		resourceConfig := bson.M{
			"stat_prefix": "default-httptohttps",
			"route_config": bson.M{
				"name": "default-httptohttps",
				"virtual_hosts": []bson.M{
					{
						"name": "default-httptohttps-host",
						"domains": []string{
							"*",
						},
						"routes": []bson.M{
							{
								"name": "default-httptohttps-route",
								"match": bson.M{
									"prefix": "/",
								},
								"redirect": bson.M{
									"https_redirect": true,
								},
							},
						},
					},
				},
			},
			"http_filters": []bson.M{
				{
					"name": "default-router",
					"config_discovery": bson.M{
						"config_source": bson.M{
							"ads":                   bson.M{},
							"initial_fetch_timeout": "5.0s",
							"resource_api_version":  "V3",
						},
						"type_urls": []string{
							"envoy.extensions.filters.http.router.v3.Router",
						},
					},
					"is_optional": false,
					"disabled":    false,
				},
			},
		}

		defaultHCM := bson.M{
			"general": bson.M{
				"name":           "default-httptohttps",
				"version":        vers,
				"type":           gtype.Type(),
				"gtype":          gtype.String(),
				"project":        projectID,
				"collection":     gtype.CollectionString(),
				"canonical_name": gtype.CanonicalName(),
				"category":       gtype.Category(),
				"metadata": bson.M{
					"is_default": true,
				},
				"permissions": bson.M{
					"users":  []string{},
					"groups": []string{groupID},
				},
				"config_discovery": configDiscovery,
				"created_at":       createdAt,
				"updated_at":       updatedAt,
			},
			"resource": bson.M{
				"version":  "1",
				"resource": resourceConfig,
			},
		}

		_, err = collection.InsertOne(ctx, defaultHCM)
		if err != nil {
			if mongo.IsDuplicateKeyError(err) {
				db.Logger.Infof("default hcm already exists: %v", err)
			} else {
				return fmt.Errorf("failed to create default hcm: %w", err)
			}
		} else {
			db.Logger.Info("default hcm created successfully")
		}
	case err != nil:
		return fmt.Errorf("failed to check for default hcm: %w", err)
	default:
		db.Logger.Info("default hcm already exists")
	}

	return nil
}

func createDefaultSettings(ctx context.Context, db *AppContext, projectID string) error {
	collection := db.Client.Collection("settings")

	count, err := collection.CountDocuments(ctx, bson.M{})
	if err != nil {
		return fmt.Errorf("failed to count settings documents: %w", err)
	}

	if count > 0 {
		db.Logger.Info("settings document already exists")
		return nil
	}

	defaultSettings := bson.M{
		"project": projectID,
		"tokens":  bson.A{},
	}

	_, err = collection.InsertOne(ctx, defaultSettings)
	if err != nil {
		return fmt.Errorf("failed to create default settings: %w", err)
	}

	db.Logger.Info("default settings created successfully")
	return nil
}
