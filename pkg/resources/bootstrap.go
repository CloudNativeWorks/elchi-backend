package resources

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type AdminPort struct {
	AdminPort int64 `json:"admin_port" bson:"admin_port"`
}

type BootstrapAdmin struct {
	Admin struct {
		Address struct {
			SocketAddress struct {
				Protocol  string `json:"Protocol"`
				Address   string `json:"address"`
				PortValue uint32 `json:"port_value"`
			} `json:"socket_address"`
		} `json:"address"`
	} `json:"admin"`
}

func GetAdminPortFromBootstrap(bootstrapAny any) (uint32, error) {
	jsonBytes, err := json.Marshal(bootstrapAny)
	if err != nil {
		return 0, err
	}

	var bs BootstrapAdmin
	err = json.Unmarshal(jsonBytes, &bs)
	if err != nil {
		return 0, err
	}

	return bs.Admin.Address.SocketAddress.PortValue, nil
}

func GetAdminPortFromService(db *mongo.Database, op models.OperationClass) (uint32, error) {
	var service AdminPort

	err := db.Collection("services").FindOne(
		context.TODO(),
		bson.M{"name": op.GetCommandName(), "project": op.GetCommandProject()},
	).Decode(&service)

	if err != nil {
		return 0, err
	}

	return uint32(service.AdminPort), nil
}

func AttachDownstreamAddressToBootstrap(bootstrapAny any, cf models.ClientFields) error {
	m, ok := bootstrapAny.(bson.M)
	if !ok {
		return errors.New("bootstrapAny is not bson.M")
	}

	// node.id güncelle
	node, ok := m["node"].(bson.M)
	if !ok {
		return errors.New("missing or invalid 'node' field (not bson.M)")
	}
	id, err := getStringField(node, "id")
	if err != nil {
		return err
	}
	if !strings.HasSuffix(id, "::"+cf.DownstreamAddress) {
		node["id"] = id + "::" + cf.DownstreamAddress
	}

	// add initial_metadata
	dynRes, ok := m["dynamic_resources"].(bson.M)
	if !ok {
		return errors.New("missing or invalid 'dynamic_resources' field (not bson.M)")
	}
	adsConfig, ok := dynRes["ads_config"].(bson.M)
	if !ok {
		return errors.New("missing or invalid 'ads_config' field (not bson.M)")
	}
	grpcServices, ok := adsConfig["grpc_services"].(primitive.A)
	if !ok || len(grpcServices) == 0 {
		return errors.New("missing, invalid or empty 'grpc_services' field (not primitive.A)")
	}
	grpcService, ok := grpcServices[0].(bson.M)
	if !ok {
		return errors.New("invalid 'grpc_services[0]' type (not bson.M)")
	}
	initialMetadata, ok := grpcService["initial_metadata"].(primitive.A)
	if !ok {
		return errors.New("missing or invalid 'initial_metadata' field (not primitive.A)")
	}

	// update nodeid metadata
	if nodeidMeta, idx := findMetadataItem(initialMetadata, "nodeid"); nodeidMeta != nil {
		val, err := getStringField(nodeidMeta, "value")
		if err != nil {
			return errors.New("invalid 'nodeid' value (not string)")
		}
		if !strings.HasSuffix(val, "::"+cf.DownstreamAddress) {
			nodeidMeta["value"] = val + "::" + cf.DownstreamAddress
			initialMetadata[idx] = nodeidMeta
		}
	}

	// if downstream_address metadata already exists, don't add it again
	if meta, _ := findMetadataItem(initialMetadata, "downstream_address"); meta == nil {
		initialMetadata = append(initialMetadata, primitive.M{
			"key":   "downstream_address",
			"value": cf.DownstreamAddress,
		})
	}

	// if client_name metadata already exists, don't add it again
	if meta, _ := findMetadataItem(initialMetadata, "client_name"); meta == nil {
		initialMetadata = append(initialMetadata, primitive.M{
			"key":   "client_name",
			"value": cf.ClientName,
		})
	}

	grpcService["initial_metadata"] = initialMetadata

	return nil
}

// Helper: safely get a string field from a bson.M
func getStringField(m bson.M, key string) (string, error) {
	val, ok := m[key]
	if !ok {
		return "", errors.New("missing '" + key + "' field")
	}
	str, ok := val.(string)
	if !ok {
		return "", errors.New("'" + key + "' is not string")
	}
	return str, nil
}

// Helper: find the first item in primitive.M that matches the key
func findMetadataItem(metadata primitive.A, key string) (primitive.M, int) {
	for i, item := range metadata {
		if m, ok := item.(primitive.M); ok {
			if m["key"] == key {
				return m, i
			}
		}
	}
	return nil, -1
}
