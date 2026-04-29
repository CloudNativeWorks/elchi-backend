// Package discovery provides Kubernetes endpoint discovery functionality
// for automatic service endpoint synchronization.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async/job"
	"github.com/CloudNativeWorks/elchi-backend/pkg/audit"
	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type DiscoveryService struct {
	dbContext         *db.AppContext
	lastSeenCache     map[string]time.Time // cluster_name:project -> last_update_time
	lastSeenCacheMux  sync.RWMutex
	lastSeenThreshold time.Duration // minimum interval between last_seen updates
	pokeService       *bridge.PokeServiceClient
	auditService      *audit.Service
	logger            *logger.Logger
}

func NewDiscoveryService(dbContext *db.AppContext, pokeService *bridge.PokeServiceClient) *DiscoveryService {
	// Initialize audit service
	auditSvc := audit.NewService(dbContext, logger.NewLogger("discovery/audit"))

	return &DiscoveryService{
		dbContext:         dbContext,
		lastSeenCache:     make(map[string]time.Time),
		lastSeenThreshold: 2 * time.Minute, // Only update MongoDB every 2 minutes max
		pokeService:       pokeService,
		auditService:      auditSvc,
		logger:            logger.NewLogger("controller/discovery/service"),
	}
}

// ProcessK8sDiscovery processes incoming K8s discovery data and updates endpoints
func (ds *DiscoveryService) ProcessK8sDiscovery(
	ctx context.Context,
	c *gin.Context,
	request K8sDiscoveryRequest,
	project string,
	isInitial bool,
) (*DiscoveryProcessResult, error) {
	// If initial (from header) is true, register or update cluster in discovery collection
	if isInitial {
		if err := ds.registerCluster(ctx, request, project); err != nil {
			ds.logger.Errorf("Failed to register cluster: %v", err)
		}
	}

	// Always update last_seen timestamp for every request (respects cache timing)
	if err := ds.updateClusterLastSeen(ctx, request.Data.ClusterInfo.ClusterName, project, false); err != nil {
		ds.logger.Errorf("Failed to update cluster last_seen: %v", err)
	}
	result := &DiscoveryProcessResult{
		ProcessedAt:    time.Now(),
		ClusterName:    request.Data.ClusterInfo.ClusterName,
		NodesReceived:  request.Data.NodeCount,
		EndpointsFound: 0,
		Updates:        []EndpointUpdateResult{},
		Success:        true,
	}

	// Validate minimum node requirement
	if len(request.Data.Nodes) == 0 {
		result.Success = false
		result.Message = "No nodes provided in discovery request"
		return result, fmt.Errorf("no nodes provided")
	}

	// Get endpoints collection
	endpointsCollection := ds.dbContext.Client.Collection("endpoints")

	// Find all endpoints that match the cluster name in elchi_discovery
	filter := bson.M{
		"general.project": project,
		"general.elchi_discovery": bson.M{
			"$elemMatch": bson.M{
				"cluster_name": request.Data.ClusterInfo.ClusterName,
			},
		},
	}

	cursor, err := endpointsCollection.Find(ctx, filter)
	if err != nil {
		result.Success = false
		result.Message = fmt.Sprintf("Failed to query endpoints: %v", err)
		return result, err
	}
	defer cursor.Close(ctx)

	var endpoints []models.DBResource
	if err = cursor.All(ctx, &endpoints); err != nil {
		result.Success = false
		result.Message = fmt.Sprintf("Failed to decode endpoints: %v", err)
		return result, err
	}

	result.EndpointsFound = len(endpoints)

	// Track if any endpoints changed
	endpointsChanged := false

	// Process each endpoint
	for _, endpoint := range endpoints {
		updateResult := ds.updateEndpointFromNodes(ctx, endpoint, request.Data.Nodes, request.Data.ClusterInfo.ClusterName)
		result.Updates = append(result.Updates, updateResult)

		if updateResult.Changed {
			endpointsChanged = true
			// Update the endpoint in database
			if err := ds.updateEndpointInDB(ctx, endpoint); err != nil {
				ds.logger.Errorf("Failed to update endpoint %s: %v", endpoint.General.Name, err)
				updateResult.Changed = false
			} else {
				ds.logger.Infof("Updated endpoint %s with %d nodes from cluster %s",
					endpoint.General.Name, updateResult.AddedNodes, request.Data.ClusterInfo.ClusterName)

				// Log discovery audit (only when IP changes and snapshot is triggered)
				ds.logDiscoveryAudit(ctx, c, endpoint, updateResult, request.Data.ClusterInfo.ClusterName)

				// Trigger async snapshot update with fast response (preliminary job)
				ds.triggerAsyncSnapshotUpdate(ctx, endpoint, project)
			}
		}
	}

	// If endpoints changed, update discovery collection with new node data and force last_seen update
	if endpointsChanged {
		if err := ds.updateClusterNodeIPs(ctx, request, project); err != nil {
			ds.logger.Errorf("Failed to update cluster node IPs in discovery: %v", err)
		}

		// Force immediate last_seen update when endpoints change (bypass cache)
		if err := ds.updateClusterLastSeen(ctx, request.Data.ClusterInfo.ClusterName, project, true); err != nil {
			ds.logger.Errorf("Failed to force update cluster last_seen after endpoint change: %v", err)
		}
	}

	if result.EndpointsFound == 0 {
		result.Message = fmt.Sprintf("No endpoints found for cluster: %s", request.Data.ClusterInfo.ClusterName)
	}

	return result, nil
}

