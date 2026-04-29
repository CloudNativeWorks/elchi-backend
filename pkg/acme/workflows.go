package acme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// InitiateCertificate creates a new certificate request with DNS challenges for multiple versions
// This is the first step in the manual DNS verification workflow
func (m *CertificateManager) InitiateCertificate(
	ctx context.Context,
	domains []string,
	environment string,
	acmeAccountID primitive.ObjectID,
	project string,
	secretName string,
	secretVersions []string,
	user models.UserDetails,
) (*ACMECertificate, error) {
	// Validate inputs
	if len(domains) == 0 {
		return nil, fmt.Errorf("domains list cannot be empty")
	}
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}
	if secretName == "" {
		return nil, fmt.Errorf("secret name is required")
	}
	if len(secretVersions) == 0 {
		return nil, fmt.Errorf("at least one version is required")
	}

	// Check if certificate already exists for this secret name
	existingCert, err := m.getCertificateByName(ctx, secretName, project)
	if err == nil && existingCert != nil {
		return nil, fmt.Errorf("certificate with name '%s' already exists. Use duplicate endpoint to add new versions", secretName)
	}

	// Validate environment
	if environment != "staging" && environment != "production" {
		return nil, fmt.Errorf("environment must be 'staging' or 'production'")
	}

	// Get and validate ACME account
	acmeAccount, err := m.GetACMEAccount(ctx, acmeAccountID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get ACME account: %w", err)
	}

	// Verify environment matches
	if acmeAccount.Environment != environment {
		return nil, fmt.Errorf("certificate environment '%s' does not match ACME account environment '%s'", environment, acmeAccount.Environment)
	}

	// Check account is registered
	if !acmeAccount.IsRegistered || acmeAccount.Status != AccountStatusRegistered {
		return nil, fmt.Errorf("ACME account is not active (status: %s)", acmeAccount.Status)
	}

	// Generate certificate private key (RSA for certificate)
	certPrivateKey, err := GenerateRSAPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate certificate private key: %w", err)
	}

	// Create CSR
	csr, err := CreateCSR(certPrivateKey, domains)
	if err != nil {
		return nil, fmt.Errorf("failed to create CSR: %w", err)
	}

	// For manual DNS-01 verification, we create placeholder challenges
	// The actual challenge tokens will be obtained from Let's Encrypt during the verification step
	// This is because Lego library doesn't expose a way to get challenges without starting verification
	// User needs to call the verify endpoint which will:
	// 1. Create a new ACME order
	// 2. Get DNS-01 challenges from Let's Encrypt
	// 3. Present challenges (manual mode - user must add TXT records)
	// 4. Wait for user to confirm DNS propagation
	// 5. Complete ACME challenge verification
	// 6. Obtain certificate
	now := time.Now()
	pendingChallenges := make([]DNSChallenge, 0, len(domains))
	for _, domain := range domains {
		// Placeholder - actual TXT record value will be generated during verification
		pendingChallenges = append(pendingChallenges, DNSChallenge{
			Domain:    domain,
			Type:      "TXT",
			Value:     fmt.Sprintf("Add TXT record _acme-challenge.%s with the value provided during verification", domain),
			CreatedAt: now,
			ExpiresAt: now.Add(24 * time.Hour), // Give 24 hours for manual setup
		})
	}

	// Create certificate metadata (NEW certificate with multiple versions)
	certID := primitive.NewObjectID()

	cert := &ACMECertificate{
		ID:             certID,
		Project:        project,
		SecretName:     secretName,
		SecretVersions: secretVersions, // Multiple versions from request
		Domains:        domains,
		PrimaryDomain:  domains[0],
		ACME: ACMEMetadata{
			AccountID:   &acmeAccountID, // NEW: Reference to ACME account
			CertURL:     "",             // Will be set after verification
			Environment: environment,
		},
		DNSVerification: DNSVerification{
			Provider:          "manual",
			PendingChallenges: pendingChallenges,
		},
		Status:          "pending_dns",
		IssuedAt:        time.Time{}, // Not issued yet
		ExpiresAt:       time.Time{}, // Not issued yet
		RenewalStartsAt: time.Time{}, // Not issued yet
		AutoRenew:       true,
		RenewalAttempts: 0,
		LastError:       nil, // No errors yet
		Permissions: Permissions{
			Users:  []string{user.UserID},
			Groups: user.Groups,
		},
		CreatedBy: user.UserID,
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Increment account usage counter
	if err := m.incrementAccountUsage(ctx, acmeAccountID, project); err != nil {
		m.logger.Warnf("Failed to increment account usage: %v", err)
	}

	m.logger.Infof("Creating certificate %s for %d version(s): %v", secretName, len(secretVersions), secretVersions)

	// Store certificate metadata
	if err := m.CreateCertificate(ctx, cert); err != nil {
		return nil, fmt.Errorf("failed to store certificate metadata: %w", err)
	}

	// Store temporary private key and CSR
	certKeyPEM, err := EncodePrivateKeyToPEM(certPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to convert certificate key to PEM: %w", err)
	}

	csrPEM := EncodeCSRToPEM(csr)

	tempKey := &TempKey{
		ID:            primitive.NewObjectID(),
		Project:       project,
		CertRequestID: certID,
		SecretName:    secretName,
		PrivateKeyPEM: certKeyPEM,
		CSRPEM:        csrPEM,
		Status:        "pending",
	}

	if err := m.CreateTempKey(ctx, tempKey); err != nil {
		// Clean up certificate metadata
		_ = m.DeleteCertificate(ctx, certID, project)
		return nil, fmt.Errorf("failed to store temporary key: %w", err)
	}

	return cert, nil
}

