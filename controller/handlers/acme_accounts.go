package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/pkg/acme"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// ========== Request/Response Models ==========

// CreateACMEAccountRequest represents the request body for creating an ACME account
type CreateACMEAccountRequest struct {
	Name        string           `json:"name" binding:"required"`
	Description string           `json:"description"`
	Email       string           `json:"email" binding:"required"`
	CAProvider  string           `json:"ca_provider" binding:"required"` // "letsencrypt" | "google"
	Environment string           `json:"environment" binding:"required"` // "staging" | "production"
	EAB         *EABRequest      `json:"eab,omitempty"`                  // Required for Google, ZeroSSL
	Permissions acme.Permissions `json:"permissions"`
}

// EABRequest represents External Account Binding credentials
type EABRequest struct {
	KeyID   string `json:"key_id" binding:"required"`
	HMACKey string `json:"hmac_key" binding:"required"`
}

// ACMEAccountResponse represents the response for an ACME account
type ACMEAccountResponse struct {
	*acme.ACMEAccount
	Certificates []string `json:"certificates,omitempty"` // List of certificate IDs using this account
}

// ========== ACME Account Management Endpoints ==========

// CreateACMEAccount creates and registers a new ACME account
// POST /api/v3/acme/acme-accounts?project=<project-id>
// Authorization: Admin/Owner only
func (h *ACMEHandler) CreateACMEAccount(c *gin.Context) {
	ctx := c.Request.Context()
	userDetails, _ := GetUserDetails(c)

	// Admin/Owner only
	if !userDetails.IsOwner && userDetails.Role != models.RoleAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "only admins and owners can create ACME accounts"})
		return
	}

	// Get project from query parameter
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project parameter is required"})
		return
	}

	// Validate project access
	if err := h.validateProjectAccess(ctx, c, userDetails, project); err != nil {
		return
	}

	// Parse request body
	var req CreateACMEAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid request: %s", err.Error())})
		return
	}

	// Validate environment
	if req.Environment != "staging" && req.Environment != "production" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "environment must be 'staging' or 'production'"})
		return
	}

	// Validate CA provider
	if req.CAProvider == "" {
		req.CAProvider = "letsencrypt" // Default to Let's Encrypt
	}

	// Check if CA provider exists and is supported
	providerConfig, exists := h.manager.GetCAProviders()[req.CAProvider]
	if !exists {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unknown CA provider: %s", req.CAProvider)})
		return
	}
	if !providerConfig.Supported {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("CA provider %s is not yet supported", req.CAProvider)})
		return
	}

	// Validate EAB credentials if required
	if providerConfig.RequiresEAB {
		if req.EAB == nil || req.EAB.KeyID == "" || req.EAB.HMACKey == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("EAB credentials required for %s", req.CAProvider)})
			return
		}
	}

	// Create account
	account := &acme.ACMEAccount{
		Project:     project,
		Name:        req.Name,
		Description: req.Description,
		Email:       req.Email,
		CAProvider:  req.CAProvider,
		Environment: req.Environment,
		Permissions: req.Permissions,
		CreatedBy:   userDetails.UserID,
	}

	// Encrypt and add EAB credentials if provided
	if req.EAB != nil {
		encryptedEAB, err := h.manager.GetEncryptor().EncryptEABCredentials(req.EAB.KeyID, req.EAB.HMACKey)
		if err != nil {
			h.logger.Errorf("Failed to encrypt EAB credentials: %v", err)
			h.auditHelper.SetAuditError(c, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to encrypt EAB credentials"})
			return
		}
		account.EAB = encryptedEAB
	}

	if err := h.manager.CreateACMEAccount(ctx, account); err != nil {
		h.logger.Errorf("Failed to create ACME account: %v", err)
		h.auditHelper.SetAuditError(c, err)
		// Check for duplicate
		if mongo.IsDuplicateKeyError(err) || err.Error() == fmt.Sprintf("ACME account with email %s already exists for %s environment in project %s", req.Email, req.Environment, project) {
			c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("ACME account with email %s already exists for %s environment", req.Email, req.Environment)})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to create ACME account: %s", err.Error())})
		return
	}

	h.logger.Infof("Created ACME account: %s (ID: %s, email: %s, env: %s)", account.Name, account.ID.Hex(), account.Email, account.Environment)

	// Set audit context
	h.auditHelper.SetACMEAccountAuditContext(c, "CREATE_ACME_ACCOUNT", account.ID.Hex(), account.Name, project)
	h.auditHelper.SetAuditSuccess(c)

	c.JSON(http.StatusCreated, gin.H{
		"message": "ACME account created and registered successfully",
		"data":    account,
	})
}

