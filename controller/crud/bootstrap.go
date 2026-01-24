package crud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/scenario/generators"
	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// encodeResourceMetadata encodes metadata using the shared struct
func encodeResourceMetadata(metadata db.DefaultResourceMetadata) string {
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

func GetBootstrap(ctx context.Context, db *mongo.Database, listenerGeneral models.General, config *config.AppConfig) (map[string]any, error) {
	// Check for template first
	template := getBootstrapTemplate(ctx, db, listenerGeneral.Project, listenerGeneral.Version)

	if template != nil {
		// Template-based generation
		return generateBootstrapFromTemplate(ctx, db, template, listenerGeneral, config)
	}

	// Fallback to hardcoded generation (existing logic unchanged)
	return generateBootstrapHardcoded(ctx, db, listenerGeneral, config)
}

// generateBootstrapHardcoded is the original hardcoded bootstrap generation logic
func generateBootstrapHardcoded(ctx context.Context, db *mongo.Database, listenerGeneral models.General, config *config.AppConfig) (map[string]any, error) {
	now := time.Now()
	CreatedAt := primitive.NewDateTimeFromTime(now)
	UpdatedAt := primitive.NewDateTimeFromTime(now)
	nodeID := fmt.Sprintf("%s::%s", listenerGeneral.Name, listenerGeneral.Project)

	cluster := createClusterConfig()
	port, err := GetNextAdminPort(ctx, db, listenerGeneral.Name, listenerGeneral.Project, listenerGeneral.Version)
	if err != nil {
		return nil, err
	}
	admin := createAdminConfig(port)
	data := createDataConfig(nodeID, config.ElchiAddress, listenerGeneral.Version, cluster, admin)
	general := createGeneralConfig(listenerGeneral, CreatedAt, UpdatedAt)
	general["managed"] = listenerGeneral.Managed
	general["metadata"] = map[string]any{}

	return map[string]any{
		"general":  general,
		"resource": map[string]any{"version": "1", "resource": data},
	}, nil
}

func createClusterConfig() map[string]any {
	cluster := map[string]any{
		"name": "elchi-control-plane",
	}
	return cluster
}

func createDataConfig(nodeID, authority, version string, cluster, admin map[string]any) map[string]any {
	return map[string]any{
		"node": map[string]any{
			"id":      nodeID,
			"cluster": nodeID,
		},
		"static_resources": map[string]any{
			"clusters": []any{cluster},
		},
		"dynamic_resources": map[string]any{
			"lds_config": map[string]any{
				"ads":                  map[string]any{},
				"resource_api_version": "V3",
			},
			"cds_config": map[string]any{
				"ads":                  map[string]any{},
				"resource_api_version": "V3",
			},
			"ads_config": map[string]any{
				"api_type":              "DELTA_GRPC",
				"transport_api_version": "V3",
				"grpc_services": []any{
					map[string]any{
						"envoy_grpc": map[string]any{
							"cluster_name": "elchi-control-plane",
							"authority":    authority,
						},
						"initial_metadata": []any{
							map[string]any{
								"key":   "nodeid",
								"value": nodeID,
							},
							map[string]any{
								"key":   "envoy-version",
								"value": version,
							},
						},
					},
				},
				"set_node_on_first_message_only": false,
			},
		},
		"stats_sinks": []map[string]any{
			{
				"name": "envoy.stat_sinks.open_telemetry",
				"typed_config": map[string]string{
					"type_url": "envoy.extensions.stat_sinks.open_telemetry.v3.SinkConfig",
					"value": encodeResourceMetadata(db.DefaultResourceMetadata{
						Name:          "elchi-control-plane-otel",
						GType:         "envoy.extensions.stat_sinks.open_telemetry.v3.SinkConfig",
						Type:          "stat_sinks",
						CanonicalName: "envoy.stat_sinks.open_telemetry",
						Category:      "envoy.stats_sinks",
						Collection:    "extensions",
					}),
				},
			},
		},
		"admin": admin,
	}
}

func createAdminConfig(port int) map[string]any {
	return map[string]any{
		"address": map[string]any{
			"socket_address": map[string]any{
				"protocol":   "TCP",
				"address":    "127.0.0.1",
				"port_value": port,
			},
		},
	}
}

func createGeneralConfig(listenerGeneral models.General, createdAt, updatedAt primitive.DateTime) map[string]any {
	return map[string]any{
		"name":                 listenerGeneral.Name,
		"version":              listenerGeneral.Version,
		"type":                 "bootstrap",
		"gtype":                "envoy.config.bootstrap.v3.Bootstrap",
		"canonical_name":       "config.bootstrap.v3.Bootstrap",
		"category":             "bootstrap",
		"collection":           "bootstrap",
		"project":              listenerGeneral.Project,
		"permissions":          map[string]any{"users": []any{}, "groups": []any{}},
		"additional_resources": []any{},
		"created_at":           createdAt,
		"updated_at":           updatedAt,
		"config_discovery":     []any{},
		"typed_config":         []any{},
	}
}

func GetNextAdminPort(ctx context.Context, db *mongo.Database, bootstrapName, project, version string) (int, error) {
	const (
		adminPortStart      = 30000
		adminPortEnd        = 39999
		adminPortCollection = "admin_ports"
	)
	collection := db.Collection(adminPortCollection)

	var existing struct{ Port int }
	err := collection.FindOne(ctx, bson.M{"name": bootstrapName, "project": project, "version": version}).Decode(&existing)
	if err == nil {
		return existing.Port, nil
	}

	opts := options.Find().SetSort(bson.D{{Key: "port", Value: -1}}).SetLimit(1)
	cursor, err := collection.Find(ctx, bson.M{}, opts)
	if err != nil {
		return 0, err
	}
	defer cursor.Close(ctx)

	maxPort := adminPortStart - 1
	for cursor.Next(ctx) {
		var rec struct{ Port int }
		if err := cursor.Decode(&rec); err == nil {
			maxPort = rec.Port
		}
	}

	var nextPort int
	switch {
	case maxPort < adminPortStart:
		nextPort = adminPortStart
	case maxPort < adminPortEnd:
		nextPort = maxPort + 1
	default:
		return 0, errors.New("no available admin port")
	}

	_, err = collection.InsertOne(ctx, bson.M{
		"name":       bootstrapName,
		"project":    project,
		"version":    version,
		"port":       nextPort,
		"created_at": time.Now(),
	})
	if err != nil {
		return 0, err
	}
	return nextPort, nil
}

// getBootstrapTemplate looks for a bootstrap template in the database
func getBootstrapTemplate(ctx context.Context, db *mongo.Database, project, version string) *models.ResourceTemplate {
	collection := db.Collection("resource_templates")
	filter := bson.M{
		"gtype":   "envoy.config.bootstrap.v3.Bootstrap",
		"project": project,
		"version": version,
	}

	var template models.ResourceTemplate
	err := collection.FindOne(ctx, filter).Decode(&template)
	if err != nil {
		return nil // Template not found, fallback to hardcoded
	}
	return &template
}

// generateBootstrapFromTemplate creates bootstrap config using template + dynamic fields
func generateBootstrapFromTemplate(ctx context.Context, db *mongo.Database, template *models.ResourceTemplate, listenerGeneral models.General, config *config.AppConfig) (map[string]any, error) {
	var resourceData map[string]any

	if template.Resource == nil {
		return generateBootstrapHardcoded(ctx, db, listenerGeneral, config)
	}

	jsonBytes, err := json.Marshal(template.Resource)
	if err != nil {
		return generateBootstrapHardcoded(ctx, db, listenerGeneral, config)
	}

	err = json.Unmarshal(jsonBytes, &resourceData)
	if err != nil {
		return generateBootstrapHardcoded(ctx, db, listenerGeneral, config)
	}

	bootstrapData := deepCopyMap(resourceData)

	nodeID := fmt.Sprintf("%s::%s", listenerGeneral.Name, listenerGeneral.Project)

	port, err := GetNextAdminPort(ctx, db, listenerGeneral.Name, listenerGeneral.Project, listenerGeneral.Version)
	if err != nil {
		return nil, err
	}
	addAdminAddress(bootstrapData, port)

	addNodeClusterAndID(bootstrapData, nodeID)

	addDynamicResources(bootstrapData, nodeID, config.ElchiAddress, listenerGeneral.Version)

	now := time.Now()
	general := createGeneralConfigFromTemplate(template.General, listenerGeneral, now, now)

	return map[string]any{
		"general":  general,
		"resource": map[string]any{"version": "1", "resource": bootstrapData},
	}, nil
}

// deepCopyMap creates a deep copy of map[string]any using JSON
func deepCopyMap(original map[string]any) map[string]any {
	jsonBytes, _ := json.Marshal(original)
	var copied map[string]any
	if err := json.Unmarshal(jsonBytes, &copied); err != nil {
		// Return empty map on error
		return make(map[string]any)
	}
	return copied
}

// addAdminAddress injects admin.address.socket_address into bootstrap data
func addAdminAddress(data map[string]any, port int) {
	if _, exists := data["admin"]; !exists {
		data["admin"] = make(map[string]any)
	}

	adminMap := data["admin"].(map[string]any)
	adminMap["address"] = map[string]any{
		"socket_address": map[string]any{
			"protocol":   "TCP",
			"address":    "127.0.0.1",
			"port_value": port,
		},
	}
}

// addNodeClusterAndID injects node.cluster and node.id into bootstrap data
func addNodeClusterAndID(data map[string]any, nodeID string) {
	if _, exists := data["node"]; !exists {
		data["node"] = make(map[string]any)
	}

	nodeMap := data["node"].(map[string]any)
	nodeMap["cluster"] = nodeID
	nodeMap["id"] = nodeID
}

// addDynamicResources injects complete dynamic_resources section into bootstrap data
func addDynamicResources(data map[string]any, nodeID, authority, version string) {
	data["dynamic_resources"] = map[string]any{
		"lds_config": map[string]any{
			"ads":                  map[string]any{},
			"resource_api_version": "V3",
		},
		"cds_config": map[string]any{
			"ads":                  map[string]any{},
			"resource_api_version": "V3",
		},
		"ads_config": map[string]any{
			"api_type":              "DELTA_GRPC",
			"transport_api_version": "V3",
			"grpc_services": []any{
				map[string]any{
					"envoy_grpc": map[string]any{
						"cluster_name": "elchi-control-plane",
						"authority":    authority,
					},
					"initial_metadata": []any{
						map[string]any{
							"key":   "nodeid",
							"value": nodeID,
						},
						map[string]any{
							"key":   "envoy-version",
							"value": version,
						},
					},
				},
			},
			"set_node_on_first_message_only": false,
		},
	}
}

// createGeneralConfigFromTemplate creates general config merging template general with base config
func createGeneralConfigFromTemplate(templateGeneral *models.TemplateGeneral, listenerGeneral models.General, createdAt, updatedAt time.Time) map[string]any {
	// Base general config (existing createGeneralConfig logic)
	createdAtPrimitive := primitive.NewDateTimeFromTime(createdAt)
	updatedAtPrimitive := primitive.NewDateTimeFromTime(updatedAt)

	general := createGeneralConfig(listenerGeneral, createdAtPrimitive, updatedAtPrimitive)
	general["managed"] = listenerGeneral.Managed
	general["metadata"] = map[string]any{}

	// Merge template general fields if they exist
	if templateGeneral != nil {
		if templateGeneral.ConfigDiscovery != nil {
			general["config_discovery"] = templateGeneral.ConfigDiscovery
		}
		if templateGeneral.TypedConfig != nil {
			general["typed_config"] = templateGeneral.TypedConfig
		}
		if templateGeneral.ElchiDiscovery != nil {
			general["elchi_discovery"] = templateGeneral.ElchiDiscovery
		}
	}

	return general
}

/* func createTLSTransportSocket() map[string]any {
	return map[string]any{
		"name": "envoy.transport_sockets.tls",
		"typed_config": map[string]any{
			"@type": "type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext",
		},
	}
} */
