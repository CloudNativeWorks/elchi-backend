package crud

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

func GetBootstrap(ctx context.Context, db *mongo.Database, listenerGeneral models.General, config *config.AppConfig) (map[string]any, error) {
	now := time.Now()
	CreatedAt := primitive.NewDateTimeFromTime(now)
	UpdatedAt := primitive.NewDateTimeFromTime(now)
	nodeID := fmt.Sprintf("%s::%s", listenerGeneral.Name, listenerGeneral.Project)

	cluster := createClusterConfig()
	port, err := GetNextAdminPort(ctx, db, listenerGeneral.Name, listenerGeneral.Project)
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
					"value":    "eyJuYW1lIjoiZWxjaGktY29udHJvbC1wbGFuZS1vdGVsIiwiY2Fub25pY2FsX25hbWUiOiJlbnZveS5zdGF0X3NpbmtzLm9wZW5fdGVsZW1ldHJ5IiwiZ3R5cGUiOiJlbnZveS5leHRlbnNpb25zLnN0YXRfc2lua3Mub3Blbl90ZWxlbWV0cnkudjMuU2lua0NvbmZpZyIsInR5cGUiOiJzdGF0X3NpbmtzIiwiY2F0ZWdvcnkiOiJlbnZveS5zdGF0c19zaW5rcyIsImNvbGxlY3Rpb24iOiJleHRlbnNpb25zIn0=",
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

func GetNextAdminPort(ctx context.Context, db *mongo.Database, bootstrapName, project string) (int, error) {
	const (
		adminPortStart      = 33100
		adminPortEnd        = 39999
		adminPortCollection = "admin_ports"
	)
	collection := db.Collection(adminPortCollection)

	var existing struct{ Port int }
	err := collection.FindOne(ctx, bson.M{"name": bootstrapName, "project": project}).Decode(&existing)
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
	if maxPort < adminPortStart {
		nextPort = adminPortStart
	} else if maxPort < adminPortEnd {
		nextPort = maxPort + 1
	} else {
		return 0, errors.New("no available admin port")
	}

	_, err = collection.InsertOne(ctx, bson.M{
		"name":       bootstrapName,
		"project":    project,
		"port":       nextPort,
		"created_at": time.Now(),
	})
	if err != nil {
		return 0, err
	}
	return nextPort, nil
}

/* func createTLSTransportSocket() map[string]any {
	return map[string]any{
		"name": "envoy.transport_sockets.tls",
		"typed_config": map[string]any{
			"@type": "type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext",
		},
	}
} */
