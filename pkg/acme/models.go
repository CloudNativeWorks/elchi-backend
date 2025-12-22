package acme

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// ACMECertificate represents a Let's Encrypt certificate metadata
// stored in the acme_certificates collection.
// The actual certificate is stored in the secrets collection.
type ACMECertificate struct {
	ID primitive.ObjectID `json:"id" bson:"_id,omitempty"`

	// PROJECT ISOLATION - MANDATORY
	Project string `json:"project" bson:"project"`

	// Link to secrets collection
	SecretName string `json:"secret_name" bson:"secret_name"`

	// Multiple Envoy versions can use the same certificate
	// When renewed, all versions are updated
	SecretVersions []string `json:"secret_versions" bson:"secret_versions"`

	// Domain Information
	Domains       []string `json:"domains" bson:"domains"`
	PrimaryDomain string   `json:"primary_domain" bson:"primary_domain"`

	// ACME Metadata
	ACME ACMEMetadata `json:"acme" bson:"acme"`

	// DNS Verification
	DNSVerification DNSVerification `json:"dns_verification" bson:"dns_verification"`

	// Lifecycle Status
	Status string `json:"status" bson:"status"`
	// Possible values: pending_dns, verifying, active, renewal_pending, renewal_failed, expired

	// Certificate Validity
	IssuedAt       time.Time `json:"issued_at" bson:"issued_at"`
	ExpiresAt      time.Time `json:"expires_at" bson:"expires_at"`
	RenewalStartsAt time.Time `json:"renewal_starts_at" bson:"renewal_starts_at"`

	// Renewal Settings
	AutoRenew            bool      `json:"auto_renew" bson:"auto_renew"`
	RenewalAttempts      int       `json:"renewal_attempts" bson:"renewal_attempts"`
	LastRenewalAttempt   time.Time `json:"last_renewal_attempt,omitempty" bson:"last_renewal_attempt,omitempty"`

	// Job Tracking (for async verification/renewal)
	LastJobID string `json:"last_job_id,omitempty" bson:"last_job_id,omitempty"`

	// Error Tracking
	LastError *ErrorDetails `json:"last_error,omitempty" bson:"last_error,omitempty"`

	// Permissions (matches secrets collection pattern)
	Permissions Permissions `json:"permissions" bson:"permissions"`

	// Audit
	CreatedBy      string    `json:"created_by" bson:"created_by"`
	CreatedAt      time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt      time.Time `json:"updated_at" bson:"updated_at"`
	LastRenewedBy  string    `json:"last_renewed_by,omitempty" bson:"last_renewed_by,omitempty"`
	LastRenewedAt  time.Time `json:"last_renewed_at,omitempty" bson:"last_renewed_at,omitempty"`
}

// ACMEMetadata contains Let's Encrypt ACME protocol metadata
type ACMEMetadata struct {
	AccountID *primitive.ObjectID `json:"account_id" bson:"account_id"` // References acme_accounts._id (REQUIRED)

	OrderURL    string `json:"order_url,omitempty" bson:"order_url,omitempty"` // ACME order URL for pending certificates
	CertURL     string `json:"cert_url" bson:"cert_url"`
	Environment string `json:"environment" bson:"environment"` // "staging" | "production"
}

// DNSVerification contains DNS verification configuration and challenges
type DNSVerification struct {
	Provider string `json:"provider" bson:"provider"` // "manual" | "google" | "godaddy" | "cloudflare" | "digitalocean" | "route53" | "lightsail"

	// For automatic verification (optional)
	DNSCredentialID   *primitive.ObjectID `json:"dns_credential_id,omitempty" bson:"dns_credential_id,omitempty"`
	DNSCredentialName string              `json:"dns_credential_name,omitempty" bson:"dns_credential_name,omitempty"`

	// For manual verification only
	PendingChallenges []DNSChallenge `json:"pending_challenges,omitempty" bson:"pending_challenges,omitempty"`
}

// DNSChallenge represents a DNS-01 challenge that needs to be completed
type DNSChallenge struct {
	Domain    string    `json:"domain" bson:"domain"`         // Base domain (e.g., example.com)
	FQDN      string    `json:"fqdn" bson:"fqdn"`             // Full DNS record name (e.g., _acme-challenge.example.com)
	Type      string    `json:"type" bson:"type"`             // "TXT"
	Value     string    `json:"value" bson:"value"`           // DNS TXT record value (base64 encoded)
	Token     string    `json:"token,omitempty" bson:"token,omitempty"` // ACME challenge token
	KeyAuth   string    `json:"-" bson:"-"`                   // Full key authorization (not stored/exposed)
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
	ExpiresAt time.Time `json:"expires_at" bson:"expires_at"` // created_at + 24 hours
}

