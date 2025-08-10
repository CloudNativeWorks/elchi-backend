package discovery

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud"
	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
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
}

func NewDiscoveryService(dbContext *db.AppContext, pokeService *bridge.PokeServiceClient) *DiscoveryService {
	return &DiscoveryService{
		dbContext:         dbContext,
		lastSeenCache:     make(map[string]time.Time),
		lastSeenThreshold: 2 * time.Minute, // Only update MongoDB every 2 minutes max
		pokeService:       pokeService,
	}
}

// ProcessK8sDiscovery processes incoming K8s discovery data and updates endpoints
func (ds *DiscoveryService) ProcessK8sDiscovery(
    ctx context.Context,
    request K8sDiscoveryRequest,
    project string,
    isInitial bool,
) (*DiscoveryProcessResult, error) {
    // If initial (from header) is true, register or update cluster in discovery collection
    if isInitial {
		if err := ds.registerCluster(ctx, request, project); err != nil {
			log.Printf("Failed to register cluster: %v", err)
		}
	}
	
	// Always update last_seen timestamp for every request (respects cache timing)
	if err := ds.updateClusterLastSeen(ctx, request.Data.ClusterInfo.ClusterName, project, false); err != nil {
		log.Printf("Failed to update cluster last_seen: %v", err)
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
				log.Printf("Failed to update endpoint %s: %v", endpoint.General.Name, err)
				updateResult.Changed = false
			} else {
				log.Printf("Updated endpoint %s with %d nodes from cluster %s", 
					endpoint.General.Name, updateResult.AddedNodes, request.Data.ClusterInfo.ClusterName)
				
				// Trigger snapshot update for all affected listeners
				ds.triggerSnapshotUpdate(ctx, endpoint, project)
			}
		}
	}
	
	// If endpoints changed, update discovery collection with new node data and force last_seen update
	if endpointsChanged {
		if err := ds.updateClusterNodeIPs(ctx, request, project); err != nil {
			log.Printf("Failed to update cluster node IPs in discovery: %v", err)
		}
		
		// Force immediate last_seen update when endpoints change (bypass cache)
		if err := ds.updateClusterLastSeen(ctx, request.Data.ClusterInfo.ClusterName, project, true); err != nil {
			log.Printf("Failed to force update cluster last_seen after endpoint change: %v", err)
		}
	}

	if result.EndpointsFound == 0 {
		result.Message = fmt.Sprintf("No endpoints found for cluster: %s", request.Data.ClusterInfo.ClusterName)
	}

	return result, nil
}

// updateEndpointFromNodes compares and updates endpoint with K8s nodes
func (ds *DiscoveryService) updateEndpointFromNodes(ctx context.Context, endpoint models.DBResource, nodes []NodeInfo, clusterName string) EndpointUpdateResult {
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

	// Build new IP list from K8s nodes
	// Use ExternalIP for cross-VM communication, fallback to InternalIP if not available
	newIPs := []string{}
	for _, node := range nodes {
		if node.Status == "Ready" {
			// Prefer ExternalIP for cross-VM access
			if externalIP, exists := node.Addresses["ExternalIP"]; exists && externalIP != "" {
				newIPs = append(newIPs, externalIP)
				updateResult.UpdatedIPs = append(updateResult.UpdatedIPs, externalIP)
			} else if internalIP, exists := node.Addresses["InternalIP"]; exists && internalIP != "" {
				// Fallback to InternalIP if ExternalIP not available
				newIPs = append(newIPs, internalIP)
				updateResult.UpdatedIPs = append(updateResult.UpdatedIPs, internalIP)
			}
		}
	}

	// Compare IP lists
	if !ds.compareIPLists(currentIPs, newIPs) {
		// Update endpoint resource with new IPs for this specific cluster
		ds.updateEndpointResourceForCluster(endpoint, newIPs, discoveryConfig, clusterName)
		updateResult.Changed = true
		updateResult.AddedNodes = len(newIPs) - len(currentIPs)
		if updateResult.AddedNodes < 0 {
			updateResult.RemovedNodes = -updateResult.AddedNodes
			updateResult.AddedNodes = 0
		}
	}

	return updateResult
}

// extractCurrentIPsForCluster extracts IP addresses from endpoint resource for a specific cluster
func (ds *DiscoveryService) extractCurrentIPsForCluster(endpoint models.DBResource, clusterName string) []string {
	ips := []string{}
	
	// Parse the resource structure
	resourceData, ok := endpoint.Resource.Resource.(map[string]interface{})
	if !ok {
		return ips
	}

	endpoints, ok := resourceData["endpoints"].([]interface{})
	if !ok {
		return ips
	}

	for _, ep := range endpoints {
		epMap, ok := ep.(map[string]interface{})
		if !ok {
			continue
		}

		// Check if this endpoint belongs to the specific cluster via locality.region
		locality, hasLocality := epMap["locality"].(map[string]interface{})
		if hasLocality {
			if region, ok := locality["region"].(string); ok && region != clusterName {
				continue // Skip this endpoint as it belongs to a different cluster
			}
		} else {
			// If no locality, skip this endpoint - we only process cluster-specific endpoints
			continue
		}

		lbEndpoints, ok := epMap["lb_endpoints"].([]interface{})
		if !ok {
			continue
		}

		for _, lbEp := range lbEndpoints {
			lbEpMap, ok := lbEp.(map[string]interface{})
			if !ok {
				continue
			}

			endpoint, ok := lbEpMap["endpoint"].(map[string]interface{})
			if !ok {
				continue
			}

			address, ok := endpoint["address"].(map[string]interface{})
			if !ok {
				continue
			}

			socketAddress, ok := address["socket_address"].(map[string]interface{})
			if !ok {
				continue
			}

			if ip, ok := socketAddress["address"].(string); ok {
				ips = append(ips, ip)
			}
		}
	}

	return ips
}

