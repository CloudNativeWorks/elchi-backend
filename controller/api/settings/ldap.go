package settings

import (
	"context"
	"net/http"
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/pkg/ldap"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// GetLDAPConfig retrieves LDAP configuration for a project
func (handler *AppHandler) GetLDAPConfig(c *gin.Context) {
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	ldapConfig, err := handler.getLDAPConfig(project)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			c.JSON(http.StatusOK, gin.H{
				"ldap_config": nil,
				"project":     project,
				"message":     "No LDAP configuration found for this project",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to get LDAP config"})
		return
	}

	// Mask password in response for security
	maskedConfig := *ldapConfig
	if maskedConfig.BindPassword != "" {
		maskedConfig.BindPassword = strings.Repeat("*", len(maskedConfig.BindPassword))
	}

	c.JSON(http.StatusOK, gin.H{
		"ldap_config": maskedConfig,
		"project":     project,
	})
}

// SetLDAPConfig creates or updates LDAP configuration for a project
func (handler *AppHandler) SetLDAPConfig(c *gin.Context) {
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	var ldapConfig models.LDAPConfig
	if err := c.ShouldBindJSON(&ldapConfig); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request body: " + err.Error()})
		return
	}

	// Validation
	if ldapConfig.Enabled {
		if ldapConfig.Server == "" {
			c.JSON(http.StatusBadRequest, gin.H{"message": "server is required when LDAP is enabled"})
			return
		}
		if ldapConfig.BaseDN == "" {
			c.JSON(http.StatusBadRequest, gin.H{"message": "base_dn is required when LDAP is enabled"})
			return
		}
		if ldapConfig.UserFilter == "" {
			c.JSON(http.StatusBadRequest, gin.H{"message": "user_filter is required when LDAP is enabled"})
			return
		}
		if !strings.Contains(ldapConfig.UserFilter, "{username}") {
			c.JSON(http.StatusBadRequest, gin.H{"message": "user_filter must contain {username} placeholder"})
			return
		}
		if ldapConfig.Port <= 0 {
			// Set default ports
			if ldapConfig.TLSEnabled {
				ldapConfig.Port = 636
			} else {
				ldapConfig.Port = 389
			}
		}
	}

	ctx := context.Background()
	settingsCollection := handler.Context.Client.Collection("settings")

	// If bind_password is not provided (empty), preserve existing password
	if ldapConfig.BindPassword == "" {
		// Get existing config to preserve password
		existingConfig, err := handler.getLDAPConfig(project)
		if err == nil && existingConfig != nil {
			// Preserve existing password
			ldapConfig.BindPassword = existingConfig.BindPassword
		}
	}

	filter := bson.M{"project": project}
	update := bson.M{
		"$set": bson.M{
			"ldap_config": ldapConfig,
		},
	}
	opts := options.Update().SetUpsert(true)

	result, err := settingsCollection.UpdateOne(ctx, filter, update, opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to save LDAP config"})
		return
	}

	response := gin.H{
		"message": "LDAP config saved successfully",
		"project": project,
	}

	if result.UpsertedID != nil {
		response["created_new_document"] = true
	}

	c.JSON(http.StatusOK, response)
}

// UpdateLDAPConfig updates LDAP configuration for a project
func (handler *AppHandler) UpdateLDAPConfig(c *gin.Context) {
	// Same as SetLDAPConfig since MongoDB upsert handles both cases
	handler.SetLDAPConfig(c)
}

// DeleteLDAPConfig removes LDAP configuration from a project
func (handler *AppHandler) DeleteLDAPConfig(c *gin.Context) {
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	ctx := context.Background()
	settingsCollection := handler.Context.Client.Collection("settings")

	filter := bson.M{"project": project}
	update := bson.M{
		"$unset": bson.M{"ldap_config": ""},
	}

	result, err := settingsCollection.UpdateOne(ctx, filter, update)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to delete LDAP config"})
		return
	}

	if result.MatchedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{"message": "Project not found in settings"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "LDAP config deleted successfully",
		"project": project,
	})
}

// TestLDAPConfig tests LDAP connection and configuration
func (handler *AppHandler) TestLDAPConfig(c *gin.Context) {
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	ldapConfig, err := handler.getLDAPConfig(project)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "LDAP config not found for this project"})
		return
	}

	if !ldapConfig.Enabled {
		c.JSON(http.StatusBadRequest, gin.H{"message": "LDAP is disabled for this project"})
		return
	}

	// Test connection
	client, err := ldap.NewClient(ldapConfig)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "LDAP client initialization failed: " + err.Error()})
		return
	}

	if err := client.TestConnection(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "LDAP connection test failed: " + err.Error(),
			"project": project,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "LDAP connection test successful",
		"project": project,
	})
}

// TestLDAPAuth tests LDAP authentication with provided credentials
func (handler *AppHandler) TestLDAPAuth(c *gin.Context) {
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	var testCreds struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
	}

	if err := c.ShouldBindJSON(&testCreds); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "username and password are required"})
		return
	}

	ldapConfig, err := handler.getLDAPConfig(project)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "LDAP config not found for this project"})
		return
	}

	if !ldapConfig.Enabled {
		c.JSON(http.StatusBadRequest, gin.H{"message": "LDAP is disabled for this project"})
		return
	}

	// Test authentication
	client, err := ldap.NewClient(ldapConfig)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "LDAP client initialization failed: " + err.Error()})
		return
	}

	if err := client.ValidatePassword(testCreds.Username, testCreds.Password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "LDAP authentication test failed: " + err.Error(),
			"project": project,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":  "LDAP authentication test successful",
		"project":  project,
		"username": testCreds.Username,
	})
}

// Helper function to get LDAP config from database
func (handler *AppHandler) getLDAPConfig(project string) (*models.LDAPConfig, error) {
	ctx := context.Background()
	settingsCollection := handler.Context.Client.Collection("settings")

	var settings models.Settings
	err := settingsCollection.FindOne(ctx, bson.M{"project": project}).Decode(&settings)
	if err != nil {
		return nil, err
	}

	if settings.LDAPConfig == nil {
		return nil, mongo.ErrNoDocuments
	}

	return settings.LDAPConfig, nil
}
