package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/CloudNativeWorks/elchi-backend/pkg/acme"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// ACMEHandler wraps the ACME certificate manager
type ACMEHandler struct {
	manager       *acme.CertificateManager
	asyncSystem   async.AsyncJobSystem // For background DNS verification
	logger        *logger.Logger
	parentHandler *Handler         // Reference to parent Handler for audit functions
	auditHelper   *ACMEAuditHelper // Audit context helper for ACME operations
}

// NewACMEHandler creates a new ACME handler wrapper
func NewACMEHandler(manager *acme.CertificateManager, asyncSystem async.AsyncJobSystem, logger *logger.Logger) *ACMEHandler {
	return &ACMEHandler{
		manager:     manager,
		asyncSystem: asyncSystem,
		logger:      logger,
		auditHelper: NewACMEAuditHelper(), // Initialize audit helper
		// parentHandler will be set after Handler initialization
	}
}

// SetParentHandler sets the parent Handler reference (for audit functions)
func (h *ACMEHandler) SetParentHandler(parent *Handler) {
	h.parentHandler = parent
}

// ========== Helper Functions ==========

// sanitizeCertificatesForResponse removes external ACME URLs from certificate data
// to prevent network filtering issues with firewalls/WAFs that block Let's Encrypt URLs
func sanitizeCertificatesForResponse(certificates []*acme.ACMECertificate) []*acme.ACMECertificate {
	for _, cert := range certificates {
		// Clear ACME URLs that may trigger network filtering
		cert.ACME.CertURL = ""
		cert.ACME.OrderURL = ""
	}
	return certificates
}

// sanitizeCertificateForResponse removes external ACME URLs from a single certificate
func sanitizeCertificateForResponse(cert *acme.ACMECertificate) *acme.ACMECertificate {
	if cert != nil {
		cert.ACME.CertURL = ""
		cert.ACME.OrderURL = ""
	}
	return cert
}

// ========== Request/Response Structures ==========

// CreateDNSCredentialRequest represents the request body for creating a DNS credential
type CreateDNSCredentialRequest struct {
	Name        string           `json:"name" binding:"required"`
	Description string           `json:"description"`
	Provider    string           `json:"provider" binding:"required"` // "google" | "godaddy"
	Credentials map[string]any   `json:"credentials" binding:"required"`
	Permissions acme.Permissions `json:"permissions"`
}

// UpdateDNSCredentialRequest represents the request body for updating a DNS credential
type UpdateDNSCredentialRequest struct {
	Name        *string           `json:"name"`
	Description *string           `json:"description"`
	Credentials map[string]any    `json:"credentials"` // Optional - only if updating credentials
	Permissions *acme.Permissions `json:"permissions"`
}

// TestDNSCredentialRequest represents the request body for testing a DNS credential
type TestDNSCredentialRequest struct {
	Provider    string         `json:"provider" binding:"required"`
	Credentials map[string]any `json:"credentials" binding:"required"`
	Domain      string         `json:"domain" binding:"required"`
}

// CreateCertificateRequest represents the request body for creating a certificate
type CreateCertificateRequest struct {
	Domains         []string `json:"domains" binding:"required"`
	SecretName      string   `json:"secret_name" binding:"required"`
	ACMEAccountID   string   `json:"acme_account_id" binding:"required"` // REQUIRED: Reference to ACME account
	Versions        []string `json:"versions" binding:"required"`        // Multiple Envoy versions (array)
	Environment     string   `json:"environment"`                        // "staging" | "production", default: staging
	DNSCredentialID string   `json:"dns_credential_id"`                  // Optional: for automatic DNS verification
}

// ChangeDNSCredentialRequest represents the request body for changing certificate DNS credential
type ChangeDNSCredentialRequest struct {
	DNSCredentialID string `json:"dns_credential_id" binding:"required"`
}

// DuplicateCertificateRequest represents the request body for duplicating a certificate to a new version
type DuplicateCertificateRequest struct {
	Version string `json:"version" binding:"required"` // New version to create
}

// ========== Certificate Management Endpoints ==========