// updateEndpointFromNodes compares and updates endpoint with K8s nodes
func (ds *DiscoveryService) updateEndpointFromNodes(_ context.Context, endpoint models.DBResource, nodes []NodeInfo, clusterName string) EndpointUpdateResult {
	updateResult := EndpointUpdateResult{
		EndpointName: endpoint.General.Name,
		ClusterName:  clusterName,
		UpdatedIPs:   []string{},
		AddedNodes:   0,
		RemovedNodes: 0,
		Changed:      false,
	}

	// Find the matching ElchiDiscovery configuration
	var discoveryConfig *models.ElchiDiscovery
	for _, discovery := range endpoint.General.ElchiDiscovery {
		if discovery.ClusterName == clusterName {
			discoveryConfig = discovery
			break
		}
	}

	if discoveryConfig == nil {
		return updateResult
	}

	// Extract current IPs from endpoint for this specific cluster
	currentIPs := ds.extractCurrentIPsForCluster(endpoint, clusterName)

	// Convert nodes to helper NodeInfo format
	helperNodes := make([]helper.NodeInfo, len(nodes))
	for i, node := range nodes {
		helperNodes[i] = helper.NodeInfo{
			Status:    node.Status,
			Addresses: node.Addresses,
			Roles:     node.Roles,
		}
	}

	// Convert discoveryConfig to helper format
	helperConfig := helper.DiscoveryConfig{
		ClusterName: discoveryConfig.ClusterName,
		Port:        discoveryConfig.Port,
		Protocol:    discoveryConfig.Protocol,
		AddressType: discoveryConfig.AddressType,
		Roles:       discoveryConfig.Roles,
	}

	// Use helper utility for filtering and IP extraction
	newIPs := helper.FilterAndExtractIPs(helperNodes, helperConfig)

	// UpdatedIPs will be set after successful filtering and comparison
	updateResult.UpdatedIPs = newIPs

	// Compare IP lists
	if !ds.compareIPLists(currentIPs, newIPs) {
		// IMPORTANT: If new IPs list is empty, don't mark as changed
		// This prevents unnecessary snapshot updates when discovery filtering returns no nodes
		if len(newIPs) == 0 && len(currentIPs) > 0 {
			ds.logger.Warnf("Discovery filtering returned 0 IPs for endpoint %s (cluster: %s), but current has %d IPs. "+
				"Check elchi_discovery config (roles, address_type). Skipping update to prevent clearing valid IPs.",
				endpoint.General.Name, clusterName, len(currentIPs))
			updateResult.UpdatedIPs = currentIPs // Keep current IPs
			updateResult.Changed = false
			return updateResult
		}

		// Calculate added and removed IPs for audit
		addedIPs := []string{}
		removedIPs := []string{}

		// Find added IPs (in new but not in current)
		currentIPMap := make(map[string]bool)
		for _, ip := range currentIPs {
			currentIPMap[ip] = true
		}
		for _, ip := range newIPs {
			if !currentIPMap[ip] {
				addedIPs = append(addedIPs, ip)
			}
		}

		// Find removed IPs (in current but not in new)
		newIPMap := make(map[string]bool)
		for _, ip := range newIPs {
			newIPMap[ip] = true
		}
		for _, ip := range currentIPs {
			if !newIPMap[ip] {
				removedIPs = append(removedIPs, ip)
			}
		}

		// Store the diff in UpdatedIPs temporarily (will be used in audit)
		// Format: "added:ip1,ip2;removed:ip3,ip4"
		diffInfo := fmt.Sprintf("added:%s;removed:%s;previous:%s",
			strings.Join(addedIPs, ","),
			strings.Join(removedIPs, ","),
			strings.Join(currentIPs, ","))

		// Update endpoint resource with new IPs for this specific cluster
		ds.updateEndpointResourceForCluster(endpoint, newIPs, discoveryConfig, clusterName)
		updateResult.Changed = true
		updateResult.AddedNodes = len(addedIPs)
		updateResult.RemovedNodes = len(removedIPs)

		// Store diff info in the first element temporarily for audit
		updateResult.UpdatedIPs = append([]string{diffInfo}, newIPs...)
	} else {
		// No change, just set the current IPs
		updateResult.UpdatedIPs = newIPs
	}

	return updateResult
}

