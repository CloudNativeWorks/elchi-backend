// Package profile provides user profile management functionality
// including authentication, password management, and OTP handling.
package profile

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/ldap"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	otpHelper "github.com/CloudNativeWorks/elchi-backend/pkg/otp"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"golang.org/x/crypto/bcrypt"
)

type ProfileHandler struct {
	AppContext *db.AppContext
	Logger     *logger.Logger
}

func NewProfileHandler(appContext *db.AppContext, logger *logger.Logger) *ProfileHandler {
	return &ProfileHandler{
		AppContext: appContext,
		Logger:     logger,
	}
}

// GetProfile returns the current user's profile information
// GET /api/v3/profile
func (h *ProfileHandler) GetProfile() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Get user ID from context (set by auth middleware)
		userID, exists := c.Get("user_id")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found"})
			return
		}

		// Fetch user from database
		var user models.User
		err := h.AppContext.Client.
			Collection("users").
			FindOne(ctx, bson.M{"user_id": userID}).
			Decode(&user)
		if err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
				return
			}
			h.Logger.Errorf("Error fetching user profile: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch profile"})
			return
		}

		// Return profile (OTP secret and backup codes are excluded via json:"-" tag)
		c.JSON(http.StatusOK, gin.H{
			"user_id":      user.UserID,
			"username":     user.Username,
			"email":        user.Email,
			"role":         user.Role,
			"auth_type":    user.AuthType,
			"base_group":   user.BaseGroup,
			"base_project": user.BaseProject,
			"active":       user.Active,
			"otp_enabled":  user.OTPEnabled,
			"otp_verified": user.OTPVerified,
			"created_at":   user.CreatedAt,
			"updated_at":   user.UpdatedAt,
		})
	}
}

// EnableOTP initiates OTP setup for the user
// POST /api/v3/profile/otp/enable
// Request body: { "password": "current_password" }
// Response: { "qr_code": "base64_image", "secret": "manual_entry_key", "backup_codes": ["CODE1", "CODE2", ...] }
func (h *ProfileHandler) EnableOTP() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		userID, exists := c.Get("user_id")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found"})
			return
		}

		// Get password from request
		var request struct {
			Password string `json:"password" binding:"required"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Password is required"})
			return
		}

		// Fetch user
		var user models.User
		err := h.AppContext.Client.
			Collection("users").
			FindOne(ctx, bson.M{"user_id": userID}).
			Decode(&user)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch user"})
			return
		}

		// Check if OTP is already enabled
		if user.OTPEnabled != nil && *user.OTPEnabled {
			c.JSON(http.StatusBadRequest, gin.H{"error": "OTP is already enabled"})
			return
		}

		// Verify password based on auth_type
		if user.AuthType != nil && *user.AuthType == "ldap" {
			// LDAP users - verify password against LDAP server
			if !h.authenticateWithLDAP(*user.Username, request.Password, user.BaseProject) {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid LDAP password"})
				return
			}
		} else {
			// Local users - verify password using bcrypt
			if user.Password == nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "User password not found"})
				return
			}

			err = bcrypt.CompareHashAndPassword([]byte(*user.Password), []byte(request.Password))
			if err != nil {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid password"})
				return
			}
		}

		// Generate OTP secret
		email := ""
		if user.Email != nil {
			email = *user.Email
		}
		key, err := otpHelper.GenerateOTPSecret(email)
		if err != nil {
			h.Logger.Errorf("Failed to generate OTP secret: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate OTP secret"})
			return
		}

		// Generate QR code
		qrCode, err := otpHelper.GenerateQRCode(key)
		if err != nil {
			h.Logger.Errorf("Failed to generate QR code: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate QR code"})
			return
		}

		// Generate backup codes
		plainCodes, hashedCodes, err := otpHelper.GenerateBackupCodes(otpHelper.BackupCodeCount)
		if err != nil {
			h.Logger.Errorf("Failed to generate backup codes: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate backup codes"})
			return
		}

		// Save to database (not verified yet)
		enabled := false
		verified := false
		secret := key.Secret()
		_, err = h.AppContext.Client.
			Collection("users").
			UpdateOne(
				ctx,
				bson.M{"user_id": userID},
				bson.M{
					"$set": bson.M{
						"otp_enabled":      &enabled,
						"otp_secret":       &secret,
						"otp_backup_codes": hashedCodes,
						"otp_verified":     &verified,
						"updated_at":       primitive.NewDateTimeFromTime(time.Now()),
					},
				},
			)
		if err != nil {
			h.Logger.Errorf("Failed to save OTP settings: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save OTP settings"})
			return
		}

		// Return QR code and backup codes
		c.JSON(http.StatusOK, gin.H{
			"message":      "OTP setup initiated. Please scan the QR code and verify with a code.",
			"qr_code":      qrCode,
			"secret":       key.Secret(), // For manual entry
			"backup_codes": plainCodes,   // Show once, never again!
		})
	}
}

// VerifyOTP completes OTP setup by validating the first code
// POST /api/v3/profile/otp/verify
// Request body: { "code": "123456" }
func (h *ProfileHandler) VerifyOTP() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		userID, exists := c.Get("user_id")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found"})
			return
		}

		// Get code from request
		var request struct {
			Code string `json:"code" binding:"required"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "OTP code is required"})
			return
		}

		// Fetch user
		var user models.User
		err := h.AppContext.Client.
			Collection("users").
			FindOne(ctx, bson.M{"user_id": userID}).
			Decode(&user)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch user"})
			return
		}

		// Check if secret exists
		if user.OTPSecret == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "OTP setup not initiated. Please enable OTP first."})
			return
		}

		// Check if already verified
		if user.OTPVerified != nil && *user.OTPVerified {
			c.JSON(http.StatusBadRequest, gin.H{"error": "OTP already verified"})
			return
		}

		// Validate the code
		valid := otpHelper.ValidateOTPCode(*user.OTPSecret, request.Code)
		if !valid {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid OTP code"})
			return
		}

		// Activate OTP
		enabled := true
		verified := true
		_, err = h.AppContext.Client.
			Collection("users").
			UpdateOne(
				ctx,
				bson.M{"user_id": userID},
				bson.M{
					"$set": bson.M{
						"otp_enabled":  &enabled,
						"otp_verified": &verified,
						"updated_at":   primitive.NewDateTimeFromTime(time.Now()),
					},
				},
			)
		if err != nil {
			h.Logger.Errorf("Failed to activate OTP: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to activate OTP"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message": "OTP successfully enabled and verified",
		})
	}
}