// ErrorDetails contains error information for failed operations
type ErrorDetails struct {
	Timestamp  time.Time `json:"timestamp" bson:"timestamp"`
	Message    string    `json:"message" bson:"message"`
	Details    string    `json:"details,omitempty" bson:"details,omitempty"`
	RetryAfter string    `json:"retry_after,omitempty" bson:"retry_after,omitempty"`
}

// Permissions defines who can access the resource
type Permissions struct {
	Users  []string `json:"users" bson:"users"`
	Groups []string `json:"groups" bson:"groups"`
}

// DNSCredential represents DNS provider credentials stored in acme_dns_credentials collection
type DNSCredential struct {
	ID primitive.ObjectID `json:"id" bson:"_id,omitempty"`

	// PROJECT ISOLATION - MANDATORY
	Project string `json:"project" bson:"project"`

	// Credential Information
	Name        string `json:"name" bson:"name"`
	Description string `json:"description,omitempty" bson:"description,omitempty"`
	Provider    string `json:"provider" bson:"provider"` // "google" | "godaddy" | "cloudflare" | "digitalocean" | "route53" | "lightsail"

	// Encrypted Credentials (provider-specific JSON)
	CredentialsEncrypted string `json:"credentials_encrypted" bson:"credentials_encrypted"`

	// Validation Status
	Status        string    `json:"status" bson:"status"` // "active" | "invalid" | "testing"
	LastValidated time.Time `json:"last_validated,omitempty" bson:"last_validated,omitempty"`
	LastError     string    `json:"last_error,omitempty" bson:"last_error,omitempty"`

	// Permissions
	Permissions Permissions `json:"permissions" bson:"permissions"`

	// Audit
	CreatedBy string    `json:"created_by" bson:"created_by"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt time.Time `json:"updated_at" bson:"updated_at"`
}

// TempKey represents a temporary private key stored during certificate issuance
// in the acme_temp_keys collection
type TempKey struct {
	ID primitive.ObjectID `json:"id" bson:"_id,omitempty"`

	// PROJECT ISOLATION - MANDATORY
	Project string `json:"project" bson:"project"`

	// Link to certificate request
	CertRequestID primitive.ObjectID `json:"cert_request_id" bson:"cert_request_id"`
	SecretName    string             `json:"secret_name" bson:"secret_name"`

	// Key Material
	PrivateKeyPEM string `json:"private_key_pem" bson:"private_key_pem"`
	CSRPEM        string `json:"csr_pem" bson:"csr_pem"`

	// Lifecycle
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
	ExpiresAt time.Time `json:"expires_at" bson:"expires_at"` // created_at + 1 hour
	Status    string    `json:"status" bson:"status"`         // "pending" | "completed" | "failed"
}

// EABCredentials represents External Account Binding credentials for CA providers that require it
type EABCredentials struct {
	KeyID            string `json:"key_id" bson:"key_id"`                               // Encrypted EAB Key ID (from CA)
	HMACKeyEncrypted string `json:"hmac_key_encrypted" bson:"hmac_key_encrypted"`       // Encrypted EAB HMAC key (base64-encoded)
}

// ACMEAccount represents an ACME account stored in the acme_accounts collection
// Multiple certificates can reference the same account for rate limit efficiency
type ACMEAccount struct {
	ID primitive.ObjectID `json:"id" bson:"_id,omitempty"`

	// PROJECT ISOLATION - MANDATORY
	Project string `json:"project" bson:"project"`

	// Account Information
	Name        string `json:"name" bson:"name"`
	Description string `json:"description,omitempty" bson:"description,omitempty"`
	Email       string `json:"email" bson:"email"`

	// CA Provider Selection (NEW for multi-CA support)
	CAProvider  string `json:"ca_provider" bson:"ca_provider"`   // "letsencrypt" | "google" | "zerossl" | "buypass"
	Environment string `json:"environment" bson:"environment"`   // "staging" | "production"

	// ACME Registration Details
	AccountKeyEncrypted string `json:"account_key_encrypted" bson:"account_key_encrypted"` // Encrypted ECDSA private key
	RegistrationURL     string `json:"registration_url,omitempty" bson:"registration_url,omitempty"`

	// External Account Binding (NEW - for Google, ZeroSSL, etc.)
	EAB *EABCredentials `json:"eab,omitempty" bson:"eab,omitempty"`

	// Account Status
	Status        string     `json:"status" bson:"status"` // "registered" | "active" | "deactivated" | "error"
	IsRegistered  bool       `json:"is_registered" bson:"is_registered"`
	LastValidated *time.Time `json:"last_validated,omitempty" bson:"last_validated,omitempty"`

	// Usage Tracking
	CertificateCount int        `json:"certificate_count" bson:"certificate_count"`
	LastUsed         *time.Time `json:"last_used,omitempty" bson:"last_used,omitempty"`

	// Error Tracking
	LastError *ErrorDetails `json:"last_error,omitempty" bson:"last_error,omitempty"`

	// Permissions
	Permissions Permissions `json:"permissions" bson:"permissions"`

	// Audit
	CreatedBy string    `json:"created_by" bson:"created_by"`
	CreatedAt time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt time.Time `json:"updated_at" bson:"updated_at"`
}

// GoogleCloudDNSCredentials represents Google Cloud DNS provider credentials
type GoogleCloudDNSCredentials struct {
	ProjectID          string `json:"project_id"`
	ServiceAccountJSON string `json:"service_account_json"`
	ZoneID             string `json:"zone_id,omitempty"` // Optional: GCP Managed Zone ID to bypass SOA lookup (fixes split-horizon DNS issues)
}

// GoDaddyCredentials represents GoDaddy DNS provider credentials
type GoDaddyCredentials struct {
	APIKey    string `json:"api_key"`
	APISecret string `json:"api_secret"`
}

// CloudflareCredentials represents Cloudflare DNS provider credentials
type CloudflareCredentials struct {
	APIToken string `json:"api_token"` // Cloudflare API token with DNS:Edit permissions
}

// DigitalOceanCredentials represents DigitalOcean DNS provider credentials
type DigitalOceanCredentials struct {
	APIToken string `json:"api_token"` // DigitalOcean API token with read/write permissions
}

// Route53Credentials represents AWS Route 53 DNS provider credentials
type Route53Credentials struct {
	AccessKeyID     string `json:"access_key_id"`     // AWS Access Key ID
	SecretAccessKey string `json:"secret_access_key"` // AWS Secret Access Key
	Region          string `json:"region,omitempty"`  // AWS Region (optional, defaults to us-east-1)
	HostedZoneID    string `json:"hosted_zone_id,omitempty"` // Specific Hosted Zone ID (optional)
}

// LightsailCredentials represents AWS Lightsail DNS provider credentials
type LightsailCredentials struct {
	AccessKeyID     string `json:"access_key_id"`     // AWS Access Key ID
	SecretAccessKey string `json:"secret_access_key"` // AWS Secret Access Key
	Region          string `json:"region,omitempty"`  // AWS Region (optional, defaults to us-east-1)
	DNSZone         string `json:"dns_zone"`          // Lightsail DNS zone (e.g., "example.com")
}

// CertificateStatus constants
const (
	StatusPendingDNS     = "pending_dns"
	StatusVerifying      = "verifying"
	StatusActive         = "active"
	StatusRenewalPending = "renewal_pending"
	StatusRenewalFailed  = "renewal_failed"
	StatusExpired        = "expired"
)

// DNS Provider constants
const (
	ProviderManual       = "manual"
	ProviderGoogle       = "google"
	ProviderGoDaddy      = "godaddy"
	ProviderCloudflare   = "cloudflare"
	ProviderDigitalOcean = "digitalocean"
	ProviderRoute53      = "route53"
	ProviderLightsail    = "lightsail"
)

// ACME Environment constants
const (
	EnvironmentStaging    = "staging"
	EnvironmentProduction = "production"
)

// Credential Status constants
const (
	CredentialStatusActive  = "active"
	CredentialStatusInvalid = "invalid"
	CredentialStatusTesting = "testing"
)

// ACME Account Status constants
const (
	AccountStatusRegistered  = "registered"
	AccountStatusActive      = "active"
	AccountStatusDeactivated = "deactivated"
	AccountStatusError       = "error"
)
