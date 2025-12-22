package acme

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/common"
	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models/downstreamfilters"
)

// CertificateManager handles ACME certificate operations
type CertificateManager struct {
	db          *mongo.Database
	encryptor   *Encryptor
	acmeConfig  *config.ACMEConfig
	caProviders map[string]config.CAProviderConfig
	logger      *logger.Logger
	appContext  *db.AppContext             // For dependency tracking
	pokeService *bridge.PokeServiceClient  // For snapshot updates
}

// NewCertificateManager creates a new certificate manager
func NewCertificateManager(db *mongo.Database, jwtSecret string, acmeCfg *config.ACMEConfig, caProviders *map[string]config.CAProviderConfig, log *logger.Logger) (*CertificateManager, error) {
	if db == nil {
		return nil, fmt.Errorf("database cannot be nil")
	}

	if jwtSecret == "" {
		return nil, fmt.Errorf("JWT secret cannot be empty")
	}

	if acmeCfg == nil {
		return nil, fmt.Errorf("ACME config cannot be nil")
	}

	if caProviders == nil || len(*caProviders) == 0 {
		return nil, fmt.Errorf("CA providers config cannot be nil or empty")
	}

	if log == nil {
		return nil, fmt.Errorf("logger cannot be nil")
	}

	// Create encryptor for credentials
	encryptor, err := NewEncryptor(jwtSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to create encryptor: %w", err)
	}

	return &CertificateManager{
		db:          db,
		encryptor:   encryptor,
		acmeConfig:  acmeCfg,
		caProviders: *caProviders,
		logger:      log,
	}, nil
}

// SetDependencyServices sets the appContext and pokeService for dependency tracking
// This must be called after manager creation to enable snapshot updates on renewal
func (m *CertificateManager) SetDependencyServices(appContext *db.AppContext, pokeService *bridge.PokeServiceClient) {
	m.appContext = appContext
	m.pokeService = pokeService
}

// GetDB returns the database instance for manager access
func (m *CertificateManager) GetDB() *mongo.Database {
	return m.db
}

// GetEncryptor returns the encryptor instance for credential encryption
func (m *CertificateManager) GetEncryptor() *Encryptor {
	return m.encryptor
}

// GetCAProviders returns the CA providers configuration
func (m *CertificateManager) GetCAProviders() map[string]config.CAProviderConfig {
	return m.caProviders
}

// CreateCertificate creates a new Let's Encrypt certificate metadata entry
func (m *CertificateManager) CreateCertificate(ctx context.Context, cert *ACMECertificate) error {
	if cert == nil {
		return fmt.Errorf("certificate cannot be nil")
	}

	// Validate project field (PROJECT ISOLATION)
	if cert.Project == "" {
		return fmt.Errorf("project field is required for project isolation")
	}

	// Set timestamps
	now := time.Now()
	cert.CreatedAt = now
	cert.UpdatedAt = now

	// Set ID if not already set
	if cert.ID.IsZero() {
		cert.ID = primitive.NewObjectID()
	}

	// Insert into database
	collection := m.db.Collection("acme_certificates")
	_, err := collection.InsertOne(ctx, cert)
	if err != nil {
		return fmt.Errorf("failed to insert certificate: %w", err)
	}

	m.logger.Infof("Created Let's Encrypt certificate: %s (project: %s)", cert.SecretName, cert.Project)
	return nil
}

// GetCertificate retrieves a certificate by ID with project filtering
func (m *CertificateManager) GetCertificate(ctx context.Context, certID primitive.ObjectID, project string) (*ACMECertificate, error) {
	if certID.IsZero() {
		return nil, fmt.Errorf("certificate ID cannot be zero")
	}

	if project == "" {
		return nil, fmt.Errorf("project is required for project isolation")
	}

	collection := m.db.Collection("acme_certificates")
	filter := bson.M{
		"_id":     certID,
		"project": project, // PROJECT ISOLATION
	}

	var cert ACMECertificate
	err := collection.FindOne(ctx, filter).Decode(&cert)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, fmt.Errorf("certificate not found")
		}
		return nil, fmt.Errorf("failed to get certificate: %w", err)
	}

	return &cert, nil
}

