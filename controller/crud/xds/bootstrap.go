package xds

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/common"
	"github.com/CloudNativeWorks/elchi-backend/pkg/errstr"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
)

type CollectorFunc func(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails, version string) (models.ResourceClass, error)

type BootstrapCollector struct {
	collectors map[string]CollectorFunc
}

func (xds *AppHandler) NewBootstrapCollector() *BootstrapCollector {
	return &BootstrapCollector{
		collectors: map[string]CollectorFunc{
			"clusters":    xds.collectBootstrapClusters,
			"access_logs": xds.collectAccessLoggers,
			"stats_sinks": xds.collectStatSinks,
		},
	}
}

func (xds *AppHandler) DownloadBootstrap(ctx context.Context, requestDetails models.RequestDetails, cf models.ClientFields) (any, error) {
	resource := &models.DBResource{}
	collection := xds.Context.Client.Collection(requestDetails.Collection)
	filter, err := common.AddResourceIDFilter(requestDetails, bson.M{"general.name": requestDetails.Name})
	if err != nil {
		return nil, errors.New("invalid id format")
	}
	filterWithRestriction := common.AddUserFilter(requestDetails, filter)
	result := collection.FindOne(ctx, filterWithRestriction)

	if result.Err() != nil {
		if errors.Is(result.Err(), mongo.ErrNoDocuments) {
			return nil, errors.New("not found: (" + requestDetails.Name + ")")
		}
		return nil, errstr.ErrUnknownDBError
	}

	if err := result.Decode(resource); err != nil {
		return nil, err
	}

	collector := xds.NewBootstrapCollector()
	bootstrap, err := collector.CollectAll(ctx, resource, requestDetails)
	if err != nil {
		return nil, err
	}

	if cf.DownstreamAddress != "" {
		err := resources.AttachDownstreamAddressToBootstrap(bootstrap.GetResource(), cf)
		if err != nil {
			return nil, err
		}
	}

	bootstrap.SetResource(bootstrap.GetResource())
	general := bootstrap.GetGeneral()
	listenerID := general.Name + "::" + general.Project
	err = AddPrefixandTagsToOtelStatsSink(bootstrap, listenerID, cf)
	if err != nil {
		return nil, err
	}

	return bootstrap.GetResource(), nil
}

func (bc *BootstrapCollector) CollectAll(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails) (models.ResourceClass, error) {
	var err error
	bootstrap := resource
	version := resource.GetGeneral().Version

	for name, collector := range bc.collectors {
		if shouldSkip, err := bc.shouldSkipCollection(bootstrap, name); err != nil {
			return nil, fmt.Errorf("checking %s collection: %w", name, err)
		} else if shouldSkip {
			continue
		}

		bootstrap, err = collector(ctx, bootstrap, requestDetails, version)
		if err != nil {
			return nil, fmt.Errorf("collecting %s: %w", name, err)
		}
	}

	return bootstrap, nil
}

func AddPrefixandTagsToOtelStatsSink(dbResource models.ResourceClass, prefix string, cf models.ClientFields) error {
	bootstrapRaw := dbResource.GetResource()
	bootstrapMap, ok := bootstrapRaw.(primitive.M)

	if !ok {
		return fmt.Errorf("resource.GetResource() is not a primitive.M")
	}
	statsSinks, ok := bootstrapMap["stats_sinks"].([]any)
	if !ok {
		return fmt.Errorf("stats_sinks not found or not a primitive.A")
	}

	if cf.ClientName != "" {
		statsConfig, ok := bootstrapMap["stats_config"].(primitive.M)
		if !ok {
			otelSink := map[string][]map[string]string{
				"stats_tags": {
					{
						"tag_name":    "client_name",
						"fixed_value": cf.ClientName,
					},
				},
			}
			bootstrapMap["stats_config"] = otelSink
		} else {
			statsTags, ok := statsConfig["stats_tags"].(primitive.A)
			if !ok {
				statsTags = primitive.A{}
			}
			found := false
			for i, tagRaw := range statsTags {
				tag, ok := tagRaw.(primitive.M)
				if !ok {
					continue
				}
				if tag["tag_name"] == "client_name" {
					tag["fixed_value"] = cf.ClientName
					statsTags[i] = tag
					found = true
					break
				}
			}
			if !found {
				statsTags = append(statsTags, primitive.M{
					"tag_name":    "client_name",
					"fixed_value": cf.ClientName,
				})
			}
			statsConfig["stats_tags"] = statsTags
			bootstrapMap["stats_config"] = statsConfig
		}
	}

	for _, sink := range statsSinks {
		sinkMap, ok := sink.(models.TC)
		if !ok {
			continue
		}

		if sinkMap.Name != "envoy.stat_sinks.open_telemetry" {
			continue
		}

		grpcService, ok := sinkMap.TypedConfig["grpc_service"].(primitive.M)
		if !ok {
			continue
		}

		envoyGrpc, ok := grpcService["envoy_grpc"].(primitive.M)
		if !ok {
			continue
		}

		clusterName, _ := envoyGrpc["cluster_name"].(string)
		if clusterName != "elchi-control-plane" {
			continue
		}

		sinkMap.TypedConfig["prefix"] = prefix

		dbResource.SetResource(bootstrapMap)
		return nil
	}
	return fmt.Errorf("no suitable stats_sink found")
}