// extractCurrentIPsForCluster extracts IP addresses from endpoint resource for a specific cluster
func (ds *DiscoveryService) extractCurrentIPsForCluster(endpoint models.DBResource, clusterName string) []string {
	ips := []string{}

	// Parse the resource structure - handle both primitive.M and map[string]any
	var resourceData map[string]any
	switch v := endpoint.Resource.Resource.(type) {
	case primitive.M:
		resourceData = map[string]any(v)
	case map[string]any:
		resourceData = v
	default:
		return ips
	}

	// Try multiple types for endpoints field
	var endpoints []any
	if endpointsField, exists := resourceData["endpoints"]; exists {
		switch v := endpointsField.(type) {
		case []any:
			endpoints = v
		case primitive.A:
			endpoints = []any(v)
		default:
			return ips
		}
	} else {
		return ips
	}

	for _, ep := range endpoints {
		var epMap map[string]any
		switch v := ep.(type) {
		case primitive.M:
			epMap = map[string]any(v)
		case map[string]any:
			epMap = v
		default:
			continue
		}

		// Check if this endpoint belongs to the specific cluster via locality.region
		var locality map[string]any
		hasLocality := false

		if localityField, exists := epMap["locality"]; exists {
			switch v := localityField.(type) {
			case primitive.M:
				locality = map[string]any(v)
				hasLocality = true
			case map[string]any:
				locality = v
				hasLocality = true
			}
		}

		if hasLocality {
			if region, ok := locality["region"].(string); ok && region != clusterName {
				continue // Skip this endpoint as it belongs to a different cluster
			}
		} else {
			// If no locality, skip this endpoint - we only process cluster-specific endpoints
			continue
		}

		// Handle lb_endpoints with multiple types
		var lbEndpoints []any
		if lbEndpointsField, exists := epMap["lb_endpoints"]; exists {
			switch v := lbEndpointsField.(type) {
			case []any:
				lbEndpoints = v
			case primitive.A:
				lbEndpoints = []any(v)
			default:
				continue
			}
		} else {
			continue
		}

		for _, lbEp := range lbEndpoints {
			var lbEpMap map[string]any
			switch v := lbEp.(type) {
			case primitive.M:
				lbEpMap = map[string]any(v)
			case map[string]any:
				lbEpMap = v
			default:
				continue
			}

			var endpointMap map[string]any
			if endpointField, exists := lbEpMap["endpoint"]; exists {
				switch v := endpointField.(type) {
				case primitive.M:
					endpointMap = map[string]any(v)
				case map[string]any:
					endpointMap = v
				default:
					continue
				}
			} else {
				continue
			}

			var address map[string]any
			if addressField, exists := endpointMap["address"]; exists {
				switch v := addressField.(type) {
				case primitive.M:
					address = map[string]any(v)
				case map[string]any:
					address = v
				default:
					continue
				}
			} else {
				continue
			}

			var socketAddress map[string]any
			if socketField, exists := address["socket_address"]; exists {
				switch v := socketField.(type) {
				case primitive.M:
					socketAddress = map[string]any(v)
				case map[string]any:
					socketAddress = v
				default:
					continue
				}
			} else {
				continue
			}

			if ip, ok := socketAddress["address"].(string); ok {
				ips = append(ips, ip)
			}
		}
	}

	return ips
}

// compareIPLists compares two IP lists to check if they're different (order independent)
func (ds *DiscoveryService) compareIPLists(current, new []string) bool {
	if len(current) != len(new) {
		return false
	}

	// Create map for fast lookup (order independent comparison)
	currentMap := make(map[string]bool)
	for _, ip := range current {
		currentMap[ip] = true
	}

	// Check if all new IPs exist in current
	for _, ip := range new {
		if !currentMap[ip] {
			return false
		}
	}

	return true
}