// getCertificateByName retrieves a certificate by secret name with project filtering
// This is used internally to check if a certificate already exists for a given name
func (m *CertificateManager) getCertificateByName(ctx context.Context, secretName string, project string) (*ACMECertificate, error) {
	if secretName == "" {
		return nil, fmt.Errorf("secret name cannot be empty")
	}

	if project == "" {
		return nil, fmt.Errorf("project is required for project isolation")
	}

	collection := m.db.Collection("acme_certificates")
	filter := bson.M{
		"secret_name": secretName,
		"project":     project, // PROJECT ISOLATION
	}

	var cert ACMECertificate
	err := collection.FindOne(ctx, filter).Decode(&cert)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil // Not an error - certificate just doesn't exist yet
		}
		return nil, fmt.Errorf("failed to get certificate by name: %w", err)
	}

	return &cert, nil
}

// ListCertificates lists all certificates for a project with permission filtering
func (m *CertificateManager) ListCertificates(ctx context.Context, project string, user models.UserDetails) ([]*ACMECertificate, error) {
	if project == "" {
		return nil, fmt.Errorf("project is required for project isolation")
	}

	collection := m.db.Collection("acme_certificates")

	// Build filter with project isolation
	filter := bson.M{
		"project": project, // PROJECT ISOLATION - MANDATORY
	}

	// Add permission filtering for non-Owner/Admin users
	if !user.IsOwner && user.Role != models.RoleAdmin {
		// Build complete group list (includes base_group)
		allGroups := append([]string{}, user.Groups...)
		if user.BaseGroup != "" {
			allGroups = append(allGroups, user.BaseGroup)
		}

		// Add permission filter
		filter["$or"] = []bson.M{
			{"permissions.groups": bson.M{"$in": allGroups}},
			{"permissions.users": user.UserID},
		}
	}

	// Query database
	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to list certificates: %w", err)
	}
	defer cursor.Close(ctx)

	// Decode results
	var certs []*ACMECertificate
	if err := cursor.All(ctx, &certs); err != nil {
		return nil, fmt.Errorf("failed to decode certificates: %w", err)
	}

	return certs, nil
}

// DeleteCertificate deletes a certificate with project filtering
// This deletes BOTH the metadata from acme_certificates AND the secret from secrets collection
func (m *CertificateManager) DeleteCertificate(ctx context.Context, certID primitive.ObjectID, project string) error {
	if certID.IsZero() {
		return fmt.Errorf("certificate ID cannot be zero")
	}

	if project == "" {
		return fmt.Errorf("project is required for project isolation")
	}

	// First, get the certificate to find the secret name
	cert, err := m.GetCertificate(ctx, certID, project)
	if err != nil {
		return fmt.Errorf("failed to get certificate: %w", err)
	}

	// Check if certificate is used by any TLS contexts in ANY version (dependency check)
	if m.appContext != nil {
		var allDependencies []string

		// Check each version separately
		for _, version := range cert.SecretVersions {
			dfm := downstreamfilters.DownstreamFilter{
				Name:    cert.SecretName,
				Project: cert.Project,
				Version: version,
			}
			dependList := common.IsDeletable(ctx, m.appContext, models.TLSCertificate, dfm)
			allDependencies = append(allDependencies, dependList...)
		}

		if len(allDependencies) > 0 {
			return fmt.Errorf("cannot delete certificate '%s'. It is currently in use by:\n%s\n\nPlease remove the certificate from these resources before deleting it",
				cert.SecretName, strings.Join(allDependencies, "\n"))
		}
	}

	// Delete ALL version secrets from secrets collection
	secretsCollection := m.db.Collection("secrets")
	totalDeleted := int64(0)

	for _, version := range cert.SecretVersions {
		secretFilter := bson.M{
			"general.name":    cert.SecretName,
			"general.version": version,
			"general.project": project,
			"general.metadata.acme_enabled": true, // Only delete if managed by ACME
		}

		secretResult, err := secretsCollection.DeleteOne(ctx, secretFilter)
		if err != nil {
			m.logger.Warnf("Failed to delete secret from secrets collection (version %s): %v", version, err)
		} else if secretResult.DeletedCount > 0 {
			totalDeleted++
			m.logger.Infof("Deleted secret from secrets collection: %s v%s (project: %s)", cert.SecretName, version, project)
		}
	}

	if totalDeleted == 0 {
		m.logger.Warnf("No secrets found in secrets collection for certificate: %s (project: %s)", cert.SecretName, project)
	}

	// Delete certificate metadata
	collection := m.db.Collection("acme_certificates")
	filter := bson.M{
		"_id":     certID,
		"project": project, // PROJECT ISOLATION
	}

	result, err := collection.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to delete certificate metadata: %w", err)
	}

	if result.DeletedCount == 0 {
		return fmt.Errorf("certificate metadata not found")
	}

	// Also delete any temporary keys
	_ = m.DeleteTempKey(ctx, certID, project)

	m.logger.Infof("Deleted Let's Encrypt certificate and secret: %s (project: %s)", cert.SecretName, project)
	return nil
}

