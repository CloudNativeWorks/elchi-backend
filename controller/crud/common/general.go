package common

import (
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

type GeneralWithID struct {
	models.General
	ID string `json:"id" bson:"_id"`
}

func TransformGenerals(records []bson.M) any {
	generals := make([]GeneralWithID, 0, len(records))

	for _, record := range records {
		bsonData, err := bson.Marshal(record["general"])
		if err != nil {
			return nil
		}

		var general models.General
		if err := bson.Unmarshal(bsonData, &general); err != nil {
			return nil
		}

		id, ok := record["_id"].(primitive.ObjectID)
		if !ok {
			continue
		}

		generalWithID := GeneralWithID{
			General: general,
			ID:      id.Hex(),
		}

		generals = append(generals, generalWithID)
	}
	return generals
}