// GetDNSChallengesFromLetsEncrypt creates an ACME order and retrieves real DNS challenge values from Let's Encrypt
// This is Step 1 of manual DNS verification - user gets the TXT record values to add to their DNS
func (m *CertificateManager) GetDNSChallengesFromLetsEncrypt(ctx context.Context, certID primitive.ObjectID, project string) ([]DNSChallenge, error) {
	// Get certificate metadata
	cert, err := m.GetCertificate(ctx, certID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get certificate: %w", err)
	}

	// Check status - only allow for pending_dns or verification_failed
	if cert.Status != "pending_dns" && cert.Status != "verification_failed" {
		return nil, fmt.Errorf("certificate is not in pending_dns or verification_failed status (current: %s)", cert.Status)
	}

	if cert.ACME.AccountID == nil {
		return nil, fmt.Errorf("ACME account ID is required")
	}

	// Create order-capable ACME client
	m.logger.Infof("Creating ACME order client for domains: %v", cert.Domains)
	m.logger.Infof("Using ACME environment: %s", cert.ACME.Environment)

	// Get ACME account (required)
	if cert.ACME.AccountID == nil {
		return nil, fmt.Errorf("ACME account ID is required")
	}

	acmeAccount, err := m.GetACMEAccount(ctx, *cert.ACME.AccountID, project)
	if err != nil {
		return nil, fmt.Errorf("ACME account not found: %w", err)
	}

	accountKeyPEM, err := m.encryptor.Decrypt(acmeAccount.AccountKeyEncrypted)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt account key: %w", err)
	}

	key, err := DecodePrivateKeyFromPEM(accountKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to parse account key: %w", err)
	}

	accountKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("account key is not an ECDSA private key")
	}

	orderClient, err := NewACMEOrderClient(acmeAccount.Email, cert.ACME.Environment, accountKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create ACME order client: %w", err)
	}

	// Create ACME order and capture challenges
	orderURL, challenges, err := orderClient.CreateOrderAndCaptureChallenge(cert.Domains)
	if err != nil {
		// Check if it's a rate limit error
		if strings.Contains(err.Error(), "rateLimited") || strings.Contains(err.Error(), "429") {
			return nil, fmt.Errorf("let's Encrypt rate limit exceeded: too many failed authorizations for this domain in the last hour. Please wait 1 hour or use a different subdomain. Original error: %w", err)
		}
		return nil, fmt.Errorf("failed to create ACME order and capture challenges: %w", err)
	}

	m.logger.Infof("ACME order created successfully: %s", orderURL)

	if len(challenges) == 0 {
		return nil, fmt.Errorf("no challenges were captured from ACME order. This might indicate: 1) ACME order creation failed, 2) Invalid domain format, or 3) ACME protocol issue")
	}

	m.logger.Infof("Successfully created ACME order: %s", orderURL)
	m.logger.Infof("Successfully captured %d DNS challenges from Let's Encrypt", len(challenges))
	now := time.Now()

	// Update certificate with order URL and new challenges
	collection := m.db.Collection("acme_certificates")
	filter := bson.M{"_id": certID, "project": project}
	update := bson.M{
		"$set": bson.M{
			"acme.order_url":                      orderURL, // CRITICAL: Store order URL for verification
			"dns_verification.pending_challenges": challenges,
			"updated_at":                          now,
		},
	}

	if _, err := collection.UpdateOne(ctx, filter, update); err != nil {
		m.logger.Warnf("Failed to update certificate challenges: %v", err)
	}

	return challenges, nil
}