// DuplicateCertificate adds a new version to an existing certificate
// If the certificate is active, it clones the secret to the new version
func (m *CertificateManager) DuplicateCertificate(ctx context.Context, certID primitive.ObjectID, newVersion string, project string) (*ACMECertificate, error) {
	if certID.IsZero() {
		return nil, fmt.Errorf("certificate ID cannot be zero")
	}

	if newVersion == "" {
		return nil, fmt.Errorf("new version is required")
	}

	if project == "" {
		return nil, fmt.Errorf("project is required for project isolation")
	}

	// Get the certificate
	cert, err := m.GetCertificate(ctx, certID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get certificate: %w", err)
	}

	// Check if version already exists
	for _, v := range cert.SecretVersions {
		if v == newVersion {
			return nil, fmt.Errorf("version %s already exists for certificate %s", newVersion, cert.SecretName)
		}
	}

	// Add new version to array
	cert.SecretVersions = append(cert.SecretVersions, newVersion)
	cert.UpdatedAt = time.Now()

	// Update in database
	collection := m.db.Collection("acme_certificates")
	filter := bson.M{
		"_id":     certID,
		"project": project,
	}
	update := bson.M{
		"$set": bson.M{
			"secret_versions": cert.SecretVersions,
			"updated_at":      cert.UpdatedAt,
		},
	}
	_, err = collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return nil, fmt.Errorf("failed to add version to certificate: %w", err)
	}

	m.logger.Infof("Added version %s to certificate %s", newVersion, cert.SecretName)

	// If certificate is active, clone the secret to the new version
	if cert.Status == StatusActive {
		secretsCollection := m.db.Collection("secrets")
		var existingSecret models.DBResource

		// Find first available secret from existing versions
		var foundSecret bool
		for _, existingVersion := range cert.SecretVersions {
			if existingVersion == newVersion {
				continue // Skip the version we just added
			}

			existingSecretFilter := bson.M{
				"general.name":    cert.SecretName,
				"general.version": existingVersion,
				"general.project": project,
			}
			err = secretsCollection.FindOne(ctx, existingSecretFilter).Decode(&existingSecret)
			if err == nil {
				foundSecret = true
				m.logger.Infof("Found existing secret from version %s to copy", existingVersion)
				break
			}
		}

		if foundSecret {
			// Clone certificate to new version
			now := time.Now()
			newSecret := existingSecret // Copy entire structure

			// Generate new _id for the cloned secret (CRITICAL - prevents duplicate key error)
			newSecret.ID = primitive.NewObjectID()

			// Update version and timestamps
			newSecret.General.Version = newVersion
			newSecret.General.CreatedAt = primitive.NewDateTimeFromTime(now)
			newSecret.General.UpdatedAt = primitive.NewDateTimeFromTime(now)

			// Insert new secret
			_, err = secretsCollection.InsertOne(ctx, &newSecret)
			if err != nil {
				m.logger.Warnf("Failed to create secret for new version %s: %v", newVersion, err)
			} else {
				m.logger.Infof("Created secret for new version %s (cloned from existing)", newVersion)
			}
		} else {
			m.logger.Warnf("No existing secret found to clone for version %s", newVersion)
		}
	} else {
		m.logger.Infof("Certificate %s is not active yet, secret will be created after verification", cert.SecretName)
	}

	return cert, nil
}

