// Package settings provides API handlers for system configuration management
// including users, groups, projects, tokens, and cloud integrations.
package settings

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// GetClouds retrieves all cloud configurations for a project
func (handler *AppHandler) GetClouds(c *gin.Context) {
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
			c.JSON(http.StatusOK, gin.H{"clouds": map[string]any{}, "message": "no clouds configured for this project"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"message": "could not get clouds"})
		return
	}

	clouds := map[string]any{}
	if cloudsData, ok := settings["clouds"].(bson.M); ok {
		// Mask sensitive credentials before sending response
		for cloudName, cloudData := range cloudsData {
			if cloud, ok := cloudData.(bson.M); ok {
				maskedCloud := maskCloudCredentials(cloud)
				clouds[cloudName] = maskedCloud
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"clouds":  clouds,
		"project": project,
	})
}

// GetCloud retrieves a specific cloud configuration
func (handler *AppHandler) GetCloud(c *gin.Context) {
	ctx := context.Background()
	settingsCollection := handler.Context.Client.Collection("settings")

	cloudName := c.Param("cloud_name")
	project := c.Query("project")

	if cloudName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "cloud_name parameter is required"})
		return
	}

	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	filter := bson.M{"project": project}

	var settings bson.M
	err := settingsCollection.FindOne(ctx, filter).Decode(&settings)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{"message": "project not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"message": "could not get cloud configuration"})
		return
	}

	if clouds, ok := settings["clouds"].(bson.M); ok {
		if cloud, exists := clouds[cloudName]; exists {
			if cloudData, ok := cloud.(bson.M); ok {
				maskedCloud := maskCloudCredentials(cloudData)
				c.JSON(http.StatusOK, gin.H{
					"cloud":      maskedCloud,
					"cloud_name": cloudName,
					"project":    project,
				})
				return
			}
		}
	}

	c.JSON(http.StatusNotFound, gin.H{"message": "cloud configuration not found"})
}

// SetCloud creates or updates a cloud configuration
func (handler *AppHandler) SetCloud(c *gin.Context) {
	ctx := context.Background()
	settingsCollection := handler.Context.Client.Collection("settings")

	cloudName := c.Param("cloud_name")
	project := c.Query("project")

	if cloudName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "cloud_name parameter is required"})
		return
	}

	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	// Validate cloud name - prevent reserved names
	reservedNames := []string{"other", "default", "system", "admin", "root"}
	for _, reserved := range reservedNames {
		if strings.ToLower(cloudName) == reserved {
			c.JSON(http.StatusBadRequest, gin.H{"message": "cloud name '" + cloudName + "' is reserved and cannot be used"})
			return
		}
	}

	var cloudConfig models.CloudConfig
	if err := c.ShouldBindJSON(&cloudConfig); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "invalid cloud configuration", "error": err.Error()})
		return
	}

	// Validate provider
	validProviders := []string{"openstack", "aws", "azure", "gcp"}
	isValidProvider := false
	for _, valid := range validProviders {
		if cloudConfig.Provider == valid {
			isValidProvider = true
			break
		}
	}

	if !isValidProvider {
		c.JSON(http.StatusBadRequest, gin.H{"message": "invalid provider. Must be one of: openstack, aws, azure, gcp"})
		return
	}

	// Check if cloud configuration already exists
	existingFilter := bson.M{"project": project}
	var existingSettings bson.M
	err := settingsCollection.FindOne(ctx, existingFilter).Decode(&existingSettings)
	if err == nil {
		if clouds, ok := existingSettings["clouds"].(bson.M); ok {
			if _, exists := clouds[cloudName]; exists {
				c.JSON(http.StatusConflict, gin.H{
					"message":    "cloud configuration with this name already exists. Use PUT to update existing configuration",
					"cloud_name": cloudName,
					"project":    project,
				})
				return
			}
		}
	} else if !errors.Is(err, mongo.ErrNoDocuments) {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "could not check existing cloud configurations"})
		return
	}

	// Provider-specific validation
	switch cloudConfig.Provider {
	case "openstack":
		// OpenStack required fields
		if cloudConfig.Auth.AuthURL == "" {
			c.JSON(http.StatusBadRequest, gin.H{"message": "auth_url is required for OpenStack provider"})
			return
		}
		if cloudConfig.Auth.ApplicationCredentialID == "" || cloudConfig.Auth.ApplicationCredentialSecret == "" {
			c.JSON(http.StatusBadRequest, gin.H{"message": "application credentials are required for OpenStack provider"})
			return
		}

		// Set OpenStack defaults
		if cloudConfig.AuthType == "" {
			cloudConfig.AuthType = "v3applicationcredential"
		}
		if cloudConfig.Interface == "" {
			cloudConfig.Interface = "public"
		}
		if cloudConfig.IdentityAPIVersion == 0 {
			cloudConfig.IdentityAPIVersion = 3
		}

	case "aws":
		// Future: AWS validation
		c.JSON(http.StatusBadRequest, gin.H{"message": "AWS provider is not yet implemented"})
		return

	case "azure":
		// Future: Azure validation
		c.JSON(http.StatusBadRequest, gin.H{"message": "Azure provider is not yet implemented"})
		return

	case "gcp":
		// Future: GCP validation
		c.JSON(http.StatusBadRequest, gin.H{"message": "GCP provider is not yet implemented"})
		return
	}

	projectFilter := bson.M{"project": project}
	cloudPath := "clouds." + cloudName

	update := bson.M{
		"$set": bson.M{
			cloudPath: cloudConfig,
		},
	}

	opts := options.Update().SetUpsert(true)
	result, err := settingsCollection.UpdateOne(ctx, projectFilter, update, opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "could not save cloud configuration"})
		return
	}

	response := gin.H{
		"message":    "cloud configuration saved successfully",
		"cloud_name": cloudName,
		"project":    project,
	}

	if result.UpsertedID != nil {
		response["created_new_document"] = true
	}

	c.JSON(http.StatusOK, response)
}