// VerifyCertificate verifies DNS challenges and completes certificate issuance
func (m *CertificateManager) VerifyCertificate(ctx context.Context, certID string, project string) (*ACMECertificate, error) {
	// Validate inputs
	if certID == "" {
		return nil, fmt.Errorf("certificate ID is required")
	}
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}

	// Convert certID to ObjectID
	certObjID, err := primitive.ObjectIDFromHex(certID)
	if err != nil {
		return nil, fmt.Errorf("invalid certificate ID: %w", err)
	}

	// Get certificate metadata
	cert, err := m.GetCertificate(ctx, certObjID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get certificate: %w", err)
	}

	// Check status - Allow retry for failed verifications
	if cert.Status != "pending_dns" && cert.Status != "verification_failed" {
		return nil, fmt.Errorf("certificate is not in pending_dns or verification_failed status (current: %s)", cert.Status)
	}

	// Get temporary key
	tempKey, err := m.GetTempKey(ctx, certObjID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get temporary key: %w", err)
	}

	// Parse private key
	privateKey, err := DecodePrivateKeyFromPEM(tempKey.PrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	if cert.ACME.AccountID == nil {
		_ = m.updateCertificateStatus(ctx, certObjID, project, "verification_failed", "ACME account ID is required")
		return nil, fmt.Errorf("ACME account ID is required")
	}

	// Complete DNS verification - different approach for manual vs automatic
	var certPEM []byte

	switch {
	case cert.DNSVerification.Provider == "manual":
		// MANUAL DNS: Use order-based verification to reuse existing ACME order
		m.logger.Infof("Starting manual DNS verification for domains: %v", cert.Domains)

		// Check if we have an order URL
		if cert.ACME.OrderURL == "" {
			_ = m.updateCertificateStatus(ctx, certObjID, project, "verification_failed", "No ACME order URL found")
			return nil, fmt.Errorf("no ACME order URL found - please call GET /dns-challenges first to create an order")
		}

		m.logger.Infof("Using existing ACME order: %s", cert.ACME.OrderURL)

		// Get ACME account (required)
		if cert.ACME.AccountID == nil {
			_ = m.updateCertificateStatus(ctx, certObjID, project, "verification_failed", "ACME account ID is required")
			return nil, fmt.Errorf("ACME account ID is required")
		}

		acmeAccount, err := m.GetACMEAccount(ctx, *cert.ACME.AccountID, project)
		if err != nil {
			return nil, fmt.Errorf("ACME account not found: %w", err)
		}

		accountKeyPEM, err := m.encryptor.Decrypt(acmeAccount.AccountKeyEncrypted)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt account key: %w", err)
		}

		key, err := DecodePrivateKeyFromPEM(accountKeyPEM)
		if err != nil {
			return nil, fmt.Errorf("failed to parse account key: %w", err)
		}

		accountKey, ok := key.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("account key is not an ECDSA private key")
		}

		// Create order client for verification
		orderClient, err := NewACMEOrderClient(acmeAccount.Email, cert.ACME.Environment, accountKey)
		if err != nil {
			return nil, fmt.Errorf("failed to create ACME order client: %w", err)
		}

		// Use order-based verification to reuse the same order from GET /dns-challenges
		certPEM, err = orderClient.VerifyExistingOrder(cert.ACME.OrderURL, cert.Domains, privateKey)
		if err != nil {
			m.logger.Errorf("DNS verification failed: %v", err)
			_ = m.updateCertificateStatus(ctx, certObjID, project, "verification_failed", err.Error())
			return nil, fmt.Errorf("DNS verification failed: %w. If DNS records are correct, wait 5-10 minutes for propagation and retry. If verification keeps failing after 1 hour, use refresh=true parameter on GET /dns-challenges endpoint to get new challenge values", err)
		}
	case cert.DNSVerification.DNSCredentialID != nil:
		// AUTOMATIC DNS: Use traditional ObtainCertificate (creates new order each time, but that's OK for automatic)
		m.logger.Infof("Starting automatic DNS verification for domains: %v", cert.Domains)
		m.logger.Infof("Setting up automatic DNS provider: %s", cert.DNSVerification.Provider)

		// Get ACME account for automatic DNS
		acmeAccount, err := m.GetACMEAccount(ctx, *cert.ACME.AccountID, project)
		if err != nil {
			return nil, fmt.Errorf("ACME account not found: %w", err)
		}

		accountKeyPEM, err := m.encryptor.Decrypt(acmeAccount.AccountKeyEncrypted)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt account key: %w", err)
		}

		key, err := DecodePrivateKeyFromPEM(accountKeyPEM)
		if err != nil {
			return nil, fmt.Errorf("failed to parse account key: %w", err)
		}

		accountKey, ok := key.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("account key is not an ECDSA private key")
		}

		// Create ACME client for automatic DNS
		acmeClient, err := NewACMEClientWithRegistration(
			acmeAccount.Email,
			CAProvider(acmeAccount.CAProvider),
			acmeAccount.Environment,
			accountKey,
			acmeAccount.RegistrationURL,
			m.caProviders,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create ACME client: %w", err)
		}

		dnsProvider, err := m.SetupDNSProvider(ctx, *cert.DNSVerification.DNSCredentialID, project)
		if err != nil {
			_ = m.updateCertificateStatus(ctx, certObjID, project, "verification_failed", err.Error())
			return nil, fmt.Errorf("failed to setup DNS provider: %w", err)
		}

		// Set DNS provider on ACME client
		// CRITICAL: Disable authoritative NS propagation check to avoid split-horizon DNS issues
		// NOTE: Removed RecursiveNSsPropagationRequirement() as it was causing SERVFAIL errors
		//       Let lego use its internal DNS resolution without forcing recursive nameserver checks
		err = acmeClient.GetClient().Challenge.SetDNS01Provider(
			dnsProvider,
			dns01.DisableAuthoritativeNssPropagationRequirement(),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to set DNS provider: %w", err)
		}

		// ObtainCertificate for automatic DNS (provider creates/deletes DNS records automatically)
		certResource, err := acmeClient.ObtainCertificate(cert.Domains, privateKey)
		if err != nil {
			m.logger.Errorf("DNS verification failed: %v", err)
			_ = m.updateCertificateStatus(ctx, certObjID, project, "verification_failed", err.Error())
			return nil, fmt.Errorf("DNS verification failed: %w", err)
		}
		certPEM = certResource.Certificate
	default:
		return nil, fmt.Errorf("no DNS verification method specified")
	}

	// Store certificate in secrets collection
	if err := m.storeCertificateInSecrets(ctx, cert, certPEM, privateKey, project); err != nil {
		return nil, fmt.Errorf("failed to store certificate in secrets: %w", err)
	}

	// Update certificate metadata
	now := time.Now()
	expiresAt := now.Add(90 * 24 * time.Hour)              // Let's Encrypt certificates are valid for 90 days
	renewalStartsAt := expiresAt.Add(-14 * 24 * time.Hour) // Start renewal 14 days before expiry

	update := bson.M{
		"$set": bson.M{
			"acme.order_url":                      "", // Clear order URL after successful verification
			"status":                              "active",
			"issued_at":                           now,
			"expires_at":                          expiresAt,
			"renewal_starts_at":                   renewalStartsAt,
			"renewal_attempts":                    0,
			"last_renewal_error":                  "",
			"dns_verification.pending_challenges": []DNSChallenge{}, // Clear challenges
			"updated_at":                          now,
		},
	}

	collection := m.db.Collection("acme_certificates")
	filter := bson.M{
		"_id":     certObjID,
		"project": project, // PROJECT ISOLATION
	}

	result, err := collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return nil, fmt.Errorf("failed to update certificate metadata: %w", err)
	}

	if result.MatchedCount == 0 {
		return nil, fmt.Errorf("certificate not found")
	}

	// Delete temporary key
	_ = m.DeleteTempKey(ctx, certObjID, project)

	// Get updated certificate
	updatedCert, err := m.GetCertificate(ctx, certObjID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get updated certificate: %w", err)
	}

	return updatedCert, nil
}

