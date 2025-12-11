package resource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/CloudNativeWorks/elchi-backend/control-plane/server/resources/common"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
)

type AllResources struct {
	*common.Resources
	mutex sync.RWMutex
}

func NewResources() *AllResources {
	return &AllResources{
		Resources: &common.Resources{},
	}
}

func GenerateSnapshot(ctx context.Context, rawListenerResource *models.DBResource, listenerName string, db *db.AppContext, logger *logger.Logger, project, version, downstreamAddress string) (*AllResources, error) {
	ar := NewResources()
	var nodeID string

	if downstreamAddress != "" {
		nodeID = fmt.Sprintf("%s::%s::%s", listenerName, project, downstreamAddress)
	} else {
		nodeID = fmt.Sprintf("%s::%s", listenerName, project)
	}

	ar.mutex.Lock()
	ar.SetNodeID(nodeID)
	ar.SetResourceVersion(version)

	// Set listener metadata for FileAccessLog path override
	ar.ListenerName = listenerName
	ar.IsManaged = rawListenerResource.General.Managed

	// Get admin port from admin_ports collection (used for access log paths)
	ar.AdminPort = resources.GetAdminPort(ctx, db, listenerName, project, version)

	ar.mutex.Unlock()
	ar.DecodeListener(ctx, rawListenerResource, db, logger, downstreamAddress)
	return ar, nil
}

// CalculateContentHash calculates SHA256 hash of all resources in the snapshot
// This is used to detect if configuration actually changed
// Version is NOT included in hash calculation as it always increments
func (ar *AllResources) CalculateContentHash() string {
	ar.mutex.RLock()
	defer ar.mutex.RUnlock()

	hasher := sha256.New()

	// Helper function to hash a list of resources deterministically
	hashResourceList := func(resources []any) {
		// Convert to sortable slice
		var sortedData [][]byte
		for _, res := range resources {
			if msg, ok := res.(proto.Message); ok {
				if data, err := proto.Marshal(msg); err == nil {
					sortedData = append(sortedData, data)
				}
			}
		}

		// Sort for deterministic hash
		sort.Slice(sortedData, func(i, j int) bool {
			return string(sortedData[i]) < string(sortedData[j])
		})

		// Write to hasher
		for _, data := range sortedData {
			hasher.Write(data)
		}
	}

	// Hash all resource types in deterministic order
	// Convert types.Resource to any for the helper function

	// Clusters
	if clusters := ar.GetClusterT(); len(clusters) > 0 {
		hasher.Write([]byte("CLUSTERS"))
		clustersAny := make([]any, len(clusters))
		for i, c := range clusters {
			clustersAny[i] = c
		}
		hashResourceList(clustersAny)
	}

	// Routes
	if routes := ar.GetRouteT(); len(routes) > 0 {
		hasher.Write([]byte("ROUTES"))
		routesAny := make([]any, len(routes))
		for i, r := range routes {
			routesAny[i] = r
		}
		hashResourceList(routesAny)
	}

	// Listeners
	if listeners := ar.GetListenerT(); len(listeners) > 0 {
		hasher.Write([]byte("LISTENERS"))
		listenersAny := make([]any, len(listeners))
		for i, l := range listeners {
			listenersAny[i] = l
		}
		hashResourceList(listenersAny)
	}

	// Endpoints
	if endpoints := ar.GetEndpointT(); len(endpoints) > 0 {
		hasher.Write([]byte("ENDPOINTS"))
		endpointsAny := make([]any, len(endpoints))
		for i, e := range endpoints {
			endpointsAny[i] = e
		}
		hashResourceList(endpointsAny)
	}

	// VirtualHosts
	if vhosts := ar.GetVirtualHostT(); len(vhosts) > 0 {
		hasher.Write([]byte("VIRTUALHOSTS"))
		vhostsAny := make([]any, len(vhosts))
		for i, v := range vhosts {
			vhostsAny[i] = v
		}
		hashResourceList(vhostsAny)
	}

	// Extensions
	if extensions := ar.GetExtensionsT(); len(extensions) > 0 {
		hasher.Write([]byte("EXTENSIONS"))
		extensionsAny := make([]any, len(extensions))
		for i, e := range extensions {
			extensionsAny[i] = e
		}
		hashResourceList(extensionsAny)
	}

	// Secrets
	if secrets := ar.GetSecretT(); len(secrets) > 0 {
		hasher.Write([]byte("SECRETS"))
		secretsAny := make([]any, len(secrets))
		for i, s := range secrets {
			secretsAny[i] = s
		}
		hashResourceList(secretsAny)
	}

	return hex.EncodeToString(hasher.Sum(nil))
}
