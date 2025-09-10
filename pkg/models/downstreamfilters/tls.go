package downstreamfilters

import "go.mongodb.org/mongo-driver/bson"

func TLSCertificateDownstreamFilters(dfm DownstreamFilter) []MongoFilters {
	return []MongoFilters{
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
	}
}

func ContextValidateDownstreamFilters(dfm DownstreamFilter) []MongoFilters {
	return []MongoFilters{
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
	}
}
