package downstreamfilters

import "go.mongodb.org/mongo-driver/bson"

func ALSDownstreamFilters(dfm DownstreamFilter) []MongoFilters {
	return []MongoFilters{
		{
			Collection: "filters",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "general.typed_config.name", Value: dfm.Name}},
				}},
			},
		},
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

func HCEFSDownstreamFilters(dfm DownstreamFilter) []MongoFilters {
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

func UTMDownstreamFilters(dfm DownstreamFilter) []MongoFilters {
	return []MongoFilters{
		{
			Collection: "virtual_hosts",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "general.typed_config.name", Value: dfm.Name}},
				}},
			},
		},
		{
			Collection: "routes",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "general.typed_config.name", Value: dfm.Name}},
				}},
			},
		},
		{
			Collection: "filters",
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

func TypedConfigDownstreamBootstrapFilters(dfm DownstreamFilter) []MongoFilters {
	return []MongoFilters{
		{
			Collection: "bootstrap",
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

func TypedConfigDownstreamFilters(dfm DownstreamFilter) []MongoFilters {
	return []MongoFilters{
		{
			Collection: "filters",
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

func TypedClusterDownstreamFilters(dfm DownstreamFilter) []MongoFilters {
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

func GenericSecretDownstreamFilters(dfm DownstreamFilter) []MongoFilters {
	return []MongoFilters{
		// OAuth2 filter - hmac_secret SDS reference
		{
			Collection: "filters",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "resource.resource.config.credentials.hmac_secret.name", Value: dfm.Name}},
				}},
			},
		},
		// OAuth2 filter - token_secret SDS reference
		{
			Collection: "filters",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "resource.resource.config.credentials.token_secret.name", Value: dfm.Name}},
				}},
			},
		},
	}
}

// DNSResolverDownstreamFilters returns downstream filters for DNS resolver extensions
// DNS resolvers can be used in both Bootstrap (typed_dns_resolver_config) and Cluster (typed_dns_resolver_config)
func DNSResolverDownstreamFilters(dfm DownstreamFilter) []MongoFilters {
	return []MongoFilters{
		{
			Collection: "bootstrap",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "general.typed_config.name", Value: dfm.Name}},
				}},
			},
		},
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
