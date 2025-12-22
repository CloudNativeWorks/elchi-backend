package acme

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// GetACMEAccount retrieves an ACME account by ID with project isolation
func (m *CertificateManager) GetACMEAccount(ctx context.Context, accountID primitive.ObjectID, project string) (*ACMEAccount, error) {
	collection := m.db.Collection("acme_accounts")

	filter := bson.M{
		"_id":     accountID,
		"project": project,
	}

	var account ACMEAccount
	err := collection.FindOne(ctx, filter).Decode(&account)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, fmt.Errorf("ACME account not found")
		}
		return nil, fmt.Errorf("failed to get ACME account: %w", err)
	}

	return &account, nil
}

// getACMEAccountByEmailAndProvider retrieves an ACME account by email, CA provider, environment, and project
func (m *CertificateManager) getACMEAccountByEmailAndProvider(ctx context.Context, email, caProvider, environment, project string) (*ACMEAccount, error) {
	collection := m.db.Collection("acme_accounts")

	filter := bson.M{
		"email":       email,
		"ca_provider": caProvider,
		"environment": environment,
		"project":     project,
	}

	var account ACMEAccount
	err := collection.FindOne(ctx, filter).Decode(&account)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil // Not found is not an error
		}
		return nil, fmt.Errorf("failed to query ACME account: %w", err)
	}

	return &account, nil
}

// CreateACMEAccount creates and registers a new ACME account with a CA provider
func (m *CertificateManager) CreateACMEAccount(ctx context.Context, account *ACMEAccount) error {
	// Validate inputs
	if account.Project == "" {
		return fmt.Errorf("project is required")
	}
	if account.Email == "" {
		return fmt.Errorf("email is required")
	}
	if account.Environment != EnvironmentStaging && account.Environment != EnvironmentProduction {
		return fmt.Errorf("environment must be 'staging' or 'production'")
	}

	// Validate CA provider
	if account.CAProvider == "" {
		return fmt.Errorf("ca_provider is required")
	}
	providerConfig, exists := m.caProviders[account.CAProvider]
	if !exists {
		return fmt.Errorf("unknown CA provider: %s", account.CAProvider)
	}
	if !providerConfig.Supported {
		return fmt.Errorf("CA provider %s is not yet supported", account.CAProvider)
	}

	// Check if environment is supported for this CA provider
	_, envExists := providerConfig.Environments[account.Environment]
	if !envExists {
		return fmt.Errorf("environment %s not supported for %s", account.Environment, account.CAProvider)
	}

	// Validate EAB credentials if required by CA provider
	if providerConfig.RequiresEAB {
		if account.EAB == nil || account.EAB.KeyID == "" || account.EAB.HMACKeyEncrypted == "" {
			return fmt.Errorf("EAB credentials required for %s but not provided", account.CAProvider)
		}
	}

	// Check uniqueness: email + ca_provider + environment + project
	existing, err := m.getACMEAccountByEmailAndProvider(ctx, account.Email, account.CAProvider, account.Environment, account.Project)
	if err != nil {
		return fmt.Errorf("failed to check existing account: %w", err)
	}
	if existing != nil {
		return fmt.Errorf("ACME account with email %s already exists for %s (%s environment) in project %s",
			account.Email, account.CAProvider, account.Environment, account.Project)
	}

	// Generate ACME account key
	accountKey, err := GenerateECDSAPrivateKey()
	if err != nil {
		return fmt.Errorf("failed to generate account key: %w", err)
	}

	// Encrypt account key
	accountKeyPEM, err := EncodePrivateKeyToPEM(accountKey)
	if err != nil {
		return fmt.Errorf("failed to encode key: %w", err)
	}

	encryptedKey, err := m.encryptor.Encrypt(accountKeyPEM)
	if err != nil {
		return fmt.Errorf("failed to encrypt key: %w", err)
	}

	account.AccountKeyEncrypted = encryptedKey
	account.Status = AccountStatusRegistered // Start as "registering"
	account.IsRegistered = false
	account.CertificateCount = 0

	// Set timestamps
	now := time.Now()
	account.CreatedAt = now
	account.UpdatedAt = now
	if account.ID.IsZero() {
		account.ID = primitive.NewObjectID()
	}

	// Decrypt EAB credentials if provided (for Google, ZeroSSL, etc.)
	var eabKeyID, eabHMACKey string
	if account.EAB != nil {
		eabKeyID, eabHMACKey, err = m.encryptor.DecryptEABCredentials(account.EAB)
		if err != nil {
			account.Status = AccountStatusError
			account.LastError = &ErrorDetails{
				Timestamp: now,
				Message:   fmt.Sprintf("Failed to decrypt EAB credentials: %v", err),
			}
			_ = m.insertACMEAccount(ctx, account)
			return fmt.Errorf("failed to decrypt EAB credentials: %w", err)
		}
	}

	// Register with ACME server (automatically registers during client creation)
	acmeClient, err := NewACMEClientWithProvider(
		account.Email,
		CAProvider(account.CAProvider),
		account.Environment,
		accountKey,
		eabKeyID,
		eabHMACKey,
		m.caProviders,
	)
	if err != nil {
		account.Status = AccountStatusError
		account.LastError = &ErrorDetails{
			Timestamp: now,
			Message:   fmt.Sprintf("ACME registration failed: %v", err),
		}
		// Still insert to DB for troubleshooting
		_ = m.insertACMEAccount(ctx, account)
		return fmt.Errorf("failed to register ACME account: %w", err)
	}

	// Registration successful (NewACMEClientWithProvider automatically registers)
	account.Status = AccountStatusRegistered
	account.IsRegistered = true
	account.RegistrationURL = acmeClient.user.Registration.URI
	account.LastValidated = &now

	// Insert into database
	if err := m.insertACMEAccount(ctx, account); err != nil {
		return fmt.Errorf("failed to insert account: %w", err)
	}

	m.logger.Infof("Created and registered ACME account: %s (email: %s, provider: %s, env: %s, project: %s)",
		account.ID.Hex(), account.Email, account.CAProvider, account.Environment, account.Project)

	return nil
}