// updateEndpointResourceForCluster updates only the specific cluster's endpoints
func (ds *DiscoveryService) updateEndpointResourceForCluster(endpoint models.DBResource, newIPs []string, discoveryConfig *models.ElchiDiscovery, clusterName string) {
	// Handle both primitive.M (from MongoDB) and map[string]any types
	var resourceData map[string]any
	switch v := endpoint.Resource.Resource.(type) {
	case primitive.M:
		resourceData = map[string]any(v)
	case map[string]any:
		resourceData = v
	default:
		// If unexpected type, create empty resource
		resourceData = make(map[string]any)
	}
	endpoints, ok := resourceData["endpoints"].([]any)
	if !ok {
		endpoints = []any{}
	}

	// Create new lb_endpoints for this cluster
	newLbEndpoints := []map[string]any{}
	for _, ip := range newIPs {
		lbEndpoint := map[string]any{
			"endpoint": map[string]any{
				"address": map[string]any{
					"socket_address": map[string]any{
						"protocol":   discoveryConfig.Protocol,
						"address":    ip,
						"port_value": discoveryConfig.Port,
					},
				},
			},
		}
		newLbEndpoints = append(newLbEndpoints, lbEndpoint)
	}

	// Create/update the endpoint for this cluster with locality
	clusterEndpoint := map[string]any{
		"locality": map[string]any{
			"region": clusterName, // Use cluster name as region for identification
		},
		"lb_endpoints": newLbEndpoints,
	}

	// Find and update the existing cluster endpoint, or add new one
	var updatedEndpoints []any
	clusterFound := false

	for _, ep := range endpoints {
		epMap, ok := ep.(map[string]any)
		if !ok {
			updatedEndpoints = append(updatedEndpoints, ep)
			continue
		}

		// Check if this endpoint belongs to our cluster
		locality, hasLocality := epMap["locality"].(map[string]any)
		if hasLocality {
			if region, ok := locality["region"].(string); ok && region == clusterName {
				// Replace this cluster's endpoint
				updatedEndpoints = append(updatedEndpoints, clusterEndpoint)
				clusterFound = true
				continue
			}
		}

		// Keep other clusters' endpoints unchanged
		updatedEndpoints = append(updatedEndpoints, ep)
	}

	// If cluster endpoint not found, add it
	if !clusterFound {
		updatedEndpoints = append(updatedEndpoints, clusterEndpoint)
	}

	// Update the resource structure
	resourceData["endpoints"] = updatedEndpoints

	// Update timestamp
	endpoint.General.UpdatedAt = primitive.NewDateTimeFromTime(time.Now())
}

// updateEndpointInDB updates the endpoint in MongoDB
func (ds *DiscoveryService) updateEndpointInDB(ctx context.Context, endpoint models.DBResource) error {
	endpointsCollection := ds.dbContext.Client.Collection("endpoints")

	filter := bson.M{"_id": endpoint.ID}
	update := bson.M{
		"$set": bson.M{
			"resource":           endpoint.Resource,
			"general.updated_at": endpoint.General.UpdatedAt,
		},
	}

	_, err := endpointsCollection.UpdateOne(ctx, filter, update)
	return err
}

// ValidateDiscoveryToken validates discovery authentication token
func (ds *DiscoveryService) ValidateDiscoveryToken(ctx context.Context, token, project string) (bool, error) {
	settingsCollection := ds.dbContext.Client.Collection("settings")

	filter := bson.M{
		"project":         project,
		"discovery_token": token,
	}

	var settings bson.M
	err := settingsCollection.FindOne(ctx, filter).Decode(&settings)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

// getClusterByID retrieves a cluster by its ID
func (ds *DiscoveryService) getClusterByID(ctx context.Context, clusterID, project string) (*ClusterDiscovery, error) {
	discoveryCollection := ds.dbContext.Client.Collection("discovery")

	// Convert string ID to ObjectID
	objectID, err := primitive.ObjectIDFromHex(clusterID)
	if err != nil {
		return nil, fmt.Errorf("invalid cluster ID format: %w", err)
	}

	filter := bson.M{
		"_id":     objectID,
		"project": project,
	}

	var cluster ClusterDiscovery
	err = discoveryCollection.FindOne(ctx, filter).Decode(&cluster)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("cluster not found with ID: %s", clusterID)
		}
		return nil, fmt.Errorf("failed to get cluster: %w", err)
	}

	return &cluster, nil
}

// GetClusterUsage returns which endpoints are using a specific cluster by ID
func (ds *DiscoveryService) GetClusterUsage(ctx context.Context, clusterID, project string) ([]ClusterUsageInfo, error) {
	// First, get the cluster name from the cluster ID
	cluster, err := ds.getClusterByID(ctx, clusterID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster: %w", err)
	}

	// Find endpoints that reference this cluster in their elchi_discovery configs
	filter := bson.M{
		"general.project": project,
		"general.elchi_discovery": bson.M{
			"$elemMatch": bson.M{
				"cluster_name": cluster.ClusterName,
			},
		},
	}

	cursor, err := ds.dbContext.Client.Collection("endpoints").Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to query endpoints: %w", err)
	}
	defer cursor.Close(ctx)

	var usage []ClusterUsageInfo
	for cursor.Next(ctx) {
		var endpoint models.DBResource
		if err := cursor.Decode(&endpoint); err != nil {
			ds.logger.Warnf("Failed to decode endpoint: %v", err)
			continue
		}

		// Extract cluster usage details
		usageInfo := ClusterUsageInfo{
			EndpointName: endpoint.General.Name,
			ResourceID:   endpoint.General.Name, // Use name as resource ID
			Version:      endpoint.General.Version,
			Project:      endpoint.General.Project,
			UpdatedAt:    time.Unix(int64(endpoint.General.UpdatedAt)/1000, 0),
		}

		// Count IPs for this cluster
		currentIPs := ds.extractCurrentIPsForCluster(endpoint, cluster.ClusterName)
		usageInfo.IPCount = len(currentIPs)
		usageInfo.IPs = currentIPs

		usage = append(usage, usageInfo)
	}

	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("cursor error: %w", err)
	}

	return usage, nil
}