// ListACMEAccounts lists all ACME accounts in a project
// GET /api/v3/acme/acme-accounts?project=<project-id>
// Authorization: Viewer+ (with permission filtering)
func (h *ACMEHandler) ListACMEAccounts(c *gin.Context) {
	ctx := c.Request.Context()
	userDetails, _ := GetUserDetails(c)

	// Get project from query parameter
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project parameter is required"})
		return
	}

	// Validate project access
	if err := h.validateProjectAccess(ctx, c, userDetails, project); err != nil {
		return
	}

	// List accounts with permission filtering
	accounts, err := h.manager.ListACMEAccounts(ctx, project, userDetails)
	if err != nil {
		h.logger.Errorf("Failed to list ACME accounts: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list ACME accounts: %s", err.Error())})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "ACME accounts retrieved successfully",
		"data":    accounts,
	})
}

// GetACMEAccount retrieves a specific ACME account
// GET /api/v3/acme/acme-accounts/:account_id?project=<project-id>
// Authorization: Viewer+ (with permission check)
func (h *ACMEHandler) GetACMEAccount(c *gin.Context) {
	ctx := c.Request.Context()
	userDetails, _ := GetUserDetails(c)

	// Get account ID from URL parameter
	accountIDStr := c.Param("account_id")
	accountID, err := primitive.ObjectIDFromHex(accountIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid account ID"})
		return
	}

	// Get project from query parameter
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project parameter is required"})
		return
	}

	// Validate project access
	if err := h.validateProjectAccess(ctx, c, userDetails, project); err != nil {
		return
	}

	// Get account
	account, err := h.manager.GetACMEAccount(ctx, accountID, project)
	if err != nil {
		h.logger.Errorf("Failed to get ACME account: %v", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "ACME account not found"})
		return
	}

	// Check permission for non-Owner/Admin
	if !userDetails.IsOwner && userDetails.Role != models.RoleAdmin {
		if !h.hasPermission(userDetails, account.Permissions) {
			c.JSON(http.StatusForbidden, gin.H{"error": "access denied to this ACME account"})
			return
		}
	}

	// Get list of certificates using this account
	certIDs, err := h.getCertificatesUsingAccount(ctx, accountID, project)
	if err != nil {
		h.logger.Warnf("Failed to get certificates for account: %v", err)
	}

	response := &ACMEAccountResponse{
		ACMEAccount:  account,
		Certificates: certIDs,
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "ACME account retrieved successfully",
		"data":    response,
	})
}

// DeleteACMEAccount deletes an ACME account with safety checks
// DELETE /api/v3/acme/acme-accounts/:account_id?project=<project-id>&force=<bool>
// Authorization: Admin/Owner only
func (h *ACMEHandler) DeleteACMEAccount(c *gin.Context) {
	ctx := c.Request.Context()
	userDetails, _ := GetUserDetails(c)

	// Admin/Owner only
	if !userDetails.IsOwner && userDetails.Role != models.RoleAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "only admins and owners can delete ACME accounts"})
		return
	}

	// Get account ID from URL parameter
	accountIDStr := c.Param("account_id")
	accountID, err := primitive.ObjectIDFromHex(accountIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid account ID"})
		return
	}

	// Get project from query parameter
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project parameter is required"})
		return
	}

	// Get force parameter
	force := c.Query("force") == "true"

	// Validate project access
	if err := h.validateProjectAccess(ctx, c, userDetails, project); err != nil {
		return
	}

	// Get account name BEFORE deletion for audit logging
	account, err := h.manager.GetACMEAccount(ctx, accountID, project)
	if err != nil {
		h.logger.Errorf("Failed to get ACME account: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusNotFound, gin.H{"error": "ACME account not found"})
		return
	}

	// Delete account
	err = h.manager.DeleteACMEAccount(ctx, accountID, project, force)
	if err != nil {
		// Check if it's an AccountInUseError
		var accountInUseErr *acme.AccountInUseError
		if errors.As(err, &accountInUseErr) {
			h.auditHelper.SetAuditError(c, accountInUseErr)
			c.JSON(http.StatusBadRequest, gin.H{
				"error": accountInUseErr.Error(),
				"details": gin.H{
					"certificate_count": accountInUseErr.CertCount,
					"certificate_ids":   accountInUseErr.CertificateIDs,
					"warning":           "Deleting this account will prevent automatic renewal of these certificates. Use force=true to proceed.",
				},
			})
			return
		}

		h.logger.Errorf("Failed to delete ACME account: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to delete ACME account: %s", err.Error())})
		return
	}

	h.logger.Infof("Deleted ACME account %s (project: %s, force: %v)", accountIDStr, project, force)

	// Set audit context (use account name fetched before deletion)
	h.auditHelper.SetACMEAccountAuditContext(c, "DELETE_ACME_ACCOUNT", accountIDStr, account.Name, project)
	h.auditHelper.SetAuditSuccess(c)

	if force {
		c.JSON(http.StatusOK, gin.H{
			"message": "ACME account force-deleted successfully",
			"warning": "Certificates using this account are now orphaned and cannot auto-renew",
		})
	} else {
		c.JSON(http.StatusOK, gin.H{
			"message": "ACME account deleted successfully",
		})
	}
}