// DisableOTP disables OTP for the user
// POST /api/v3/profile/otp/disable
// Request body: { "password": "current_password", "otp_code": "123456" } OR { "password": "current_password", "backup_code": "ABCD1234" }
func (h *ProfileHandler) DisableOTP() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		userID, exists := c.Get("user_id")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found"})
			return
		}

		// Get request
		var request struct {
			Password   string `json:"password" binding:"required"`
			OTPCode    string `json:"otp_code"`
			BackupCode string `json:"backup_code"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Password is required"})
			return
		}

		if request.OTPCode == "" && request.BackupCode == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Either otp_code or backup_code is required"})
			return
		}

		// Fetch user
		var user models.User
		err := h.AppContext.Client.
			Collection("users").
			FindOne(ctx, bson.M{"user_id": userID}).
			Decode(&user)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch user"})
			return
		}

		// Verify password based on auth_type
		if user.AuthType != nil && *user.AuthType == "ldap" {
			// LDAP users - verify password against LDAP server
			if !h.authenticateWithLDAP(*user.Username, request.Password, user.BaseProject) {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid LDAP password"})
				return
			}
		} else {
			// Local users - verify password using bcrypt
			if user.Password == nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "User password not found"})
				return
			}

			err = bcrypt.CompareHashAndPassword([]byte(*user.Password), []byte(request.Password))
			if err != nil {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid password"})
				return
			}
		}

		// Check if OTP is enabled
		if user.OTPEnabled == nil || !*user.OTPEnabled {
			c.JSON(http.StatusBadRequest, gin.H{"error": "OTP is not enabled"})
			return
		}

		// Verify OTP code or backup code
		valid := false
		if request.OTPCode != "" && user.OTPSecret != nil {
			valid = otpHelper.ValidateOTPCode(*user.OTPSecret, request.OTPCode)
		} else if request.BackupCode != "" {
			valid, _ = otpHelper.ValidateBackupCode(user.OTPBackupCodes, request.BackupCode)
		}

		if !valid {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid OTP or backup code"})
			return
		}

		// Disable OTP
		_, err = h.AppContext.Client.
			Collection("users").
			UpdateOne(
				ctx,
				bson.M{"user_id": userID},
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
			h.Logger.Errorf("Failed to disable OTP: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to disable OTP"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message": "OTP successfully disabled",
		})
	}
}

// RegenerateBackupCodes generates new backup codes
// POST /api/v3/profile/otp/regenerate-backup-codes
// Request body: { "otp_code": "123456" }
func (h *ProfileHandler) RegenerateBackupCodes() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		userID, exists := c.Get("user_id")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found"})
			return
		}

		// Get OTP code
		var request struct {
			OTPCode string `json:"otp_code" binding:"required"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "OTP code is required"})
			return
		}

		// Fetch user
		var user models.User
		err := h.AppContext.Client.
			Collection("users").
			FindOne(ctx, bson.M{"user_id": userID}).
			Decode(&user)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch user"})
			return
		}

		// Check if OTP is enabled
		if user.OTPEnabled == nil || !*user.OTPEnabled || user.OTPSecret == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "OTP is not enabled"})
			return
		}

		// Verify OTP code
		valid := otpHelper.ValidateOTPCode(*user.OTPSecret, request.OTPCode)
		if !valid {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid OTP code"})
			return
		}

		// Generate new backup codes
		plainCodes, hashedCodes, err := otpHelper.GenerateBackupCodes(otpHelper.BackupCodeCount)
		if err != nil {
			h.Logger.Errorf("Failed to generate backup codes: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate backup codes"})
			return
		}

		// Save to database
		_, err = h.AppContext.Client.
			Collection("users").
			UpdateOne(
				ctx,
				bson.M{"user_id": userID},
				bson.M{
					"$set": bson.M{
						"otp_backup_codes": hashedCodes,
						"updated_at":       primitive.NewDateTimeFromTime(time.Now()),
					},
				},
			)
		if err != nil {
			h.Logger.Errorf("Failed to save backup codes: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save backup codes"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message":      "Backup codes regenerated successfully",
			"backup_codes": plainCodes,
		})
	}
}