// InitiateCertificateWithDNSProvider creates a new certificate request with automatic DNS verification for multiple versions
func (m *CertificateManager) InitiateCertificateWithDNSProvider(
	ctx context.Context,
	domains []string,
	environment string,
	acmeAccountID primitive.ObjectID,
	project string,
	secretName string,
	secretVersions []string,
	dnsCredentialID primitive.ObjectID,
	user models.UserDetails,
) (*ACMECertificate, error) {
	// Validate inputs
	if len(domains) == 0 {
		return nil, fmt.Errorf("domains list cannot be empty")
	}
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}
	if secretName == "" {
		return nil, fmt.Errorf("secret name is required")
	}
	if len(secretVersions) == 0 {
		return nil, fmt.Errorf("at least one version is required")
	}
	if dnsCredentialID.IsZero() {
		return nil, fmt.Errorf("DNS credential ID is required for automatic verification")
	}

	// Check if certificate already exists for this secret name
	existingCert, err := m.getCertificateByName(ctx, secretName, project)
	if err == nil && existingCert != nil {
		return nil, fmt.Errorf("certificate with name '%s' already exists. Use duplicate endpoint to add new versions", secretName)
	}

	// Validate environment
	if environment != "staging" && environment != "production" {
		return nil, fmt.Errorf("environment must be 'staging' or 'production'")
	}

	// Get and validate ACME account
	acmeAccount, err := m.GetACMEAccount(ctx, acmeAccountID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get ACME account: %w", err)
	}

	// Verify environment matches
	if acmeAccount.Environment != environment {
		return nil, fmt.Errorf("certificate environment '%s' does not match ACME account environment '%s'", environment, acmeAccount.Environment)
	}

	// Check account is registered
	if !acmeAccount.IsRegistered || acmeAccount.Status != AccountStatusRegistered {
		return nil, fmt.Errorf("ACME account is not active (status: %s)", acmeAccount.Status)
	}

	// Verify DNS credential exists and user has access
	dnsCred, _, err := m.GetDNSCredential(ctx, dnsCredentialID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get DNS credential: %w", err)
	}

	// Generate certificate private key (RSA for certificate)
	certPrivateKey, err := GenerateRSAPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate certificate private key: %w", err)
	}

	// Create CSR
	csr, err := CreateCSR(certPrivateKey, domains)
	if err != nil {
		return nil, fmt.Errorf("failed to create CSR: %w", err)
	}

	// Create certificate metadata
	now := time.Now()
	certID := primitive.NewObjectID()

	cert := &ACMECertificate{
		ID:             certID,
		Project:        project,
		SecretName:     secretName,
		SecretVersions: secretVersions, // Multiple versions from request
		Domains:        domains,
		PrimaryDomain:  domains[0],
		ACME: ACMEMetadata{
			AccountID:   &acmeAccountID, // NEW: Reference to ACME account
			CertURL:     "",             // Will be set after verification
			Environment: environment,
		},
		DNSVerification: DNSVerification{
			Provider:          dnsCred.Provider,
			DNSCredentialID:   &dnsCredentialID,
			DNSCredentialName: dnsCred.Name,
			PendingChallenges: []DNSChallenge{}, // Will be set during verification
		},
		Status:          "pending_verification",
		IssuedAt:        time.Time{}, // Not issued yet
		ExpiresAt:       time.Time{}, // Not issued yet
		RenewalStartsAt: time.Time{}, // Not issued yet
		AutoRenew:       true,
		RenewalAttempts: 0,
		LastError:       nil,
		Permissions: Permissions{
			Users:  []string{user.UserID},
			Groups: user.Groups,
		},
		CreatedBy: user.UserID,
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Increment account usage counter
	if err := m.incrementAccountUsage(ctx, acmeAccountID, project); err != nil {
		m.logger.Warnf("Failed to increment account usage: %v", err)
	}

	// Store certificate metadata
	if err := m.CreateCertificate(ctx, cert); err != nil {
		return nil, fmt.Errorf("failed to store certificate metadata: %w", err)
	}

	// Store temporary private key and CSR
	certKeyPEM, err := EncodePrivateKeyToPEM(certPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to convert certificate key to PEM: %w", err)
	}

	csrPEM := EncodeCSRToPEM(csr)

	tempKey := &TempKey{
		ID:            primitive.NewObjectID(),
		Project:       project,
		CertRequestID: certID,
		SecretName:    secretName,
		PrivateKeyPEM: certKeyPEM,
		CSRPEM:        csrPEM,
		Status:        "pending",
	}

	if err := m.CreateTempKey(ctx, tempKey); err != nil {
		// Clean up certificate metadata
		_ = m.DeleteCertificate(ctx, certID, project)
		return nil, fmt.Errorf("failed to store temporary key: %w", err)
	}

	// DO NOT call VerifyCertificateWithDNSProvider here!
	// For async workflow, the handler will create a background job
	// that calls VerifyCertificateWithDNSProviderAsync instead
	m.logger.Infof("Created Let's Encrypt certificate: %s", secretName)

	return cert, nil
}

