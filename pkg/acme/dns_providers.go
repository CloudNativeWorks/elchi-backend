package acme

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/digitalocean"
	"github.com/go-acme/lego/v4/providers/dns/gcloud"
	"github.com/go-acme/lego/v4/providers/dns/godaddy"
	"github.com/go-acme/lego/v4/providers/dns/lightsail"
	"github.com/go-acme/lego/v4/providers/dns/route53"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"golang.org/x/oauth2/google"
	gdns "google.golang.org/api/dns/v1"
)

// DNSProvider interface for DNS challenge providers
type DNSProvider interface {
	Present(domain, token, keyAuth string) error
	CleanUp(domain, token, keyAuth string) error
}

// NOTE: DNS resolver configuration (AddRecursiveNameservers, AddDNSTimeout)
// is performed globally in main.go init() function to ensure it's set before
// any lego package initialization. This prevents split-horizon DNS issues
// where internal Kubernetes DNS cannot resolve external SOA records.

// Common DNS provider configuration
const (
	DefaultPropagationTimeout = 5 * time.Minute
	DefaultPollingInterval    = 10 * time.Second
	DefaultDNSTimeout         = 3 * time.Second
)

// providerFactory defines a function that creates a DNS provider
type providerFactory func(m *CertificateManager, ctx context.Context, creds string) (DNSProvider, error)

// providerRegistry maps provider names to their factory functions
var providerRegistry = map[string]providerFactory{
	"google":       (*CertificateManager).createGoogleCloudProvider,
	"godaddy":      (*CertificateManager).createGoDaddyProvider,
	"cloudflare":   (*CertificateManager).createCloudflareProvider,
	"digitalocean": (*CertificateManager).createDigitalOceanProvider,
	"route53":      (*CertificateManager).createRoute53Provider,
	"lightsail":    (*CertificateManager).createLightsailProvider,
}

// SetupDNSProvider creates appropriate DNS provider based on credential type
// This is the main entry point used by workflows
func (m *CertificateManager) SetupDNSProvider(ctx context.Context, credentialID primitive.ObjectID, project string) (DNSProvider, error) {
	// Get and decrypt credential
	cred, decrypted, err := m.GetDNSCredential(ctx, credentialID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get DNS credential: %w", err)
	}

	// Get factory function
	factory, exists := providerRegistry[cred.Provider]
	if !exists {
		return nil, fmt.Errorf("unsupported DNS provider: %s", cred.Provider)
	}

	// Create provider
	provider, err := factory(m, ctx, decrypted)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s provider: %w", cred.Provider, err)
	}

	m.logger.Infof("Created %s DNS provider with %v propagation timeout", cred.Provider, DefaultPropagationTimeout)
	return provider, nil
}

// Google Cloud DNS Provider Factory
func (m *CertificateManager) createGoogleCloudProvider(ctx context.Context, credsJSON string) (DNSProvider, error) {
	var creds GoogleCloudDNSCredentials
	if err := json.Unmarshal([]byte(credsJSON), &creds); err != nil {
		return nil, fmt.Errorf("failed to parse credentials: %w", err)
	}

	if creds.ProjectID == "" || creds.ServiceAccountJSON == "" {
		return nil, fmt.Errorf("project_id and service_account_json are required")
	}

	// Create config with custom timeouts and project ID
	config := gcloud.NewDefaultConfig()
	config.Project = creds.ProjectID
	config.PropagationTimeout = DefaultPropagationTimeout
	config.PollingInterval = DefaultPollingInterval
	config.TTL = 300

	// Create HTTP client from service account
	conf, err := google.JWTConfigFromJSON([]byte(creds.ServiceAccountJSON), gdns.NdevClouddnsReadwriteScope)
	if err != nil {
		return nil, fmt.Errorf("failed to parse service account: %w", err)
	}
	config.HTTPClient = conf.Client(ctx)

	return gcloud.NewDNSProviderConfig(config)
}

// GoDaddy Provider Factory
func (m *CertificateManager) createGoDaddyProvider(ctx context.Context, credsJSON string) (DNSProvider, error) {
	var creds GoDaddyCredentials
	if err := json.Unmarshal([]byte(credsJSON), &creds); err != nil {
		return nil, fmt.Errorf("failed to parse credentials: %w", err)
	}

	if creds.APIKey == "" || creds.APISecret == "" {
		return nil, fmt.Errorf("api_key and api_secret are required")
	}

	config := godaddy.NewDefaultConfig()
	config.APIKey = creds.APIKey
	config.APISecret = creds.APISecret
	config.PropagationTimeout = DefaultPropagationTimeout
	config.PollingInterval = DefaultPollingInterval
	config.TTL = 600

	return godaddy.NewDNSProviderConfig(config)
}

// Cloudflare Provider Factory
func (m *CertificateManager) createCloudflareProvider(ctx context.Context, credsJSON string) (DNSProvider, error) {
	var creds CloudflareCredentials
	if err := json.Unmarshal([]byte(credsJSON), &creds); err != nil {
		m.logger.Errorf("Failed to parse Cloudflare credentials. Raw JSON: %s, Error: %v", credsJSON, err)
		return nil, fmt.Errorf("failed to parse credentials: %w", err)
	}

	// Debug log to verify parsed values
	m.logger.Debugf("Parsed Cloudflare credentials: APIToken length=%d", len(creds.APIToken))

	if creds.APIToken == "" {
		m.logger.Errorf("Cloudflare APIToken is empty after parsing. Raw JSON: %s", credsJSON)
		return nil, fmt.Errorf("api_token is required")
	}

	config := cloudflare.NewDefaultConfig()
	config.AuthToken = creds.APIToken
	config.PropagationTimeout = DefaultPropagationTimeout
	config.PollingInterval = DefaultPollingInterval
	config.TTL = 120

	return cloudflare.NewDNSProviderConfig(config)
}