// ValidateACMEAccount tests an ACME account registration status
// POST /api/v3/acme/acme-accounts/:account_id/validate?project=<project-id>
// Authorization: Admin/Owner only
func (h *ACMEHandler) ValidateACMEAccount(c *gin.Context) {
	ctx := c.Request.Context()
	userDetails, _ := GetUserDetails(c)

	// Admin/Owner only
	if !userDetails.IsOwner && userDetails.Role != models.RoleAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "only admins and owners can validate ACME accounts"})
		return
	}

	// Get account ID from URL parameter
	accountIDStr := c.Param("account_id")
	accountID, err := primitive.ObjectIDFromHex(accountIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid account ID"})
		return
	}

	// Get project from query parameter
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project parameter is required"})
		return
	}

	// Validate project access
	if err := h.validateProjectAccess(ctx, c, userDetails, project); err != nil {
		return
	}

	// Validate account
	if err := h.manager.ValidateACMEAccount(ctx, accountID, project); err != nil {
		h.logger.Errorf("Failed to validate ACME account: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("validation failed: %s", err.Error())})
		return
	}

	// Get updated account
	account, err := h.manager.GetACMEAccount(ctx, accountID, project)
	if err != nil {
		h.logger.Errorf("Failed to get updated account: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get updated account"})
		return
	}

	h.logger.Infof("Validated ACME account %s (project: %s)", accountIDStr, project)

	// Set audit context
	h.auditHelper.SetACMEAccountAuditContext(c, "VALIDATE_ACME_ACCOUNT", accountIDStr, account.Name, project)
	h.auditHelper.SetAuditSuccess(c)

	c.JSON(http.StatusOK, gin.H{
		"message": "ACME account validated successfully",
		"data": gin.H{
			"status":           account.Status,
			"registration_url": account.RegistrationURL,
			"validated_at":     account.LastValidated,
		},
	})
}

// ========== Helper Functions ==========

// hasPermission checks if a user has permission to access a resource
func (h *ACMEHandler) hasPermission(user models.UserDetails, permissions acme.Permissions) bool {
	// Check user ID
	for _, userID := range permissions.Users {
		if userID == user.UserID {
			return true
		}
	}

	// Check groups
	allGroups := append([]string{}, user.Groups...)
	if user.BaseGroup != "" {
		allGroups = append(allGroups, user.BaseGroup)
	}

	for _, group := range permissions.Groups {
		for _, userGroup := range allGroups {
			if group == userGroup {
				return true
			}
		}
	}

	return false
}

// getCertificatesUsingAccount retrieves the list of certificate IDs using a specific account
func (h *ACMEHandler) getCertificatesUsingAccount(ctx context.Context, accountID primitive.ObjectID, project string) ([]string, error) {
	collection := h.manager.GetDB().Collection("acme_certificates")
	cursor, err := collection.Find(ctx, map[string]any{
		"project":         project,
		"acme.account_id": accountID,
	})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var certIDs []string
	for cursor.Next(ctx) {
		var cert struct {
			ID primitive.ObjectID `bson:"_id"`
		}
		if err := cursor.Decode(&cert); err != nil {
			continue
		}
		certIDs = append(certIDs, cert.ID.Hex())
	}

	return certIDs, nil
}
