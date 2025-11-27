package resource

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	cluster "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/cluster/v3"
	core "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/core/v3"
	endpoint "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/endpoint/v3"
	listener "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/listener/v3"
	route "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/route/v3"
	hcm "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	"github.com/CloudNativeWorks/versioned-go-control-plane/pkg/cache/types"
	"github.com/tidwall/gjson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/errstr"
	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
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
func (ar *AllResources) DecodeListener(ctx context.Context, rawListenerResource *models.DBResource, context *db.AppContext, logger *logger.Logger, downstreamAddress string) {
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
func (ar *AllResources) initializeListener(ctx context.Context, rawListenerResource *models.DBResource, context *db.AppContext, logger *logger.Logger, downstreamAddress string) error {
	rawListeners, ok := rawListenerResource.Resource.Resource.(primitive.A)
	if !ok {
		return errstr.ErrUnexpectedResource
	}

	// Version increment moved to GenerateSnapshot for centralized control
	ar.mutex.Lock()
	ar.SetVersion(rawListenerResource.Resource.Version)
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
func (ar *AllResources) processConfigDiscoveries(ctx context.Context, configDiscoveries []*models.ConfigDiscovery, context *db.AppContext, logger *logger.Logger) {
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
func (ar *AllResources) processExtension(ctx context.Context, extension *models.ConfigDiscovery, parentName string, context *db.AppContext, logger *logger.Logger) {
	// For VirtualHost, we don't check duplicate at this level because the same VH
	// can be used by multiple routes (e.g., hopper-route and hopper-route-quic)
	// Duplicate check will be done in AddToCollection with parent-specific unique key
	if extension.GType != models.VirtualHost {
		uniqKey := fmt.Sprintf("%s__%s__%s", extension.Name, parentName, extension.GType.String())
		if ar.checkAndMarkDuplicate(uniqKey) {
			return
		}
	}

	extConfigs, additionalExtResources, err := ar.CollectAllResourcesWithParent(ctx, extension.GType, extension.Name, parentName, context, logger)
	if err != nil {
		logger.Errorf("Error collecting resources: %v", err)
		return
	}

	for i, extConfig := range extConfigs {
		if extension.GType == models.VirtualHost {
			// Extract actual VH name for unique key generation
			var virtualHostName string
			if virtualHost, ok := extConfig.(*route.VirtualHost); ok {
				virtualHostName = virtualHost.Name
			} else {
				virtualHostName = fmt.Sprintf("unknown_%d", i)
			}
			// Use actual VH name AND parent name in unique key to allow same VH in multiple routes
			// This allows the same virtual host to be used by multiple routes (e.g., HTTP/2 and HTTP/3)
			uniqKey := fmt.Sprintf("%s__%s__%s__%s", extension.Name, parentName, virtualHostName, extension.GType.String())
			ar.AddToCollection(extConfig, extension.GType, uniqKey, &parentName, extension.Name, logger)
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
func (ar *AllResources) CollectAllResourcesWithParent(ctx context.Context, gtype models.GType, resourceName, parentName string, context *db.AppContext, logger *logger.Logger) ([]proto.Message, []*models.ConfigDiscovery, error) {
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
			jsonStringStr, err := helper.MarshalJSON(item, context.Logger)
			if err != nil {
				logger.Errorf("Error marshaling array item: %v", err)
				return nil, nil, err
			}

			// Transform WASM configuration if needed (decode base64, add @type)
			if gtype == models.HTTPWasm {
				jsonStringStr, err = ar.transformWasmConfiguration(jsonStringStr, logger)
				if err != nil {
					logger.Warnf("Error transforming WASM configuration: %v", err)
				}
			}

			typedProtoMsg := proto.Clone(gtype.ProtoMessage())
			if err := ar.processTypedConfigsAndUpstream(ctx, typedProtoMsg, &jsonStringStr, gtype, parentName, context, logger); err != nil {
				logger.Errorf("Error processing typed configs and upstream resources: %v", err)
				return nil, nil, err
			}

			protoMessages = append(protoMessages, typedProtoMsg)
			finalConfigDiscoveries = resource.General.ConfigDiscovery
			ar.processConfigDiscoveries(ctx, resource.General.ConfigDiscovery, context, logger)
		}
	default:
		jsonStringStr, err := helper.MarshalJSON(resourceData, context.Logger)
		if err != nil {
			return nil, nil, err
		}

		// Transform WASM configuration if needed (decode base64, add @type)
		if gtype == models.HTTPWasm {
			jsonStringStr, err = ar.transformWasmConfiguration(jsonStringStr, logger)
			if err != nil {
				logger.Warnf("Error transforming WASM configuration: %v", err)
			}
		}

		typedProtoMsg := proto.Clone(gtype.ProtoMessage())
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
func (ar *AllResources) processTypedConfigsAndUpstream(ctx context.Context, protoMsg proto.Message, jsonStringStr *string, gtype models.GType, parentName string, context *db.AppContext, logger *logger.Logger) error {
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
func (ar *AllResources) processTypedConfigPaths(ctx context.Context, configPaths []models.TypedConfigPath, jsonStringStr *string, context *db.AppContext, logger *logger.Logger) {
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
func (ar *AllResources) processUpstreamPaths(ctx context.Context, upstreamPaths map[string]models.GType, jsonStringStr *string, parentName string, context *db.AppContext, logger *logger.Logger) {
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
func processUpstreamPaths(ctx context.Context, result gjson.Result, upstreamType models.GType, parentName string, ar *AllResources, context *db.AppContext, logger *logger.Logger) {
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
				ar.AddToCollection(protoMsg, upstreamType, uniqKey, nil, resourceName, logger)
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
func (ar *AllResources) AddToCollection(resource proto.Message, gtype models.GType, uniqName string, parentName *string, resourceName string, logger *logger.Logger) {
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
			logger.Errorf("Type assertion failed for VirtualHost")
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
		// Update nodeid metadata if it exists, otherwise add it
		nodeIDUpdated := false
		for _, metadata := range vhdsConfig.GrpcServices[0].InitialMetadata {
			if metadata.Key == "nodeid" {
				metadata.Value = ar.NodeID
				nodeIDUpdated = true
				break
			}
		}

		// If nodeid metadata not found, add it at the beginning
		if !nodeIDUpdated {
			nodeIDMetadata := &core.HeaderValue{
				Key:   "nodeid",
				Value: ar.NodeID,
			}
			vhdsConfig.GrpcServices[0].InitialMetadata = append(
				[]*core.HeaderValue{nodeIDMetadata},
				vhdsConfig.GrpcServices[0].InitialMetadata...,
			)
		}

		// Add version metadata if not already present
		versionExists := false
		for _, metadata := range vhdsConfig.GrpcServices[0].InitialMetadata {
			if metadata.Key == "envoy-version" {
				metadata.Value = ar.ResourceVersion
				versionExists = true
				break
			}
		}

		if !versionExists {
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
}

// transformWasmConfiguration decodes base64 value in configuration field and adds @type
// transformWasmConfiguration transforms WASM configuration from MongoDB format to protobuf format.
// It orchestrates the decoding, validation, and encoding process.
func (ar *AllResources) transformWasmConfiguration(jsonStr string, logger *logger.Logger) (string, error) {
	// Parse input JSON
	data, _, configuration, err := ar.extractWasmConfigFields(jsonStr)
	if err != nil {
		return jsonStr, err
	}
	if configuration == nil {
		return jsonStr, nil // Not a WASM config, return as-is
	}

	// Extract and decode base64 config value
	typeURL, decodedValue, err := ar.decodeWasmConfigValue(configuration, logger)
	if err != nil {
		return jsonStr, err
	}

	// Validate the decoded config is valid JSON
	if err := ar.validateWasmConfigJSON(decodedValue, logger); err != nil {
		return jsonStr, err
	}

	// Transform to protobuf format using placeholder technique
	result, err := ar.applyProtobufTransformation(data, configuration, typeURL, decodedValue, logger)
	if err != nil {
		return jsonStr, err 
	}

	return result, nil
}

// extractWasmConfigFields extracts the configuration fields from WASM extension JSON.
// Returns nil configuration if this is not a WASM extension.
func (ar *AllResources) extractWasmConfigFields(jsonStr string) (map[string]any, map[string]any, map[string]any, error) {
	var data map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	config, ok := data["config"].(map[string]any)
	if !ok {
		return data, nil, nil, nil // No config field
	}

	configuration, ok := config["configuration"].(map[string]any)
	if !ok {
		return data, config, nil, nil // No configuration field
	}

	return data, config, configuration, nil
}

// decodeWasmConfigValue decodes the base64-encoded WASM configuration value.
func (ar *AllResources) decodeWasmConfigValue(configuration map[string]any, logger *logger.Logger) (string, []byte, error) {
	typeURL, hasTypeURL := configuration["type_url"].(string)
	valueBase64, hasValue := configuration["value"].(string)

	if !hasTypeURL || !hasValue {
		return "", nil, fmt.Errorf("missing type_url or value fields")
	}

	decodedValue, err := base64.StdEncoding.DecodeString(valueBase64)
	if err != nil {
		logger.Warnf("Failed to decode base64 configuration value: %v", err)
		return "", nil, fmt.Errorf("invalid base64 encoding in WASM config: %w", err)
	}

	// Log decoded config (truncated for safety)
	logLen := len(decodedValue)
	if logLen > 500 {
		logLen = 500
	}
	logger.Infof("📥 WAF Config RAW (first %d chars): %s", logLen, string(decodedValue[:logLen]))

	return typeURL, decodedValue, nil
}

// validateWasmConfigJSON validates that the decoded config is valid JSON.
func (ar *AllResources) validateWasmConfigJSON(decodedValue []byte, logger *logger.Logger) error {
	var testParse any
	if err := json.Unmarshal(decodedValue, &testParse); err != nil {
		logger.Errorf("❌ Decoded WAF config is NOT valid JSON: %v", err)
		return fmt.Errorf("decoded WAF config is not valid JSON: %w", err)
	}
	logger.Infof("✅ Decoded WAF config is valid JSON")
	return nil
}

// applyProtobufTransformation applies the protobuf StringValue transformation using placeholder technique.
// This avoids double-escaping by using a placeholder and manual replacement.
func (ar *AllResources) applyProtobufTransformation(data, configuration map[string]any, typeURL string, decodedValue []byte, logger *logger.Logger) (string, error) {
	// Generate unique placeholder
	placeholder := fmt.Sprintf("__WASM_CFG_%d_PLACEHOLDER__", len(decodedValue))

	// Update configuration with @type and placeholder
	configuration["@type"] = typeURL
	configuration["value"] = placeholder
	delete(configuration, "type_url")

	logger.Debugf("🔍 Transformed WASM configuration: type=%s, decoded_value_length=%d", typeURL, len(decodedValue))

	// Marshal with placeholder
	modifiedJSON, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("failed to marshal modified JSON: %w", err)
	}

	// Replace placeholder with properly escaped config
	result, err := ar.replacePlaceholderWithConfig(string(modifiedJSON), placeholder, decodedValue, logger)
	if err != nil {
		return "", err
	}

	return result, nil
}

// replacePlaceholderWithConfig replaces the placeholder with the actual WASM config.
// It properly escapes the config as a JSON string to avoid double-escaping issues.
func (ar *AllResources) replacePlaceholderWithConfig(modifiedJSON, placeholder string, decodedValue []byte, logger *logger.Logger) (string, error) {
	quotedPlaceholder := `"` + placeholder + `"`

	// Marshal as JSON string (this escapes all special characters)
	marshaledString, err := json.Marshal(string(decodedValue))
	if err != nil {
		return "", fmt.Errorf("failed to marshal WAF config string: %w", err)
	}

	// Verify placeholder exists (safety check)
	if !strings.Contains(modifiedJSON, quotedPlaceholder) {
		logger.Errorf("❌ Placeholder not found in marshaled JSON - internal error")
		return "", fmt.Errorf("internal error: placeholder not found in JSON")
	}

	// Replace placeholder with marshaled config
	result := strings.ReplaceAll(modifiedJSON, quotedPlaceholder, string(marshaledString))

	// Log result (truncated for safety)
	resultLogLen := len(result)
	if resultLogLen > 600 {
		resultLogLen = 600
	}
	logger.Infof("📤 Final JSON (first %d chars): %s", resultLogLen, result[:resultLogLen])

	return result, nil
}
