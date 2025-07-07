package downstreamfilters

import "go.mongodb.org/mongo-driver/bson"

func ConfigDiscoveryListenerDownstreamFilters(dfm DownstreamFilter) []MongoFilters {
	return []MongoFilters{
		{
			Collection: "listeners",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: generalConfigDiscoveryName, Value: dfm.Name}},
				}},
			},
		},
	}
}

func ConfigDiscoveryHTTPFilterDownstreamFilters(dfm DownstreamFilter) []MongoFilters {
	return []MongoFilters{
		{
			Collection: "filters",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: generalConfigDiscoveryName, Value: dfm.Name}},
				}},
			},
		},
	}
}

func TypedHTTPFilterDownstreamFilters(dfm DownstreamFilter) []MongoFilters {
	return []MongoFilters{
		{
			Collection: "routes",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: generalTypedConfigName, Value: dfm.Name}},
				}},
			},
		},
		{
			Collection: "virtual_hosts",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: generalTypedConfigName, Value: dfm.Name}},
				}},
			},
		},
		{
			Collection: "filters",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: generalTypedConfigName, Value: dfm.Name}},
				}},
			},
		},
	}
}

func DiscoverAndTypedHTTPFilterDownstreamFilters(dfm DownstreamFilter) []MongoFilters {
	return []MongoFilters{
		{
			Collection: "filters",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: generalConfigDiscoveryName, Value: dfm.Name}},
				}},
			},
		},
		{
			Collection: "routes",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: generalTypedConfigName, Value: dfm.Name}},
				}},
			},
		},
		{
			Collection: "virtual_hosts",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: generalTypedConfigName, Value: dfm.Name}},
				}},
			},
		},
	}
}
