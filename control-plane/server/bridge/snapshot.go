package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/CloudNativeWorks/versioned-go-control-plane/pkg/cache/types"

	resource "github.com/CloudNativeWorks/versioned-go-control-plane/pkg/resource/v3"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
)

// GetNodeSnapshot returns the snapshot for a specific node
func (s *SnapshotServiceServer) GetNodeSnapshot(_ context.Context, req *bridge.NodeSnapshotRequest) (*bridge.NodeSnapshotResponse, error) {
	nodeID := req.NodeId

	snapshot, err := s.context.Cache.Cache.GetSnapshot(nodeID)
	if err != nil {
		logrus.Errorf("Error getting snapshot for node %s: %v", nodeID, err)
		return nil, err
	}
	status := s.context.Cache.Cache.GetStatusInfo(nodeID)

	var numWatches int
	var lastWatchTime string

	if status != nil {
		numWatches = status.GetNumDeltaWatches()
		lastWatchTime = status.GetLastDeltaWatchRequestTime().Format(time.RFC3339)
	} else {
		numWatches = 0
		lastWatchTime = ""
	}

	resources := map[string]map[string]types.Resource{
		"Cluster":       snapshot.GetResources(resource.ClusterType),
		"Endpoint":      snapshot.GetResources(resource.EndpointType),
		"Extension":     snapshot.GetResources(resource.ExtensionConfigType),
		"Listener":      snapshot.GetResources(resource.ListenerType),
		"Route":         snapshot.GetResources(resource.RouteType),
		"Runtime":       snapshot.GetResources(resource.RuntimeType),
		"Scoped Route":  snapshot.GetResources(resource.ScopedRouteType),
		"Secret":        snapshot.GetResources(resource.SecretType),
		"Thrift Router": snapshot.GetResources(resource.ThriftRouteType),
		"virtual Host":  snapshot.GetResources(resource.VirtualHostType),
	}

	resourceTypes := make([]string, 0, len(resources))
	for resourceType := range resources {
		resourceTypes = append(resourceTypes, resourceType)
	}
	sort.Strings(resourceTypes)

	// Build response
	response := &bridge.NodeSnapshotResponse{
		NodeId:     nodeID,
		NumWatches: int64(numWatches),
		LastWatch:  lastWatchTime,
	}

	for _, resourceType := range resourceTypes {
		resourceData := resources[resourceType]

		// Mask sensitive data for Secret resources
		shouldMaskSecrets := resourceType == "Secret"
		protoStruct, err := convertToStructPB(resourceData, shouldMaskSecrets)
		if err != nil {
			logrus.Errorf("Error converting resource data for type %s: %v", resourceType, err)
			return nil, err
		}

		response.Resources = append(response.Resources, &bridge.SnapshotResource{
			Type: resourceType,
			Data: protoStruct,
		})
	}

	return response, nil
}

// ClearNodeSnapshot clears the snapshot cache for a specific node
func (s *SnapshotServiceServer) ClearNodeSnapshot(_ context.Context, req *bridge.NodeSnapshotRequest) (*bridge.ClearSnapshotResponse, error) {
	nodeID := req.NodeId

	// Clear the snapshot from cache
	s.context.Cache.Cache.ClearSnapshot(nodeID)

	logrus.Infof("Cleared snapshot for node: %s", nodeID)

	return &bridge.ClearSnapshotResponse{
		Success: true,
		Message: fmt.Sprintf("Snapshot cleared for node %s", nodeID),
		NodeId:  nodeID,
	}, nil
}

func convertToStructPB(resourceData map[string]types.Resource, maskSecrets bool) (*structpb.Struct, error) {
	dataMap := make(map[string]any)

	// Use custom marshaler options to properly handle Any types
	marshaler := protojson.MarshalOptions{
		UseProtoNames: true,
	}

	for key, res := range resourceData {
		resProto, ok := res.(proto.Message)
		if !ok {
			return nil, fmt.Errorf("resource %s is not proto.Message", key)
		}

		jsonBytes, err := marshaler.Marshal(resProto)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal resource %s: %w", key, err)
		}

		// Debug: Log for resources containing "coraza"
		protoStr := fmt.Sprintf("%v", resProto)
		if len(protoStr) > 100 && (contains(protoStr, "coraza") || contains(protoStr, "directives_map")) {
			logrus.Debugf("🔍 [SNAPSHOT] Resource '%s' JSON (800 chars): %s", key, truncate(string(jsonBytes), 800))
		}

		var jsonData any
		if err := json.Unmarshal(jsonBytes, &jsonData); err != nil {
			return nil, fmt.Errorf("failed to unmarshal JSON for resource %s: %w", key, err)
		}

		// Mask sensitive secret data
		if maskSecrets {
			jsonData = maskSecretData(jsonData)
		}

		dataMap[key] = jsonData
	}

	return structpb.NewStruct(dataMap)
}

// maskSecretData recursively masks sensitive secret fields
func maskSecretData(data any) any {
	switch v := data.(type) {
	case map[string]any:
		masked := make(map[string]any)
		for key, value := range v {
			// Mask sensitive fields
			if isSensitiveField(key) {
				masked[key] = "***REDACTED***"
			} else {
				// Recursively process nested objects
				masked[key] = maskSecretData(value)
			}
		}
		return masked
	case []any:
		// Process arrays
		masked := make([]any, len(v))
		for i, item := range v {
			masked[i] = maskSecretData(item)
		}
		return masked
	default:
		return v
	}
}

// isSensitiveField checks if a field name contains sensitive data
func isSensitiveField(fieldName string) bool {
	// Exact match fields (case-insensitive)
	exactMatchFields := []string{
		"private_key",
		"password",
		"token",
		"api_key",
		"credential",
		"inline_bytes",
		"inline_string",
	}

	fieldLower := ""
	for _, c := range fieldName {
		if c >= 'A' && c <= 'Z' {
			fieldLower += string(c + 32)
		} else {
			fieldLower += string(c)
		}
	}

	// Check exact matches
	for _, sensitive := range exactMatchFields {
		if fieldLower == sensitive {
			return true
		}
	}

	// Special case: "secret" only if it's the exact field name or ends with "_secret"
	// This avoids masking "sds_secret_configs" which is just configuration
	if fieldLower == "secret" || contains(fieldLower, "_secret") {
		// But exclude configuration fields that just reference secrets
		if !contains(fieldLower, "secret_config") && !contains(fieldLower, "secret_provider") {
			return true
		}
	}

	return false
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
