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
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
	"github.com/CloudNativeWorks/elchi-backend/pkg/validation"
)

// populateEndpointFromDiscovery populates endpoint resource with node IPs from discovery
// When elchi_discovery is present, it REPLACES all endpoints with discovery data
func (xds *AppHandler) populateEndpointFromDiscovery(ctx context.Context, resource models.ResourceClass) error {
	general := resource.GetGeneral()
	
	// Only process endpoints with elchi_discovery
	if general.GType != models.Endpoint || len(general.ElchiDiscovery) == 0 {
		return nil
	}
	
	// Get current resource data
	resourceData, ok := resource.GetResource().(map[string]interface{})
	if !ok {
		resourceData = make(map[string]interface{})
	}
	
	// Clear existing endpoints - when elchi_discovery is present, ONLY discovery nodes should exist
	endpoints := []interface{}{}
	
	// Process each elchi_discovery configuration
	for _, discoveryConfig := range general.ElchiDiscovery {
		// Query discovery collection for this cluster
		discoveryCollection := xds.Context.Client.Collection("discovery")
		
		var clusterData struct {
			Nodes []struct {
				Status    string            `bson:"status"`
				Addresses map[string]string `bson:"addresses"`
			} `bson:"nodes"`
		}
		
		err := discoveryCollection.FindOne(ctx, bson.M{
			"cluster_name": discoveryConfig.ClusterName,
			"project":      general.Project,
		}).Decode(&clusterData)
		
		if err != nil {
			if err == mongo.ErrNoDocuments {
				// No cluster found, skip
				xds.Logger.Warnf("No discovery data found for cluster: %s", discoveryConfig.ClusterName)
				continue
			}
			return fmt.Errorf("failed to query discovery data: %v", err)
		}
		
		// Build lb_endpoints from nodes
		var lbEndpoints []map[string]interface{}
		for _, node := range clusterData.Nodes {
			if node.Status != "Ready" {
				continue
			}
			
			// Get IP (prefer ExternalIP, fallback to InternalIP)
			var nodeIP string
			if externalIP, exists := node.Addresses["ExternalIP"]; exists && externalIP != "" {
				nodeIP = externalIP
			} else if internalIP, exists := node.Addresses["InternalIP"]; exists && internalIP != "" {
				nodeIP = internalIP
			} else {
				continue
			}
			
			lbEndpoint := map[string]interface{}{
				"endpoint": map[string]interface{}{
					"address": map[string]interface{}{
						"socket_address": map[string]interface{}{
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
		clusterEndpoint := map[string]interface{}{
			"locality": map[string]interface{}{
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

func (xds *AppHandler) SetResource(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	// Validate XDS resource name format
	if err := validation.ValidateXDSResource(resource); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	// Validate project access before creating resource
	if err := authorization.ValidateResourceProject(ctx, xds.Context.Client, requestDetails.User, resource); err != nil {
		return nil, fmt.Errorf("project access denied: %w", err)
	}

	// Auto-validate resource based on GType if validation is requested
	if validationResult, err := validation.GlobalValidatorRegistry.ValidateByGType(resource, requestDetails, xds.Logger); err != nil {
		return validationResult, err
	}

	general := resource.GetGeneral()
	err := resources.PrepareResource(resource, requestDetails, xds.Logger, xds.ResourceService)
	if err != nil {
		return nil, err
	}
	
	// Populate endpoint from discovery if elchi_discovery is present
	if err := xds.populateEndpointFromDiscovery(ctx, resource); err != nil {
		xds.Logger.Errorf("Failed to populate endpoint from discovery: %v", err)
		// Continue even if population fails - user might want to create endpoint without initial nodes
	}
	bootstrapID := ""
	resourceID := ""
	serviceID := ""
	adminPort := uint32(0)
	collection := xds.Context.Client.Collection(general.Collection)
	inserResult, err := collection.InsertOne(ctx, resource)
	if err != nil {
		if er := new(mongo.WriteException); errors.As(err, &er) && er.WriteErrors[0].Code == 11000 {
			return nil, parseDuplicateKeyError(err, general.Name)
		}
		return nil, err
	}

	if general.GType == models.Listener {
		bootstrapID, adminPort, err = xds.createBootstrap(ctx, general, requestDetails)
		if err != nil {
			return nil, err
		}

		if general.Managed {
			serviceID, err = xds.createService(ctx, general.Name, general.Project, general.Version, adminPort, general.Permissions)
			if err != nil {
				return nil, err
			}
		}
	}

	if oid, ok := inserResult.InsertedID.(primitive.ObjectID); ok {
		resource.SetID(oid)
		resourceID = oid.Hex()
	}

	data := map[string]any{"bootstrap_id": bootstrapID, "resource_id": resourceID, "service_id": serviceID}

	return map[string]any{"message": "Success", "data": data}, nil
}

func (xds *AppHandler) createService(ctx context.Context, serviceName string, project string, version string, adminPort uint32, listenerPermissions models.Permissions) (string, error) {
	var service models.Service
	collection := xds.Context.Client.Collection("services")
	service.Name = serviceName
	service.Project = project
	service.Version = version
	service.AdminPort = adminPort
	service.Clients = []models.ServiceClients{}
	
	// Service permissions inherit from the listener that created it
	service.Permissions = listenerPermissions

	inserResult, err := collection.InsertOne(ctx, service)
	if err != nil {
		if er := new(mongo.WriteException); errors.As(err, &er) && er.WriteErrors[0].Code == 11000 {
			return "", parseDuplicateKeyError(err, serviceName)
		}
		return "", err
	}

	if oid, ok := inserResult.InsertedID.(primitive.ObjectID); ok {
		return oid.Hex(), nil
	}

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
