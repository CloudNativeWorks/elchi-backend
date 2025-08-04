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

func (s *SnapshotServiceServer) GetSnapshotKeys(_ context.Context, _ *bridge.Empty) (*bridge.SnapshotKeyList, error) {
	snapshotKeys := s.context.Cache.Cache.GetStatusKeys()
	return &bridge.SnapshotKeyList{Keys: snapshotKeys}, nil
}

func (s *SnapshotServiceServer) GetSnapshotResources(_ context.Context, req *bridge.SnapshotKey) (*bridge.SnapshotResourceList, error) {
	snapshot, err := s.context.Cache.Cache.GetSnapshot(req.Key)
	if err != nil {
		logrus.Errorf("Error getting snapshot for key %s: %v", req.Key, err)
		return nil, err
	}
	status := s.context.Cache.Cache.GetStatusInfo(req.Key)

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

	var resourceList bridge.SnapshotResourceList
	resourceList.NumWatches = int64(numWatches)
	resourceList.LastWatch = lastWatchTime

	for _, resourceType := range resourceTypes {
		resourceData := resources[resourceType]

		protoStruct, err := convertToStructPB(resourceData)
		if err != nil {
			logrus.Errorf("Error converting resource data for type %s: %v", resourceType, err)
			return nil, err
		}

		resourceList.Resources = append(resourceList.Resources, &bridge.SnapshotResource{
			Type: resourceType,
			Data: protoStruct,
		})
	}

	return &resourceList, nil
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