// GetCertificatesForRenewal retrieves certificates that need renewal
func (m *CertificateManager) GetCertificatesForRenewal(ctx context.Context, project string) ([]*ACMECertificate, error) {
	if project == "" {
		return nil, fmt.Errorf("project is required for project isolation")
	}

	collection := m.db.Collection("acme_certificates")
	filter := bson.M{
		"project":            project,                               // PROJECT ISOLATION
		"auto_renew":         true,                                  // Only auto-renew enabled
		"status":             StatusActive,                          // Only active certificates
		"renewal_starts_at": bson.M{"$lte": time.Now()},           // Renewal window reached
	}

	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to query certificates for renewal: %w", err)
	}
	defer cursor.Close(ctx)

	var certs []*ACMECertificate
	if err := cursor.All(ctx, &certs); err != nil {
		return nil, fmt.Errorf("failed to decode certificates: %w", err)
	}

	return certs, nil
}

// ===== DNS Credential Operations =====

// CreateDNSCredential creates a new DNS provider credential
func (m *CertificateManager) CreateDNSCredential(ctx context.Context, cred *DNSCredential, plaintextCreds string) error {
	if cred == nil {
		return fmt.Errorf("credential cannot be nil")
	}

	if cred.Project == "" {
		return fmt.Errorf("project field is required")
	}

	if plaintextCreds == "" {
		return fmt.Errorf("credentials cannot be empty")
	}

	// Encrypt credentials
	encrypted, err := m.encryptor.Encrypt(plaintextCreds)
	if err != nil {
		return fmt.Errorf("failed to encrypt credentials: %w", err)
	}

	cred.CredentialsEncrypted = encrypted

	// Set timestamps
	now := time.Now()
	cred.CreatedAt = now
	cred.UpdatedAt = now

	// Set ID if not already set
	if cred.ID.IsZero() {
		cred.ID = primitive.NewObjectID()
	}

	// Insert into database
	collection := m.db.Collection("acme_dns_credentials")
	_, err = collection.InsertOne(ctx, cred)
	if err != nil {
		return fmt.Errorf("failed to insert DNS credential: %w", err)
	}

	m.logger.Infof("Created DNS credential: %s (provider: %s, project: %s)", cred.Name, cred.Provider, cred.Project)
	return nil
}

// GetDNSCredential retrieves a DNS credential by ID with project filtering and decryption
func (m *CertificateManager) GetDNSCredential(ctx context.Context, credID primitive.ObjectID, project string) (*DNSCredential, string, error) {
	if credID.IsZero() {
		return nil, "", fmt.Errorf("credential ID cannot be zero")
	}

	if project == "" {
		return nil, "", fmt.Errorf("project is required for project isolation")
	}

	collection := m.db.Collection("acme_dns_credentials")
	filter := bson.M{
		"_id":     credID,
		"project": project, // PROJECT ISOLATION
	}

	var cred DNSCredential
	err := collection.FindOne(ctx, filter).Decode(&cred)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, "", fmt.Errorf("credential not found")
		}
		return nil, "", fmt.Errorf("failed to get credential: %w", err)
	}

	// Decrypt credentials
	decrypted, err := m.encryptor.Decrypt(cred.CredentialsEncrypted)
	if err != nil {
		return nil, "", fmt.Errorf("failed to decrypt credentials: %w", err)
	}

	return &cred, decrypted, nil
}

