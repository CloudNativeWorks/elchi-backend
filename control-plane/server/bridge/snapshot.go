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

		protoStruct, err := convertToStructPB(resourceData)
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

func convertToStructPB(resourceData map[string]types.Resource) (*structpb.Struct, error) {
	dataMap := make(map[string]any)
	for key, res := range resourceData {
		resProto, ok := res.(proto.Message)
		if !ok {
			return nil, fmt.Errorf("resource %s is not proto.Message", key)
		}

		jsonBytes, err := protojson.Marshal(resProto)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal resource %s: %w", key, err)
		}

		var jsonData any
		if err := json.Unmarshal(jsonBytes, &jsonData); err != nil {
			return nil, fmt.Errorf("failed to unmarshal JSON for resource %s: %w", key, err)
		}
		dataMap[key] = jsonData
	}

	return structpb.NewStruct(dataMap)
}
