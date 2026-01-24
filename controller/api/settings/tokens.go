package settings

import (
	"context"
	"errors"
	"fmt"
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
	settingsCollection := handler.Context.Client.Collection("settings")

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
						} else if len(tokenValue) > 4 {
							visible := tokenValue[:4]
							masked := strings.Repeat("*", len(tokenValue)-4)
							token["token"] = visible + masked
						}
					}
				} else if token, ok := tokenInterface.(primitive.M); ok {
					if tokenValue, ok := token["token"].(string); ok {
						dashIndex := strings.Index(tokenValue, "-")
						if dashIndex > 0 {
							visible := tokenValue[:dashIndex]
							masked := strings.Repeat("*", len(tokenValue)-dashIndex)
							token["token"] = visible + masked
						} else if len(tokenValue) > 4 {
							visible := tokenValue[:4]
							masked := strings.Repeat("*", len(tokenValue)-4)
							token["token"] = visible + masked
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
	settingsCollection := handler.Context.Client.Collection("settings")

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

	// Generate client token with embedded project ID
	baseToken := uuid.New().String()
	tokenWithProject := fmt.Sprintf("%s--%s", baseToken, project)

	newToken := bson.M{
		"name":  name,
		"id":    uuid.New().String(),
		"token": tokenWithProject,
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
	settingsCollection := handler.Context.Client.Collection("settings")

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

func (handler *AppHandler) GetOpenRouterToken(c *gin.Context) {
	ctx := context.Background()
	settingsCollection := handler.Context.Client.Collection("settings")

	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	filter := bson.M{"project": project}

	var settings bson.M
	err := settingsCollection.FindOne(ctx, filter).Decode(&settings)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusOK, gin.H{"openrouter_token": "", "ai_default_model": "", "message": "no OpenRouter token set for this project"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"message": "could not get OpenRouter token"})
		return
	}

	openrouterToken := ""
	if token, ok := settings["openrouter_token"].(string); ok {
		if len(token) > 8 {
			visible := token[:8]
			masked := strings.Repeat("*", len(token)-8)
			openrouterToken = visible + masked
		} else {
			openrouterToken = token
		}
	}

	aiDefaultModel := ""
	if model, ok := settings["ai_default_model"].(string); ok {
		aiDefaultModel = model
	}

	c.JSON(http.StatusOK, gin.H{
		"openrouter_token": openrouterToken,
		"ai_default_model": aiDefaultModel,
		"project":          project,
	})
}

func (handler *AppHandler) SetOpenRouterToken(c *gin.Context) {
	ctx := context.Background()
	settingsCollection := handler.Context.Client.Collection("settings")

	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	var requestBody struct {
		OpenRouterToken string `json:"openrouter_token" binding:"required"`
		AIDefaultModel  string `json:"ai_default_model,omitempty"`
	}

	if err := c.ShouldBindJSON(&requestBody); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "openrouter_token is required", "error": err.Error()})
		return
	}

	// Token validation
	if !strings.HasPrefix(requestBody.OpenRouterToken, "sk-or-") {
		c.JSON(http.StatusBadRequest, gin.H{"message": "invalid OpenRouter API key format. Must start with 'sk-or-'"})
		return
	}

	projectFilter := bson.M{"project": project}
	updateFields := bson.M{"openrouter_token": requestBody.OpenRouterToken}

	if requestBody.AIDefaultModel != "" {
		updateFields["ai_default_model"] = requestBody.AIDefaultModel
	}

	update := bson.M{
		"$set": updateFields,
	}

	opts := options.Update().SetUpsert(true)
	result, err := settingsCollection.UpdateOne(ctx, projectFilter, update, opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "could not set OpenRouter token"})
		return
	}

	response := gin.H{
		"message": "OpenRouter token set successfully",
		"project": project,
	}

	if result.UpsertedID != nil {
		response["created_new_document"] = true
	}

	c.JSON(http.StatusOK, response)
}

