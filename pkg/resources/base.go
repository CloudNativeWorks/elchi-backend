package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/common"
	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/errstr"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

type GeneralResponse struct {
	General models.General `bson:"general"`
}

func GetResourceNGeneral(ctx context.Context, db *db.AppContext, collectionName, name, project, version string) (*models.DBResource, error) {
	var doc models.DBResource

	collection := db.Client.Collection(collectionName)
	findOptions := options.FindOne()
	findOptions.SetProjection(bson.D{{Key: "resource", Value: 1}, {Key: "_id", Value: 1}, {Key: "general", Value: 1}})

	filter := bson.D{{Key: "general.name", Value: name}, {Key: "general.project", Value: project}, {Key: "general.version", Value: version}}

	err := collection.FindOne(ctx, filter, findOptions).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("resource not found - type: %s, name: %s, project: %s, version: %s",
				collectionName,
				name,
				project,
				version,
			)
		}
		return nil, errstr.ErrUnknownDBError
	}

	return &doc, nil
}

func IncrementResourceVersion(ctx context.Context, db *db.AppContext, name, project, version string) (string, error) {
	collection := db.Client.Collection("listeners")

	var doc models.DBResource
	filter := bson.D{{Key: "general.name", Value: name}, {Key: "general.project", Value: project}, {Key: "general.version", Value: version}}
	findOptions := options.FindOne()
	findOptions.SetProjection(bson.D{{Key: "resource.version", Value: 1}})

	err := collection.FindOne(ctx, filter, findOptions).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return "", errors.New("not found: (" + name + ")")
		}
		return "", errstr.ErrUnknownDBError
	}

	versionInt, err := strconv.Atoi(doc.Resource.Version)
	if err != nil {
		return "", errstr.ErrInvalidVersion
	}

	versionInt++
	newVersion := strconv.Itoa(versionInt)
	update := bson.D{{Key: "$set", Value: bson.D{{Key: "resource.version", Value: newVersion}}}}

	_, err = collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return "", errstr.ErrFailedToUpdateVersion
	}

	return newVersion, nil
}

func GetGenerals(ctx context.Context, context *db.AppContext, collectionName string, filter primitive.D) ([]*models.General, error) {
	collection := context.Client.Collection(collectionName)

	pipeline := mongo.Pipeline{
		bson.D{{Key: "$match", Value: filter}},
		bson.D{{Key: "$project", Value: bson.D{{Key: "general", Value: 1}, {Key: "_id", Value: 0}}}},
	}

	cursor, err := collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var results []*models.General
	for cursor.Next(ctx) {
		var resp GeneralResponse
		if err = cursor.Decode(&resp); err != nil {
			context.Logger.Debug(err)
			return nil, err
		}
		results = append(results, &resp.General)
	}

	if err = cursor.Err(); err != nil {
		return nil, err
	}

	return results, nil
}

func ValidateResourceWithClient(ctx context.Context, resourceGType models.GTypes, version string, nodeid string, resourceData any, resourceService *bridge.ResourceServiceClient) error {
	if resourceService == nil {
		return nil
	}

	var isArray bool
	switch resourceData.(type) {
	case []any:
		isArray = true
	case []map[string]any:
		isArray = true
	default:
		rt := reflect.TypeOf(resourceData)
		if rt != nil && rt.Kind() == reflect.Slice {
			isArray = true
		}
	}

	jsonBytes, err := json.Marshal(resourceData)
	if err != nil {
		return errors.New("resource to json error: " + err.Error())
	}

	if len(jsonBytes) > 0 && jsonBytes[0] == '[' {
		isArray = true
	}

	var anyValue *anypb.Any
	if isArray {
		var resourceSlice []any
		if err := json.Unmarshal(jsonBytes, &resourceSlice); err != nil {
			return errors.New("json to array error: " + err.Error())
		}

		listValue, err := structpb.NewList(resourceSlice)
		if err != nil {
			return errors.New("array to structpb.List error: " + err.Error())
		}

		anyValue, err = anypb.New(listValue)
		if err != nil {
			return errors.New("list to anypb error: " + err.Error())
		}
	} else {
		var resourceMap map[string]any
		if err := json.Unmarshal(jsonBytes, &resourceMap); err != nil {
			return errors.New("json to map error: " + err.Error())
		}

		structValue, err := structpb.NewStruct(resourceMap)
		if err != nil {
			return errors.New("map to structpb error: " + err.Error())
		}

		anyValue, err = anypb.New(structValue)
		if err != nil {
			return errors.New("struct to anypb error: " + err.Error())
		}
	}

	md := metadata.Pairs("envoy-version", version, "nodeid", nodeid)
	ctxOut := metadata.NewOutgoingContext(ctx, md)

	timeoutCtx, cancel := context.WithTimeout(ctxOut, 7*time.Second)
	defer cancel()

	validateResp, err := (*resourceService).ValidateResource(timeoutCtx, &bridge.ValidateResourceRequest{
		Gtype:    string(resourceGType),
		Resource: anyValue,
	})

	if err != nil {
		return errors.New("gRPC error: " + err.Error())
	}

	if validateResp.Error != "" {
		return errors.New("Validation error: " + validateResp.Error)
	}

	return nil
}

func PrepareResource(resource models.ResourceClass, requestDetails models.RequestDetails, logger *logrus.Logger, resourceService *bridge.ResourceServiceClient) error {
	general := resource.GetGeneral()
	now := time.Now()
	general.CreatedAt = primitive.NewDateTimeFromTime(now)
	general.UpdatedAt = primitive.NewDateTimeFromTime(now)
	resource.SetGeneral(&general)
	nodeid := fmt.Sprintf("%s::%s", requestDetails.Name, requestDetails.Project)

	if err := ValidateResourceWithClient(context.Background(), resource.GetGeneral().GType, resource.GetGeneral().Version, nodeid, resource.GetResource(), resourceService); err != nil {
		return err
	}

	resource.SetTypedConfig(DecodeSetTypedConfigs(resource, logger))
	common.DetectSetPermissions(resource, requestDetails)

	return nil
}

func GetDBResource(db *mongo.Database, collectionName string, filter bson.M) (*models.DBResource, error) {
	var resource models.DBResource
	err := db.Collection(collectionName).FindOne(context.Background(), filter).Decode(&resource)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, fmt.Errorf("resource not found")
		}
		return nil, fmt.Errorf("error fetching resource: %v", err)
	}
	return &resource, nil
}