// VerifyCertificateWithDNSProviderAsync is the async-compatible version with progress callbacks
// This function is designed to run in a background job with timeout handling and progress tracking
// If isRenewal is true, the status check is skipped (for renewing active certificates)
func (m *CertificateManager) VerifyCertificateWithDNSProviderAsync(
	ctx context.Context,
	certID string,
	project string,
	progressCallback func(phase string, domain string, completed int, total int),
	isRenewal bool,
) (*ACMECertificate, error) {
	// Validate inputs
	if certID == "" {
		return nil, fmt.Errorf("certificate ID is required")
	}
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}

	// Convert certID to ObjectID
	certObjID, err := primitive.ObjectIDFromHex(certID)
	if err != nil {
		return nil, fmt.Errorf("invalid certificate ID: %w", err)
	}

	// Report initial progress
	if progressCallback != nil {
		progressCallback("initializing", "", 0, 1)
	}

	// Get certificate metadata
	cert, err := m.GetCertificate(ctx, certObjID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get certificate: %w", err)
	}

	// Check status (skip for renewal - active certificates can be renewed)
	if !isRenewal {
		if cert.Status != "pending_verification" && cert.Status != "verification_failed" {
			return nil, fmt.Errorf("certificate is not in pending_verification or verification_failed status (current: %s)", cert.Status)
		}
	} else {
		// For renewal, also accept verification_failed/renewal_failed so a previously
		// issued cert that failed a renewal attempt can be retried (manual or scheduler).
		switch cert.Status {
		case "active", "renewal_pending", "renewal_failed":
			// ok
		case "verification_failed":
			if len(cert.SecretVersions) == 0 {
				return nil, fmt.Errorf("cannot renew never-issued certificate in verification_failed state")
			}
		default:
			return nil, fmt.Errorf("cannot renew certificate in status: %s", cert.Status)
		}
	}

	// Validate DNS credential
	if cert.DNSVerification.DNSCredentialID.IsZero() {
		return nil, fmt.Errorf("no DNS credential configured for automatic verification")
	}

	// Get temporary key
	tempKey, err := m.GetTempKey(ctx, certObjID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get temporary key: %w", err)
	}

	// Parse private key
	privateKey, err := DecodePrivateKeyFromPEM(tempKey.PrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	// ACME account is REQUIRED
	if cert.ACME.AccountID == nil {
		return nil, fmt.Errorf("ACME account ID is required")
	}

	// Report DNS setup phase
	if progressCallback != nil {
		progressCallback("dns_setup", cert.Domains[0], 0, len(cert.Domains))
	}

	// Get ACME account
	acmeAccount, err := m.GetACMEAccount(ctx, *cert.ACME.AccountID, project)
	if err != nil {
		return nil, fmt.Errorf("ACME account not found: %w", err)
	}

	accountKeyPEM, err := m.encryptor.Decrypt(acmeAccount.AccountKeyEncrypted)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt account key: %w", err)
	}

	key, err := DecodePrivateKeyFromPEM(accountKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to parse account key: %w", err)
	}

	accountKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("account key is not an ECDSA private key")
	}

	// Create ACME client
	acmeClient, err := NewACMEClientWithRegistration(
		acmeAccount.Email,
		CAProvider(acmeAccount.CAProvider),
		acmeAccount.Environment,
		accountKey,
		acmeAccount.RegistrationURL,
		m.caProviders,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create ACME client: %w", err)
	}

	// Setup DNS provider
	dnsProvider, err := m.SetupDNSProvider(ctx, *cert.DNSVerification.DNSCredentialID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to setup DNS provider: %w", err)
	}

	// Set DNS provider on ACME client
	// DNS resolver options (public nameservers, timeouts) are configured globally in main.go init()
	// CRITICAL: Disable authoritative NS propagation check to avoid split-horizon DNS issues
	// where authoritative NS (ns3.hepsi.io) resolves to internal IP (192.168.x.x) that pods can't reach.
	// NOTE: Removed RecursiveNSsPropagationRequirement() as it was causing SERVFAIL errors
	//       Let lego use its internal DNS resolution without forcing recursive nameserver checks
	if err := acmeClient.GetClient().Challenge.SetDNS01Provider(
		dnsProvider,
		dns01.DisableAuthoritativeNssPropagationRequirement(),
	); err != nil {
		return nil, fmt.Errorf("failed to set DNS provider: %w", err)
	}

	// Note: ACME account registration happens automatically during client creation
	// No need to call RegisterAccount() separately

	// Report DNS propagation phase (this will take the longest)
	if progressCallback != nil {
		progressCallback("dns_propagation", cert.Domains[0], 0, len(cert.Domains))
	}

	// Complete DNS verification and obtain certificate
	// This is where the timeout will most likely occur if DNS takes too long
	certResource, err := acmeClient.ObtainCertificate(cert.Domains, privateKey)
	if err != nil {
		// Check if context was canceled (timeout)
		if ctx.Err() == context.DeadlineExceeded {
			errMsg := "DNS verification timeout after 5 minutes"
			_ = m.updateCertificateStatus(ctx, certObjID, project, "verification_failed", errMsg)
			return nil, fmt.Errorf("%s", errMsg)
		}

		// Update certificate with error
		_ = m.updateCertificateStatus(ctx, certObjID, project, "verification_failed", err.Error())
		return nil, fmt.Errorf("DNS verification failed: %w", err)
	}

	// Report certificate download phase
	if progressCallback != nil {
		progressCallback("certificate_download", cert.Domains[0], len(cert.Domains), len(cert.Domains))
	}

	// Report storing secrets phase
	if progressCallback != nil {
		progressCallback("storing_secrets", "", 0, len(cert.SecretVersions))
	}

	// Store certificate in secrets collection
	if err := m.storeCertificateInSecrets(ctx, cert, certResource.Certificate, privateKey, project); err != nil {
		return nil, fmt.Errorf("failed to store certificate in secrets: %w", err)
	}

	// Update certificate metadata
	now := time.Now()
	expiresAt := now.Add(90 * 24 * time.Hour)              // Let's Encrypt certificates are valid for 90 days
	renewalStartsAt := expiresAt.Add(-14 * 24 * time.Hour) // Start renewal 14 days before expiry

	update := bson.M{
		"$set": bson.M{
			"acme.cert_url":                       certResource.CertURL,
			"status":                              "active",
			"issued_at":                           now,
			"expires_at":                          expiresAt,
			"renewal_starts_at":                   renewalStartsAt,
			"renewal_attempts":                    0,
			"last_renewal_error":                  "",
			"dns_verification.pending_challenges": []DNSChallenge{}, // Clear challenges
			"updated_at":                          now,
		},
	}

	collection := m.db.Collection("acme_certificates")
	filter := bson.M{
		"_id":     certObjID,
		"project": project, // PROJECT ISOLATION
	}

	result, err := collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return nil, fmt.Errorf("failed to update certificate metadata: %w", err)
	}

	if result.MatchedCount == 0 {
		return nil, fmt.Errorf("certificate not found")
	}

	// Delete temporary key
	_ = m.DeleteTempKey(ctx, certObjID, project)

	// Report completion
	if progressCallback != nil {
		progressCallback("storing_secrets", "", len(cert.SecretVersions), len(cert.SecretVersions))
	}

	// Get updated certificate
	updatedCert, err := m.GetCertificate(ctx, certObjID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get updated certificate: %w", err)
	}

	// NOTE: Snapshot updates are now handled asynchronously via worker jobs
	// The worker creates snapshot update jobs after certificate verification completes
	// This prevents blocking the certificate renewal process and provides better visibility
	// See: pkg/async/worker/acme_job.go - createSnapshotUpdateJobs()

	return updatedCert, nil
}

