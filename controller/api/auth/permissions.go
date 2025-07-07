package auth

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

var generalName = "general.name"
var versionNum = "general.version"

func (handler *AppHandler) GetPermissions(c *gin.Context) {
	ctx := c.Request.Context()
	project := c.Query("project")
	userOrGroup := c.Param("kind")
	filter := bson.M{"general.project": project}

	if userOrGroup == "users" {
		filter["general.permissions.users"] = c.Param("id")
	} else {
		filter["general.permissions.groups"] = c.Param("id")
	}

	all, err := handler.GetData(ctx, bson.M{"general.project": project}, c.Param("type"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
		return
	}

	selected, err := handler.GetData(ctx, filter, c.Param("type"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
		return
	}

	records := map[string]any{"all": all, "selected": selected}
	c.JSON(http.StatusOK, records)
}

func (handler *AppHandler) SetPermission(permissions models.Permission, userOrGroupID, kind string) {
	updatePermissions := func(collection *mongo.Collection, filter, update bson.M, action, name string) {
		_, err := collection.UpdateMany(context.TODO(), filter, update)
		if err != nil {
			handler.Logger.Warnf("failed to %s user/group for %s: %v", action, name, err)
		}
	}

	fields := map[string]*models.InnerPermission{
		"listeners":  permissions.Listeners,
		"routes":     permissions.Routes,
		"clusters":   permissions.Clusters,
		"endpoints":  permissions.Endpoints,
		"secrets":    permissions.Secrets,
		"extensions": permissions.Extensions,
		"filters":    permissions.Filters,
		"bootstrap":  permissions.Bootstrap,
	}

	for name, p := range fields {
		if p == nil || (len(p.Added) == 0 && len(p.Removed) == 0) {
			continue
		}

		collection := handler.Context.Client.Collection(name)
		for _, addedID := range p.Added {
			objID, err := primitive.ObjectIDFromHex(addedID)
			if err != nil {
				handler.Logger.Warnf("invalid object id: %v", err)
				continue
			}
			filter := bson.M{"_id": objID}
			update := bson.M{
				"$addToSet": bson.M{"general.permissions." + kind: userOrGroupID},
			}
			updatePermissions(collection, filter, update, "add", name)
		}

		for _, removedID := range p.Removed {
			objID, err := primitive.ObjectIDFromHex(removedID)
			if err != nil {
				handler.Logger.Warnf("invalid object id: %v", err)
				continue
			}
			filter := bson.M{"_id": objID}
			update := bson.M{
				"$pull": bson.M{"general.permissions." + kind: userOrGroupID},
			}
			updatePermissions(collection, filter, update, "remove", name)
		}
	}
}

func (handler *AppHandler) GetData(ctx context.Context, filter bson.M, typ string) ([]bson.M, error) {
	var resourceCollection *mongo.Collection = handler.Context.Client.Collection(typ)
	opts := options.Find().SetProjection(bson.M{generalName: 1, versionNum: 1})

	cursor, err := resourceCollection.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}

	var records []bson.M
	if err = cursor.All(ctx, &records); err != nil {
		return nil, err
	}

	return records, err
}