// compareIPLists compares two IP lists to check if they're different
func (ds *DiscoveryService) compareIPLists(current, new []string) bool {
	if len(current) != len(new) {
		return false
	}

	// Create map for fast lookup
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
	resourceData := endpoint.Resource.Resource.(map[string]interface{})
	endpoints, ok := resourceData["endpoints"].([]interface{})
	if !ok {
		endpoints = []interface{}{}
	}

	// Create new lb_endpoints for this cluster
	newLbEndpoints := []map[string]interface{}{}
	for _, ip := range newIPs {
		lbEndpoint := map[string]interface{}{
			"endpoint": map[string]interface{}{
				"address": map[string]interface{}{
					"socket_address": map[string]interface{}{
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
	clusterEndpoint := map[string]interface{}{
		"locality": map[string]interface{}{
			"region": clusterName, // Use cluster name as region for identification
		},
		"lb_endpoints": newLbEndpoints,
	}

	// Find and update the existing cluster endpoint, or add new one
	var updatedEndpoints []interface{}
	clusterFound := false

	for _, ep := range endpoints {
		epMap, ok := ep.(map[string]interface{})
		if !ok {
			updatedEndpoints = append(updatedEndpoints, ep)
			continue
		}

		// Check if this endpoint belongs to our cluster
		locality, hasLocality := epMap["locality"].(map[string]interface{})
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
		if err == mongo.ErrNoDocuments {
			return false, nil
		}
		return false, err
	}

	return true, nil
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
	if err != nil && err != mongo.ErrNoDocuments {
		return fmt.Errorf("failed to check existing cluster: %v", err)
	}
	
	if err == mongo.ErrNoDocuments {
		// Create new cluster record
		newCluster := ClusterDiscovery{
			ClusterName:       request.Data.ClusterInfo.ClusterName,
			Project:           project,
			Nodes:            request.Data.Nodes,
			LastSeen:         now,
			CreatedAt:        now,
			UpdatedAt:        now,
			NodeCount:        request.Data.NodeCount,
			ClusterVersion:   request.Data.ClusterInfo.ClusterVersion,
			DiscoveryDuration: request.Data.DiscoveryDuration,
			LastRequestTime:  request.Data.Timestamp,
		}
		
		_, err = discoveryCollection.InsertOne(ctx, newCluster)
		if err != nil {
			return fmt.Errorf("failed to insert cluster: %v", err)
		}
		
		log.Printf("Registered new cluster: %s for project: %s with %d nodes", request.Data.ClusterInfo.ClusterName, project, len(request.Data.Nodes))
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
			return fmt.Errorf("failed to update cluster: %v", err)
		}
		
		log.Printf("Updated cluster: %s for project: %s with %d nodes", request.Data.ClusterInfo.ClusterName, project, len(request.Data.Nodes))
	}
	
	return nil
}


// GetClusters returns all registered clusters for a project
func (ds *DiscoveryService) GetClusters(ctx context.Context, project string) ([]ClusterDiscovery, error) {
	discoveryCollection := ds.dbContext.Client.Collection("discovery")
	
	filter := bson.M{"project": project}
	cursor, err := discoveryCollection.Find(ctx, filter, options.Find().SetSort(bson.M{"updated_at": -1}))
	if err != nil {
		return nil, fmt.Errorf("failed to query clusters: %v", err)
	}
	defer cursor.Close(ctx)
	
	var clusters []ClusterDiscovery
	if err = cursor.All(ctx, &clusters); err != nil {
		return nil, fmt.Errorf("failed to decode clusters: %v", err)
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
		return fmt.Errorf("failed to update last_seen: %v", err)
	}
	
	// If update was successful, update cache
	if result.MatchedCount > 0 {
		ds.lastSeenCacheMux.Lock()
		ds.lastSeenCache[cacheKey] = now
		ds.lastSeenCacheMux.Unlock()
	} else {
		// If no document was matched/updated, the cluster doesn't exist yet
		// This is normal for clusters that haven't sent initial=true yet
		log.Printf("Cluster %s not found for last_seen update (no initial registration yet)", clusterName)
	}
	
	return nil
}

// triggerSnapshotUpdate triggers snapshot update for listeners using this endpoint
func (ds *DiscoveryService) triggerSnapshotUpdate(ctx context.Context, endpoint models.DBResource, project string) {
	if ds.pokeService == nil {
		log.Printf("Warning: PokeService not available, skipping snapshot update")
		return
	}

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
		log.Printf("Triggered snapshot update for endpoint %s, affected listeners: %v", 
			endpoint.General.Name, processed.Listeners)
	}
}

// extractNodeIPsFromRequest extracts all node IPs from the request
// Prefers ExternalIP for cross-VM communication, fallback to InternalIP
func (ds *DiscoveryService) extractNodeIPsFromRequest(request K8sDiscoveryRequest) []string {
	var nodeIPs []string
	for _, node := range request.Data.Nodes {
		if node.Status == "Ready" {
			// Prefer ExternalIP for cross-VM access
			if externalIP, exists := node.Addresses["ExternalIP"]; exists && externalIP != "" {
				nodeIPs = append(nodeIPs, externalIP)
			} else if internalIP, exists := node.Addresses["InternalIP"]; exists && internalIP != "" {
				// Fallback to InternalIP if ExternalIP not available
				nodeIPs = append(nodeIPs, internalIP)
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
		return fmt.Errorf("failed to update cluster nodes: %v", err)
	}
	
	nodeIPs := ds.extractNodeIPsFromRequest(request)
	log.Printf("Updated cluster %s nodes in discovery: %v", request.Data.ClusterInfo.ClusterName, nodeIPs)
	return nil
}