// DigitalOcean Provider Factory
func (m *CertificateManager) createDigitalOceanProvider(ctx context.Context, credsJSON string) (DNSProvider, error) {
	var creds DigitalOceanCredentials
	if err := json.Unmarshal([]byte(credsJSON), &creds); err != nil {
		m.logger.Errorf("Failed to parse DigitalOcean credentials. Raw JSON: %s, Error: %v", credsJSON, err)
		return nil, fmt.Errorf("failed to parse credentials: %w", err)
	}

	// Debug log to verify parsed values
	m.logger.Debugf("Parsed DigitalOcean credentials: APIToken length=%d", len(creds.APIToken))

	if creds.APIToken == "" {
		m.logger.Errorf("DigitalOcean APIToken is empty after parsing. Raw JSON: %s", credsJSON)
		return nil, fmt.Errorf("api_token is required")
	}

	config := digitalocean.NewDefaultConfig()
	config.AuthToken = creds.APIToken
	config.PropagationTimeout = DefaultPropagationTimeout
	config.PollingInterval = DefaultPollingInterval
	config.TTL = 120

	return digitalocean.NewDNSProviderConfig(config)
}

// Route53 Provider Factory
func (m *CertificateManager) createRoute53Provider(ctx context.Context, credsJSON string) (DNSProvider, error) {
	var creds Route53Credentials
	if err := json.Unmarshal([]byte(credsJSON), &creds); err != nil {
		m.logger.Errorf("Failed to parse Route 53 credentials. Raw JSON: %s, Error: %v", credsJSON, err)
		return nil, fmt.Errorf("failed to parse credentials: %w", err)
	}

	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return nil, fmt.Errorf("access_key_id and secret_access_key are required")
	}

	if creds.Region == "" {
		creds.Region = "us-east-1"
	}

	config := route53.NewDefaultConfig()
	config.AccessKeyID = creds.AccessKeyID
	config.SecretAccessKey = creds.SecretAccessKey
	config.Region = creds.Region
	if creds.HostedZoneID != "" {
		config.HostedZoneID = creds.HostedZoneID
	}
	config.PropagationTimeout = DefaultPropagationTimeout
	config.PollingInterval = DefaultPollingInterval
	config.TTL = 60

	return route53.NewDNSProviderConfig(config)
}

// Lightsail Provider Factory
func (m *CertificateManager) createLightsailProvider(ctx context.Context, credsJSON string) (DNSProvider, error) {
	var creds LightsailCredentials
	if err := json.Unmarshal([]byte(credsJSON), &creds); err != nil {
		m.logger.Errorf("Failed to parse Lightsail credentials. Raw JSON: %s, Error: %v", credsJSON, err)
		return nil, fmt.Errorf("failed to parse credentials: %w", err)
	}

	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" || creds.DNSZone == "" {
		return nil, fmt.Errorf("access_key_id, secret_access_key, and dns_zone are required")
	}

	if creds.Region == "" {
		creds.Region = "us-east-1"
	}

	// Lightsail uses environment variables
	originalAccessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	originalSecretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	originalRegion := os.Getenv("AWS_REGION")

	defer func() {
		// Restore original env vars
		restoreEnv("AWS_ACCESS_KEY_ID", originalAccessKey)
		restoreEnv("AWS_SECRET_ACCESS_KEY", originalSecretKey)
		restoreEnv("AWS_REGION", originalRegion)
	}()

	os.Setenv("AWS_ACCESS_KEY_ID", creds.AccessKeyID)
	os.Setenv("AWS_SECRET_ACCESS_KEY", creds.SecretAccessKey)
	os.Setenv("AWS_REGION", creds.Region)

	config := lightsail.NewDefaultConfig()
	config.DNSZone = creds.DNSZone // CRITICAL: Set the DNS zone for Lightsail
	config.PropagationTimeout = DefaultPropagationTimeout
	config.PollingInterval = DefaultPollingInterval

	return lightsail.NewDNSProviderConfig(config)
}

// Helper: restore environment variable
func restoreEnv(key, value string) {
	if value != "" {
		os.Setenv(key, value)
	} else {
		os.Unsetenv(key)
	}
}

// TestDNSCredentials tests DNS credentials
func (m *CertificateManager) TestDNSCredentials(ctx context.Context, provider string, credentialsJSON string, testDomain string) error {
	// Validate provider
	if err := ValidateProvider(provider); err != nil {
		return err
	}

	factory, exists := providerRegistry[provider]
	if !exists {
		return fmt.Errorf("unsupported DNS provider: %s", provider)
	}

	dnsProvider, err := factory(m, ctx, credentialsJSON)
	if err != nil {
		return fmt.Errorf("failed to create DNS provider: %w", err)
	}

	// Test with a dummy challenge
	testToken := fmt.Sprintf("elchi-test-%d", time.Now().Unix())
	testKeyAuth := "test-key-auth-value"

	m.logger.Infof("Testing %s DNS credentials for domain: %s", provider, testDomain)

	if err := dnsProvider.Present(testDomain, testToken, testKeyAuth); err != nil {
		return fmt.Errorf("failed to create test DNS record: %w", err)
	}

	m.logger.Infof("Successfully created test DNS record for domain: %s", testDomain)
	time.Sleep(2 * time.Second)

	if err := dnsProvider.CleanUp(testDomain, testToken, testKeyAuth); err != nil {
		m.logger.Warnf("Failed to cleanup test DNS record (non-critical): %v", err)
	} else {
		m.logger.Infof("Successfully cleaned up test DNS record for domain: %s", testDomain)
	}

	return nil
}
