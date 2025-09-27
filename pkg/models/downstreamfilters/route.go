package downstreamfilters

import "go.mongodb.org/mongo-driver/bson"

func RouteDownstreamFilters(dfm DownstreamFilter) []MongoFilters {
	return []MongoFilters{
		{
			Collection: "filters",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "resource.resource.rds.route_config_name", Value: dfm.Name}},
				}},
			},
		},
	}
}

func VirtualHostDownstreamFilters(dfm DownstreamFilter) []MongoFilters {
	return []MongoFilters{
		{
			Collection: "routes",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "general.config_discovery.name", Value: dfm.Name}},
				}},
			},
		},
		{
			Collection: "filters",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "general.config_discovery.name", Value: dfm.Name}},
				}},
			},
		},
	}
}