// RenewCertificate renews an existing certificate
func (m *CertificateManager) RenewCertificate(ctx context.Context, certID string, project string) (*ACMECertificate, error) {
	// Validate inputs
	if certID == "" {
		return nil, fmt.Errorf("certificate ID is required")
	}
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}

	// Convert certID to ObjectID
	certObjID, err := primitive.ObjectIDFromHex(certID)
	if err != nil {
		return nil, fmt.Errorf("invalid certificate ID: %w", err)
	}

	// Get certificate metadata
	cert, err := m.GetCertificate(ctx, certObjID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get certificate: %w", err)
	}

	// Check status — allow retrying renewal after a failed attempt provided the cert
	// was previously issued (SecretVersions populated).
	switch cert.Status {
	case "active", "renewal_pending", "renewal_failed":
		// ok
	case "verification_failed":
		if len(cert.SecretVersions) == 0 {
			return nil, fmt.Errorf("cannot renew never-issued certificate in verification_failed state")
		}
	default:
		return nil, fmt.Errorf("cannot renew certificate in status: %s", cert.Status)
	}

	// For manual verification, we need to create new challenges for user to add
	if cert.DNSVerification.Provider == "manual" {
		return m.renewCertificateManual(ctx, cert, certObjID, project)
	}

	// For automatic DNS verification
	if cert.DNSVerification.DNSCredentialID == nil || cert.DNSVerification.DNSCredentialID.IsZero() {
		return nil, fmt.Errorf("no DNS credential configured for automatic renewal")
	}

	// Increment renewal attempts
	cert.RenewalAttempts++

	// Generate new private key for renewal
	newPrivateKey, err := GenerateRSAPrivateKey()
	if err != nil {
		_ = m.updateCertificateStatus(ctx, certObjID, project, "renewal_failed", err.Error())
		return nil, fmt.Errorf("failed to generate new private key: %w", err)
	}

	if cert.ACME.AccountID == nil {
		_ = m.updateCertificateStatus(ctx, certObjID, project, "renewal_failed", "ACME account ID is required")
		return nil, fmt.Errorf("ACME account ID is required")
	}

	m.logger.Infof("Renewing certificate %s with ACME account: %s", certID, cert.ACME.AccountID.Hex())

	acmeAccount, err := m.GetACMEAccount(ctx, *cert.ACME.AccountID, project)
	if err != nil {
		_ = m.updateCertificateStatus(ctx, certObjID, project, "renewal_failed", "ACME account not found")
		return nil, fmt.Errorf("ACME account not found: %w", err)
	}

	// Verify account is active
	if acmeAccount.Status != AccountStatusRegistered {
		errMsg := fmt.Sprintf("ACME account is not active (status: %s)", acmeAccount.Status)
		_ = m.updateCertificateStatus(ctx, certObjID, project, "renewal_failed", errMsg)
		return nil, fmt.Errorf("%s", errMsg)
	}

	// Decrypt account key
	accountKeyPEM, err := m.encryptor.Decrypt(acmeAccount.AccountKeyEncrypted)
	if err != nil {
		_ = m.updateCertificateStatus(ctx, certObjID, project, "renewal_failed", err.Error())
		return nil, fmt.Errorf("failed to decrypt account key: %w", err)
	}

	key, err := DecodePrivateKeyFromPEM(accountKeyPEM)
	if err != nil {
		_ = m.updateCertificateStatus(ctx, certObjID, project, "renewal_failed", err.Error())
		return nil, fmt.Errorf("failed to parse account key: %w", err)
	}

	accountKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		_ = m.updateCertificateStatus(ctx, certObjID, project, "renewal_failed", "account key is not ECDSA")
		return nil, fmt.Errorf("account key is not an ECDSA private key")
	}

	// Create ACME client
	acmeClient, err := NewACMEClientWithRegistration(
		acmeAccount.Email,
		CAProvider(acmeAccount.CAProvider),
		acmeAccount.Environment,
		accountKey,
		acmeAccount.RegistrationURL,
		m.caProviders,
	)
	if err != nil {
		_ = m.updateCertificateStatus(ctx, certObjID, project, "renewal_failed", err.Error())
		return nil, fmt.Errorf("failed to create ACME client: %w", err)
	}

	// Update last_used timestamp
	if err := m.updateAccountLastUsed(ctx, *cert.ACME.AccountID, project); err != nil {
		m.logger.Warnf("Failed to update account last_used: %v", err)
	}

	// Setup DNS provider
	dnsProvider, err := m.SetupDNSProvider(ctx, *cert.DNSVerification.DNSCredentialID, project)
	if err != nil {
		_ = m.updateCertificateStatus(ctx, certObjID, project, "renewal_failed", err.Error())
		return nil, fmt.Errorf("failed to setup DNS provider: %w", err)
	}

	// Set DNS provider on ACME client
	// DNS resolver options (public nameservers, timeouts) are configured globally in main.go init()
	// CRITICAL: Disable authoritative NS propagation check to avoid split-horizon DNS issues
	// NOTE: Removed RecursiveNSsPropagationRequirement() as it was causing SERVFAIL errors
	//       Let lego use its internal DNS resolution without forcing recursive nameserver checks
	if err := acmeClient.GetClient().Challenge.SetDNS01Provider(
		dnsProvider,
		dns01.DisableAuthoritativeNssPropagationRequirement(),
	); err != nil {
		_ = m.updateCertificateStatus(ctx, certObjID, project, "renewal_failed", err.Error())
		return nil, fmt.Errorf("failed to set DNS provider: %w", err)
	}

	// Obtain new certificate
	certResource, err := acmeClient.ObtainCertificate(cert.Domains, newPrivateKey)
	if err != nil {
		_ = m.updateCertificateStatus(ctx, certObjID, project, "renewal_failed", err.Error())
		return nil, fmt.Errorf("certificate renewal failed: %w", err)
	}

	// Store renewed certificate in secrets collection
	if err := m.storeCertificateInSecrets(ctx, cert, certResource.Certificate, newPrivateKey, project); err != nil {
		_ = m.updateCertificateStatus(ctx, certObjID, project, "renewal_failed", err.Error())
		return nil, fmt.Errorf("failed to store renewed certificate: %w", err)
	}

	// Update certificate metadata
	now := time.Now()
	expiresAt := now.Add(90 * 24 * time.Hour)
	renewalStartsAt := expiresAt.Add(-14 * 24 * time.Hour)

	update := bson.M{
		"$set": bson.M{
			"acme.cert_url":      certResource.CertURL,
			"status":             "active",
			"issued_at":          now,
			"expires_at":         expiresAt,
			"renewal_starts_at":  renewalStartsAt,
			"renewal_attempts":   0, // Reset on success
			"last_renewal_error": "",
			"last_renewal_at":    now,
			"updated_at":         now,
		},
	}

	collection := m.db.Collection("acme_certificates")
	filter := bson.M{
		"_id":     certObjID,
		"project": project,
	}

	result, err := collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return nil, fmt.Errorf("failed to update certificate metadata: %w", err)
	}

	if result.MatchedCount == 0 {
		return nil, fmt.Errorf("certificate not found")
	}

	m.logger.Infof("Certificate %s renewed successfully (project: %s)", cert.SecretName, project)

	// Get updated certificate
	updatedCert, err := m.GetCertificate(ctx, certObjID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get updated certificate: %w", err)
	}

	// Trigger snapshot update for dependent resources (TLS contexts, listeners)
	// This updates ALL versions that use this certificate
	if m.appContext != nil && m.pokeService != nil {
		m.triggerSnapshotUpdate(ctx, cert.SecretName, cert.SecretVersions, project)
	}

	return updatedCert, nil
}