// registerCluster registers or updates cluster information in discovery collection
func (ds *DiscoveryService) registerCluster(ctx context.Context, request K8sDiscoveryRequest, project string) error {
	discoveryCollection := ds.dbContext.Client.Collection("discovery")

	now := time.Now()

	// Check if cluster already exists
	filter := bson.M{
		"cluster_name": request.Data.ClusterInfo.ClusterName,
		"project":      project,
	}

	var existingCluster ClusterDiscovery
	err := discoveryCollection.FindOne(ctx, filter).Decode(&existingCluster)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return fmt.Errorf("failed to check existing cluster: %w", err)
	}

	if errors.Is(err, mongo.ErrNoDocuments) {
		// Create new cluster record
		newCluster := ClusterDiscovery{
			ClusterName:       request.Data.ClusterInfo.ClusterName,
			Project:           project,
			Nodes:             request.Data.Nodes,
			LastSeen:          now,
			CreatedAt:         now,
			UpdatedAt:         now,
			NodeCount:         request.Data.NodeCount,
			ClusterVersion:    request.Data.ClusterInfo.ClusterVersion,
			DiscoveryDuration: request.Data.DiscoveryDuration,
			LastRequestTime:   request.Data.Timestamp,
		}

		_, err = discoveryCollection.InsertOne(ctx, newCluster)
		if err != nil {
			return fmt.Errorf("failed to insert cluster: %w", err)
		}

		ds.logger.Infof("Registered new cluster: %s for project: %s with %d nodes", request.Data.ClusterInfo.ClusterName, project, len(request.Data.Nodes))
	} else {
		// Update existing cluster
		update := bson.M{
			"$set": bson.M{
				"nodes":              request.Data.Nodes,
				"last_seen":          now,
				"updated_at":         now,
				"node_count":         request.Data.NodeCount,
				"cluster_version":    request.Data.ClusterInfo.ClusterVersion,
				"discovery_duration": request.Data.DiscoveryDuration,
				"last_request_time":  request.Data.Timestamp,
			},
		}

		_, err = discoveryCollection.UpdateOne(ctx, filter, update)
		if err != nil {
			return fmt.Errorf("failed to update cluster: %w", err)
		}

		ds.logger.Infof("Updated cluster: %s for project: %s with %d nodes", request.Data.ClusterInfo.ClusterName, project, len(request.Data.Nodes))
	}

	return nil
}

// GetClusters returns all registered clusters for a project
func (ds *DiscoveryService) GetClusters(ctx context.Context, project string) ([]ClusterDiscovery, error) {
	discoveryCollection := ds.dbContext.Client.Collection("discovery")

	filter := bson.M{"project": project}
	cursor, err := discoveryCollection.Find(ctx, filter, options.Find().SetSort(bson.M{"updated_at": -1}))
	if err != nil {
		return nil, fmt.Errorf("failed to query clusters: %w", err)
	}
	defer cursor.Close(ctx)

	var clusters []ClusterDiscovery
	if err = cursor.All(ctx, &clusters); err != nil {
		return nil, fmt.Errorf("failed to decode clusters: %w", err)
	}

	return clusters, nil
}

// updateClusterLastSeen updates only the last_seen timestamp efficiently
// Uses in-memory cache to minimize MongoDB load - only updates every 2 minutes max per cluster
// forceUpdate bypasses cache when endpoints change
func (ds *DiscoveryService) updateClusterLastSeen(ctx context.Context, clusterName, project string, forceUpdate bool) error {
	cacheKey := fmt.Sprintf("%s:%s", clusterName, project)
	now := time.Now()

	// Skip cache check if forceUpdate is true (when endpoints change)
	if !forceUpdate {
		// Check cache to see if we recently updated this cluster
		ds.lastSeenCacheMux.RLock()
		lastUpdate, exists := ds.lastSeenCache[cacheKey]
		ds.lastSeenCacheMux.RUnlock()

		// If we updated this cluster recently, skip MongoDB update
		if exists && now.Sub(lastUpdate) < ds.lastSeenThreshold {
			return nil
		}
	}

	// Proceed with MongoDB update
	discoveryCollection := ds.dbContext.Client.Collection("discovery")

	filter := bson.M{
		"cluster_name": clusterName,
		"project":      project,
	}

	// Only update last_seen field with minimal MongoDB operation
	update := bson.M{
		"$set": bson.M{
			"last_seen": now,
		},
	}

	// Use upsert: false to avoid creating new documents, only update existing ones
	opts := options.Update().SetUpsert(false)
	result, err := discoveryCollection.UpdateOne(ctx, filter, update, opts)
	if err != nil {
		return fmt.Errorf("failed to update last_seen: %w", err)
	}

	// If update was successful, update cache
	if result.MatchedCount > 0 {
		ds.lastSeenCacheMux.Lock()
		ds.lastSeenCache[cacheKey] = now
		ds.lastSeenCacheMux.Unlock()
	} else {
		// If no document was matched/updated, the cluster doesn't exist yet
		// This is normal for clusters that haven't sent initial=true yet
		ds.logger.Warnf("Cluster %s not found for last_seen update (no initial registration yet)", clusterName)
	}

	return nil
}

