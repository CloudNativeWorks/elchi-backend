package resource

import (
	"context"
	"errors"
	"fmt"

	cluster "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/cluster/v3"
	core "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/core/v3"
	endpoint "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/endpoint/v3"
	listener "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/listener/v3"
	route "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/route/v3"
	hcm "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	"github.com/CloudNativeWorks/versioned-go-control-plane/pkg/cache/types"
	"github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/errstr"
	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
)

// DecodeListener decodes the listener configuration from the provided input.
// It parses the input data and converts it into a structured format that can be used by the application.
// Parameters:
// - input: the raw input data to be decoded
// Returns:
// - ListenerConfig: a structured representation of the listener configuration
// - error: an error if any occurred during the decoding process
func (ar *AllResources) DecodeListener(ctx context.Context, rawListenerResource *models.DBResource, context *db.AppContext, logger *logrus.Logger, downstreamAddress string) {
	ar.mutex.Lock()
	ar.UniqueResources = make(map[string]struct{})
	ar.mutex.Unlock()

	if err := ar.initializeListener(ctx, rawListenerResource, context, logger, downstreamAddress); err != nil {
		logger.Errorf("Error initializing listener: %v", err)
	}

	ar.processConfigDiscoveries(ctx, rawListenerResource.General.ConfigDiscovery, context, logger)
}

// SetSocketIPAddress sets the IP address of the socket address.
// It takes an address and a new IP address as input and returns an error if the address is not a socket address or the socket address is nil.
// Parameters:
// - addr: the address to set the IP address of
// - newIP: the new IP address to set
// Returns:
// - error: an error if any occurred during the setting of the IP address
func SetSocketIPAddress(addr *core.Address, newIP string) error {
	socketAddr, ok := addr.Address.(*core.Address_SocketAddress)
	if !ok || socketAddr.SocketAddress == nil {
		return errors.New("address is not Address_SocketAddress or SocketAddress is nil")
	}
	socketAddr.SocketAddress.Address = newIP
	return nil
}

// initializeListener initializes the listener with the provided configuration.
// It sets up the necessary parameters and prepares the listener for operation.
// Parameters:
// - config: the configuration settings for the listener
// Returns:
// - Listener: an initialized listener instance
// - error: an error if any occurred during the initialization process
func (ar *AllResources) initializeListener(ctx context.Context, rawListenerResource *models.DBResource, context *db.AppContext, logger *logrus.Logger, downstreamAddress string) error {
	rawListeners, ok := rawListenerResource.Resource.Resource.(primitive.A)
	if !ok {
		return errstr.ErrUnexpectedResource
	}

	newVersion, err := resources.IncrementResourceVersion(ctx, context, rawListenerResource.General.Name, rawListenerResource.General.Project, ar.ResourceVersion)
	if err != nil {
		return err
	}
	ar.mutex.Lock()
	ar.SetVersion(newVersion)
	ar.SetProject(rawListenerResource.General.Project)
	ar.mutex.Unlock()

	listeners := make([]types.Resource, 0, len(rawListeners))
	for _, lstnr := range rawListeners {
		listenerWithTransportSocket, _ := ar.GetTypedConfigs(ctx, rawListenerResource.GetGtype().TypedConfigPaths(), lstnr, context)

		singleListener := &listener.Listener{}
		if err := helper.MarshalUnmarshalWithType(listenerWithTransportSocket, singleListener); err != nil {
			logger.Errorf("Listener Unmarshal error: %s", err)
			continue
		}

		if downstreamAddress != "" {
			if err := SetSocketIPAddress(singleListener.Address, downstreamAddress); err != nil {
				logger.Errorf("Error setting socket address IP: %s", err)
				continue
			}
		}

		listeners = append(listeners, singleListener)
	}

	ar.mutex.Lock()
	ar.SetListener(listeners)
	ar.mutex.Unlock()

	return nil
}

// processConfigDiscoveries processes the discovered configurations.
// It iterates through the discovered configurations and processes each one to collect resources.
// Parameters:
// - ctx: context for controlling the request lifetime
// - configDiscoveries: a slice of discovered configurations to be processed
// - context: application context containing database connections and other settings
// - logger: logger for logging errors and information
func (ar *AllResources) processConfigDiscoveries(ctx context.Context, configDiscoveries []*models.ConfigDiscovery, context *db.AppContext, logger *logrus.Logger) {
	for _, configDiscovery := range configDiscoveries {
		ar.processExtension(ctx, configDiscovery, configDiscovery.ParentName, context, logger)
	}
}

