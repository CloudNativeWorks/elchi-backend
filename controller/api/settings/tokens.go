package settings

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func (handler *AppHandler) GetTokens(c *gin.Context) {
	ctx := context.Background()
	var settingsCollection *mongo.Collection = handler.Context.Client.Collection("settings")

	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	filter := bson.M{"project": project}

	cursor, err := settingsCollection.Find(ctx, filter)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "could not find records"})
		return
	}

	var records []bson.M
	if err = cursor.All(ctx, &records); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "could not decode records"})
		return
	}

	for _, record := range records {
		if tokens, ok := record["tokens"].(primitive.A); ok {
			for _, tokenInterface := range tokens {
				if token, ok := tokenInterface.(bson.M); ok {
					if tokenValue, ok := token["token"].(string); ok {
						dashIndex := strings.Index(tokenValue, "-")
						if dashIndex > 0 {
							visible := tokenValue[:dashIndex]
							masked := strings.Repeat("*", len(tokenValue)-dashIndex)
							token["token"] = visible + masked
						} else {
							if len(tokenValue) > 4 {
								visible := tokenValue[:4]
								masked := strings.Repeat("*", len(tokenValue)-4)
								token["token"] = visible + masked
							}
						}
					}
				} else if token, ok := tokenInterface.(primitive.M); ok {
					if tokenValue, ok := token["token"].(string); ok {
						dashIndex := strings.Index(tokenValue, "-")
						if dashIndex > 0 {
							visible := tokenValue[:dashIndex]
							masked := strings.Repeat("*", len(tokenValue)-dashIndex)
							token["token"] = visible + masked
						} else {
							if len(tokenValue) > 4 {
								visible := tokenValue[:4]
								masked := strings.Repeat("*", len(tokenValue)-4)
								token["token"] = visible + masked
							}
						}
					}
				}
			}
		}
	}

	c.JSON(http.StatusOK, records)
}

func (handler *AppHandler) SetToken(c *gin.Context) {
	ctx := context.Background()
	var settingsCollection *mongo.Collection = handler.Context.Client.Collection("settings")

	project := c.Query("project")
	name := c.Query("name")

	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "name parameter is required"})
		return
	}

	filter := bson.M{
		"project":     project,
		"tokens.name": name,
	}

	count, err := settingsCollection.CountDocuments(ctx, filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "could not check existing tokens"})
		return
	}

	if count > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "token with this name already exists in this project"})
		return
	}

	newToken := bson.M{
		"name":  name,
		"id":    uuid.New().String(),
		"token": uuid.New().String(),
	}

	projectFilter := bson.M{"project": project}
	update := bson.M{
		"$push": bson.M{"tokens": newToken},
	}

	opts := options.Update().SetUpsert(true)
	result, err := settingsCollection.UpdateOne(ctx, projectFilter, update, opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "could not create token"})
		return
	}

	response := gin.H{
		"message": "token created successfully",
		"token":   newToken,
	}

	if result.UpsertedID != nil {
		response["created_new_document"] = true
	}

	c.JSON(http.StatusOK, response)
}

func (handler *AppHandler) DeleteToken(c *gin.Context) {
	ctx := context.Background()
	var settingsCollection *mongo.Collection = handler.Context.Client.Collection("settings")

	tokenID := c.Param("token_id")
	project := c.Query("project")

	if tokenID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "token_id parameter is required"})
		return
	}

	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	filter := bson.M{
		"project":   project,
		"tokens.id": tokenID,
	}

	update := bson.M{
		"$pull": bson.M{
			"tokens": bson.M{"id": tokenID},
		},
	}

	result, err := settingsCollection.UpdateOne(ctx, filter, update)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "could not delete token"})
		return
	}

	if result.ModifiedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{"message": "token not found in this project"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":          "token deleted successfully",
		"deleted_token_id": tokenID,
		"project":          project,
	})
}