// triggerAsyncSnapshotUpdate triggers async snapshot update with fast response pattern (only when nodes change)
func (ds *DiscoveryService) triggerAsyncSnapshotUpdate(ctx context.Context, endpoint models.DBResource, project string) {
	if ds.pokeService == nil {
		ds.logger.Warnf("PokeService not available, skipping snapshot update")
		return
	}

	// Initialize async job system for discovery updates
	asyncSystem, err := async.NewAsyncJobSystem(&async.Config{
		MongoDB:     ds.dbContext.Client,
		DBContext:   ds.dbContext,
		WorkerCount: 2, // Fewer workers for discovery updates
		BatchSize:   3,
	})
	if err != nil {
		ds.logger.Errorf("Failed to initialize async system for discovery update, falling back to sync: %v", err)
		// Fallback to synchronous processing
		ds.triggerSyncSnapshotUpdate(ctx, endpoint, project)
		return
	}

	// Set poke service for async system
	if err := asyncSystem.SetPokeService(ds.pokeService); err != nil {
		ds.logger.Errorf("Failed to set poke service for discovery update, falling back to sync: %v", err)
		// Fallback to synchronous processing
		ds.triggerSyncSnapshotUpdate(ctx, endpoint, project)
		return
	}

	// Create preliminary job immediately (fast response - no upfront analysis)
	jobReq := &async.CreateJobRequest{
		Type:   job.JobTypeSnapshotUpdate,
		Status: job.JobStatusAnalyzing, // Will be set automatically by CreatePreliminaryJob
		Metadata: &job.JobMetadata{
			SourceResource: &job.SourceResource{
				Type:       "endpoint",
				Name:       endpoint.General.Name,
				Collection: "endpoints",
				Action:     job.ActionType("DISCOVERY_UPDATE"),
				ProjectID:  project,
				Version:    endpoint.General.Version,
			},
			TriggerUser: &job.TriggerUser{
				ID:       "system",
				Username: "discovery-service",
				Role:     "system",
			},
			AffectedListeners: []string{}, // Will be populated by background analysis
			TotalAffected:     0,          // Will be updated after analysis
			AnalysisDuration:  0,          // Will be set after analysis completes
		},
	}

	preliminaryJob, err := asyncSystem.CreatePreliminaryJob(ctx, jobReq)
	if err != nil {
		ds.logger.Errorf("Failed to create preliminary discovery job, falling back to sync: %v", err)
		// Fallback to synchronous processing
		ds.triggerSyncSnapshotUpdate(ctx, endpoint, project)
		return
	}

	// Start background dependency analysis
	asyncSystem.PerformBackgroundAnalysis(preliminaryJob.ID.Hex(), &endpoint, nil, project)

	ds.logger.Infof("Created preliminary discovery job %s for endpoint %s (node changes), analyzing dependencies in background",
		preliminaryJob.JobID, endpoint.General.Name)
}

// triggerSyncSnapshotUpdate is the fallback synchronous version
func (ds *DiscoveryService) triggerSyncSnapshotUpdate(ctx context.Context, endpoint models.DBResource, project string) {
	// Create request details for HandleResourceChange
	requestDetails := models.RequestDetails{
		Name:          endpoint.General.Name,
		Collection:    "endpoints",
		Version:       endpoint.General.Version,
		Project:       project,
		SaveOrPublish: "publish", // Force publish to trigger snapshot update
	}

	// Use HandleResourceChange to detect and update affected listeners
	processed := crud.HandleResourceChange(
		ctx,
		&endpoint,
		requestDetails,
		ds.dbContext,
		project,
		ds.pokeService,
	)

	if processed != nil {
		ds.logger.Infof("Triggered sync snapshot update for endpoint %s, affected listeners: %v",
			endpoint.General.Name, processed.Listeners)
	}
}