// CreateCertificate initiates a new Let's Encrypt certificate request
// POST /api/v3/acme/certificates?project=<project-id>
// Authorization: Editor+ (can create certificates)
func (h *ACMEHandler) CreateCertificate(c *gin.Context) {
	ctx := c.Request.Context()
	userDetails, _ := GetUserDetails(c)

	// Editor+ can create certificates
	if userDetails.Role == models.RoleViewer {
		c.JSON(http.StatusForbidden, gin.H{"error": "viewers cannot create certificates"})
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
	var req CreateCertificateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid request: %s", err.Error())})
		return
	}

	// Validate required fields
	if len(req.Domains) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "domains list cannot be empty"})
		return
	}
	if req.SecretName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "secret_name is required"})
		return
	}
	if req.ACMEAccountID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "acme_account_id is required"})
		return
	}
	if len(req.Versions) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "versions array cannot be empty"})
		return
	}

	// Validate and get ACME account
	acmeAccountID, err := primitive.ObjectIDFromHex(req.ACMEAccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid acme_account_id format"})
		return
	}

	acmeAccount, err := h.manager.GetACMEAccount(ctx, acmeAccountID, project)
	if err != nil {
		h.logger.Errorf("Failed to get ACME account: %v", err)
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("ACME account not found: %s", err.Error())})
		return
	}

	// Check if user has permission to use this ACME account (for non-Owner/Admin)
	if !userDetails.IsOwner && userDetails.Role != models.RoleAdmin {
		hasPermission := false
		// Check user ID
		for _, userID := range acmeAccount.Permissions.Users {
			if userID == userDetails.UserID {
				hasPermission = true
				break
			}
		}
		// Check groups
		if !hasPermission {
			allGroups := append([]string{}, userDetails.Groups...)
			if userDetails.BaseGroup != "" {
				allGroups = append(allGroups, userDetails.BaseGroup)
			}
			for _, group := range acmeAccount.Permissions.Groups {
				for _, userGroup := range allGroups {
					if group == userGroup {
						hasPermission = true
						break
					}
				}
				if hasPermission {
					break
				}
			}
		}
		if !hasPermission {
			c.JSON(http.StatusForbidden, gin.H{"error": "access denied to this ACME account"})
			return
		}
	}

	// Validate account is registered and active
	if !acmeAccount.IsRegistered || acmeAccount.Status != acme.AccountStatusRegistered {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("ACME account is not active (status: %s)", acmeAccount.Status)})
		return
	}

	// Validate environment matches (if provided)
	environment := req.Environment
	if environment == "" {
		environment = acmeAccount.Environment // Use account's environment as default
	}
	if environment != "staging" && environment != "production" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "environment must be 'staging' or 'production'"})
		return
	}
	// Verify environment matches ACME account
	if environment != acmeAccount.Environment {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("environment mismatch: certificate environment '%s' does not match ACME account environment '%s'", environment, acmeAccount.Environment),
		})
		return
	}

	// Check if DNS credential ID is provided for automatic verification
	if req.DNSCredentialID != "" {
		// AUTOMATIC DNS - ASYNC PATH (runs in background job)
		dnsCredentialID, err := primitive.ObjectIDFromHex(req.DNSCredentialID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid dns_credential_id format"})
			return
		}

		// 1. Create certificate in pending_verification status
		cert, err := h.manager.InitiateCertificateWithDNSProvider(
			ctx,
			req.Domains,
			environment,
			acmeAccountID,
			project,
			req.SecretName,
			req.Versions,
			dnsCredentialID,
			userDetails,
		)
		if err != nil {
			h.logger.Errorf("Failed to initiate certificate with DNS provider: %v", err)
			h.auditHelper.SetAuditError(c, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to create certificate: %s", err.Error())})
			return
		}

		h.logger.Infof("Certificate initiated for automatic verification (ID: %s)", cert.ID.Hex())

		// 2. Create async job for DNS verification
		job, err := h.asyncSystem.CreateACMEVerificationJob(ctx, &async.CreateACMEJobRequest{
			CertificateID:   cert.ID.Hex(),
			CertificateName: req.SecretName,
			Domains:         req.Domains,
			Environment:     environment,
			DNSCredentialID: req.DNSCredentialID,
			ACMEAccountID:   req.ACMEAccountID,
			Project:         project,
			Versions:        req.Versions,
			TriggerUser:     userDetails,
			IsRenewal:       false, // Initial verification
		})
		if err != nil {
			h.logger.Errorf("Failed to create verification job: %v", err)
			h.auditHelper.SetAuditError(c, err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":       "Certificate created but failed to start verification job",
				"certificate": cert,
			})
			return
		}

		h.logger.Infof("Created verification job %s for certificate %s", job.JobID, cert.ID.Hex())

		// 2.1. Update certificate status to pending_verification and set job ID
		err = h.manager.UpdateCertificateStatusAndJobID(ctx, cert.ID.Hex(), project, "pending_verification", job.JobID)
		if err != nil {
			h.logger.Errorf("Failed to update certificate status and job ID: %v", err)
			h.auditHelper.SetAuditError(c, err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":       "Certificate created but failed to update status",
				"certificate": cert,
			})
			return
		}

		// Set audit context
		h.auditHelper.SetCertificateAuditContext(c, "CREATE_CERTIFICATE", cert.ID.Hex(), cert.SecretName, project)
		h.auditHelper.SetAuditSuccess(c)

		// 3. Return immediately with job ID (certificate is pending_verification)
		c.JSON(http.StatusCreated, gin.H{
			"message": "Certificate creation initiated. DNS verification running in background.",
			"data": gin.H{
				"certificate_id": cert.ID.Hex(),
				"job_id":         job.JobID, // Human-friendly ID like "EC-123"
				"status":         "pending_verification",
			},
		})
		return
	}

	// Manual DNS verification mode
	cert, err := h.manager.InitiateCertificate(
		ctx,
		req.Domains,
		environment,
		acmeAccountID,
		project,
		req.SecretName,
		req.Versions,
		userDetails,
	)
	if err != nil {
		h.logger.Errorf("Failed to initiate certificate: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to create certificate: %s", err.Error())})
		return
	}

	h.logger.Infof("Certificate creation initiated for domains %v (ID: %s)", req.Domains, cert.ID.Hex())

	// Set audit context
	h.auditHelper.SetCertificateAuditContext(c, "CREATE_CERTIFICATE", cert.ID.Hex(), cert.SecretName, project)
	h.auditHelper.SetAuditSuccess(c)

	c.JSON(http.StatusCreated, gin.H{
		"message":     "Certificate creation initiated. Please add DNS TXT records and verify.",
		"certificate": cert,
	})
}

// ListCertificates lists all certificates for a project with permission filtering
// GET /api/v3/acme/certificates?project=<project-id>
// Authorization: Viewer+ (with permission filtering)
func (h *ACMEHandler) ListCertificates(c *gin.Context) {
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

	// List certificates with permission filtering
	certificates, err := h.manager.ListCertificates(ctx, project, userDetails)
	if err != nil {
		h.logger.Errorf("Failed to list certificates: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list certificates: %s", err.Error())})
		return
	}

	// Build account existence cache to avoid repeated lookups
	accountCache := make(map[string]bool)
	orphanedCount := 0

	// Check for orphaned certificates
	for _, cert := range certificates {
		if cert.ACME.AccountID != nil {
			accountIDStr := cert.ACME.AccountID.Hex()

			// Check cache first
			exists, cached := accountCache[accountIDStr]
			if !cached {
				// Check if account exists
				_, err := h.manager.GetACMEAccount(ctx, *cert.ACME.AccountID, project)
				exists = (err == nil)
				accountCache[accountIDStr] = exists
			}

			// Mark as orphaned if account doesn't exist
			if !exists {
				orphanedCount++
			}
		}
	}

	// Sanitize certificates to remove ACME URLs that trigger network filtering
	sanitizedCerts := sanitizeCertificatesForResponse(certificates)

	response := gin.H{
		"message": "Certificates retrieved successfully",
		"data":    sanitizedCerts,
	}

	// Add warning if there are orphaned certificates
	if orphanedCount > 0 {
		response["warning"] = fmt.Sprintf("%d certificate(s) have missing ACME accounts. Automatic renewal will fail for these certificates.", orphanedCount)
		response["orphaned_count"] = orphanedCount
	}

	c.JSON(http.StatusOK, response)
}