// renewCertificateManual handles manual DNS renewal by creating new challenges
func (m *CertificateManager) renewCertificateManual(ctx context.Context, cert *ACMECertificate, certObjID primitive.ObjectID, project string) (*ACMECertificate, error) {
	m.logger.Infof("Starting manual DNS renewal for certificate %s", cert.SecretName)

	// ACME account is REQUIRED
	if cert.ACME.AccountID == nil {
		return nil, fmt.Errorf("ACME account ID is required for renewal")
	}

	// Get ACME account
	acmeAccount, err := m.GetACMEAccount(ctx, *cert.ACME.AccountID, project)
	if err != nil {
		return nil, fmt.Errorf("ACME account not found: %w", err)
	}

	// Verify account is active
	if acmeAccount.Status != AccountStatusRegistered {
		return nil, fmt.Errorf("ACME account is not active (status: %s)", acmeAccount.Status)
	}

	// Decrypt account key
	accountKeyPEM, err := m.encryptor.Decrypt(acmeAccount.AccountKeyEncrypted)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt account key: %w", err)
	}

	key, err := DecodePrivateKeyFromPEM(accountKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to parse account key: %w", err)
	}

	accountKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("account key is not an ECDSA private key")
	}

	// Create ACME order client
	orderClient, err := NewACMEOrderClient(acmeAccount.Email, cert.ACME.Environment, accountKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create ACME order client: %w", err)
	}

	// Create ACME order and capture challenges
	orderURL, challenges, err := orderClient.CreateOrderAndCaptureChallenge(cert.Domains)
	if err != nil {
		return nil, fmt.Errorf("failed to create ACME order and capture challenges: %w", err)
	}

	m.logger.Infof("ACME order created for renewal: %s", orderURL)

	if len(challenges) == 0 {
		return nil, fmt.Errorf("no challenges were captured from ACME order")
	}

	// Generate new private key
	newPrivateKey, err := GenerateRSAPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate new private key: %w", err)
	}

	// Convert private key to PEM
	privateKeyPEM, err := EncodePrivateKeyToPEM(newPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to encode private key: %w", err)
	}

	// Generate CSR (fix: domains first, then privateKey)
	csrBytes, err := GenerateCSR(cert.Domains, newPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to generate CSR: %w", err)
	}

	// Convert CSR to PEM format
	csrPEM := EncodeCSRToPEM(csrBytes)

	// Update certificate status to pending_dns (renewal in progress)
	// Store order URL and challenges for later verification
	now := time.Now()
	update := bson.M{
		"$set": bson.M{
			"acme.order_url":                      orderURL,
			"status":                              "pending_dns",
			"dns_verification.pending_challenges": challenges,
			"renewal_attempts":                    cert.RenewalAttempts + 1,
			"updated_at":                          now,
		},
	}

	collection := m.db.Collection("acme_certificates")
	filter := bson.M{
		"_id":     certObjID,
		"project": project,
	}

	_, err = collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return nil, fmt.Errorf("failed to update certificate status: %w", err)
	}

	// Store temporary key for renewal (fix: csrPEM is string)
	tempKey := &TempKey{
		ID:            primitive.NewObjectID(),
		Project:       project,
		CertRequestID: certObjID,
		SecretName:    cert.SecretName,
		PrivateKeyPEM: privateKeyPEM,
		CSRPEM:        csrPEM,
		CreatedAt:     now,
		ExpiresAt:     now.Add(1 * time.Hour),
		Status:        "pending",
	}

	if err := m.CreateTempKey(ctx, tempKey); err != nil {
		return nil, fmt.Errorf("failed to store temporary key: %w", err)
	}

	m.logger.Infof("Manual DNS renewal initiated for certificate %s. User must add DNS TXT records and call /verify endpoint.", cert.SecretName)

	// Return updated certificate with new challenges
	cert.Status = "pending_dns"
	cert.DNSVerification.PendingChallenges = challenges
	cert.ACME.OrderURL = orderURL
	cert.UpdatedAt = now

	return cert, nil
}

