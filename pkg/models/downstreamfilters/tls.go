package downstreamfilters

import "go.mongodb.org/mongo-driver/bson"

func TLSCertificateDownstreamFilters(dfm DownstreamFilter) []MongoFilters {
	return []MongoFilters{
		// DownstreamTlsContext and UpstreamTlsContext - direct common_tls_context
		{
			Collection: "tls",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "resource.resource.common_tls_context.tls_certificate_sds_secret_configs.name", Value: dfm.Name}},
				}},
			},
		},
		// QuicUpstreamTransport - upstream_tls_context.common_tls_context
		{
			Collection: "tls",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "resource.resource.upstream_tls_context.common_tls_context.tls_certificate_sds_secret_configs.name", Value: dfm.Name}},
				}},
			},
		},
		// QuicDownstreamTransport - downstream_tls_context.common_tls_context
		{
			Collection: "tls",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "resource.resource.downstream_tls_context.common_tls_context.tls_certificate_sds_secret_configs.name", Value: dfm.Name}},
				}},
			},
		},
	}
}

func ContextValidateDownstreamFilters(dfm DownstreamFilter) []MongoFilters {
	return []MongoFilters{
		// DownstreamTlsContext and UpstreamTlsContext - direct common_tls_context
		{
			Collection: "tls",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "resource.resource.common_tls_context.validation_context_sds_secret_config.name", Value: dfm.Name}},
				}},
			},
		},
		// QuicUpstreamTransport - upstream_tls_context.common_tls_context
		{
			Collection: "tls",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "resource.resource.upstream_tls_context.common_tls_context.validation_context_sds_secret_config.name", Value: dfm.Name}},
				}},
			},
		},
		// QuicDownstreamTransport - downstream_tls_context.common_tls_context
		{
			Collection: "tls",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "resource.resource.downstream_tls_context.common_tls_context.validation_context_sds_secret_config.name", Value: dfm.Name}},
				}},
			},
		},
	}
}

func DownstreamTypedFilters(dfm DownstreamFilter) []MongoFilters {
	return []MongoFilters{
		{
			Collection: "listeners",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "general.typed_config.name", Value: dfm.Name}},
				}},
			},
		},
	}
}

func UpstreamTLSDownstreamFilters(dfm DownstreamFilter) []MongoFilters {
	return []MongoFilters{
		// Direct cluster reference (transport_socket in cluster)
		{
			Collection: "clusters",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "general.typed_config.name", Value: dfm.Name}},
				}},
			},
		},
		// ProxyProtocolUpstreamTransport - inner transport_socket reference via general.typed_config
		{
			Collection: "tls",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "general.typed_config.name", Value: dfm.Name}},
				}},
			},
		},
	}
}

// TransportSocketDownstreamFilters returns downstream filters for transport sockets
// that can be used in both Listener (downstream), Cluster (upstream), and as inner transport in ProxyProtocol
// Examples: RawBuffer, StartTls
func TransportSocketDownstreamFilters(dfm DownstreamFilter) []MongoFilters {
	return []MongoFilters{
		// Direct listener reference (transport_socket in listener filter_chains)
		{
			Collection: "listeners",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "general.typed_config.name", Value: dfm.Name}},
				}},
			},
		},
		// Direct cluster reference (transport_socket in cluster)
		{
			Collection: "clusters",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "general.typed_config.name", Value: dfm.Name}},
				}},
			},
		},
		// ProxyProtocolUpstreamTransport - inner transport_socket reference via general.typed_config
		{
			Collection: "tls",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "general.typed_config.name", Value: dfm.Name}},
				}},
			},
		},
	}
}