// extractNodeIPsFromRequest extracts all node IPs from the request
// Uses specified addressType preference, defaults to ExternalIP with InternalIP fallback
func (ds *DiscoveryService) extractNodeIPsFromRequest(request K8sDiscoveryRequest, addressType string) []string {
	// Convert nodes to helper NodeInfo format
	helperNodes := make([]helper.NodeInfo, len(request.Data.Nodes))
	for i, node := range request.Data.Nodes {
		helperNodes[i] = helper.NodeInfo{
			Status:    node.Status,
			Addresses: node.Addresses,
			Roles:     node.Roles,
		}
	}

	// Special handling: when no roles specified, include all nodes regardless of role
	var nodeIPs []string
	for _, node := range helperNodes {
		if node.Status == "Ready" {
			if nodeIP := helper.GetNodeIP(node, addressType); nodeIP != "" {
				nodeIPs = append(nodeIPs, nodeIP)
			}
		}
	}
	return nodeIPs
}

// updateClusterNodeIPs updates nodes in discovery collection when endpoints change
func (ds *DiscoveryService) updateClusterNodeIPs(ctx context.Context, request K8sDiscoveryRequest, project string) error {
	discoveryCollection := ds.dbContext.Client.Collection("discovery")

	filter := bson.M{
		"cluster_name": request.Data.ClusterInfo.ClusterName,
		"project":      project,
	}

	update := bson.M{
		"$set": bson.M{
			"nodes":              request.Data.Nodes,
			"updated_at":         time.Now(),
			"node_count":         request.Data.NodeCount,
			"cluster_version":    request.Data.ClusterInfo.ClusterVersion,
			"discovery_duration": request.Data.DiscoveryDuration,
			"last_request_time":  request.Data.Timestamp,
		},
	}

	_, err := discoveryCollection.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to update cluster nodes: %w", err)
	}

	nodeIPs := ds.extractNodeIPsFromRequest(request, "") // Use default behavior for logging
	ds.logger.Infof("Updated cluster %s nodes in discovery: %v", request.Data.ClusterInfo.ClusterName, nodeIPs)
	return nil
}

// checkClusterUsage checks if a cluster is being used by any endpoints
func (ds *DiscoveryService) checkClusterUsage(ctx context.Context, clusterName, project string) error {
	endpointsCollection := ds.dbContext.Client.Collection("endpoints")

	// Find any endpoints that have this cluster in their elchi_discovery
	filter := bson.M{
		"general.project": project,
		"general.elchi_discovery": bson.M{
			"$elemMatch": bson.M{
				"cluster_name": clusterName,
			},
		},
	}

	// Count documents instead of fetching all for performance
	count, err := endpointsCollection.CountDocuments(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to check cluster usage: %w", err)
	}

	if count > 0 {
		// Get endpoint names for detailed error message
		cursor, err := endpointsCollection.Find(ctx, filter, options.Find().SetProjection(bson.M{"general.name": 1}))
		if err != nil {
			return fmt.Errorf("cannot delete cluster '%s': it is being used by %d endpoint(s)", clusterName, count)
		}
		defer cursor.Close(ctx)

		var usedByEndpoints []string
		for cursor.Next(ctx) {
			var endpoint struct {
				General struct {
					Name string `bson:"name"`
				} `bson:"general"`
			}
			if err := cursor.Decode(&endpoint); err == nil {
				usedByEndpoints = append(usedByEndpoints, endpoint.General.Name)
			}
		}

		endpointList := strings.Join(usedByEndpoints, ", ")
		if len(usedByEndpoints) > 5 {
			endpointList = strings.Join(usedByEndpoints[:5], ", ") + fmt.Sprintf(" and %d more", len(usedByEndpoints)-5)
		}

		return fmt.Errorf("cannot delete cluster '%s': it is being used by %d endpoint(s): %s",
			clusterName, count, endpointList)
	}

	return nil
}

// DeleteCluster deletes a cluster by ID from discovery collection
func (ds *DiscoveryService) DeleteCluster(ctx context.Context, clusterID, project string) error {
	discoveryCollection := ds.dbContext.Client.Collection("discovery")

	// Convert string ID to ObjectID
	objectID, err := primitive.ObjectIDFromHex(clusterID)
	if err != nil {
		return fmt.Errorf("invalid cluster ID format: %w", err)
	}

	// Find the cluster first to get cluster name for usage check
	var cluster ClusterDiscovery
	filter := bson.M{
		"_id":     objectID,
		"project": project,
	}

	err = discoveryCollection.FindOne(ctx, filter).Decode(&cluster)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return fmt.Errorf("cluster not found")
		}
		return fmt.Errorf("failed to find cluster: %w", err)
	}

	// Check if cluster is being used by any endpoints
	if err := ds.checkClusterUsage(ctx, cluster.ClusterName, project); err != nil {
		return err
	}

	// Delete the cluster
	result, err := discoveryCollection.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to delete cluster: %w", err)
	}

	if result.DeletedCount == 0 {
		return fmt.Errorf("cluster not found")
	}

	ds.logger.Infof("Deleted cluster: %s (ID: %s) from project: %s", cluster.ClusterName, clusterID, project)
	return nil
}