// insertACMEAccount inserts an ACME account into the database
func (m *CertificateManager) insertACMEAccount(ctx context.Context, account *ACMEAccount) error {
	collection := m.db.Collection("acme_accounts")
	_, err := collection.InsertOne(ctx, account)
	return err
}

// ListACMEAccounts lists ACME accounts with project isolation and permission filtering
func (m *CertificateManager) ListACMEAccounts(ctx context.Context, project string, user models.UserDetails) ([]*ACMEAccount, error) {
	collection := m.db.Collection("acme_accounts")

	filter := bson.M{"project": project}

	// Add permission filtering for non-Owner/Admin users
	if !user.IsOwner && user.Role != models.RoleAdmin {
		allGroups := append([]string{}, user.Groups...)
		if user.BaseGroup != "" {
			allGroups = append(allGroups, user.BaseGroup)
		}

		filter["$or"] = []bson.M{
			{"permissions.groups": bson.M{"$in": allGroups}},
			{"permissions.users": user.UserID},
		}
	}

	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to list ACME accounts: %w", err)
	}
	defer cursor.Close(ctx)

	var accounts []*ACMEAccount
	if err := cursor.All(ctx, &accounts); err != nil {
		return nil, fmt.Errorf("failed to decode ACME accounts: %w", err)
	}

	return accounts, nil
}

// DeleteACMEAccount deletes an ACME account with safety checks
func (m *CertificateManager) DeleteACMEAccount(ctx context.Context, accountID primitive.ObjectID, project string, force bool) error {
	// Verify account exists
	_, err := m.GetACMEAccount(ctx, accountID, project)
	if err != nil {
		return fmt.Errorf("account not found: %w", err)
	}

	// Count certificates using this account
	certCollection := m.db.Collection("acme_certificates")
	filter := bson.M{
		"project":         project,
		"acme.account_id": accountID,
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

		return &AccountInUseError{
			AccountID:      accountID,
			CertCount:      int(count),
			CertificateIDs: certIDs,
		}
	}

	// If force=true, mark certificates as orphaned
	if count > 0 {
		m.logger.Warnf("Force-deleting ACME account %s with %d active certificates (they will become orphaned)",
			accountID.Hex(), count)

		update := bson.M{
			"$set": bson.M{
				"acme.account_deleted":    true,
				"acme.account_deleted_at": time.Now(),
			},
		}
		_, err = certCollection.UpdateMany(ctx, filter, update)
		if err != nil {
			m.logger.Errorf("Failed to mark certificates as orphaned: %v", err)
		}
	}

	// Delete account
	accountCollection := m.db.Collection("acme_accounts")
	result, err := accountCollection.DeleteOne(ctx, bson.M{"_id": accountID, "project": project})
	if err != nil {
		return fmt.Errorf("failed to delete account: %w", err)
	}

	if result.DeletedCount == 0 {
		return fmt.Errorf("account not found")
	}

	m.logger.Infof("Deleted ACME account %s (project: %s)", accountID.Hex(), project)
	return nil
}