// GetCertificate retrieves a specific certificate by ID
// GET /api/v3/acme/certificates/:cert_id?project=<project-id>
// Authorization: Viewer+ (with permission check)
func (h *ACMEHandler) GetCertificate(c *gin.Context) {
	ctx := c.Request.Context()
	userDetails, _ := GetUserDetails(c)

	// Get certificate ID from URL parameter
	certIDStr := c.Param("cert_id")
	certID, err := primitive.ObjectIDFromHex(certIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid certificate ID"})
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

	// Get certificate
	certificate, err := h.manager.GetCertificate(ctx, certID, project)
	if err != nil {
		h.logger.Errorf("Failed to get certificate: %v", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "certificate not found"})
		return
	}

	// Check user has permission to view this certificate
	if !acme.HasPermission(userDetails, certificate.Permissions) {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied to this certificate"})
		return
	}

	// Check for orphaned certificate (ACME account deleted)
	var warning string
	if certificate.ACME.AccountID != nil {
		// Check if account still exists
		_, err := h.manager.GetACMEAccount(ctx, *certificate.ACME.AccountID, project)
		if err != nil {
			// Account not found - certificate is orphaned
			warning = "ACME account not found. Automatic renewal may fail. The account may have been deleted."
		}
	}

	// Sanitize certificate to remove ACME URLs that trigger network filtering
	sanitizedCert := sanitizeCertificateForResponse(certificate)

	response := gin.H{
		"message": "Certificate retrieved successfully",
		"data":    sanitizedCert,
	}

	if warning != "" {
		response["warning"] = warning
	}

	c.JSON(http.StatusOK, response)
}

// DeleteCertificate deletes a certificate
// DELETE /api/v3/acme/certificates/:cert_id?project=<project-id>
// Authorization: Admin+ or certificate creator
func (h *ACMEHandler) DeleteCertificate(c *gin.Context) {
	ctx := c.Request.Context()
	userDetails, _ := GetUserDetails(c)

	// Get certificate ID from URL parameter
	certIDStr := c.Param("cert_id")
	certID, err := primitive.ObjectIDFromHex(certIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid certificate ID"})
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

	// Get certificate to check permissions (also needed for audit logging)
	certificate, err := h.manager.GetCertificate(ctx, certID, project)
	if err != nil {
		h.logger.Errorf("Failed to get certificate: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusNotFound, gin.H{"error": "certificate not found"})
		return
	}

	// Check if user can modify this certificate
	if !acme.CanModifyResource(userDetails, certificate.Permissions, certificate.CreatedBy) {
		c.JSON(http.StatusForbidden, gin.H{"error": "insufficient permissions to delete this certificate"})
		return
	}

	// Delete certificate (both metadata and secret from secrets collection)
	if err := h.manager.DeleteCertificate(ctx, certID, project); err != nil {
		h.logger.Errorf("Failed to delete certificate: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to delete certificate: %s", err.Error())})
		return
	}

	h.logger.Infof("Certificate and secret deleted: %s (project: %s)", certIDStr, project)

	// Set audit context (use certificate name fetched before deletion)
	h.auditHelper.SetCertificateAuditContext(c, "DELETE_CERTIFICATE", certIDStr, certificate.SecretName, project)
	h.auditHelper.SetAuditSuccess(c)

	c.JSON(http.StatusOK, gin.H{
		"message": "Certificate deleted successfully",
	})
}

// DuplicateCertificate adds a new version to an existing certificate
// POST /api/v3/acme/certificates/:cert_id/duplicate?project=<project-id>
// Authorization: Editor+ (can duplicate certificates)
// Body: { "version": "v1.37.0" }
func (h *ACMEHandler) DuplicateCertificate(c *gin.Context) {
	ctx := c.Request.Context()
	userDetails, _ := GetUserDetails(c)

	// Editor+ can duplicate certificates
	if userDetails.Role == models.RoleViewer {
		c.JSON(http.StatusForbidden, gin.H{"error": "viewers cannot duplicate certificates"})
		return
	}

	// Get certificate ID from URL parameter
	certIDStr := c.Param("cert_id")
	certID, err := primitive.ObjectIDFromHex(certIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid certificate ID"})
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
	var req DuplicateCertificateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid request: %s", err.Error())})
		return
	}

	// Duplicate certificate
	cert, err := h.manager.DuplicateCertificate(ctx, certID, req.Version, project)
	if err != nil {
		h.logger.Errorf("Failed to duplicate certificate: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to duplicate certificate: %s", err.Error())})
		return
	}

	h.logger.Infof("Certificate %s duplicated to version %s (ID: %s)", cert.SecretName, req.Version, certID.Hex())

	// Set audit context
	h.auditHelper.SetCertificateAuditContext(c, "DUPLICATE_CERTIFICATE", certID.Hex(), cert.SecretName, project)
	h.auditHelper.SetAuditSuccess(c)

	c.JSON(http.StatusOK, gin.H{
		"message":     fmt.Sprintf("Certificate duplicated successfully to version %s", req.Version),
		"certificate": cert,
	})
}

