package settings

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// DNS zone validation regex
var dnsZoneRegex = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*$`)

// GetGSLBConfig retrieves GSLB configuration for a project
func (handler *AppHandler) GetGSLBConfig(c *gin.Context) {
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	gslbConfig, err := handler.getGSLBConfig(project)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusOK, gin.H{
				"gslb_config": nil,
				"project":     project,
				"message":     "No GSLB configuration found for this project",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to get GSLB config"})
		return
	}

	// Mask DNS secret for security (show only first 4 characters)
	maskedConfig := *gslbConfig
	if len(maskedConfig.DNSSecret) > 4 {
		maskedConfig.DNSSecret = maskedConfig.DNSSecret[:4] + "****"
	} else if maskedConfig.DNSSecret != "" {
		maskedConfig.DNSSecret = "****"
	}

	c.JSON(http.StatusOK, gin.H{
		"gslb_config": maskedConfig,
		"project":     project,
	})
}

// SetGSLBConfig creates or updates GSLB configuration
func (handler *AppHandler) SetGSLBConfig(c *gin.Context) {
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	var newConfig models.GSLBConfig
	if err := c.ShouldBindJSON(&newConfig); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request body: " + err.Error()})
		return
	}

	ctx := context.Background()
	settingsCollection := handler.Context.Client.Collection("settings")

	// Get existing config to check zone immutability and preserve secret
	existingConfig, err := handler.getGSLBConfig(project)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		handler.Logger.Errorf("Failed to get existing GSLB config: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to retrieve existing configuration"})
		return
	}

	// ZONE IMMUTABILITY CHECK
	if existingConfig != nil && existingConfig.Zone != "" && existingConfig.Zone != newConfig.Zone {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": fmt.Sprintf("Zone cannot be changed after creation. Current zone: %s", existingConfig.Zone),
		})
		return
	}

	// If DNSSecret is not provided (empty), preserve existing secret
	// IMPORTANT: Do this BEFORE validation so validation doesn't fail
	if newConfig.DNSSecret == "" {
		if existingConfig != nil {
			// Preserve existing secret
			newConfig.DNSSecret = existingConfig.DNSSecret
		}
	}

	// Validate configuration (after preserving secret)
	if err := validateGSLBConfig(&newConfig); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	// Upsert GSLB config
	filter := bson.M{"project": project}
	update := bson.M{
		"$set": bson.M{
			"gslb_config": newConfig,
		},
	}
	opts := options.Update().SetUpsert(true)

	result, err := settingsCollection.UpdateOne(ctx, filter, update, opts)
	if err != nil {
		// Check for duplicate zone error (unique index violation)
		if mongo.IsDuplicateKeyError(err) {
			c.JSON(http.StatusConflict, gin.H{
				"message": fmt.Sprintf("Zone '%s' is already in use by another project", newConfig.Zone),
			})
			return
		}
		handler.Logger.Errorf("Failed to set GSLB config: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to save GSLB configuration"})
		return
	}

	response := gin.H{
		"message": "GSLB config saved successfully",
		"project": project,
	}

	if result.UpsertedID != nil {
		response["message"] = "GSLB config created successfully"
	} else {
		response["message"] = "GSLB config updated successfully"
	}

	c.JSON(http.StatusOK, response)
}

// UpdateGSLBConfig is an alias for SetGSLBConfig (follows RESTful pattern)
func (handler *AppHandler) UpdateGSLBConfig(c *gin.Context) {
	handler.SetGSLBConfig(c)
}

// DeleteGSLBConfig removes GSLB configuration
func (handler *AppHandler) DeleteGSLBConfig(c *gin.Context) {
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	ctx := context.Background()
	settingsCollection := handler.Context.Client.Collection("settings")

	// Get existing zone to check for GSLB records
	existingConfig, err := handler.getGSLBConfig(project)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusNotFound, gin.H{"message": "GSLB configuration not found"})
			return
		}
		handler.Logger.Errorf("Failed to get GSLB config: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to retrieve GSLB configuration"})
		return
	}

	if existingConfig == nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "GSLB configuration not found"})
		return
	}

	// ZONE DELETION PROTECTION: Check if any GSLB records exist for this zone
	if existingConfig.Zone != "" {
		gslbRecordsCollection := handler.Context.Client.Collection("gslb_records")
		count, err := gslbRecordsCollection.CountDocuments(ctx, bson.M{"zone": existingConfig.Zone})
		if err != nil {
			handler.Logger.Errorf("Failed to count GSLB records: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to check GSLB records"})
			return
		}

		if count > 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"message": fmt.Sprintf("Cannot delete zone '%s' with %d existing GSLB records. Delete records first.", existingConfig.Zone, count),
			})
			return
		}
	}

	// Delete GSLB config
	filter := bson.M{"project": project}
	update := bson.M{
		"$unset": bson.M{"gslb_config": ""},
	}

	result, err := settingsCollection.UpdateOne(ctx, filter, update)
	if err != nil {
		handler.Logger.Errorf("Failed to delete GSLB config: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to delete GSLB configuration"})
		return
	}

	if result.ModifiedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{"message": "GSLB configuration not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "GSLB configuration deleted successfully",
		"project": project,
	})
}

// GetGSLBFailoverZones retrieves failover zones from GSLB configuration
func (handler *AppHandler) GetGSLBFailoverZones(c *gin.Context) {
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project parameter is required"})
		return
	}

	gslbConfig, err := handler.getGSLBConfig(project)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			c.JSON(http.StatusOK, gin.H{
				"failover_zones": []string{},
				"project":        project,
				"message":        "No GSLB configuration found for this project",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to get GSLB config"})
		return
	}

	// Return failover zones array
	failoverZones := gslbConfig.FailoverZones
	if failoverZones == nil {
		failoverZones = []string{}
	}

	c.JSON(http.StatusOK, gin.H{
		"failover_zones": failoverZones,
		"project":        project,
	})
}

// Helper function to get GSLB config from database
func (handler *AppHandler) getGSLBConfig(project string) (*models.GSLBConfig, error) {
	ctx := context.Background()
	settingsCollection := handler.Context.Client.Collection("settings")

	var settings models.Settings
	err := settingsCollection.FindOne(ctx, bson.M{"project": project}).Decode(&settings)
	if err != nil {
		return nil, err
	}

	if settings.GSLBConfig == nil {
		return nil, mongo.ErrNoDocuments
	}

	return settings.GSLBConfig, nil
}

// validateGSLBConfig validates GSLB configuration fields
func validateGSLBConfig(config *models.GSLBConfig) error {
	// If enabled, validate required fields
	if config.Enabled {
		if config.Zone == "" {
			return fmt.Errorf("zone is required when GSLB is enabled")
		}
		if config.DNSSecret == "" {
			return fmt.Errorf("dns_secret is required when GSLB is enabled")
		}
		if config.DefaultTTL == 0 {
			return fmt.Errorf("default_ttl is required when GSLB is enabled")
		}
	}

	// Validate zone format (DNS name format)
	if config.Zone != "" && !dnsZoneRegex.MatchString(config.Zone) {
		return fmt.Errorf("invalid zone format: must be a valid DNS name (e.g., 'avrupa-gslb.elchi')")
	}

	// Validate failover zones format if provided
	for i, failoverZone := range config.FailoverZones {
		if failoverZone != "" && !dnsZoneRegex.MatchString(failoverZone) {
			return fmt.Errorf("invalid failover_zones[%d] format: must be a valid DNS name", i)
		}
	}

	// Validate DefaultTTL range using global constants
	if config.DefaultTTL > 0 && (config.DefaultTTL < models.MinTTL || config.DefaultTTL > models.MaxTTL) {
		return fmt.Errorf("default_ttl must be between %d and %d seconds", models.MinTTL, models.MaxTTL)
	}

	return nil
}
