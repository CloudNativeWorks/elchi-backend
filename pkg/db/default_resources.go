package db

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/pkg/errstr"
	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/version"
)

func createDefaults(ctx context.Context, context *AppContext, logger *logrus.Logger) {
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
}

func createAdminUser(ctx context.Context, db *AppContext) (string, error) {
	collection := db.Client.Collection("users")
	var user models.User
	err := collection.FindOne(ctx, bson.M{"username": "admin"}).Decode(&user)

	if errors.Is(err, mongo.ErrNoDocuments) {
		hashedPassword := helper.HashPassword("admin")
		user.Password = &hashedPassword
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
	collection := db.Client.Collection("clusters")
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
				"canonical_name": "envoy.upstreams.http.http_protocol_options",
				"gtype":          "envoy.extensions.upstreams.http.v3.HttpProtocolOptions",
				"type":           "http_protocol_options",
				"category":       "envoy.upstreams.http.http_protocol_options",
				"collection":     "extensions",
				"disabled":       false,
				"priority":       0,
				"parent_name":    "",
			},
		}

		if db.Config.ElchiTLSEnabled == "true" {
			typedConfig = append(typedConfig, bson.M{
				"name":           "elchi-control-plane-tls",
				"canonical_name": "envoy.transport_sockets.upstream",
				"gtype":          "envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext",
				"type":           "secret",
				"category":       "envoy.transport_sockets.tls",
				"collection":     "tls",
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
					"value":    "eyJuYW1lIjoiZWxjaGktY29udHJvbC1wbGFuZS1ocG8iLCJjYW5vbmljYWxfbmFtZSI6ImVudm95LnVwc3RyZWFtcy5odHRwLmh0dHBfcHJvdG9jb2xfb3B0aW9ucyIsImd0eXBlIjoiZW52b3kuZXh0ZW5zaW9ucy51cHN0cmVhbXMuaHR0cC52My5IdHRwUHJvdG9jb2xPcHRpb25zIiwidHlwZSI6Imh0dHBfcHJvdG9jb2xfb3B0aW9ucyIsImNhdGVnb3J5IjoiZW52b3kudXBzdHJlYW1zLmh0dHAuaHR0cF9wcm90b2NvbF9vcHRpb25zIiwiY29sbGVjdGlvbiI6ImV4dGVuc2lvbnMifQ==",
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
					"value":    "eyJuYW1lIjoiZWxjaGktY29udHJvbC1wbGFuZS10bHMiLCJjYW5vbmljYWxfbmFtZSI6ImVudm95LnRyYW5zcG9ydF9zb2NrZXRzLnVwc3RyZWFtIiwiZ3R5cGUiOiJlbnZveS5leHRlbnNpb25zLnRyYW5zcG9ydF9zb2NrZXRzLnRscy52My5VcHN0cmVhbVRsc0NvbnRleHQiLCJ0eXBlIjoic2VjcmV0IiwiY2F0ZWdvcnkiOiJlbnZveS50cmFuc3BvcnRfc29ja2V0cy50bHMiLCJjb2xsZWN0aW9uIjoidGxzIn0=",
					"type_url": "envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext",
				},
			}
		}

		defaultCluster := bson.M{
			"general": bson.M{
				"name":           "elchi-control-plane",
				"version":        vers,
				"type":           "cluster",
				"gtype":          "envoy.config.cluster.v3.Cluster",
				"project":        projectID,
				"collection":     "clusters",
				"canonical_name": "config.cluster.v3.Cluster",
				"category":       "cluster",
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
	collection := db.Client.Collection("extensions")
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
				"type":           "http_protocol_options",
				"gtype":          "envoy.extensions.upstreams.http.v3.HttpProtocolOptions",
				"project":        projectID,
				"collection":     "extensions",
				"canonical_name": "envoy.upstreams.http.http_protocol_options",
				"category":       "envoy.upstreams.http.http_protocol_options",
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
					"explicit_http_config": bson.M{
						"http2_protocol_options": bson.M{
							"connection_keepalive": bson.M{
								"interval": "30s",
								"timeout":  "5s",
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
	collection := db.Client.Collection("tls")
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
				"type":           "secret",
				"gtype":          "envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext",
				"project":        projectID,
				"collection":     "tls",
				"canonical_name": "envoy.transport_sockets.upstream",
				"category":       "envoy.transport_sockets.tls",
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
	collection := db.Client.Collection("extensions")
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
				"type":           "stat_sinks",
				"gtype":          "envoy.extensions.stat_sinks.open_telemetry.v3.SinkConfig",
				"project":        projectID,
				"collection":     "extensions",
				"canonical_name": "envoy.stat_sinks.open_telemetry",
				"category":       "envoy.stats_sinks",
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
	collection := db.Client.Collection("filters")
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
				"type":           "http_filter",
				"gtype":          "envoy.extensions.filters.http.router.v3.Router",
				"project":        projectID,
				"collection":     "filters",
				"canonical_name": "envoy.filters.http.router",
				"category":       "envoy.filters.http",
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

	defaultToken := uuid.New().String()
	defaultSettings := bson.M{
		"project": projectID,
		"tokens": bson.A{
			bson.M{
				"name":  "default",
				"token": defaultToken,
			},
		},
	}

	_, err = collection.InsertOne(ctx, defaultSettings)
	if err != nil {
		return fmt.Errorf("failed to create default settings: %w", err)
	}

	db.Logger.Info("default settings created successfully")
	return nil
}
