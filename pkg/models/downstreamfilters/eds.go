package downstreamfilters

import "go.mongodb.org/mongo-driver/bson"

func EdsDownstreamFilters(dfm DownstreamFilter) []MongoFilters {
	return []MongoFilters{
		{
			Collection: "clusters",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: generalProject, Value: dfm.Project}},
					bson.D{{Key: generalVersion, Value: dfm.Version}},
					bson.D{{Key: "resource.resource.eds_cluster_config.service_name", Value: dfm.Name}},
				}},
			},
		},
	}
}