// RetryVerification retries DNS verification for a failed/pending certificate
// POST /api/v3/acme/certificates/:cert_id/retry-verification?project=<project-id>
// Authorization: Editor+ (can retry verification)
func (h *ACMEHandler) RetryVerification(c *gin.Context) {
	ctx := c.Request.Context()
	userDetails, _ := GetUserDetails(c)

	// Editor+ can retry verification
	if userDetails.Role == models.RoleViewer {
		c.JSON(http.StatusForbidden, gin.H{"error": "viewers cannot retry verification"})
		return
	}

	// Get certificate ID from URL parameter
	certIDStr := c.Param("cert_id")
	certID, err := primitive.ObjectIDFromHex(certIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid certificate ID"})
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

	// Get certificate metadata
	cert, err := h.manager.GetCertificate(ctx, certID, project)
	if err != nil {
		h.logger.Errorf("Failed to get certificate: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusNotFound, gin.H{"error": "certificate not found"})
		return
	}

	// Check if user can modify this certificate
	if !acme.CanModifyResource(userDetails, cert.Permissions, cert.CreatedBy) {
		c.JSON(http.StatusForbidden, gin.H{"error": "insufficient permissions to retry verification for this certificate"})
		return
	}

	// Validate certificate status (must be pending_verification or verification_failed)
	if cert.Status != "pending_verification" && cert.Status != "verification_failed" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("Cannot retry certificate with status: %s (must be pending_verification or verification_failed)", cert.Status),
		})
		return
	}

	// Check if DNS credential exists (automatic verification)
	if cert.DNSVerification.DNSCredentialID.IsZero() {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Certificate was not created with automatic DNS verification. Use manual verification endpoint instead.",
		})
		return
	}

	// Ensure a temp key (private_key + CSR) is present for the worker.
	// CreateCertificate stores one initially, but it can disappear between
	// attempts — legacy TTL cleanup, a previous failure that nuked it, or
	// manual ops. The worker calls GetTempKey unconditionally and fails the
	// job with "temp key not found" when missing. Always refreshing here
	// matches the behavior RenewCertificate already implements.
	//
	// Safety: this branch is reached only on the automatic DNS path (manual
	// is rejected above). Automatic verification calls lego's ObtainCertificate
	// which always opens a fresh ACME order, so the regenerated key + CSR are
	// compatible — cert.ACME.OrderURL is irrelevant on this code path.
	privateKeyPEM, csrPEM, err := h.manager.PrepareRenewalKey(ctx, cert.Domains)
	if err != nil {
		h.logger.Errorf("Failed to prepare retry-verification key for cert %s: %v", certIDStr, err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to prepare retry-verification key"})
		return
	}
	if err := h.manager.CreateOrUpdateTempKeyForRenewal(ctx, certID, project, cert.SecretName, privateKeyPEM, csrPEM); err != nil {
		h.logger.Errorf("Failed to store retry-verification temp key for cert %s: %v", certIDStr, err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store retry-verification temp key"})
		return
	}
	h.logger.Infof("Refreshed temp key for retry-verification of certificate %s", certIDStr)

	// Create new async job for retry
	job, err := h.asyncSystem.CreateACMEVerificationJob(ctx, &async.CreateACMEJobRequest{
		CertificateID:   certIDStr,
		CertificateName: cert.SecretName,
		Domains:         cert.Domains,
		Environment:     cert.ACME.Environment,
		DNSCredentialID: cert.DNSVerification.DNSCredentialID.Hex(),
		ACMEAccountID:   cert.ACME.AccountID.Hex(),
		Project:         project,
		Versions:        cert.SecretVersions,
		TriggerUser:     userDetails,
		IsRenewal:       false, // Retry verification (not renewal)
	})
	if err != nil {
		h.logger.Errorf("Failed to create retry verification job: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to create verification job",
		})
		return
	}

	h.logger.Infof("Created retry verification job %s for certificate %s", job.JobID, certIDStr)

	// Update certificate status to pending_verification and set job ID
	err = h.manager.UpdateCertificateStatusAndJobID(ctx, certIDStr, project, "pending_verification", job.JobID)
	if err != nil {
		h.logger.Errorf("Failed to update certificate status and job ID: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to update certificate after creating job",
		})
		return
	}

	// Set audit context
	h.auditHelper.SetCertificateAuditContext(c, "RETRY_CERTIFICATE_VERIFICATION", certIDStr, cert.SecretName, project)
	h.auditHelper.SetAuditSuccess(c)

	c.JSON(http.StatusOK, gin.H{
		"message": "Verification retry initiated",
		"data": gin.H{
			"job_id": job.JobID,
			"status": "pending_verification",
		},
	})
}

// GetDNSChallenges retrieves DNS challenges for a pending certificate
// GET /api/v3/acme/certificates/:cert_id/dns-challenges?project=<project-id>
// Authorization: Viewer+ (with permission check)
func (h *ACMEHandler) GetDNSChallenges(c *gin.Context) {
	ctx := c.Request.Context()
	userDetails, _ := GetUserDetails(c)

	// Get certificate ID from URL parameter
	certIDStr := c.Param("cert_id")
	certID, err := primitive.ObjectIDFromHex(certIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid certificate ID"})
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

	// Get certificate
	certificate, err := h.manager.GetCertificate(ctx, certID, project)
	if err != nil {
		h.logger.Errorf("Failed to get certificate: %v", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "certificate not found"})
		return
	}

	// Check user has permission to view this certificate
	if !acme.HasPermission(userDetails, certificate.Permissions) {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied to this certificate"})
		return
	}

	// Check if user wants to force refresh challenges
	forceRefresh := c.Query("refresh") == "true"

	// Check if we need to fetch real DNS challenges from Let's Encrypt
	// If challenges are placeholder messages, empty, or user requested refresh, fetch them
	needsFetch := forceRefresh
	if !needsFetch {
		if len(certificate.DNSVerification.PendingChallenges) == 0 {
			needsFetch = true
		} else {
			// Check if challenges are placeholders
			for _, ch := range certificate.DNSVerification.PendingChallenges {
				if strings.Contains(ch.Value, "Call GET") ||
					strings.Contains(ch.Value, "Add TXT record") ||
					strings.Contains(ch.Value, "provided during verification") {
					needsFetch = true
					break
				}
			}
		}
	}

	// If manual DNS mode and challenges need to be fetched, get them from Let's Encrypt
	if certificate.DNSVerification.Provider == "manual" && needsFetch &&
		(certificate.Status == "pending_dns" || certificate.Status == "verification_failed") {
		h.logger.Infof("Fetching real DNS challenges from Let's Encrypt for certificate %s", certIDStr)

		_, err := h.manager.GetDNSChallengesFromLetsEncrypt(ctx, certID, project)
		if err != nil {
			h.logger.Errorf("Failed to get DNS challenges from Let's Encrypt: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   "failed to get DNS challenges from Let's Encrypt",
				"details": err.Error(),
				"hint":    "Please check logs or try using automatic DNS verification",
			})
			return
		}

		// Reload certificate to get updated challenges
		certificate, err = h.manager.GetCertificate(ctx, certID, project)
		if err != nil {
			h.logger.Errorf("Failed to reload certificate: %v", err)
		}
	}

	// Return DNS challenges
	c.JSON(http.StatusOK, gin.H{
		"message": "DNS challenges retrieved successfully",
		"data": map[string]any{
			"status":             certificate.Status,
			"pending_challenges": certificate.DNSVerification.PendingChallenges,
			"provider":           certificate.DNSVerification.Provider,
		},
	})
}