func (bc *BootstrapCollector) shouldSkipCollection(resource models.ResourceClass, collectorName string) (bool, error) {
	bootstrapMap, ok := resource.GetResource().(primitive.M)
	if !ok {
		return false, errors.New("invalid bootstrap format")
	}

	switch collectorName {
	case "access_logs":
		admin, ok := bootstrapMap["admin"].(primitive.M)
		if !ok {
			return true, nil
		}
		_, hasAccessLog := admin["access_log"].(primitive.A)
		return !hasAccessLog, nil

	case "clusters":
		staticResources, ok := bootstrapMap["static_resources"].(primitive.M)
		if !ok {
			return true, nil
		}
		_, hasClusters := staticResources["clusters"].(primitive.A)
		return !hasClusters, nil

	case "stats_sinks":
		statSinks, ok := bootstrapMap["stats_sinks"].(primitive.A)
		if !ok || len(statSinks) == 0 {
			return true, nil
		}
		return false, nil

	default:
		return false, fmt.Errorf("unknown collector: %s", collectorName)
	}
}

func (xds *AppHandler) collectBootstrapClusters(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails, version string) (models.ResourceClass, error) {
	bootstrap := resource.GetResource()
	bootstrapMap, ok := bootstrap.(primitive.M)
	if !ok {
		return nil, fmt.Errorf("failed to parse bootstrap as primitive.M, got type: %T", bootstrap)
	}

	staticResources, ok := bootstrapMap["static_resources"].(primitive.M)
	if !ok {
		return nil, errors.New("'static_resources' key not found or invalid")
	}

	clusters, ok := staticResources["clusters"].(primitive.A)
	if !ok {
		return nil, errors.New("'clusters' key not found or invalid")
	}

	var clusterNames []string
	for _, cluster := range clusters {
		clusterMap, ok := cluster.(primitive.M)
		if !ok {
			continue
		}

		if name, ok := clusterMap["name"].(string); ok {
			clusterNames = append(clusterNames, name)
		}
	}

	clusters, err := xds.GetNonEdsClusters(ctx, clusterNames, requestDetails, version)
	if err != nil {
		return nil, err
	}

	resource.SetBootstrapClusters(clusters)

	return resource, nil
}

func (xds *AppHandler) GetNonEdsClusters(ctx context.Context, clusterNames []string, requestDetails models.RequestDetails, version string) ([]any, error) {
	resource := &models.DBResource{}
	collection := xds.Context.Client.Collection("clusters")
	results := []any{}
	for _, clusterName := range clusterNames {
		filter := bson.M{"general.name": clusterName, "general.project": requestDetails.Project, "general.version": version}
		result := collection.FindOne(ctx, filter)

		if result.Err() != nil {
			if errors.Is(result.Err(), mongo.ErrNoDocuments) {
				return nil, errors.New("not found: (" + clusterName + ")")
			}
			return nil, errstr.ErrUnknownDBError
		}

		err := result.Decode(resource)
		if err != nil {
			return nil, err
		}

		general := resource.GetGeneral()
		if len(general.TypedConfig) != 0 {
			res := resource.GetResource()
			cluster, ok := res.(primitive.M)
			if !ok {
				return nil, errors.New("failed to parse cluster")
			}

			for _, typed := range general.TypedConfig {
				switch typed.Gtype {
				case "envoy.extensions.upstreams.http.v3.HttpProtocolOptions":
					protocolOptions, err := xds.GetHttpProtocolOptions(ctx, typed.Collection, typed.Name, requestDetails, version)
					if err != nil {
						return nil, err
					}

					if len(protocolOptions) != 0 {
						cluster["typed_extension_protocol_options"] = primitive.M{
							"envoy.extensions.upstreams.http.v3.HttpProtocolOptions": protocolOptions,
						}
					}
				case "envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext":
					transportSocket, err := xds.GetTransportSocket(ctx, typed.Collection, typed.Name, requestDetails, version)
					if err != nil {
						return nil, err
					}

					if len(transportSocket) != 0 {
						cluster["transport_socket"] = transportSocket
					}
				}
			}

			results = append(results, cluster)
		} else {
			results = append(results, resource.GetResource())
		}
	}

	return results, nil
}