// ListDNSCredentials lists all DNS credentials for a project with permission filtering
func (m *CertificateManager) ListDNSCredentials(ctx context.Context, project string, user models.UserDetails) ([]*DNSCredential, error) {
	if project == "" {
		return nil, fmt.Errorf("project is required for project isolation")
	}

	collection := m.db.Collection("acme_dns_credentials")

	// Build filter with project isolation
	filter := bson.M{
		"project": project, // PROJECT ISOLATION - MANDATORY
	}

	// Add permission filtering for non-Owner/Admin users
	if !user.IsOwner && user.Role != models.RoleAdmin {
		// Build complete group list (includes base_group)
		allGroups := append([]string{}, user.Groups...)
		if user.BaseGroup != "" {
			allGroups = append(allGroups, user.BaseGroup)
		}

		// Add permission filter
		filter["$or"] = []bson.M{
			{"permissions.groups": bson.M{"$in": allGroups}},
			{"permissions.users": user.UserID},
		}
	}

	// Query database
	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to list DNS credentials: %w", err)
	}
	defer cursor.Close(ctx)

	// Decode results (WITHOUT decrypted credentials for security)
	var creds []*DNSCredential
	if err := cursor.All(ctx, &creds); err != nil {
		return nil, fmt.Errorf("failed to decode DNS credentials: %w", err)
	}

	return creds, nil
}

// UpdateDNSCredential updates an existing DNS credential
func (m *CertificateManager) UpdateDNSCredential(ctx context.Context, credID primitive.ObjectID, project string, updates map[string]any, newCredentialsJSON string) error {
	if credID.IsZero() {
		return fmt.Errorf("credential ID cannot be zero")
	}

	if project == "" {
		return fmt.Errorf("project is required for project isolation")
	}

	// Build update document
	updateDoc := bson.M{
		"$set": bson.M{
			"updated_at": time.Now(),
		},
	}

	// Add provided updates
	for key, value := range updates {
		updateDoc["$set"].(bson.M)[key] = value
	}

	// If new credentials provided, encrypt and add to update
	if newCredentialsJSON != "" {
		encrypted, err := m.encryptor.Encrypt(newCredentialsJSON)
		if err != nil {
			return fmt.Errorf("failed to encrypt credentials: %w", err)
		}
		updateDoc["$set"].(bson.M)["credentials_encrypted"] = encrypted
	}

	// Update in database with project isolation
	collection := m.db.Collection("acme_dns_credentials")
	filter := bson.M{
		"_id":     credID,
		"project": project, // PROJECT ISOLATION
	}

	result, err := collection.UpdateOne(ctx, filter, updateDoc)
	if err != nil {
		return fmt.Errorf("failed to update DNS credential: %w", err)
	}

	if result.MatchedCount == 0 {
		return fmt.Errorf("credential not found")
	}

	m.logger.Infof("Updated DNS credential: %s (project: %s)", credID.Hex(), project)
	return nil
}

// DeleteDNSCredential deletes a DNS credential with project filtering
func (m *CertificateManager) DeleteDNSCredential(ctx context.Context, credID primitive.ObjectID, project string, force bool) error {
	if credID.IsZero() {
		return fmt.Errorf("credential ID cannot be zero")
	}

	if project == "" {
		return fmt.Errorf("project is required for project isolation")
	}

	// Verify credential exists
	_, _, err := m.GetDNSCredential(ctx, credID, project)
	if err != nil {
		return fmt.Errorf("credential not found: %w", err)
	}

	// Count certificates using this DNS credential
	certCollection := m.db.Collection("acme_certificates")
	filter := bson.M{
		"project":                          project,
		"dns_verification.dns_credential_id": credID,
	}

	count, err := certCollection.CountDocuments(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to count certificates: %w", err)
	}

	if count > 0 && !force {
		// List certificate IDs for user reference
		cursor, _ := certCollection.Find(ctx, filter)
		defer cursor.Close(ctx)

		var certIDs []string
		var certs []struct {
			ID         primitive.ObjectID `bson:"_id"`
			SecretName string             `bson:"secret_name"`
		}
		if err := cursor.All(ctx, &certs); err == nil {
			for _, cert := range certs {
				certIDs = append(certIDs, cert.ID.Hex())
			}
		}

		return &DNSCredentialInUseError{
			CredentialID:   credID,
			CertCount:      int(count),
			CertificateIDs: certIDs,
		}
	}

	// If force=true, mark certificates as having deleted credential
	if count > 0 {
		m.logger.Warnf("Force-deleting DNS credential %s with %d active certificates (they will lose automatic renewal)",
			credID.Hex(), count)

		update := bson.M{
			"$set": bson.M{
				"dns_verification.credential_deleted":    true,
				"dns_verification.credential_deleted_at": time.Now(),
			},
		}
		_, err = certCollection.UpdateMany(ctx, filter, update)
		if err != nil {
			m.logger.Errorf("Failed to mark certificates as having deleted credential: %v", err)
		}
	}

	// Delete credential
	collection := m.db.Collection("acme_dns_credentials")
	credFilter := bson.M{
		"_id":     credID,
		"project": project, // PROJECT ISOLATION
	}

	result, err := collection.DeleteOne(ctx, credFilter)
	if err != nil {
		return fmt.Errorf("failed to delete DNS credential: %w", err)
	}

	if result.DeletedCount == 0 {
		return fmt.Errorf("credential not found")
	}

	m.logger.Infof("Deleted DNS credential: %s (project: %s)", credID.Hex(), project)
	return nil
}

