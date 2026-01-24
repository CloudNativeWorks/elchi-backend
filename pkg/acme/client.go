// Package acme provides ACME protocol client implementation for automated
// certificate management including account handling, DNS challenges, and renewals.
package acme

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
)

// CAProvider represents a Certificate Authority provider
type CAProvider string

const (
	CAProviderLetsEncrypt CAProvider = "letsencrypt"
	CAProviderGoogle      CAProvider = "google"
	CAProviderZeroSSL     CAProvider = "zerossl"
	CAProviderBuypass     CAProvider = "buypass"
)

// ACMEUser implements the registration.User interface required by Lego
type ACMEUser struct {
	Email        string
	Registration *registration.Resource
	key          crypto.PrivateKey
}

// GetEmail returns the user's email address
func (u *ACMEUser) GetEmail() string {
	return u.Email
}

// GetRegistration returns the user's registration resource
func (u *ACMEUser) GetRegistration() *registration.Resource {
	return u.Registration
}

// GetPrivateKey returns the user's private key
func (u *ACMEUser) GetPrivateKey() crypto.PrivateKey {
	return u.key
}

// ACMEClient wraps the Lego client for ACME operations with any CA provider
type ACMEClient struct {
	client      *lego.Client
	user        *ACMEUser
	provider    CAProvider
	environment string
}

// NewACMEClientWithProvider creates a new ACME client with multi-CA support
// This is the main function for creating ACME clients - supports Let's Encrypt, Google Trust Services, etc.
func NewACMEClientWithProvider(
	email string,
	caProvider CAProvider,
	environment string,
	accountKey crypto.PrivateKey,
	eabKeyID, eabHMACKey string,
	caProviders map[string]config.CAProviderConfig,
) (*ACMEClient, error) {
	if email == "" {
		return nil, fmt.Errorf("email cannot be empty")
	}

	if environment != EnvironmentStaging && environment != EnvironmentProduction {
		return nil, fmt.Errorf("invalid environment: %s (must be 'staging' or 'production')", environment)
	}

	// Get CA provider configuration
	providerConfig, exists := caProviders[string(caProvider)]
	if !exists {
		return nil, fmt.Errorf("unknown CA provider: %s", caProvider)
	}

	if !providerConfig.Supported {
		return nil, fmt.Errorf("CA provider %s is not yet supported", caProvider)
	}

	// Get directory URL for this provider and environment
	envConfig, exists := providerConfig.Environments[environment]
	if !exists {
		return nil, fmt.Errorf("environment %s not supported for %s", environment, caProvider)
	}

	directoryURL := envConfig.DirectoryURL

	// Create or reuse account key
	var key crypto.PrivateKey
	var err error

	if accountKey != nil {
		key = accountKey
	} else {
		// Generate new ECDSA P-256 key (smaller and faster than RSA)
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("failed to generate account key: %w", err)
		}
	}

	// Create ACME user
	user := &ACMEUser{
		Email: email,
		key:   key,
	}

	// Configure Lego client
	legoCfg := lego.NewConfig(user)
	legoCfg.CADirURL = directoryURL
	legoCfg.Certificate.KeyType = certcrypto.RSA2048 // Envoy compatibility

	// Create Lego client
	client, err := lego.NewClient(legoCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create ACME client: %w", err)
	}

	// Register account with ACME server
	if providerConfig.RequiresEAB {
		// Register with External Account Binding (Google, ZeroSSL, etc.)
		if eabKeyID == "" || eabHMACKey == "" {
			return nil, fmt.Errorf("EAB credentials required for %s but not provided", caProvider)
		}

		reg, err := client.Registration.RegisterWithExternalAccountBinding(registration.RegisterEABOptions{
			TermsOfServiceAgreed: true,
			Kid:                  eabKeyID,
			HmacEncoded:          eabHMACKey,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to register with EAB: %w", err)
		}

		user.Registration = reg
	} else {
		// Register without EAB (Let's Encrypt, Buypass)
		reg, err := client.Registration.Register(registration.RegisterOptions{
			TermsOfServiceAgreed: true,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to register account: %w", err)
		}

		user.Registration = reg
	}

	return &ACMEClient{
		client:      client,
		user:        user,
		provider:    caProvider,
		environment: environment,
	}, nil
}

// NewACMEClientWithRegistration creates an ACME client with existing account registration
// This is used when working with an already-registered ACME account (for renewals, etc.)
func NewACMEClientWithRegistration(
	email string,
	caProvider CAProvider,
	environment string,
	accountKey crypto.PrivateKey,
	registrationURL string,
	caProviders map[string]config.CAProviderConfig,
) (*ACMEClient, error) {
	if email == "" {
		return nil, fmt.Errorf("email cannot be empty")
	}

	if environment != EnvironmentStaging && environment != EnvironmentProduction {
		return nil, fmt.Errorf("invalid environment: %s (must be 'staging' or 'production')", environment)
	}

	if accountKey == nil {
		return nil, fmt.Errorf("account key cannot be nil for existing registration")
	}

	if registrationURL == "" {
		return nil, fmt.Errorf("registration URL cannot be empty for existing registration")
	}

	// Get CA provider configuration
	providerConfig, exists := caProviders[string(caProvider)]
	if !exists {
		return nil, fmt.Errorf("unknown CA provider: %s", caProvider)
	}

	if !providerConfig.Supported {
		return nil, fmt.Errorf("CA provider %s is not yet supported", caProvider)
	}

	// Get directory URL
	envConfig, exists := providerConfig.Environments[environment]
	if !exists {
		return nil, fmt.Errorf("environment %s not supported for %s", environment, caProvider)
	}

	directoryURL := envConfig.DirectoryURL

	// Create ACME user with existing registration
	user := &ACMEUser{
		Email: email,
		key:   accountKey,
		Registration: &registration.Resource{
			URI: registrationURL,
		},
	}

	// Configure Lego client
	legoCfg := lego.NewConfig(user)
	legoCfg.CADirURL = directoryURL
	legoCfg.Certificate.KeyType = certcrypto.RSA2048

	// Create Lego client
	client, err := lego.NewClient(legoCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create ACME client: %w", err)
	}

	return &ACMEClient{
		client:      client,
		user:        user,
		provider:    caProvider,
		environment: environment,
	}, nil
}

// ObtainCertificate requests a new certificate from the ACME server
// Requires DNS-01 challenges to be set up beforehand
func (c *ACMEClient) ObtainCertificate(domains []string, privateKey crypto.PrivateKey) (*certificate.Resource, error) {
	if len(domains) == 0 {
		return nil, fmt.Errorf("domains list cannot be empty")
	}

	if privateKey == nil {
		return nil, fmt.Errorf("private key cannot be nil")
	}

	// Create certificate request
	request := certificate.ObtainRequest{
		Domains:    domains,
		Bundle:     true, // Include intermediate certificates
		PrivateKey: privateKey,
	}

	// Obtain certificate
	cert, err := c.client.Certificate.Obtain(request)
	if err != nil {
		return nil, fmt.Errorf("failed to obtain certificate: %w", err)
	}

	return cert, nil
}

// GetClient returns the underlying Lego client for advanced operations
func (c *ACMEClient) GetClient() *lego.Client {
	return c.client
}

// GenerateRSAPrivateKey generates a new RSA-2048 private key for certificates
func GenerateRSAPrivateKey() (crypto.PrivateKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("failed to generate RSA key: %w", err)
	}
	return key, nil
}

