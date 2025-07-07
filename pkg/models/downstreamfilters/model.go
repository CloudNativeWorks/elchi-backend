package downstreamfilters

import (
	"go.mongodb.org/mongo-driver/bson"
)

type MongoFilters struct {
	Collection string
	Filter     bson.D
}

type DownstreamFilter struct {
	Name    string
	Project string
	Version string
}