// ===== Temporary Key Operations =====

// CreateTempKey stores a temporary private key during certificate issuance
func (m *CertificateManager) CreateTempKey(ctx context.Context, tempKey *TempKey) error {
	if tempKey == nil {
		return fmt.Errorf("temp key cannot be nil")
	}

	if tempKey.Project == "" {
		return fmt.Errorf("project field is required")
	}

	// Set timestamps
	now := time.Now()
	tempKey.CreatedAt = now
	tempKey.ExpiresAt = now.Add(1 * time.Hour) // 1 hour expiration

	// Set ID if not already set
	if tempKey.ID.IsZero() {
		tempKey.ID = primitive.NewObjectID()
	}

	// Insert into database
	collection := m.db.Collection("acme_temp_keys")
	_, err := collection.InsertOne(ctx, tempKey)
	if err != nil {
		return fmt.Errorf("failed to insert temp key: %w", err)
	}

	return nil
}

// GetTempKey retrieves a temporary key by certificate request ID
func (m *CertificateManager) GetTempKey(ctx context.Context, certRequestID primitive.ObjectID, project string) (*TempKey, error) {
	if certRequestID.IsZero() {
		return nil, fmt.Errorf("certificate request ID cannot be zero")
	}

	if project == "" {
		return nil, fmt.Errorf("project is required for project isolation")
	}

	collection := m.db.Collection("acme_temp_keys")
	filter := bson.M{
		"cert_request_id": certRequestID,
		"project":         project, // PROJECT ISOLATION
	}

	var tempKey TempKey
	err := collection.FindOne(ctx, filter).Decode(&tempKey)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, fmt.Errorf("temp key not found")
		}
		return nil, fmt.Errorf("failed to get temp key: %w", err)
	}

	return &tempKey, nil
}

// DeleteTempKey deletes a temporary key
func (m *CertificateManager) DeleteTempKey(ctx context.Context, certRequestID primitive.ObjectID, project string) error {
	if certRequestID.IsZero() {
		return fmt.Errorf("certificate request ID cannot be zero")
	}

	if project == "" {
		return fmt.Errorf("project is required for project isolation")
	}

	collection := m.db.Collection("acme_temp_keys")
	filter := bson.M{
		"cert_request_id": certRequestID,
		"project":         project, // PROJECT ISOLATION
	}

	_, err := collection.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to delete temp key: %w", err)
	}

	return nil
}

// ===== Dependency Tracking and Snapshot Update Helpers =====

