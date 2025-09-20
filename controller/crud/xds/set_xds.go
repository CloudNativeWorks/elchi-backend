package xds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud"
	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
	"github.com/CloudNativeWorks/elchi-backend/pkg/validation"
)

// SetResourceResult contains the result of resource creation
type SetResourceResult struct {
	BootstrapID string `json:"bootstrap_id"`
	ResourceID  string `json:"resource_id"`
	ServiceID   string `json:"service_id"`
}

// ResourceRollback handles rollback operations for failed resource creation
type ResourceRollback struct {
	xds        *AppHandler
	ctx        context.Context
	resource   models.ResourceClass
	collection *mongo.Collection
	insertedID primitive.ObjectID
	logger     *logger.Logger
}

// populateEndpointFromDiscovery populates endpoint resource with node IPs from discovery
// When elchi_discovery is present, it REPLACES all endpoints with discovery data
func (xds *AppHandler) populateEndpointFromDiscovery(ctx context.Context, resource models.ResourceClass) error {
	general := resource.GetGeneral()

	// Only process endpoints with elchi_discovery
	if general.GType != models.Endpoint || len(general.ElchiDiscovery) == 0 {
		return nil
	}

	// Get current resource data
	resourceData, ok := resource.GetResource().(map[string]any)
	if !ok {
		resourceData = make(map[string]any)
	}

	// Clear existing endpoints - when elchi_discovery is present, ONLY discovery nodes should exist
	endpoints := []any{}

	// Process each elchi_discovery configuration
	for _, discoveryConfig := range general.ElchiDiscovery {
		// Query discovery collection for this cluster
		discoveryCollection := xds.Context.Client.Collection("discovery")

		var clusterData struct {
			Nodes []struct {
				Status    string            `bson:"status"`
				Addresses map[string]string `bson:"addresses"`
				Roles     []string          `bson:"roles"`
			} `bson:"nodes"`
		}

		err := discoveryCollection.FindOne(ctx, bson.M{
			"cluster_name": discoveryConfig.ClusterName,
			"project":      general.Project,
		}).Decode(&clusterData)

		if err != nil {
			if err == mongo.ErrNoDocuments {
				// No cluster found, return error to prevent creating endpoint with invalid cluster
				return fmt.Errorf("discovery cluster not found: %s. Please ensure the cluster is registered before creating endpoints", discoveryConfig.ClusterName)
			}
			return fmt.Errorf("failed to query discovery data: %v", err)
		}

		// Convert nodes to helper NodeInfo format
		helperNodes := make([]helper.NodeInfo, len(clusterData.Nodes))
		for i, node := range clusterData.Nodes {
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

		// Use helper utility for complete filtering and analysis
		filterResult := helper.FilterNodesWithResult(helperNodes, helperConfig)

		xds.Logger.Infof("Processing endpoint for cluster %s: %d total nodes, %d matching nodes (roles: %v)",
			discoveryConfig.ClusterName, filterResult.TotalNodes, filterResult.MatchingNodes, filterResult.RequiredRoles)

		// Build lb_endpoints from filtered IPs
		var lbEndpoints []map[string]any
		for _, nodeIP := range filterResult.ExtractedIPs {
			lbEndpoint := map[string]any{
				"endpoint": map[string]any{
					"address": map[string]any{
						"socket_address": map[string]any{
							"protocol":   discoveryConfig.Protocol,
							"address":    nodeIP,
							"port_value": discoveryConfig.Port,
						},
					},
				},
			}
			lbEndpoints = append(lbEndpoints, lbEndpoint)
		}

		// Create endpoint for this cluster (always append, we cleared endpoints array earlier)
		clusterEndpoint := map[string]any{
			"locality": map[string]any{
				"region": discoveryConfig.ClusterName,
			},
			"lb_endpoints": lbEndpoints,
		}

		// Add this cluster's endpoint
		endpoints = append(endpoints, clusterEndpoint)

		xds.Logger.Infof("Populated endpoint %s with %d nodes from cluster %s",
			general.Name, len(lbEndpoints), discoveryConfig.ClusterName)
	}

	// Update resource with populated endpoints
	resourceData["endpoints"] = endpoints
	resource.SetResource(resourceData)

	return nil
}

// parseDuplicateKeyError extracts a more user-friendly error message from MongoDB duplicate key errors
func parseDuplicateKeyError(err error, resourceName string) error {
	if err == nil {
		return nil
	}

	errStr := err.Error()
	if strings.Contains(errStr, "E11000 duplicate key error") {
		// Extract collection name from error message
		collection := "resource"
		if strings.Contains(errStr, "collection: elchi.") {
			parts := strings.Split(errStr, "collection: elchi.")
			if len(parts) > 1 {
				collectionPart := strings.Split(parts[1], " ")[0]
				collection = collectionPart
			}
		}

		return fmt.Errorf("A %s with the name \"%s\" already exists. Please choose a different name.", collection, resourceName)
	}

	return err
}



// rollbackResource rolls back the main resource only
func (r *ResourceRollback) rollbackResource() error {
	r.logger.Debugf("Rolling back resource %s", r.resource.GetGeneral().Name)
	result, err := r.collection.DeleteOne(r.ctx, bson.M{"_id": r.insertedID})
	if err != nil {
		r.logger.Debugf("Resource rollback failed: %v", err)
		return err
	}
	if result.DeletedCount > 0 {
		r.logger.Debugf("Resource rolled back successfully")
	} else {
		r.logger.Debugf("No resource found to rollback")
	}
	return nil
}

// rollbackWithBootstrap rolls back both bootstrap and main resource
func (r *ResourceRollback) rollbackWithBootstrap(bootstrapID string) error {
	general := r.resource.GetGeneral()
	r.logger.Debugf("Rolling back bootstrap and resource for %s", general.Name)
	
	// Delete bootstrap first
	if bootstrapID != "" {
		bootstrapCollection := r.xds.Context.Client.Collection("bootstrap")
		if bootstrapOID, err := primitive.ObjectIDFromHex(bootstrapID); err == nil {
			if result, err := bootstrapCollection.DeleteOne(r.ctx, bson.M{"_id": bootstrapOID}); err != nil {
				r.logger.Debugf("Bootstrap rollback failed: %v", err)
			} else if result.DeletedCount > 0 {
				r.logger.Debugf("Bootstrap rolled back successfully")
			}
		}
	}
	
	// Then delete main resource
	return r.rollbackResource()
}

// validateResourcePrerequisites performs all validation checks before resource creation
func (xds *AppHandler) validateResourcePrerequisites(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) error {
	// Validate XDS resource name format
	if err := validation.ValidateXDSResource(resource); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// Validate project access before creating resource
	if err := authorization.ValidateResourceProject(ctx, xds.Context.Client, requestDetails.User, resource); err != nil {
		return fmt.Errorf("project access denied: %w", err)
	}

	// Note: GType validation is handled in main SetResource function to preserve validationResult return

	return nil
}

// prepareResourceForInsertion prepares the resource and handles discovery population
func (xds *AppHandler) prepareResourceForInsertion(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) error {
	err := resources.PrepareResource(resource, requestDetails, xds.Logger, xds.ResourceService)
	if err != nil {
		return err
	}

	// Populate endpoint from discovery if elchi_discovery is present
	if err := xds.populateEndpointFromDiscovery(ctx, resource); err != nil {
		return fmt.Errorf("failed to populate endpoint from discovery: %v", err)
	}

	return nil
}

// insertResourceToMongoDB inserts the resource to MongoDB and returns the result
func (xds *AppHandler) insertResourceToMongoDB(ctx context.Context, resource models.ResourceClass) (*mongo.InsertOneResult, error) {
	general := resource.GetGeneral()
	collection := xds.Context.Client.Collection(general.Collection)
	
	xds.Logger.Debugf("Inserting %s to collection %s", general.Name, general.Collection)
	
	result, err := collection.InsertOne(ctx, resource)
	if err != nil {
		if er := new(mongo.WriteException); errors.As(err, &er) && er.WriteErrors[0].Code == 11000 {
			return nil, parseDuplicateKeyError(err, general.Name)
		}
		return nil, err
	}
	
	xds.Logger.Debugf("Resource %s inserted successfully (ID: %v)", general.Name, result.InsertedID)
	return result, nil
}

// processListenerSpecificResources handles bootstrap and service creation for listeners
func (xds *AppHandler) processListenerSpecificResources(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails, rollback *ResourceRollback) (string, string, uint32, error) {
	general := resource.GetGeneral()
	if general.GType != models.Listener {
		return "", "", 0, nil // Not a listener, skip
	}
	
	xds.Logger.Debugf("Processing listener-specific resources for %s", general.Name)
	
	// Create bootstrap
	bootstrapID, adminPort, err := xds.createBootstrap(ctx, general, requestDetails)
	if err != nil {
		if rollbackErr := rollback.rollbackResource(); rollbackErr != nil {
			xds.Logger.Debugf("Rollback failed during bootstrap creation: %v", rollbackErr)
		}
		return "", "", 0, fmt.Errorf("Bootstrap creation failed for %s: %w", general.Name, err)
	}
	
	xds.Logger.Debugf("Bootstrap created for %s (ID: %s, AdminPort: %d)", general.Name, bootstrapID, adminPort)
	
	// Create service if managed
	var serviceID string
	if general.Managed {
		xds.Logger.Debugf("Creating service for managed listener %s", general.Name)
		
		serviceID, err = xds.createService(ctx, general.Name, general.Project, general.Version, adminPort, general.Permissions)
		if err != nil {
			if rollbackErr := rollback.rollbackWithBootstrap(bootstrapID); rollbackErr != nil {
				xds.Logger.Debugf("Rollback failed during service creation: %v", rollbackErr)
			}
			return "", "", 0, fmt.Errorf("Service creation failed for %s: %w", general.Name, err)
		}
		
		xds.Logger.Debugf("Service created for %s (ID: %s)", general.Name, serviceID)
	} else {
		xds.Logger.Debugf("Skipping service creation for unmanaged listener %s", general.Name)
	}
	
	return bootstrapID, serviceID, adminPort, nil
}

// SetResource creates a new XDS resource with proper validation and rollback handling
func (xds *AppHandler) SetResource(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	// Step 1: Validate prerequisites
	if err := xds.validateResourcePrerequisites(ctx, resource, requestDetails); err != nil {
		return nil, err
	}
	
	// Step 1.5: Handle GType validation separately (preserves validationResult)
	if validationResult, err := validation.GlobalValidatorRegistry.ValidateByGType(resource, requestDetails, xds.Logger); err != nil {
		return validationResult, err
	}
	
	// Step 2: Prepare resource for insertion
	if err := xds.prepareResourceForInsertion(ctx, resource, requestDetails); err != nil {
		return nil, err
	}
	
	// Step 3: Insert resource to MongoDB
	inserResult, err := xds.insertResourceToMongoDB(ctx, resource)
	if err != nil {
		return nil, err
	}
	
	// Step 4: Setup rollback mechanism
	general := resource.GetGeneral()
	insertedID := inserResult.InsertedID.(primitive.ObjectID)
	rollback := &ResourceRollback{
		xds:        xds,
		ctx:        ctx,
		resource:   resource,
		collection: xds.Context.Client.Collection(general.Collection),
		insertedID: insertedID,
		logger:     xds.Logger,
	}

	// Step 5: Process listener-specific resources (bootstrap & service)
	bootstrapID, serviceID, _, err := xds.processListenerSpecificResources(ctx, resource, requestDetails, rollback)
	if err != nil {
		return nil, err
	}

	// Step 6: Finalize and return result
	resource.SetID(insertedID)
	resourceID := insertedID.Hex()
	
	result := SetResourceResult{
		BootstrapID: bootstrapID,
		ResourceID:  resourceID,
		ServiceID:   serviceID,
	}
	
	xds.Logger.Debugf("Resource creation completed successfully for %s", general.Name)
	return map[string]any{"message": "Success", "data": result}, nil
}

func (xds *AppHandler) createService(ctx context.Context, serviceName string, project string, version string, adminPort uint32, listenerPermissions models.Permissions) (string, error) {
	xds.Logger.Debugf("🛠️ createService called - serviceName: %s, project: %s, version: %s, adminPort: %d", serviceName, project, version, adminPort)
	
	var service models.Service
	collection := xds.Context.Client.Collection("services")
	service.Name = serviceName
	service.Project = project
	service.Version = version
	service.AdminPort = adminPort
	service.Clients = []models.ServiceClients{}
	
	xds.Logger.Debugf("📄 Service document prepared: %+v", service)

	// Service permissions inherit from the listener that created it
	service.Permissions = listenerPermissions
	xds.Logger.Debugf("🔐 Service permissions set: %+v", listenerPermissions)

	xds.Logger.Debugf("💾 Attempting to insert service into MongoDB...")  
	inserResult, err := collection.InsertOne(ctx, service)
	if err != nil {
		xds.Logger.Debugf("❌ Service MongoDB insert failed: %v", err)
		if er := new(mongo.WriteException); errors.As(err, &er) && er.WriteErrors[0].Code == 11000 {
			xds.Logger.Debugf("⚠️ Service duplicate key error detected for: %s", serviceName)
			return "", parseDuplicateKeyError(err, serviceName)
		}
		return "", err
	}
	xds.Logger.Debugf("✅ Service MongoDB insert successful, ID: %v", inserResult.InsertedID)

	if oid, ok := inserResult.InsertedID.(primitive.ObjectID); ok {
		hexID := oid.Hex()
		xds.Logger.Debugf("🎯 Service created successfully with hex ID: %s", hexID)
		return hexID, nil
	}

	xds.Logger.Debugf("❌ Service insert ID type assertion failed: %T", inserResult.InsertedID)
	return "", errors.New("inserted ID is not a valid ObjectID")
}

func (xds *AppHandler) createBootstrap(ctx context.Context, listenerGeneral models.General, requestDetails models.RequestDetails) (string, uint32, error) {
	collection := xds.Context.Client.Collection("bootstrap")
	bootstrap, err := crud.GetBootstrap(ctx, xds.Context.Client, listenerGeneral, xds.Context.Config)
	if err != nil {
		return "", 0, err
	}
	resource, err := DecodeFromMap(bootstrap)
	if err != nil {
		return "", 0, err
	}

	adminPort, err := resources.GetAdminPortFromBootstrap(resource.GetResource())
	if err != nil {
		return "", 0, err
	}

	err = resources.PrepareResource(resource, requestDetails, xds.Logger, xds.ResourceService)
	if err != nil {
		return "", 0, err
	}

	inserResult, err := collection.InsertOne(ctx, resource)
	if err != nil {
		if er := new(mongo.WriteException); errors.As(err, &er) && er.WriteErrors[0].Code == 11000 {
			return "", 0, parseDuplicateKeyError(err, listenerGeneral.Name)
		}
		return "", 0, err
	}

	if oid, ok := inserResult.InsertedID.(primitive.ObjectID); ok {
		return oid.Hex(), adminPort, nil
	}

	return "", 0, errors.New("inserted ID is not a valid ObjectID")
}

func DecodeFromMap(data map[string]any) (models.ResourceClass, error) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	var resource models.DBResource
	if err := json.Unmarshal(jsonData, &resource); err != nil {
		return nil, err
	}

	return &resource, nil
}