// GetOTPStatus returns the current OTP status
// GET /api/v3/profile/otp/status
func (h *ProfileHandler) GetOTPStatus() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		userID, exists := c.Get("user_id")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found"})
			return
		}

		// Fetch user
		var user models.User
		err := h.AppContext.Client.
			Collection("users").
			FindOne(ctx, bson.M{"user_id": userID}).
			Decode(&user)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch user"})
			return
		}

		backupCodesCount := len(user.OTPBackupCodes)

		c.JSON(http.StatusOK, gin.H{
			"otp_enabled":         user.OTPEnabled,
			"otp_verified":        user.OTPVerified,
			"backup_codes_count":  backupCodesCount,
			"backup_codes_remain": backupCodesCount > 0,
		})
	}
}

// UpdateEmail updates the user's email (local auth only)
// PUT /api/v3/profile/email
// Request body: { "email": "new@example.com", "password": "current_password" }
func (h *ProfileHandler) UpdateEmail() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		userID, exists := c.Get("user_id")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found"})
			return
		}

		// Get request
		var request struct {
			Email    string `json:"email" binding:"required,email"`
			Password string `json:"password" binding:"required"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
			return
		}

		// Fetch user
		var user models.User
		err := h.AppContext.Client.
			Collection("users").
			FindOne(ctx, bson.M{"user_id": userID}).
			Decode(&user)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch user"})
			return
		}

		// Check auth type - only local users can change email
		if user.AuthType != nil && *user.AuthType == "ldap" {
			c.JSON(http.StatusForbidden, gin.H{"error": "LDAP users cannot change email via this endpoint"})
			return
		}

		// Verify password
		if user.Password == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "User password not found"})
			return
		}

		err = bcrypt.CompareHashAndPassword([]byte(*user.Password), []byte(request.Password))
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid password"})
			return
		}

		// Update email
		_, err = h.AppContext.Client.
			Collection("users").
			UpdateOne(
				ctx,
				bson.M{"user_id": userID},
				bson.M{
					"$set": bson.M{
						"email":      request.Email,
						"updated_at": primitive.NewDateTimeFromTime(time.Now()),
					},
				},
			)
		if err != nil {
			h.Logger.Errorf("Failed to update email: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update email"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message": "Email updated successfully",
			"email":   request.Email,
		})
	}
}

// UpdatePassword updates the user's password (local auth only)
// PUT /api/v3/profile/password
// Request body: { "current_password": "old", "new_password": "new" }
func (h *ProfileHandler) UpdatePassword() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		userID, exists := c.Get("user_id")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found"})
			return
		}

		// Get request
		var request struct {
			CurrentPassword string `json:"current_password" binding:"required"`
			NewPassword     string `json:"new_password" binding:"required,min=6"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request. New password must be at least 6 characters."})
			return
		}

		// Fetch user
		var user models.User
		err := h.AppContext.Client.
			Collection("users").
			FindOne(ctx, bson.M{"user_id": userID}).
			Decode(&user)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch user"})
			return
		}

		// Check auth type - only local users can change password
		if user.AuthType != nil && *user.AuthType == "ldap" {
			c.JSON(http.StatusForbidden, gin.H{"error": "LDAP users cannot change password via this endpoint"})
			return
		}

		// Verify current password
		if user.Password == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "User password not found"})
			return
		}

		err = bcrypt.CompareHashAndPassword([]byte(*user.Password), []byte(request.CurrentPassword))
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid current password"})
			return
		}

		// Hash new password
		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(request.NewPassword), bcrypt.DefaultCost)
		if err != nil {
			h.Logger.Errorf("Failed to hash password: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
			return
		}

		newPasswordStr := string(hashedPassword)

		// Update password
		_, err = h.AppContext.Client.
			Collection("users").
			UpdateOne(
				ctx,
				bson.M{"user_id": userID},
				bson.M{
					"$set": bson.M{
						"password":   newPasswordStr,
						"updated_at": primitive.NewDateTimeFromTime(time.Now()),
					},
				},
			)
		if err != nil {
			h.Logger.Errorf("Failed to update password: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update password"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message": "Password updated successfully",
		})
	}
}

