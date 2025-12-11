package settings

import (
	"context"
	"net/http"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// GetOTPConfig returns the project-level OTP configuration
// GET /api/v3/setting/otp-config
func (handler *AppHandler) GetOTPConfig() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Get project from query parameter
		project := c.Query("project")
		if project == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "project parameter is required"})
			return
		}

		// Fetch project settings
		var settings models.Settings
		err := handler.Context.Client.
			Collection("settings").
			FindOne(ctx, bson.M{"project": project}).
			Decode(&settings)

		if err != nil {
			if err == mongo.ErrNoDocuments {
				// No settings yet, return default
				c.JSON(http.StatusOK, gin.H{
					"project":      project,
					"otp_enforced": false,
				})
				return
			}
			handler.Logger.Errorf("Failed to fetch OTP config: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch OTP configuration"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"project":      settings.Project,
			"otp_enforced": settings.OTPEnforced,
		})
	}
}

// UpdateOTPConfig updates the project-level OTP enforcement setting
// PUT /api/v3/setting/otp-config
// Request body: { "otp_enforced": true }
// Admin/Owner only
func (handler *AppHandler) UpdateOTPConfig() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Get project from query parameter
		project := c.Query("project")
		if project == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "project parameter is required"})
			return
		}

		// Get request body
		var request struct {
			OTPEnforced bool `json:"otp_enforced"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
			return
		}

		// Update or create settings
		_, err := handler.Context.Client.
			Collection("settings").
			UpdateOne(
				ctx,
				bson.M{"project": project},
				bson.M{
					"$set": bson.M{
						"otp_enforced": request.OTPEnforced,
					},
					"$setOnInsert": bson.M{
						"project": project,
						"tokens":  []models.Token{},
					},
				},
				&options.UpdateOptions{
					Upsert: &[]bool{true}[0],
				},
			)

		if err != nil {
			handler.Logger.Errorf("Failed to update OTP config: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update OTP configuration"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message":      "OTP configuration updated successfully",
			"otp_enforced": request.OTPEnforced,
		})
	}
}

// ResetUserOTP resets (disables) OTP for a specific user (emergency use)
// POST /api/v3/setting/otp/reset-user/:user_id
// Owner only
func (handler *AppHandler) ResetUserOTP() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Get target user ID from URL
		targetUserID := c.Param("user_id")
		if targetUserID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "User ID is required"})
			return
		}

		// Reset OTP for target user
		result, err := handler.Context.Client.
			Collection("users").
			UpdateOne(
				ctx,
				bson.M{"user_id": targetUserID},
				bson.M{
					"$set": bson.M{
						"otp_enabled":      nil,
						"otp_secret":       nil,
						"otp_backup_codes": []string{},
						"otp_verified":     nil,
						"updated_at":       primitive.NewDateTimeFromTime(time.Now()),
					},
				},
			)

		if err != nil {
			handler.Logger.Errorf("Failed to reset user OTP: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reset user OTP"})
			return
		}

		if result.MatchedCount == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message": "User OTP reset successfully",
			"user_id": targetUserID,
		})
	}
}