// processExtension processes a single configuration discovery.
// It reads the configuration details and converts them into a structured format for further processing.
// Parameters:
// - ctx: context for controlling the request lifetime
// - configDiscovery: the discovered configuration to be processed
// - parentName: the name of the parent configuration
// - context: application context containing database connections and other settings
// - logger: logger for logging errors and information
// Returns:
// - error: an error if any occurred during the processing of the configuration
func (ar *AllResources) processExtension(ctx context.Context, extension *models.ConfigDiscovery, parentName string, context *db.AppContext, logger *logrus.Logger) {
	uniqKey := fmt.Sprintf("%s__%s__%s", extension.Name, parentName, extension.GType.String())
	if ar.checkAndMarkDuplicate(uniqKey) {
		return
	}

	extConfigs, additionalExtResources, err := ar.CollectAllResourcesWithParent(ctx, extension.GType, extension.Name, parentName, context, logger)
	if err != nil {
		logger.Errorf("Error collecting resources: %v", err)
		return
	}

	for _, extConfig := range extConfigs {
		if extension.GType == models.VirtualHost {
			uniqKey := fmt.Sprintf("%s__%s", extension.Name, extension.GType.String())
			ar.AddToCollection(extConfig, extension.GType, uniqKey, &parentName, extension.Name)
		} else {
			if extension.GType == models.HTTPConnectionManager {
				hcmConfig, ok := extConfig.(*hcm.HttpConnectionManager)
				if !ok {
					logger.Errorf("Error casting extConfig to HttpConnectionManager")
					continue
				}

				if routeConfig := hcmConfig.GetRouteConfig(); routeConfig != nil {
					if vhds := routeConfig.GetVhds(); vhds != nil {
						ar.UpdateVhdsMetadataNodeID(vhds)
					}
				}
			}

			typedConfigAsAny, err := anypb.New(extConfig)
			if err != nil {
				logger.Errorf("Error converting extTypedConfig to *anypb.Any for %s: %v", parentName, err)
				continue
			}

			ar.addTypedExtensionConfig(typedConfigAsAny, parentName)
		}
	}

	if additionalExtResources != nil {
		ar.processConfigDiscoveries(ctx, additionalExtResources, context, logger)
	}
}

// addTypedExtensionConfig adds a typed extension configuration to the given resource.
// It creates and attaches the extension configuration based on the provided parameters.
// Parameters:
// - resource: the resource to which the extension configuration will be added
// - extensionName: the name of the extension to be added
// - config: the configuration details for the extension
// Returns:
// - error: an error if any occurred during the addition of the extension configuration
func (ar *AllResources) addTypedExtensionConfig(typedConfig *anypb.Any, parentName string) {
	typedExtensionConfig := &core.TypedExtensionConfig{
		Name:        parentName,
		TypedConfig: typedConfig,
	}

	ar.mutex.Lock()
	ar.Extensions = append(ar.Extensions, typedExtensionConfig)
	ar.mutex.Unlock()
}

// checkAndMarkDuplicate checks for duplicate entries in the provided list and marks them if found.
// It iterates through the list and identifies any duplicate entries based on the specified criteria.
// Parameters:
// - list: the list of entries to be checked for duplicates
// - markFunc: a function to mark the duplicate entries
// Returns:
// - error: an error if any occurred during the duplicate checking process
func (ar *AllResources) checkAndMarkDuplicate(name string) bool {
	if _, exists := ar.UniqueResources[name]; exists {
		return true
	}

	ar.UniqueResources[name] = struct{}{}
	return false
}