// VerifyDNS triggers DNS verification for a pending certificate
// POST /api/v3/acme/certificates/:cert_id/verify?project=<project-id>
// Authorization: Editor+ (with permission check)
func (h *ACMEHandler) VerifyDNS(c *gin.Context) {
	ctx := c.Request.Context()
	userDetails, _ := GetUserDetails(c)

	// Editor+ can verify certificates
	if userDetails.Role == models.RoleViewer {
		c.JSON(http.StatusForbidden, gin.H{"error": "viewers cannot verify certificates"})
		return
	}

	// Get certificate ID from URL parameter
	certIDStr := c.Param("cert_id")
	certID, err := primitive.ObjectIDFromHex(certIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid certificate ID"})
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

	// Get certificate
	certificate, err := h.manager.GetCertificate(ctx, certID, project)
	if err != nil {
		h.logger.Errorf("Failed to get certificate: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusNotFound, gin.H{"error": "certificate not found"})
		return
	}

	// Check user has permission to modify this certificate
	if !acme.CanModifyResource(userDetails, certificate.Permissions, certificate.CreatedBy) {
		c.JSON(http.StatusForbidden, gin.H{"error": "insufficient permissions to verify this certificate"})
		return
	}

	// Verify DNS and complete certificate issuance
	updatedCert, err := h.manager.VerifyCertificate(ctx, certIDStr, project)
	if err != nil {
		h.logger.Errorf("Failed to verify certificate: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("verification failed: %s", err.Error())})
		return
	}

	h.logger.Infof("Certificate verified successfully (ID: %s)", certIDStr)

	// Set audit context
	h.auditHelper.SetCertificateAuditContext(c, "VERIFY_CERTIFICATE_DNS", certIDStr, updatedCert.SecretName, project)
	h.auditHelper.SetAuditSuccess(c)

	c.JSON(http.StatusOK, gin.H{
		"message":     "Certificate verified and issued successfully",
		"certificate": updatedCert,
	})
}

// RenewCertificate manually renews a certificate
// POST /api/v3/acme/certificates/:cert_id/renew?project=<project-id>
// Authorization: Admin+ or certificate creator
func (h *ACMEHandler) RenewCertificate(c *gin.Context) {
	ctx := c.Request.Context()
	userDetails, _ := GetUserDetails(c)

	// Only Admin/Owner can manually renew
	if !h.checkAdminOrOwner(c, userDetails) {
		return
	}

	// Get certificate ID from URL parameter
	certIDStr := c.Param("cert_id")
	certID, err := primitive.ObjectIDFromHex(certIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid certificate ID"})
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

	// Get certificate
	certificate, err := h.manager.GetCertificate(ctx, certID, project)
	if err != nil {
		h.logger.Errorf("Failed to get certificate: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusNotFound, gin.H{"error": "certificate not found"})
		return
	}

	// Allow renewal for active certs and for certs that previously issued but failed
	// during a renewal attempt (verification_failed / renewal_failed). A non-empty
	// SecretVersions list means the cert was active at some point, so retrying renewal
	// is safe — the alternative is being permanently stuck after one failed attempt.
	switch certificate.Status {
	case "active", "renewal_failed":
		// ok
	case "verification_failed":
		if len(certificate.SecretVersions) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "certificate has never been issued; use retry-verification instead"})
			return
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("cannot renew certificate in status: %s", certificate.Status)})
		return
	}

	// Check if certificate uses automatic DNS provider
	if certificate.DNSVerification.DNSCredentialID != nil && !certificate.DNSVerification.DNSCredentialID.IsZero() {
		// AUTOMATIC DNS - ASYNC PATH (runs in background job)
		h.logger.Infof("Starting automatic DNS renewal for certificate %s (async job)", certIDStr)

		// 1. Validate DNS credential still exists and user has access
		dnsCredential, _, err := h.manager.GetDNSCredential(ctx, *certificate.DNSVerification.DNSCredentialID, project)
		if err != nil {
			h.logger.Errorf("Failed to get DNS credential: %v", err)
			h.auditHelper.SetAuditError(c, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "DNS credential not found or inaccessible"})
			return
		}

		// 2. Validate ACME account still exists
		if certificate.ACME.AccountID == nil || certificate.ACME.AccountID.IsZero() {
			c.JSON(http.StatusBadRequest, gin.H{"error": "certificate has no ACME account reference"})
			return
		}

		_, err = h.manager.GetACMEAccount(ctx, *certificate.ACME.AccountID, project)
		if err != nil {
			h.logger.Errorf("Failed to get ACME account: %v", err)
			h.auditHelper.SetAuditError(c, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "ACME account not found or inaccessible"})
			return
		}

		// 3. Prepare renewal temporary key (required by async worker)
		// Generate new private key and CSR for renewal
		privateKeyPEM, csrPEM, err := h.manager.PrepareRenewalKey(ctx, certificate.Domains)
		if err != nil {
			h.logger.Errorf("Failed to prepare renewal key: %v", err)
			h.auditHelper.SetAuditError(c, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to prepare renewal key"})
			return
		}

		// Store temporary key for async worker to use
		err = h.manager.CreateOrUpdateTempKeyForRenewal(ctx, certID, project, certificate.SecretName, privateKeyPEM, csrPEM)
		if err != nil {
			h.logger.Errorf("Failed to store temporary key: %v", err)
			h.auditHelper.SetAuditError(c, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store temporary key"})
			return
		}

		h.logger.Infof("Created temporary key for renewal of certificate %s", certIDStr)

		// 4. Create async job for DNS renewal
		job, err := h.asyncSystem.CreateACMEVerificationJob(ctx, &async.CreateACMEJobRequest{
			CertificateID:   certIDStr,
			CertificateName: certificate.SecretName,
			Domains:         certificate.Domains,
			Environment:     certificate.ACME.Environment,
			DNSCredentialID: certificate.DNSVerification.DNSCredentialID.Hex(),
			ACMEAccountID:   certificate.ACME.AccountID.Hex(),
			Project:         project,
			Versions:        certificate.SecretVersions,
			TriggerUser:     userDetails,
			IsRenewal:       true, // This is a renewal operation
		})
		if err != nil {
			h.logger.Errorf("Failed to create renewal job: %v", err)
			h.auditHelper.SetAuditError(c, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create renewal job"})
			return
		}

		h.logger.Infof("Created renewal job %s for certificate %s", job.JobID, certIDStr)

		// 3.1. Update certificate status to "renewal_pending" and set job ID
		err = h.manager.UpdateCertificateStatusAndJobID(ctx, certIDStr, project, "renewal_pending", job.JobID)
		if err != nil {
			h.logger.Errorf("Failed to update certificate status: %v", err)
			h.auditHelper.SetAuditError(c, err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Failed to update certificate after creating renewal job",
			})
			return
		}

		// Set audit context
		h.auditHelper.SetCertificateAuditContext(c, "RENEW_CERTIFICATE", certIDStr, certificate.SecretName, project)
		h.auditHelper.SetAuditSuccess(c)

		// 4. Return immediately with job ID
		c.JSON(http.StatusOK, gin.H{
			"message": "Certificate renewal initiated. DNS verification running in background.",
			"data": gin.H{
				"certificate_id": certIDStr,
				"job_id":         job.JobID,
				"status":         "renewal_pending", // Status updated to renewal_pending
				"dns_provider":   dnsCredential.Provider,
			},
		})
		return
	}

	// MANUAL DNS or NO DNS CREDENTIAL - SYNCHRONOUS PATH
	h.logger.Infof("Starting manual DNS renewal for certificate %s (synchronous)", certIDStr)

	// Renew certificate synchronously
	updatedCert, err := h.manager.RenewCertificate(ctx, certIDStr, project)
	if err != nil {
		h.logger.Errorf("Failed to renew certificate: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("renewal failed: %s", err.Error())})
		return
	}

	h.logger.Infof("Certificate renewed successfully (ID: %s)", certIDStr)

	// Set audit context
	h.auditHelper.SetCertificateAuditContext(c, "RENEW_CERTIFICATE", certIDStr, updatedCert.SecretName, project)
	h.auditHelper.SetAuditSuccess(c)

	c.JSON(http.StatusOK, gin.H{
		"message":     "Certificate renewed successfully",
		"certificate": updatedCert,
	})
}

// ========== DNS Credentials Management Endpoints ==========

// CreateDNSCredential creates a new DNS provider credential
// POST /api/v3/acme/dns-credentials?project=<project-id>
// Authorization: Admin+ (Editor cannot create DNS credentials)
func (h *ACMEHandler) CreateDNSCredential(c *gin.Context) {
	ctx := c.Request.Context()
	userDetails, _ := GetUserDetails(c)

	// Only Admin/Owner can create DNS credentials
	if !h.checkAdminOrOwner(c, userDetails) {
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
	var req CreateDNSCredentialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid request: %s", err.Error())})
		return
	}

	// Validate provider
	if err := acme.ValidateProvider(req.Provider); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Convert credentials to JSON string
	credentialsJSON, err := h.credentialsToJSON(req.Credentials)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid credentials format: %s", err.Error())})
		return
	}

	// Create DNS credential object
	credential := &acme.DNSCredential{
		Project:     project,
		Name:        req.Name,
		Description: req.Description,
		Provider:    req.Provider,
		Status:      acme.CredentialStatusActive,
		Permissions: req.Permissions,
		CreatedBy:   userDetails.UserID,
	}

	// Create credential in database (manager handles encryption)
	if err := h.manager.CreateDNSCredential(ctx, credential, credentialsJSON); err != nil {
		h.logger.Errorf("Failed to create DNS credential: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to create DNS credential: %s", err.Error())})
		return
	}

	h.logger.Infof("DNS credential created: %s (provider: %s, project: %s)", credential.Name, credential.Provider, project)

	// Set audit context
	h.auditHelper.SetDNSCredentialAuditContext(c, "CREATE_DNS_CREDENTIAL", credential.ID.Hex(), credential.Name, project)
	h.auditHelper.SetAuditSuccess(c)

	// Return sanitized response (without encrypted credentials)
	c.JSON(http.StatusOK, gin.H{
		"message": "DNS credential created successfully",
		"data":    acme.SanitizeCredentialForDisplay(credential),
	})
}