// UpdateCloud updates specific fields of a cloud configuration
func (handler *AppHandler) UpdateCloud(c *gin.Context) {
	ctx := context.Background()
	settingsCollection := handler.Context.Client.Collection("settings")

	cloudName := c.Param("cloud_name")
	project := c.Query("project")

	if cloudName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "cloud_name parameter is required"})
		return
	}

	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	// Validate cloud name - prevent reserved names
	reservedNames := []string{"other", "default", "system", "admin", "root"}
	for _, reserved := range reservedNames {
		if strings.ToLower(cloudName) == reserved {
			c.JSON(http.StatusBadRequest, gin.H{"message": "cloud name '" + cloudName + "' is reserved and cannot be used"})
			return
		}
	}

	var partialUpdate map[string]interface{}
	if err := c.ShouldBindJSON(&partialUpdate); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "invalid request body", "error": err.Error()})
		return
	}

	// Get existing cloud configuration to preserve unmodified fields
	filter := bson.M{"project": project}
	var existingSettings bson.M
	err := settingsCollection.FindOne(ctx, filter).Decode(&existingSettings)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{"message": "project not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"message": "could not get existing configuration"})
		return
	}

	// Check if cloud exists
	clouds, ok := existingSettings["clouds"].(bson.M)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"message": "no cloud configurations found"})
		return
	}

	_, exists := clouds[cloudName]
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"message": "cloud configuration not found"})
		return
	}

	// Build update fields with proper paths, preserving masked credentials
	updateFields := bson.M{}
	for key, value := range partialUpdate {
		switch key {
		case "auth":
			if auth, ok := value.(map[string]interface{}); ok {
				for authKey, authValue := range auth {
					// Skip masked credentials - don't update them
					if authStr, ok := authValue.(string); ok {
						switch authKey {
						case "application_credential_id":
							if isMaskedCredential(authStr) {
								continue // Skip masked credential
							}
						case "application_credential_secret":
							if isMaskedCredential(authStr) {
								continue // Skip masked credential
							}
						}
					}
					authPath := "clouds." + cloudName + ".auth." + authKey
					updateFields[authPath] = authValue
				}
			}
		case "provider", "region_name", "interface", "identity_api_version", "auth_type":
			fieldPath := "clouds." + cloudName + "." + key
			updateFields[fieldPath] = value
		default:
			// Skip unknown fields
			continue
		}
	}

	if len(updateFields) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "no valid fields to update (masked credentials ignored)"})
		return
	}

	update := bson.M{"$set": updateFields}

	result, err := settingsCollection.UpdateOne(ctx, filter, update)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "could not update cloud configuration"})
		return
	}

	if result.MatchedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{"message": "project not found in settings"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":    "cloud configuration updated successfully",
		"cloud_name": cloudName,
		"project":    project,
	})
}

// Helper function to detect masked credentials
func isMaskedCredential(value string) bool {
	// Check if the string contains asterisks (masked)
	return strings.Contains(value, "*")
}

// DeleteCloud deletes a cloud configuration
func (handler *AppHandler) DeleteCloud(c *gin.Context) {
	ctx := context.Background()
	settingsCollection := handler.Context.Client.Collection("settings")

	cloudName := c.Param("cloud_name")
	project := c.Query("project")

	if cloudName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "cloud_name parameter is required"})
		return
	}

	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	filter := bson.M{"project": project}
	cloudPath := "clouds." + cloudName
	update := bson.M{
		"$unset": bson.M{
			cloudPath: "",
		},
	}

	result, err := settingsCollection.UpdateOne(ctx, filter, update)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "could not delete cloud configuration"})
		return
	}

	if result.MatchedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{"message": "project not found in settings"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":    "cloud configuration deleted successfully",
		"cloud_name": cloudName,
		"project":    project,
	})
}

// Helper function to mask sensitive cloud credentials
func maskCloudCredentials(cloud bson.M) bson.M {
	maskedCloud := bson.M{}

	for key, value := range cloud {
		if key == "auth" {
			if auth, ok := value.(bson.M); ok {
				maskedAuth := bson.M{}
				for authKey, authValue := range auth {
					if authStr, ok := authValue.(string); ok {
						switch authKey {
						case "application_credential_id":
							if len(authStr) > 8 {
								maskedAuth[authKey] = authStr[:8] + strings.Repeat("*", len(authStr)-8)
							} else {
								maskedAuth[authKey] = authStr
							}
						case "application_credential_secret":
							if len(authStr) > 4 {
								maskedAuth[authKey] = authStr[:4] + strings.Repeat("*", len(authStr)-4)
							} else {
								maskedAuth[authKey] = strings.Repeat("*", len(authStr))
							}
						default:
							maskedAuth[authKey] = authValue
						}
					} else {
						maskedAuth[authKey] = authValue
					}
				}
				maskedCloud[key] = maskedAuth
			} else {
				maskedCloud[key] = value
			}
		} else {
			maskedCloud[key] = value
		}
	}

	return maskedCloud
}