// CollectAllResourcesWithParent collects all resources associated with a given parent.
// It retrieves the resources from the database and organizes them based on the parent-child relationship.
// Parameters:
// - ctx: context for controlling the request lifetime
// - parentID: the ID of the parent resource
// - db: database connection to fetch the resources from
// Returns:
// - []Resource: a slice containing all resources associated with the parent
// - error: an error if any occurred during the resource collection process
func (ar *AllResources) CollectAllResourcesWithParent(ctx context.Context, gtype models.GTypes, resourceName, parentName string, context *db.AppContext, logger *logrus.Logger) ([]proto.Message, []*models.ConfigDiscovery, error) {
	resource, err := resources.GetResourceNGeneral(ctx, context, gtype.CollectionString(), resourceName, ar.Project, ar.ResourceVersion)
	if err != nil {
		logger.Errorf("Error getting resource %s: %v", resourceName, err)
		return nil, nil, err
	}

	resourceData := resource.GetResource()

	var protoMessages []proto.Message
	var finalConfigDiscoveries []*models.ConfigDiscovery

	switch res := resourceData.(type) {
	case primitive.A:
		for _, item := range res {
			jsonStringStr, err := helper.MarshalJSON(item, context.Logger.Logger)
			if err != nil {
				logger.Errorf("Error marshaling array item: %v", err)
				return nil, nil, err
			}

			typedProtoMsg := gtype.ProtoMessage()
			if err := ar.processTypedConfigsAndUpstream(ctx, typedProtoMsg, &jsonStringStr, gtype, parentName, context, logger); err != nil {
				logger.Errorf("Error processing typed configs and upstream resources: %v", err)
				return nil, nil, err
			}

			protoMessages = append(protoMessages, typedProtoMsg)
			finalConfigDiscoveries = resource.General.ConfigDiscovery
			ar.processConfigDiscoveries(ctx, resource.General.ConfigDiscovery, context, logger)
		}
	default:
		jsonStringStr, err := helper.MarshalJSON(resourceData, context.Logger.Logger)
		if err != nil {
			return nil, nil, err
		}

		typedProtoMsg := gtype.ProtoMessage()
		if err := ar.processTypedConfigsAndUpstream(ctx, typedProtoMsg, &jsonStringStr, gtype, parentName, context, logger); err != nil {
			logger.Errorf("Error processing typed configs and upstream resources: %v", err)
			return nil, nil, err
		}

		protoMessages = append(protoMessages, typedProtoMsg)
		finalConfigDiscoveries = resource.General.ConfigDiscovery
		ar.processConfigDiscoveries(ctx, resource.General.ConfigDiscovery, context, logger)
	}

	return protoMessages, finalConfigDiscoveries, nil
}

// processTypedConfigsAndUpstream processes typed configurations and upstream settings.
// It reads the typed configurations and upstream settings, then applies necessary transformations or actions.
// Parameters:
// - ctx: context for controlling the request lifetime
// - typedConfigs: a slice of typed configurations to be processed
// - upstreamSettings: settings related to upstream configurations
// Returns:
// - error: an error if any occurred during the processing of the configurations and settings
func (ar *AllResources) processTypedConfigsAndUpstream(ctx context.Context, protoMsg proto.Message, jsonStringStr *string, gtype models.GTypes, parentName string, context *db.AppContext, logger *logrus.Logger) error {
	typedConfigPaths := gtype.TypedConfigPaths()

	ar.processTypedConfigPaths(ctx, typedConfigPaths, jsonStringStr, context, logger)
	ar.processUpstreamPaths(ctx, gtype.UpstreamPaths(), jsonStringStr, parentName, context, logger)

	if err := helper.Unmarshaler.Unmarshal([]byte(*jsonStringStr), protoMsg); err != nil {
		logger.Errorf("Error unmarshalling to proto message after processing nested configs: %v", err)
		return err
	}

	return nil
}

// processTypedConfigPaths processes the paths for typed configurations.
// It iterates through the provided paths and processes each one to collect and apply the configurations.
// Parameters:
// - ctx: context for controlling the request lifetime
// - paths: a slice of paths to the typed configurations to be processed
// Returns:
// - error: an error if any occurred during the processing of the configuration paths
func (ar *AllResources) processTypedConfigPaths(ctx context.Context, configPaths []models.TypedConfigPath, jsonStringStr *string, context *db.AppContext, logger *logrus.Logger) {
	for _, path := range configPaths {
		if err := ar.processTypedConfigPath(ctx, path, jsonStringStr, context); err != nil {
			logger.Warnf("Error processing typed config path: %v", err)
		}
	}
}

// processUpstreamPaths processes the paths for upstream configurations.
// It iterates through the provided paths and processes each one to collect and apply the upstream configurations.
// Parameters:
// - ctx: context for controlling the request lifetime
// - paths: a slice of paths to the upstream configurations to be processed
// Returns:
// - error: an error if any occurred during the processing of the upstream paths
func (ar *AllResources) processUpstreamPaths(ctx context.Context, upstreamPaths map[string]models.GTypes, jsonStringStr *string, parentName string, context *db.AppContext, logger *logrus.Logger) {
	for jsonPath, upstreamType := range upstreamPaths {
		result := gjson.Get(*jsonStringStr, jsonPath)
		if result.Exists() {
			processUpstreamPaths(ctx, result, upstreamType, parentName, ar, context, logger)
		}
	}
}