// ListDNSCredentials lists all DNS credentials for a project with permission filtering
// GET /api/v3/acme/dns-credentials?project=<project-id>
// Authorization: Viewer+ (with permission filtering)
func (h *ACMEHandler) ListDNSCredentials(c *gin.Context) {
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

	// List credentials with permission filtering
	credentials, err := h.manager.ListDNSCredentials(ctx, project, userDetails)
	if err != nil {
		h.logger.Errorf("Failed to list DNS credentials: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list DNS credentials: %s", err.Error())})
		return
	}

	// Sanitize all credentials (remove encrypted data)
	sanitized := make([]map[string]any, len(credentials))
	for i, cred := range credentials {
		sanitized[i] = acme.SanitizeCredentialForDisplay(cred)
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "DNS credentials retrieved successfully",
		"data":    sanitized,
	})
}

// GetDNSCredential retrieves a specific DNS credential by ID
// GET /api/v3/acme/dns-credentials/:cred_id?project=<project-id>
// Authorization: Viewer+ (with permission check)
func (h *ACMEHandler) GetDNSCredential(c *gin.Context) {
	ctx := c.Request.Context()
	userDetails, _ := GetUserDetails(c)

	// Get credential ID from URL parameter
	credIDStr := c.Param("cred_id")
	credID, err := primitive.ObjectIDFromHex(credIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid credential ID"})
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

	// Get credential (WITHOUT decrypted credentials for security)
	credential, _, err := h.manager.GetDNSCredential(ctx, credID, project)
	if err != nil {
		h.logger.Errorf("Failed to get DNS credential: %v", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "DNS credential not found"})
		return
	}

	// Check user has permission to view this credential
	if !acme.HasPermission(userDetails, credential.Permissions) {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied to this DNS credential"})
		return
	}

	// Return sanitized response (without decrypted credentials)
	c.JSON(http.StatusOK, gin.H{
		"message": "DNS credential retrieved successfully",
		"data":    acme.SanitizeCredentialForDisplay(credential),
	})
}

