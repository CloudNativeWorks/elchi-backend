package downstreamfilters

import (
	"go.mongodb.org/mongo-driver/bson"
)

func ClusterDownstreamFilters(dfm DownstreamFilter) []MongoFilters {
	return []MongoFilters{
		{
			Collection: "routes",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "$or", Value: bson.A{
						bson.D{{Key: "resource.resource.virtual_hosts.routes.route.cluster", Value: dfm.Name}},
						bson.D{{Key: "resource.resource.virtual_hosts.routes.route.weighted_clusters.clusters.name", Value: dfm.Name}},
						bson.D{{Key: "resource.resource.virtual_hosts.request_mirror_policies.cluster", Value: dfm.Name}},
						bson.D{{Key: requestMirrorPoliciesCluster, Value: dfm.Name}},
					}}},
				}},
			},
		},
		{
			Collection: "filters",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "$or", Value: bson.A{
						bson.D{
							{Key: "$and", Value: bson.A{
								bson.D{{Key: "general.gtype", Value: "envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy"}},
								bson.D{{Key: "$or", Value: bson.A{
									bson.D{{Key: "resource.resource.cluster", Value: dfm.Name}},
									bson.D{{Key: "resource.resource.weighted_clusters.clusters.name", Value: dfm.Name}},
								}}},
							}},
						},
						bson.D{
							{Key: "$and", Value: bson.A{
								bson.D{{Key: "general.gtype", Value: "envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager"}},
								bson.D{{Key: "$or", Value: bson.A{
									bson.D{{Key: "resource.resource.route_config.virtual_hosts.routes.route.cluster", Value: dfm.Name}},
									bson.D{{Key: "resource.resource.route_config.virtual_hosts.routes.route.weighted_clusters.clusters.name", Value: dfm.Name}},
									bson.D{{Key: "resource.resource.route_config.virtual_hosts.request_mirror_policies.cluster", Value: dfm.Name}},
								}}},
							}},
						},
					}}},
				}},
			},
		},
		{
			Collection: "extensions",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "$or", Value: bson.A{
						bson.D{
							{Key: "$and", Value: bson.A{
								bson.D{{Key: "general.gtype", Value: "envoy.extensions.access_loggers.fluentd.v3.FluentdAccessLogConfig"}},
								bson.D{{Key: "resource.resource.cluster", Value: dfm.Name}},
							}},
						},
						bson.D{
							{Key: "$and", Value: bson.A{
								bson.D{{Key: "general.gtype", Value: "envoy.extensions.stat_sinks.open_telemetry.v3.SinkConfig"}},
								bson.D{{Key: "resource.resource.grpc_service.envoy_grpc.cluster_name", Value: dfm.Name}},
							}},
						},
					}}},
				}},
			},
		},
		{
			Collection: "virtual_hosts",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "$or", Value: bson.A{
						bson.D{{Key: "resource.resource.routes.route.cluster", Value: dfm.Name}},
						bson.D{{Key: "resource.resource.routes.route.weighted_clusters.clusters.name", Value: dfm.Name}},
						bson.D{{Key: requestMirrorPoliciesCluster, Value: dfm.Name}},
						bson.D{{Key: requestMirrorPoliciesCluster, Value: dfm.Name}},
					}}},
				}},
			},
		},
		{
			Collection: "bootstrap",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "$or", Value: bson.A{
						bson.D{{Key: "resource.resource.static_resources.clusters.name", Value: dfm.Name}},
					}}},
				}},
			},
		},
	}
}