// storeCertificateInSecrets stores the certificate in the secrets collection for ALL versions
// This ensures the same certificate is available across multiple Envoy versions
func (m *CertificateManager) storeCertificateInSecrets(
	ctx context.Context,
	cert *ACMECertificate,
	certificatePEM []byte,
	privateKey crypto.PrivateKey,
	project string,
) error {
	// Convert private key to PEM
	privateKeyPEM, err := EncodePrivateKeyToPEM(privateKey)
	if err != nil {
		return fmt.Errorf("failed to convert private key to PEM: %w", err)
	}

	// Create metadata for linking
	metadata := map[string]any{
		"acme_enabled": true,
		"acme_cert_id": cert.ID.Hex(),
	}

	collection := m.db.Collection("secrets")
	now := time.Now()

	// Store/Update secret for EACH version
	for _, version := range cert.SecretVersions {
		// Build resource structure for Envoy TlsCertificate
		resource := &models.DBResource{
			General: models.General{
				Name:          cert.SecretName,
				Project:       project,
				Version:       version, // Different version for each entry
				Type:          "secret",
				GType:         models.TLSCertificate,
				Collection:    "secrets",
				CanonicalName: "envoy.transport_sockets.tls_certificate",
				Category:      "envoy.transport_sockets.tls",
				Metadata:      metadata,
				Permissions: models.Permissions{
					Users:  cert.Permissions.Users,
					Groups: cert.Permissions.Groups,
				},
				CreatedAt: primitive.NewDateTimeFromTime(now),
				UpdatedAt: primitive.NewDateTimeFromTime(now),
			},
			Resource: models.Resource{
				Version: "2",
				Resource: []map[string]any{
					{
						"certificate_chain": map[string]string{
							"inline_string": string(certificatePEM),
						},
						"private_key": map[string]string{
							"inline_string": privateKeyPEM,
						},
					},
				},
			},
		}

		// Check if secret already exists for this version
		filter := bson.M{
			"general.name":    cert.SecretName,
			"general.version": version,
			"general.project": project,
		}

		var existing models.DBResource
		err = collection.FindOne(ctx, filter).Decode(&existing)

		switch {
		case errors.Is(err, mongo.ErrNoDocuments):
			// Insert new secret for this version
			_, err = collection.InsertOne(ctx, resource)
			if err != nil {
				m.logger.Warnf("Failed to insert secret for version %s: %v", version, err)
				continue
			}
			m.logger.Infof("Created secret %s for version %s", cert.SecretName, version)
		case err == nil:
			// Update existing secret for this version
			update := bson.M{
				"$set": bson.M{
					"resource.resource.0.certificate_chain.inline_string": string(certificatePEM),
					"resource.resource.0.private_key.inline_string":       privateKeyPEM,
					"general.metadata":   metadata,
					"general.updated_at": now,
				},
			}
			_, err = collection.UpdateOne(ctx, filter, update)
			if err != nil {
				m.logger.Warnf("Failed to update secret for version %s: %v", version, err)
				continue
			}
			m.logger.Infof("Updated secret %s for version %s", cert.SecretName, version)
		default:
			m.logger.Warnf("Failed to check existing secret for version %s: %v", version, err)
			continue
		}
	}

	return nil
}

// updateCertificateStatus updates the status and error message of a certificate
func (m *CertificateManager) updateCertificateStatus(ctx context.Context, certID primitive.ObjectID, project, status, errorMsg string) error {
	collection := m.db.Collection("acme_certificates")
	filter := bson.M{
		"_id":     certID,
		"project": project, // PROJECT ISOLATION
	}

	updateFields := bson.M{
		"status":     status,
		"updated_at": time.Now(),
	}

	// Set error if provided
	if errorMsg != "" {
		updateFields["last_error"] = ErrorDetails{
			Timestamp: time.Now(),
			Message:   errorMsg,
		}
	}

	update := bson.M{
		"$set": updateFields,
	}

	if status == "active" {
		update["$inc"] = bson.M{"renewal_attempts": 1}
	}

	_, err := collection.UpdateOne(ctx, filter, update)
	return err
}