// UpdateDNSCredential updates an existing DNS credential
// PUT /api/v3/acme/dns-credentials/:cred_id?project=<project-id>
// Authorization: Admin+ (Editor cannot update DNS credentials)
func (h *ACMEHandler) UpdateDNSCredential(c *gin.Context) {
	ctx := c.Request.Context()
	userDetails, _ := GetUserDetails(c)

	// Only Admin/Owner can update DNS credentials
	if !h.checkAdminOrOwner(c, userDetails) {
		return
	}

	// Get credential ID from URL parameter
	credIDStr := c.Param("cred_id")
	credID, err := primitive.ObjectIDFromHex(credIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid credential ID"})
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
	var req UpdateDNSCredentialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid request: %s", err.Error())})
		return
	}

	// Get existing credential to verify it exists
	_, _, err = h.manager.GetDNSCredential(ctx, credID, project)
	if err != nil {
		h.logger.Errorf("Failed to get DNS credential: %v", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "DNS credential not found"})
		return
	}

	// Build updates map for fields that are provided
	updates := make(map[string]any)
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.Permissions != nil {
		updates["permissions"] = *req.Permissions
	}

	// If new credentials provided, validate and prepare for encryption
	var credentialsJSON string
	if req.Credentials != nil {
		credentialsJSON, err = h.credentialsToJSON(req.Credentials)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid credentials format: %s", err.Error())})
			return
		}
	}

	// Update credential in database
	if err := h.manager.UpdateDNSCredential(ctx, credID, project, updates, credentialsJSON); err != nil {
		h.logger.Errorf("Failed to update DNS credential: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to update DNS credential: %s", err.Error())})
		return
	}

	h.logger.Infof("DNS credential updated: %s (project: %s)", credIDStr, project)

	// Reload credential and return sanitized response
	updatedCred, _, err := h.manager.GetDNSCredential(ctx, credID, project)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "DNS credential updated successfully"})
		return
	}

	// Set audit context
	h.auditHelper.SetDNSCredentialAuditContext(c, "UPDATE_DNS_CREDENTIAL", credIDStr, updatedCred.Name, project)
	h.auditHelper.SetAuditSuccess(c)

	c.JSON(http.StatusOK, gin.H{
		"message": "DNS credential updated successfully",
		"data":    acme.SanitizeCredentialForDisplay(updatedCred),
	})
}

// DeleteDNSCredential deletes a DNS credential
// DELETE /api/v3/acme/dns-credentials/:cred_id?project=<project-id>
// Authorization: Admin+ (Editor cannot delete DNS credentials)
func (h *ACMEHandler) DeleteDNSCredential(c *gin.Context) {
	ctx := c.Request.Context()
	userDetails, _ := GetUserDetails(c)

	// Only Admin/Owner can delete DNS credentials
	if !h.checkAdminOrOwner(c, userDetails) {
		return
	}

	// Get credential ID from URL parameter
	credIDStr := c.Param("cred_id")
	credID, err := primitive.ObjectIDFromHex(credIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid credential ID"})
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

	// Get force parameter
	force := c.Query("force") == "true"

	// Get credential name BEFORE deletion for audit logging
	credential, _, err := h.manager.GetDNSCredential(ctx, credID, project)
	if err != nil {
		h.logger.Errorf("Failed to get DNS credential: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusNotFound, gin.H{"error": "DNS credential not found"})
		return
	}

	// Delete credential with usage check
	err = h.manager.DeleteDNSCredential(ctx, credID, project, force)
	if err != nil {
		// Check if it's a DNSCredentialInUseError
		var credInUseErr *acme.DNSCredentialInUseError
		if errors.As(err, &credInUseErr) {
			h.auditHelper.SetAuditError(c, credInUseErr)
			c.JSON(http.StatusBadRequest, gin.H{
				"error": credInUseErr.Error(),
				"details": gin.H{
					"certificate_count": credInUseErr.CertCount,
					"certificate_ids":   credInUseErr.CertificateIDs,
					"warning":           "Deleting this credential will disable automatic renewal for these certificates. Use force=true to proceed.",
				},
			})
			return
		}

		h.logger.Errorf("Failed to delete DNS credential: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to delete DNS credential: %s", err.Error())})
		return
	}

	h.logger.Infof("DNS credential deleted: %s (project: %s, force: %v)", credIDStr, project, force)

	// Set audit context (use credential name fetched before deletion)
	h.auditHelper.SetDNSCredentialAuditContext(c, "DELETE_DNS_CREDENTIAL", credIDStr, credential.Name, project)
	h.auditHelper.SetAuditSuccess(c)

	if force {
		c.JSON(http.StatusOK, gin.H{
			"message": "DNS credential force-deleted successfully",
			"warning": "Certificates using this credential are now orphaned and cannot auto-renew",
		})
	} else {
		c.JSON(http.StatusOK, gin.H{
			"message": "DNS credential deleted successfully",
		})
	}
}

// TestDNSCredential tests a DNS credential by attempting to create a TXT record
// POST /api/v3/acme/dns-credentials/test?project=<project-id>
// Authorization: Admin+ (Editor cannot test DNS credentials)
func (h *ACMEHandler) TestDNSCredential(c *gin.Context) {
	ctx := c.Request.Context()
	userDetails, _ := GetUserDetails(c)

	// Only Admin/Owner can test DNS credentials
	if !h.checkAdminOrOwner(c, userDetails) {
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
	var req TestDNSCredentialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid request: %s", err.Error())})
		return
	}

	// Validate provider
	if err := acme.ValidateProvider(req.Provider); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Convert credentials map to JSON string
	credentialsJSON, err := h.credentialsToJSON(req.Credentials)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid credentials format: %s", err.Error())})
		return
	}

	// Test the DNS credentials
	if err := h.manager.TestDNSCredentials(ctx, req.Provider, credentialsJSON, req.Domain); err != nil {
		h.logger.Errorf("DNS credential test failed: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "DNS credential test failed",
			"details": err.Error(),
		})
		return
	}

	h.logger.Infof("DNS credential test succeeded for provider: %s, domain: %s", req.Provider, req.Domain)

	// Set audit context (use domain as name since this is a test without saved credential)
	h.auditHelper.SetDNSCredentialAuditContext(c, "TEST_DNS_CREDENTIAL", "", fmt.Sprintf("%s:%s", req.Provider, req.Domain), project)
	h.auditHelper.SetAuditSuccess(c)

	c.JSON(http.StatusOK, gin.H{
		"message": "DNS credentials are valid and working",
		"details": gin.H{
			"provider": req.Provider,
			"domain":   req.Domain,
			"status":   "successfully created and cleaned up test DNS record",
		},
	})
}