// ValidateACMEAccount validates an ACME account with Let's Encrypt
func (m *CertificateManager) ValidateACMEAccount(ctx context.Context, accountID primitive.ObjectID, project string) error {
	account, err := m.GetACMEAccount(ctx, accountID, project)
	if err != nil {
		return fmt.Errorf("account not found: %w", err)
	}

	// Decrypt account key
	accountKeyPEM, err := m.encryptor.Decrypt(account.AccountKeyEncrypted)
	if err != nil {
		return fmt.Errorf("failed to decrypt key: %w", err)
	}

	accountKey, err := DecodePrivateKeyFromPEM(accountKeyPEM)
	if err != nil {
		return fmt.Errorf("failed to decode key: %w", err)
	}

	// Create ACME client with existing registration
	acmeClient, err := NewACMEClientWithRegistration(
		account.Email,
		CAProvider(account.CAProvider),
		account.Environment,
		accountKey,
		account.RegistrationURL,
		m.caProviders,
	)
	if err != nil {
		return fmt.Errorf("failed to create ACME client: %w", err)
	}

	// Test account by checking registration (should always exist with NewACMEClientWithRegistration)
	if acmeClient.user.Registration == nil {
		return fmt.Errorf("account not registered with Let's Encrypt")
	}

	// Update last_validated timestamp
	now := time.Now()
	collection := m.db.Collection("acme_accounts")
	filter := bson.M{"_id": accountID, "project": project}
	update := bson.M{
		"$set": bson.M{
			"last_validated": now,
			"status":         AccountStatusRegistered,
			"updated_at":     now,
		},
	}

	_, err = collection.UpdateOne(ctx, filter, update)
	return err
}

// incrementAccountUsage increments the certificate_count for an account
func (m *CertificateManager) incrementAccountUsage(ctx context.Context, accountID primitive.ObjectID, project string) error {
	collection := m.db.Collection("acme_accounts")
	filter := bson.M{"_id": accountID, "project": project}
	update := bson.M{
		"$inc": bson.M{"certificate_count": 1},
		"$set": bson.M{
			"last_used":  time.Now(),
			"updated_at": time.Now(),
		},
	}

	_, err := collection.UpdateOne(ctx, filter, update)
	return err
}

// updateAccountLastUsed updates the last_used timestamp for an account
func (m *CertificateManager) updateAccountLastUsed(ctx context.Context, accountID primitive.ObjectID, project string) error {
	collection := m.db.Collection("acme_accounts")
	filter := bson.M{"_id": accountID, "project": project}
	update := bson.M{
		"$set": bson.M{
			"last_used":  time.Now(),
			"updated_at": time.Now(),
		},
	}

	_, err := collection.UpdateOne(ctx, filter, update)
	return err
}

// AccountInUseError represents an error when trying to delete an account that's in use
type AccountInUseError struct {
	AccountID      primitive.ObjectID
	CertCount      int
	CertificateIDs []string
}

func (e *AccountInUseError) Error() string {
	return fmt.Sprintf("Cannot delete ACME account. %d certificate(s) are using this account", e.CertCount)
}

// DNSCredentialInUseError indicates a DNS credential cannot be deleted because certificates depend on it
type DNSCredentialInUseError struct {
	CredentialID   primitive.ObjectID
	CertCount      int
	CertificateIDs []string
}

func (e *DNSCredentialInUseError) Error() string {
	return fmt.Sprintf("Cannot delete DNS credential. %d certificate(s) are using this credential for automatic renewal", e.CertCount)
}