func (xds *AppHandler) GetTransportSocket(ctx context.Context, collectionName, name string, requestDetails models.RequestDetails, version string) (primitive.M, error) {
	resource := &models.DBResource{}
	collection := xds.Context.Client.Collection(collectionName)
	filter := bson.M{"general.name": name, "general.project": requestDetails.Project, "general.version": version}
	result := collection.FindOne(ctx, filter)

	if result.Err() != nil {
		if errors.Is(result.Err(), mongo.ErrNoDocuments) {
			return nil, errors.New("not found: (" + name + ")")
		}
		return nil, errstr.ErrUnknownDBError
	}

	err := result.Decode(resource)
	if err != nil {
		return nil, err
	}

	resourceData, ok := resource.GetResource().(primitive.M)
	if !ok {
		return nil, fmt.Errorf("failed to parse resource.GetResource() as primitive.M, got: %T", resource.GetResource())
	}

	general := resource.GetGeneral()
	transportSocket := primitive.M{
		"name": "envoy.transport_sockets.tls",
		"typed_config": primitive.M{
			"@type": "type.googleapis.com/" + general.GType,
		},
	}

	maps.Copy(transportSocket["typed_config"].(primitive.M), resourceData)

	return transportSocket, nil
}

func (xds *AppHandler) GetHttpProtocolOptions(ctx context.Context, collectionName, name string, requestDetails models.RequestDetails, version string) (primitive.M, error) {
	resource := &models.DBResource{}
	collection := xds.Context.Client.Collection(collectionName)
	filter := bson.M{"general.name": name, "general.project": requestDetails.Project, "general.version": version}
	result := collection.FindOne(ctx, filter)

	if result.Err() != nil {
		if errors.Is(result.Err(), mongo.ErrNoDocuments) {
			return nil, errors.New("not found: (" + name + ")")
		}
		return nil, errstr.ErrUnknownDBError
	}

	err := result.Decode(resource)
	if err != nil {
		return nil, err
	}

	resourceData, ok := resource.GetResource().(primitive.M)
	if !ok {
		return nil, fmt.Errorf("failed to parse resource.GetResource() as primitive.M, got: %T", resource.GetResource())
	}

	httpProtocolOptions := primitive.M{
		"@type": "type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions",
	}

	for key, value := range resourceData {
		httpProtocolOptions[key] = value
	}

	return httpProtocolOptions, nil
}

func (xds *AppHandler) collectAccessLoggers(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails, version string) (models.ResourceClass, error) {
	bootstrap := resource.GetResource()
	bootstrapMap, ok := bootstrap.(primitive.M)
	if !ok {
		return nil, fmt.Errorf("failed to parse bootstrap as primitive.M, got type: %T", bootstrap)
	}

	admin, ok := bootstrapMap["admin"].(primitive.M)
	if !ok {
		return nil, errors.New("'admin' key not found or invalid")
	}

	accessLog, ok := admin["access_log"].(primitive.A)
	if !ok {
		return nil, errors.New("'access_log' key not found or invalid")
	}

	var accessLogs []string
	for _, aclog := range accessLog {
		acLogMap, ok := aclog.(primitive.M)
		if !ok {
			continue
		}

		if typedConfig, ok := acLogMap["typed_config"].(primitive.M); ok {
			typedConf, err := resources.DecodeBase64Config(typedConfig["value"].(string))
			if err != nil {
				return nil, err
			}
			accessLogs = append(accessLogs, typedConf.Name)
		}
	}

	accessLoggers, err := xds.GetAccessLoggers(ctx, accessLogs, requestDetails, version)
	if err != nil {
		return nil, err
	}

	resource.SetBootstrapAccessLoggers(accessLoggers)

	return resource, nil
}