// triggerSnapshotUpdate triggers snapshot updates for all versions of this certificate
// This is called after certificate renewal to ensure ALL Envoys get the updated certificate
func (m *CertificateManager) triggerSnapshotUpdate(
	ctx context.Context,
	secretName string,
	secretVersions []string,
	project string,
) error {
	totalListeners := 0
	allListeners := make([]string, 0)

	// Trigger snapshot update for EACH version
	for _, version := range secretVersions {
		// Load the secret resource from secrets collection
		secretResource, err := m.loadSecretResource(ctx, secretName, version, project)
		if err != nil {
			m.logger.Warnf("Failed to load secret resource for version %s: %v", version, err)
			continue
		}

		// Create system user RequestDetails for the snapshot update
		requestDetails := models.RequestDetails{
			Name:          secretName,
			Collection:    "secrets",
			Project:       project,
			Version:       version,
			SaveOrPublish: "publish", // CRITICAL: Must be "publish" to trigger dependency tracking
			GType:         models.TLSCertificate,
			User: models.UserDetails{
				UserID:   "system",
				UserName: "letsencrypt-auto-renewal",
				Role:     models.RoleOwner,
				IsOwner:  true, // System user has owner privileges
				Projects: []string{project},
			},
		}

		// Trigger dependency tracking and snapshot updates
		// This will:
		// 1. Find all TLS contexts using this certificate (for this version)
		// 2. Find all listeners using those TLS contexts
		// 3. Trigger snapshot regeneration for those listeners
		changedResources := crud.HandleResourceChange(
			ctx,
			secretResource,
			requestDetails,
			m.appContext,
			project,
			m.pokeService,
		)

		if changedResources != nil && len(changedResources.Listeners) > 0 {
			totalListeners += len(changedResources.Listeners)
			allListeners = append(allListeners, changedResources.Listeners...)
			m.logger.Infof(
				"Certificate %s (v%s) renewed - triggered %d listener snapshot(s): %v",
				secretName,
				version,
				len(changedResources.Listeners),
				changedResources.Listeners,
			)
		}
	}

	if totalListeners > 0 {
		m.logger.Infof(
			"Certificate %s renewed across %d version(s) - total %d listener(s) updated: %v",
			secretName,
			len(secretVersions),
			totalListeners,
			allListeners,
		)
	} else {
		m.logger.Infof(
			"Certificate %s renewed but no dependent listeners found (certificate may not be in use yet)",
			secretName,
		)
	}

	return nil
}

// loadSecretResource loads a secret resource from the secrets collection
func (m *CertificateManager) loadSecretResource(
	ctx context.Context,
	secretName string,
	secretVersion string,
	project string,
) (models.ResourceClass, error) {
	collection := m.db.Collection("secrets")

	filter := bson.M{
		"general.name":    secretName,
		"general.version": secretVersion,
		"general.project": project,
	}

	var resource models.DBResource
	err := collection.FindOne(ctx, filter).Decode(&resource)
	if err != nil {
		return nil, fmt.Errorf("failed to find secret: %w", err)
	}

	return &resource, nil
}

// UpdateCertificateJobID updates the certificate with the last job ID
// This allows tracking which async job is processing the certificate
func (m *CertificateManager) UpdateCertificateJobID(ctx context.Context, certID, project, jobID string) error {
	certObjID, err := primitive.ObjectIDFromHex(certID)
	if err != nil {
		return fmt.Errorf("invalid certificate ID: %w", err)
	}

	collection := m.db.Collection("acme_certificates")
	filter := bson.M{
		"_id":     certObjID,
		"project": project, // PROJECT ISOLATION
	}

	update := bson.M{
		"$set": bson.M{
			"last_job_id": jobID,
			"updated_at":  time.Now(),
		},
	}

	_, err = collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to update certificate job ID: %w", err)
	}

	m.logger.Debugf("Updated certificate %s with job ID %s", certID, jobID)
	return nil
}

// UpdateCertificateStatusAndJobID updates both the certificate status and job ID atomically
// This is used when creating a new verification/renewal job
func (m *CertificateManager) UpdateCertificateStatusAndJobID(ctx context.Context, certID, project, status, jobID string) error {
	certObjID, err := primitive.ObjectIDFromHex(certID)
	if err != nil {
		return fmt.Errorf("invalid certificate ID: %w", err)
	}

	collection := m.db.Collection("acme_certificates")
	filter := bson.M{
		"_id":     certObjID,
		"project": project, // PROJECT ISOLATION
	}

	update := bson.M{
		"$set": bson.M{
			"status":      status,
			"last_job_id": jobID,
			"updated_at":  time.Now(),
		},
	}

	result, err := collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to update certificate status and job ID: %w", err)
	}

	if result.MatchedCount == 0 {
		return fmt.Errorf("certificate not found or access denied")
	}

	m.logger.Infof("Updated certificate %s: status=%s, job_id=%s", certID, status, jobID)
	return nil
}