// logDiscoveryAudit logs discovery-related endpoint updates for audit trail
func (ds *DiscoveryService) logDiscoveryAudit(_ context.Context, c *gin.Context, endpoint models.DBResource, updateResult EndpointUpdateResult, clusterName string) {
	// Skip audit if service not available
	if ds.auditService == nil {
		ds.logger.Warnf("Audit service not available, skipping audit log")
		return
	}

	// Extract cluster information from elchi_discovery
	var allClusters []string
	for _, discovery := range endpoint.General.ElchiDiscovery {
		allClusters = append(allClusters, discovery.ClusterName)
	}

	// Extract diff information from the first element of UpdatedIPs
	var addedIPs []string
	var removedIPs []string
	var previousIPs []string
	currentIPs := updateResult.UpdatedIPs

	// Check if we have diff info in the first element
	if len(updateResult.UpdatedIPs) > 0 && strings.Contains(updateResult.UpdatedIPs[0], "added:") {
		diffInfo := updateResult.UpdatedIPs[0]
		// Extract the actual current IPs (skip the first diff element)
		if len(updateResult.UpdatedIPs) > 1 {
			currentIPs = updateResult.UpdatedIPs[1:]
		} else {
			currentIPs = []string{}
		}

		// Parse the diff info
		parts := strings.Split(diffInfo, ";")
		for _, part := range parts {
			if addedStr, ok := strings.CutPrefix(part, "added:"); ok {
				if addedStr != "" {
					addedIPs = strings.Split(addedStr, ",")
				}
			} else if removedStr, ok := strings.CutPrefix(part, "removed:"); ok {
				if removedStr != "" {
					removedIPs = strings.Split(removedStr, ",")
				}
			} else if previousStr, ok := strings.CutPrefix(part, "previous:"); ok {
				if previousStr != "" {
					previousIPs = strings.Split(previousStr, ",")
				}
			}
		}
	}

	// Build changes object with only added/removed IPs
	changes := bson.M{
		"before": bson.M{
			"cluster":  clusterName,
			"ip_count": len(previousIPs),
		},
		"after": bson.M{
			"cluster":  clusterName,
			"ip_count": len(currentIPs),
		},
		"diff": bson.M{
			"added_ips":     addedIPs,
			"removed_ips":   removedIPs,
			"added_count":   updateResult.AddedNodes,
			"removed_count": updateResult.RemovedNodes,
		},
	}

	// Combine changes and details for audit
	auditChanges := map[string]any{
		"before": changes["before"],
		"after":  changes["after"],
		"diff":   changes["diff"],
		"details": map[string]any{
			"trigger_cluster":    clusterName,
			"all_clusters":       allClusters,
			"endpoint_name":      endpoint.General.Name,
			"discovery_count":    len(endpoint.General.ElchiDiscovery),
			"snapshot_triggered": true,
			"source":             "elchi-discovery",
			"version":            endpoint.General.Version,
		},
	}

	// Create individual audit entry for each endpoint (cannot use middleware pattern due to multiple endpoints per request)
	// Use static discovery service values
	userID := "system"
	username := "discovery-service"
	userRole := "system"

	// Get request ID from context or generate new one
	requestID := ""
	if reqID, exists := c.Get("request_id"); exists {
		if reqIDStr, ok := reqID.(string); ok {
			requestID = reqIDStr
		}
	}
	if requestID == "" {
		requestID = "disc-" + time.Now().Format("20060102150405") + "-" + endpoint.General.Name
	}

	auditEntry := &models.AuditEntry{
		Timestamp: time.Now(),

		// User Context - static discovery service values
		UserID:   userID,
		Username: username,
		UserRole: userRole,

		// Request Context - from HTTP request
		ClientIP:  c.ClientIP(),
		UserAgent: c.Request.UserAgent(),
		RequestID: requestID,

		// API Context
		APIType: models.AuditAPITypeREST,
		Method:  c.Request.Method,
		Path:    c.Request.URL.Path,

		// Business Context - specific to this endpoint
		Action:       "DISCOVERY_UPDATE_ENDPOINT",
		ResourceType: "endpoints",
		ResourceID:   endpoint.ID.Hex(),
		ResourceName: endpoint.General.Name,
		Project:      endpoint.General.Project,

		// Changes with discovery-specific data
		Changes: auditChanges,

		// Success context
		ResponseStatus: 200,
		Success:        true,
	}

	// Store audit entry directly (bypass middleware to avoid conflicts with multiple endpoints)
	ds.auditService.StoreAsync(auditEntry)
	ds.logger.Debugf("Discovery audit logged for endpoint %s - cluster: %s, IP changes: +%d/-%d",
		endpoint.General.Name, clusterName, updateResult.AddedNodes, updateResult.RemovedNodes)
}