// ChangeCertificateDNSCredential updates the DNS credential for an existing certificate
// PUT /api/v3/acme/certificates/:cert_id/dns-credential?project=<project-id>
// Authorization: Admin/Owner only
func (h *ACMEHandler) ChangeCertificateDNSCredential(c *gin.Context) {
	ctx := c.Request.Context()
	userDetails, _ := GetUserDetails(c)

	// Admin/Owner only
	if !h.checkAdminOrOwner(c, userDetails) {
		return
	}

	// Get certificate ID from URL parameter
	certIDStr := c.Param("cert_id")
	if certIDStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "certificate ID is required"})
		return
	}

	// Convert certificate ID to ObjectID
	certID, err := primitive.ObjectIDFromHex(certIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid certificate ID format"})
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
	var req ChangeDNSCredentialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid request: %s", err.Error())})
		return
	}

	// Get certificate
	certificate, err := h.manager.GetCertificate(ctx, certID, project)
	if err != nil {
		h.logger.Errorf("Failed to get certificate: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusNotFound, gin.H{"error": "certificate not found"})
		return
	}

	// Check user has permission to modify this certificate
	if !acme.HasPermission(userDetails, certificate.Permissions) {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied to this certificate"})
		return
	}

	// Validate certificate uses automatic DNS verification
	if certificate.DNSVerification.Provider == "manual" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Cannot change DNS credential for certificates using manual DNS verification",
		})
		return
	}

	// Validate new DNS credential ID format
	newCredentialID, err := primitive.ObjectIDFromHex(req.DNSCredentialID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid dns_credential_id format"})
		return
	}

	// Get and validate new DNS credential (returns credential, decrypted string, error)
	newCredential, _, err := h.manager.GetDNSCredential(ctx, newCredentialID, project)
	if err != nil {
		h.logger.Errorf("Failed to get new DNS credential: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("DNS credential not found: %s", err.Error())})
		return
	}

	// Check user has permission to use new DNS credential (for non-Owner/Admin)
	if !userDetails.IsOwner && userDetails.Role != models.RoleAdmin {
		hasPermission := false
		// Check user ID
		for _, userID := range newCredential.Permissions.Users {
			if userID == userDetails.UserID {
				hasPermission = true
				break
			}
		}
		// Check groups
		if !hasPermission {
			allGroups := append([]string{}, userDetails.Groups...)
			if userDetails.BaseGroup != "" {
				allGroups = append(allGroups, userDetails.BaseGroup)
			}
			for _, group := range newCredential.Permissions.Groups {
				for _, userGroup := range allGroups {
					if group == userGroup {
						hasPermission = true
						break
					}
				}
				if hasPermission {
					break
				}
			}
		}
		if !hasPermission {
			c.JSON(http.StatusForbidden, gin.H{"error": "access denied to this DNS credential"})
			return
		}
	}

	// Validate new credential is active
	if newCredential.Status != "active" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("DNS credential is not active (status: %s)", newCredential.Status),
		})
		return
	}

	// Update certificate DNS credential
	err = h.manager.UpdateCertificateDNSCredential(ctx, certID, project, newCredentialID, newCredential.Name, newCredential.Provider)
	if err != nil {
		h.logger.Errorf("Failed to update certificate DNS credential: %v", err)
		h.auditHelper.SetAuditError(c, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to update DNS credential: %s", err.Error()),
		})
		return
	}

	h.logger.Infof("Updated DNS credential for certificate %s (project: %s) to credential %s",
		certID.Hex(), project, newCredential.Name)

	// Set audit context
	h.auditHelper.SetCertificateAuditContext(c, "CHANGE_CERTIFICATE_DNS_CREDENTIAL", certID.Hex(), certificate.SecretName, project)
	h.auditHelper.SetAuditSuccess(c)

	c.JSON(http.StatusOK, gin.H{
		"message": "DNS credential updated successfully",
		"data": gin.H{
			"certificate_id":     certID.Hex(),
			"new_dns_credential": newCredential.Name,
			"new_provider":       newCredential.Provider,
		},
	})
}

// ========== Helper Methods ==========

// checkAdminOrOwner checks if user is Admin or Owner
func (h *ACMEHandler) checkAdminOrOwner(c *gin.Context, userDetails models.UserDetails) bool {
	if !userDetails.IsOwner && userDetails.Role != models.RoleAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "only admins and owners can perform this operation"})
		return false
	}
	return true
}

// validateProjectAccess validates that user has access to the project
func (h *ACMEHandler) validateProjectAccess(_ context.Context, c *gin.Context, userDetails models.UserDetails, project string) error {
	// Owner has access to all projects
	if userDetails.IsOwner {
		return nil
	}

	// Check if user has access to this project
	hasAccess := false
	for _, p := range userDetails.Projects {
		if p == project {
			hasAccess = true
			break
		}
	}

	if !hasAccess {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied to this project"})
		return fmt.Errorf("access denied")
	}

	return nil
}

// credentialsToJSON converts credentials map to JSON string
func (h *ACMEHandler) credentialsToJSON(credentials map[string]any) (string, error) {
	// Convert map to JSON string
	// In a real implementation, we would validate the structure based on provider
	jsonBytes, err := json.Marshal(credentials)
	if err != nil {
		return "", fmt.Errorf("failed to marshal credentials: %w", err)
	}
	return string(jsonBytes), nil
}

// ========== Audit Helper Functions ==========
// Audit logging is now handled by ACMEAuditHelper in audit_acme.go
