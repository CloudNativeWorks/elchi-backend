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

// GetClaudeToken project için Claude API token'ını getirir
func (handler *AppHandler) GetClaudeToken(c *gin.Context) {
	ctx := context.Background()
	var settingsCollection *mongo.Collection = handler.Context.Client.Collection("settings")

	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	filter := bson.M{"project": project}
	
	var settings bson.M
	err := settingsCollection.FindOne(ctx, filter).Decode(&settings)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			c.JSON(http.StatusOK, gin.H{"claude_token": "", "message": "no Claude token set for this project"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"message": "could not get Claude token"})
		return
	}

	claudeToken := ""
	if token, ok := settings["claude_token"].(string); ok {
		// Token'ın sadece ilk 8 karakterini göster, geri kalanını maskele
		if len(token) > 8 {
			visible := token[:8]
			masked := strings.Repeat("*", len(token)-8)
			claudeToken = visible + masked
		} else {
			claudeToken = token
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"claude_token": claudeToken,
		"project":     project,
	})
}

// SetClaudeToken project için Claude API token'ını set eder
func (handler *AppHandler) SetClaudeToken(c *gin.Context) {
	ctx := context.Background()
	var settingsCollection *mongo.Collection = handler.Context.Client.Collection("settings")

	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	var requestBody struct {
		ClaudeToken string `json:"claude_token" binding:"required"`
	}

	if err := c.ShouldBindJSON(&requestBody); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "claude_token is required", "error": err.Error()})
		return
	}

	// Token validation - Claude API key format kontrolü
	if !strings.HasPrefix(requestBody.ClaudeToken, "sk-ant-") {
		c.JSON(http.StatusBadRequest, gin.H{"message": "invalid Claude API key format. Must start with 'sk-ant-'"})
		return
	}

	projectFilter := bson.M{"project": project}
	update := bson.M{
		"$set": bson.M{"claude_token": requestBody.ClaudeToken},
	}

	opts := options.Update().SetUpsert(true)
	result, err := settingsCollection.UpdateOne(ctx, projectFilter, update, opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "could not set Claude token"})
		return
	}

	response := gin.H{
		"message": "Claude token set successfully",
		"project": project,
	}

	if result.UpsertedID != nil {
		response["created_new_document"] = true
	}

	c.JSON(http.StatusOK, response)
}

// UpdateClaudeToken project için Claude API token'ını günceller
func (handler *AppHandler) UpdateClaudeToken(c *gin.Context) {
	ctx := context.Background()
	var settingsCollection *mongo.Collection = handler.Context.Client.Collection("settings")

	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	var requestBody struct {
		ClaudeToken string `json:"claude_token" binding:"required"`
	}

	if err := c.ShouldBindJSON(&requestBody); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "claude_token is required", "error": err.Error()})
		return
	}

	// Token validation
	if !strings.HasPrefix(requestBody.ClaudeToken, "sk-ant-") {
		c.JSON(http.StatusBadRequest, gin.H{"message": "invalid Claude API key format. Must start with 'sk-ant-'"})
		return
	}

	filter := bson.M{"project": project}
	update := bson.M{
		"$set": bson.M{"claude_token": requestBody.ClaudeToken},
	}

	result, err := settingsCollection.UpdateOne(ctx, filter, update)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "could not update Claude token"})
		return
	}

	if result.MatchedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{"message": "project not found in settings"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Claude token updated successfully",
		"project": project,
	})
}

// DeleteClaudeToken project için Claude API token'ını siler
func (handler *AppHandler) DeleteClaudeToken(c *gin.Context) {
	ctx := context.Background()
	var settingsCollection *mongo.Collection = handler.Context.Client.Collection("settings")

	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	filter := bson.M{"project": project}
	update := bson.M{
		"$unset": bson.M{"claude_token": ""},
	}

	result, err := settingsCollection.UpdateOne(ctx, filter, update)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "could not delete Claude token"})
		return
	}

	if result.MatchedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{"message": "project not found in settings"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Claude token deleted successfully",
		"project": project,
	})
}