// PrepareRenewalKey generates new private key and CSR for certificate renewal
func (m *CertificateManager) PrepareRenewalKey(ctx context.Context, domains []string) (string, string, error) {
	// Generate new private key for renewal
	newPrivateKey, err := GenerateRSAPrivateKey()
	if err != nil {
		return "", "", fmt.Errorf("failed to generate private key: %w", err)
	}

	// Convert private key to PEM
	privateKeyPEM, err := EncodePrivateKeyToPEM(newPrivateKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to encode private key: %w", err)
	}

	// Generate CSR
	csrBytes, err := GenerateCSR(domains, newPrivateKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate CSR: %w", err)
	}

	// Convert CSR to PEM format
	csrPEM := EncodeCSRToPEM(csrBytes)

	return privateKeyPEM, csrPEM, nil
}

// CreateOrUpdateTempKeyForRenewal creates or updates temporary key for renewal
func (m *CertificateManager) CreateOrUpdateTempKeyForRenewal(ctx context.Context, certID primitive.ObjectID, project, secretName, privateKeyPEM, csrPEM string) error {
	// Check if temp key already exists
	existingKey, err := m.GetTempKey(ctx, certID, project)
	if err == nil && existingKey != nil {
		// Update existing temp key
		collection := m.db.Collection("acme_temp_keys")
		filter := bson.M{
			"cert_request_id": certID,
			"project":         project,
		}

		now := time.Now()
		update := bson.M{
			"$set": bson.M{
				"private_key_pem": privateKeyPEM,
				"csr_pem":         csrPEM,
				"updated_at":      now,
				"expires_at":      now.Add(1 * time.Hour),
				"status":          "pending",
			},
		}

		_, err = collection.UpdateOne(ctx, filter, update)
		if err != nil {
			return fmt.Errorf("failed to update temp key: %w", err)
		}

		m.logger.Infof("Updated temporary key for renewal of certificate %s", certID.Hex())
		return nil
	}

	// Create new temp key
	now := time.Now()
	tempKey := &TempKey{
		ID:            primitive.NewObjectID(),
		Project:       project,
		CertRequestID: certID,
		SecretName:    secretName,
		PrivateKeyPEM: privateKeyPEM,
		CSRPEM:        csrPEM,
		CreatedAt:     now,
		ExpiresAt:     now.Add(1 * time.Hour),
		Status:        "pending",
	}

	if err := m.CreateTempKey(ctx, tempKey); err != nil {
		return fmt.Errorf("failed to create temp key: %w", err)
	}

	m.logger.Infof("Created temporary key for renewal of certificate %s", certID.Hex())
	return nil
}

// UpdateCertificateDNSCredential updates the DNS credential for an existing certificate
func (m *CertificateManager) UpdateCertificateDNSCredential(ctx context.Context, certID primitive.ObjectID, project string, newCredentialID primitive.ObjectID, credentialName, provider string) error {
	collection := m.db.Collection("acme_certificates")

	// Build filter with project isolation
	filter := bson.M{
		"_id":     certID,
		"project": project,
	}

	// Build update
	now := time.Now()
	update := bson.M{
		"$set": bson.M{
			"dns_verification.dns_credential_id":   newCredentialID,
			"dns_verification.dns_credential_name": credentialName,
			"dns_verification.provider":            provider,
			"updated_at":                           now,
		},
	}

	// Update certificate
	result, err := collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to update certificate DNS credential: %w", err)
	}

	if result.MatchedCount == 0 {
		return fmt.Errorf("certificate not found or access denied")
	}

	m.logger.Infof("Updated DNS credential for certificate %s to %s (%s)", certID.Hex(), credentialName, provider)
	return nil
}