// authenticateWithLDAP validates username/password against LDAP using project config
func (h *ProfileHandler) authenticateWithLDAP(username, password string, preferredProject *string) bool {
	ldapConfig, err := h.getLDAPConfigForAuthentication(preferredProject)
	if err != nil || ldapConfig == nil || !ldapConfig.Enabled {
		h.Logger.Errorf("Failed to get LDAP config for authentication: %v", err)
		return false
	}

	client, err := ldap.NewClient(ldapConfig)
	if err != nil {
		h.Logger.Errorf("Failed to create LDAP client: %v", err)
		return false
	}
	defer client.Close()

	if err := client.ValidatePassword(username, password); err != nil {
		h.Logger.Errorf("LDAP password validation failed for user %s: %v", username, err)
		return false
	}

	return true
}

// getLDAPConfigForAuthentication gets LDAP config from preferred project or any available project
func (h *ProfileHandler) getLDAPConfigForAuthentication(preferredProject *string) (*models.LDAPConfig, error) {
	ctx := context.Background()
	settingsCollection := h.AppContext.Client.Collection("settings")

	// If preferred project is specified, try to get config from it first
	if preferredProject != nil && *preferredProject != "" {
		var settings models.Settings
		err := settingsCollection.FindOne(ctx, bson.M{"project": *preferredProject}).Decode(&settings)
		if err == nil && settings.LDAPConfig != nil && settings.LDAPConfig.Enabled {
			return settings.LDAPConfig, nil
		}
	}

	// Otherwise, find any project with enabled LDAP config
	ldapConfig, _, err := h.getFirstAvailableLDAPConfig()
	return ldapConfig, err
}

// getFirstAvailableLDAPConfig finds the first project with enabled LDAP config
func (h *ProfileHandler) getFirstAvailableLDAPConfig() (*models.LDAPConfig, string, error) {
	ctx := context.Background()
	settingsCollection := h.AppContext.Client.Collection("settings")

	filter := bson.M{
		"ldap_config.enabled": true,
	}

	var settings models.Settings
	err := settingsCollection.FindOne(ctx, filter).Decode(&settings)
	if err != nil {
		return nil, "", fmt.Errorf("no enabled LDAP configuration found")
	}

	return settings.LDAPConfig, settings.Project, nil
}