func (handler *AppHandler) UpdateOpenRouterToken(c *gin.Context) {
	ctx := context.Background()
	settingsCollection := handler.Context.Client.Collection("settings")

	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	var requestBody struct {
		OpenRouterToken string `json:"openrouter_token,omitempty"`
		AIDefaultModel  string `json:"ai_default_model,omitempty"`
	}

	if err := c.ShouldBindJSON(&requestBody); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "request body error", "error": err.Error()})
		return
	}

	if requestBody.OpenRouterToken == "" && requestBody.AIDefaultModel == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "at least one of openrouter_token or ai_default_model is required"})
		return
	}

	if requestBody.OpenRouterToken != "" && !strings.HasPrefix(requestBody.OpenRouterToken, "sk-or-") {
		c.JSON(http.StatusBadRequest, gin.H{"message": "invalid OpenRouter API key format. Must start with 'sk-or-'"})
		return
	}

	filter := bson.M{"project": project}
	updateFields := bson.M{}

	if requestBody.OpenRouterToken != "" {
		updateFields["openrouter_token"] = requestBody.OpenRouterToken
	}
	if requestBody.AIDefaultModel != "" {
		updateFields["ai_default_model"] = requestBody.AIDefaultModel
	}

	update := bson.M{
		"$set": updateFields,
	}

	result, err := settingsCollection.UpdateOne(ctx, filter, update)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "could not update OpenRouter settings"})
		return
	}

	if result.MatchedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{"message": "project not found in settings"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "OpenRouter settings updated successfully",
		"project": project,
	})
}

func (handler *AppHandler) DeleteOpenRouterToken(c *gin.Context) {
	ctx := context.Background()
	settingsCollection := handler.Context.Client.Collection("settings")

	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	filter := bson.M{"project": project}
	update := bson.M{
		"$unset": bson.M{
			"openrouter_token": "",
			"ai_default_model": "",
		},
	}

	result, err := settingsCollection.UpdateOne(ctx, filter, update)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "could not delete OpenRouter token"})
		return
	}

	if result.MatchedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{"message": "project not found in settings"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "OpenRouter settings deleted successfully",
		"project": project,
	})
}

// GetDiscoveryToken retrieves the discovery token for a project
func (handler *AppHandler) GetDiscoveryToken(c *gin.Context) {
	ctx := context.Background()
	settingsCollection := handler.Context.Client.Collection("settings")

	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	filter := bson.M{"project": project}

	var settings bson.M
	err := settingsCollection.FindOne(ctx, filter).Decode(&settings)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusOK, gin.H{"discovery_token": "", "message": "no discovery token set for this project"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"message": "could not get discovery token"})
		return
	}

	discoveryToken := ""
	if token, ok := settings["discovery_token"].(string); ok {
		// Return full token without masking so agents can parse project information
		discoveryToken = token
	}

	c.JSON(http.StatusOK, gin.H{
		"discovery_token": discoveryToken,
		"project":         project,
	})
}

// DeleteDiscoveryToken deletes the discovery token for a project
func (handler *AppHandler) DeleteDiscoveryToken(c *gin.Context) {
	ctx := context.Background()
	settingsCollection := handler.Context.Client.Collection("settings")

	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	filter := bson.M{"project": project}
	update := bson.M{
		"$unset": bson.M{"discovery_token": ""},
	}

	result, err := settingsCollection.UpdateOne(ctx, filter, update)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "could not delete discovery token"})
		return
	}

	if result.MatchedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{"message": "project not found in settings"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Discovery token deleted successfully",
		"project": project,
	})
}

// GenerateDiscoveryToken generates a new UUID-based discovery token for a project
func (handler *AppHandler) GenerateDiscoveryToken(c *gin.Context) {
	ctx := context.Background()
	settingsCollection := handler.Context.Client.Collection("settings")

	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	// Generate new UUID-based token with embedded project ID
	baseToken := uuid.New().String()
	newToken := fmt.Sprintf("%s--%s", baseToken, project)

	projectFilter := bson.M{"project": project}
	update := bson.M{
		"$set": bson.M{"discovery_token": newToken},
	}

	opts := options.Update().SetUpsert(true)
	result, err := settingsCollection.UpdateOne(ctx, projectFilter, update, opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "could not generate discovery token"})
		return
	}

	response := gin.H{
		"message":         "Discovery token generated successfully",
		"project":         project,
		"discovery_token": newToken,
	}

	if result.UpsertedID != nil {
		response["created_new_document"] = true
	}

	c.JSON(http.StatusOK, response)
}