// GenerateECDSAPrivateKey generates a new ECDSA P-256 private key for ACME accounts
func GenerateECDSAPrivateKey() (crypto.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate ECDSA key: %w", err)
	}
	return key, nil
}

// EncodePrivateKeyToPEM encodes a private key to PEM format
func EncodePrivateKeyToPEM(key crypto.PrivateKey) (string, error) {
	var pemBlock *pem.Block

	switch k := key.(type) {
	case *rsa.PrivateKey:
		pemBlock = &pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(k),
		}
	case *ecdsa.PrivateKey:
		bytes, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			return "", fmt.Errorf("failed to marshal ECDSA key: %w", err)
		}
		pemBlock = &pem.Block{
			Type:  "EC PRIVATE KEY",
			Bytes: bytes,
		}
	default:
		return "", fmt.Errorf("unsupported private key type: %T", key)
	}

	return string(pem.EncodeToMemory(pemBlock)), nil
}

// DecodePrivateKeyFromPEM decodes a PEM-encoded private key
func DecodePrivateKeyFromPEM(pemStr string) (crypto.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}

	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse RSA private key: %w", err)
		}
		return key, nil
	case "EC PRIVATE KEY":
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse ECDSA private key: %w", err)
		}
		return key, nil
	case "PRIVATE KEY":
		// PKCS8 format
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse PKCS8 private key: %w", err)
		}
		return key, nil
	default:
		return nil, fmt.Errorf("unsupported PEM block type: %s", block.Type)
	}
}

// GenerateCSR generates a Certificate Signing Request for the given domains and private key
func GenerateCSR(domains []string, privateKey crypto.PrivateKey) ([]byte, error) {
	if len(domains) == 0 {
		return nil, fmt.Errorf("domains list cannot be empty")
	}

	template := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: domains[0]},
		DNSNames: domains,
	}

	csrBytes, err := x509.CreateCertificateRequest(rand.Reader, template, privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create CSR: %w", err)
	}

	return csrBytes, nil
}

// EncodeCSRToPEM encodes CSR to PEM format
func EncodeCSRToPEM(csrBytes []byte) string {
	pemBlock := &pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csrBytes,
	}
	return string(pem.EncodeToMemory(pemBlock))
}

// CreateCSR is a helper that combines GenerateCSR and returns raw bytes
// This is an alias for GenerateCSR for backwards compatibility
func CreateCSR(privateKey crypto.PrivateKey, domains []string) ([]byte, error) {
	return GenerateCSR(domains, privateKey)
}