func (xds *AppHandler) GetAccessLoggers(ctx context.Context, alNames []string, requestDetails models.RequestDetails, version string) ([]any, error) {
	resource := &models.DBResource{}
	collection := xds.Context.Client.Collection("extensions")
	results := []any{}
	for _, alName := range alNames {
		filter := bson.M{"general.name": alName, "general.project": requestDetails.Project, "general.version": version}
		result := collection.FindOne(ctx, filter)

		if result.Err() != nil {
			if errors.Is(result.Err(), mongo.ErrNoDocuments) {
				return nil, errors.New("not found: (" + alName + ")")
			}
			return nil, errstr.ErrUnknownDBError
		}

		err := result.Decode(resource)
		if err != nil {
			return nil, err
		}

		resourceData, ok := resource.GetResource().(primitive.M)
		if !ok {
			return nil, fmt.Errorf("failed to parse resource.GetResource() as primitive.M, got: %T", resource.GetResource())
		}

		general := resource.GetGeneral()
		typedConfig := models.TC{
			Name: general.CanonicalName,
			TypedConfig: map[string]any{
				"@type": "type.googleapis.com/" + general.GType,
			},
		}

		for key, value := range resourceData {
			typedConfig.TypedConfig[key] = value
		}

		results = append(results, typedConfig)
	}

	return results, nil
}

func (xds *AppHandler) collectStatSinks(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails, version string) (models.ResourceClass, error) {
	bootstrap := resource.GetResource()
	bootstrapMap, ok := bootstrap.(primitive.M)
	if !ok {
		return nil, fmt.Errorf("failed to parse bootstrap as primitive.M, got type: %T", bootstrap)
	}

	statSinks, ok := bootstrapMap["stats_sinks"].(primitive.A)
	if !ok {
		return nil, errors.New("'stats_sinks' key not found or invalid")
	}

	var statSinkNames []string
	for _, sink := range statSinks {
		sinkMap, ok := sink.(primitive.M)
		if !ok {
			continue
		}

		if typedConfig, ok := sinkMap["typed_config"].(primitive.M); ok {
			typedConf, err := resources.DecodeBase64Config(typedConfig["value"].(string))
			if err != nil {
				return nil, err
			}
			statSinkNames = append(statSinkNames, typedConf.Name)
		}
	}

	statSinkConfigs, err := xds.GetStatSinks(ctx, statSinkNames, requestDetails, version)
	if err != nil {
		return nil, err
	}

	resource.SetBootstrapStatSinks(statSinkConfigs)

	return resource, nil
}

func (xds *AppHandler) GetStatSinks(ctx context.Context, sinkNames []string, requestDetails models.RequestDetails, version string) ([]any, error) {
	resource := &models.DBResource{}
	collection := xds.Context.Client.Collection("extensions")
	results := []any{}
	for _, sinkName := range sinkNames {
		filter := bson.M{"general.name": sinkName, "general.project": requestDetails.Project, "general.version": version}
		result := collection.FindOne(ctx, filter)

		if result.Err() != nil {
			if errors.Is(result.Err(), mongo.ErrNoDocuments) {
				return nil, errors.New("not found: (" + sinkName + ")")
			}
			return nil, errstr.ErrUnknownDBError
		}

		err := result.Decode(resource)
		if err != nil {
			return nil, err
		}

		resourceData, ok := resource.GetResource().(primitive.M)
		if !ok {
			return nil, fmt.Errorf("failed to parse resource.GetResource() as primitive.M, got: %T", resource.GetResource())
		}

		general := resource.GetGeneral()
		typedConfig := models.TC{
			Name: general.CanonicalName,
			TypedConfig: map[string]any{
				"@type": "type.googleapis.com/" + string(general.GType),
			},
		}

		maps.Copy(typedConfig.TypedConfig, resourceData)

		results = append(results, typedConfig)
	}

	return results, nil
}