// processUpstreamPaths processes the paths for upstream configurations.
// It iterates through the provided paths and processes each one to collect and apply the upstream configurations.
// Parameters:
// - ctx: context for controlling the request lifetime
// - paths: a slice of paths to the upstream configurations to be processed
// Returns:
// - error: an error if any occurred during the processing of the upstream paths
func processUpstreamPaths(ctx context.Context, result gjson.Result, upstreamType models.GTypes, parentName string, ar *AllResources, context *db.AppContext, logger *logrus.Logger) {
	if result.IsArray() {
		result.ForEach(func(_, item gjson.Result) bool {
			processUpstreamPaths(ctx, item, upstreamType, parentName, ar, context, logger)
			return true
		})
	} else {
		resourceName := result.String()
		upstreamResourceProtoMsgs, additionalExtResources, err := ar.CollectAllResourcesWithParent(ctx, upstreamType, resourceName, parentName, context, logger)
		if err != nil {
			logger.Errorf("Error collecting upstream resources: %v", err)
			return
		}

		for _, protoMsg := range upstreamResourceProtoMsgs {
			uniqKey := fmt.Sprintf("%s__%s", resourceName, upstreamType.String())
			if protoMsg != nil {
				ar.AddToCollection(protoMsg, upstreamType, uniqKey, nil, resourceName)
			}
		}
		if additionalExtResources != nil {
			ar.processConfigDiscoveries(ctx, additionalExtResources, context, logger)
		}
	}
}

// AddToCollection adds an item to the specified collection.
// It takes the item and the collection as input and appends the item to the collection.
// Parameters:
// - collection: the collection to which the item will be added
// - item: the item to be added to the collection
// Returns:
// - error: an error if any occurred during the addition of the item to the collection
func (ar *AllResources) AddToCollection(resource proto.Message, gtype models.GTypes, uniqName string, parentName *string, resourceName string) {
	ar.mutex.Lock()
	defer ar.mutex.Unlock()
	if ar.checkAndMarkDuplicate(uniqName) {
		fmt.Printf("Skipping duplicate collection of resource: %s", uniqName)
		return
	}

	switch gtype {
	case models.Cluster:
		if newCluster, ok := proto.Clone(resource).(*cluster.Cluster); ok {
			ar.Cluster = append(ar.Cluster, newCluster)
		} else {
			fmt.Printf("Type assertion failed for Cluster")
		}
	case models.Route:
		if newRoute, ok := proto.Clone(resource).(*route.RouteConfiguration); ok {
			if vhds := newRoute.GetVhds(); vhds != nil {
				ar.UpdateVhdsMetadataNodeID(vhds)
			}
			ar.Route = append(ar.Route, newRoute)
		} else {
			fmt.Printf("Type assertion failed for RouteConfiguration")
		}
	case models.Endpoint:
		if newEndpoint, ok := proto.Clone(resource).(*endpoint.ClusterLoadAssignment); ok {
			ar.Endpoint = append(ar.Endpoint, newEndpoint)
		} else {
			fmt.Printf("Type assertion failed for ClusterLoadAssignment")
		}
	case models.VirtualHost:
		if newVirtualHost, ok := proto.Clone(resource).(*route.VirtualHost); ok {
			newVirtualHost.Name = fmt.Sprintf("%s/%s", *parentName, newVirtualHost.Name)
			ar.VirtualHost = append(ar.VirtualHost, newVirtualHost)
		} else {
			fmt.Printf("Type assertion failed for VirtualHost")
		}
	case models.CertificateValidationContext, models.TLSCertificate, models.TLSSessionTicketKeys, models.GenericSecret:
		newSecret := GetSecret(resourceName, resource)
		ar.AppendSecret(newSecret)
	default:
		ar.Extensions = append(ar.Extensions, resource)
	}
}

func (ar *AllResources) UpdateVhdsMetadataNodeID(vhds *route.Vhds) {
	if vhdsConfig := vhds.ConfigSource.GetApiConfigSource(); vhdsConfig != nil {
		vhdsConfig.GrpcServices[0].InitialMetadata[0].Value = ar.NodeID

		versionMetadata := &core.HeaderValue{
			Key:   "envoy-version",
			Value: ar.ResourceVersion,
		}
		vhdsConfig.GrpcServices[0].InitialMetadata = append(
			vhdsConfig.GrpcServices[0].InitialMetadata,
			versionMetadata,
		)
	}
}
