package acme

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/digitalocean"
	"github.com/go-acme/lego/v4/providers/dns/gcloud"
	"github.com/go-acme/lego/v4/providers/dns/godaddy"
	"github.com/go-acme/lego/v4/providers/dns/lightsail"
	"github.com/go-acme/lego/v4/providers/dns/route53"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// DNSProvider interface for DNS challenge providers
type DNSProvider interface {
	Present(domain, token, keyAuth string) error
	CleanUp(domain, token, keyAuth string) error
}

// createDNSProviderOptions creates DNS provider options for DNS-01 challenge
// Returns recursive nameserver configuration for split-horizon DNS environments.
// - Adds public recursive DNS (8.8.8.8, 1.1.1.1) as additional resolvers
// - Authoritative nameservers are still queried (fallback mechanism)
// - If authoritative NS timeout (internal IP unreachable), public DNS provides fallback
func createDNSProviderOptions() []dns01.ChallengeOption {
	// Add public recursive DNS servers (in addition to authoritative NS)
	// This way both authoritative NS (ns3.hepsi.io) and public DNS (8.8.8.8) are checked
	// If authoritative NS times out, public DNS provides fallback
	return []dns01.ChallengeOption{
		dns01.AddRecursiveNameservers([]string{
			"8.8.8.8:53",  // Google Public DNS Primary
			"8.8.4.4:53",  // Google Public DNS Secondary
			"1.1.1.1:53",  // Cloudflare DNS Primary
			"1.0.0.1:53",  // Cloudflare DNS Secondary
		}),
		// NOTE: NOT using DisableAuthoritativeNssPropagationRequirement()
		// We don't want to completely disable authoritative NS checks
		// Both authoritative and recursive DNS checks should happen (fallback mechanism)
	}
}

// SetupGoogleCloudDNSProvider creates a Google Cloud DNS provider
func (m *CertificateManager) SetupGoogleCloudDNSProvider(ctx context.Context, credentialID primitive.ObjectID, project string) (DNSProvider, error) {
	// Get and decrypt credentials
	cred, decrypted, err := m.GetDNSCredential(ctx, credentialID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get DNS credential: %w", err)
	}

	// Validate provider type
	if cred.Provider != "google" {
		return nil, fmt.Errorf("credential is not for Google Cloud DNS (got: %s)", cred.Provider)
	}

	// Parse credentials
	var gcpCreds GoogleCloudDNSCredentials
	if err := json.Unmarshal([]byte(decrypted), &gcpCreds); err != nil {
		return nil, fmt.Errorf("failed to parse Google Cloud DNS credentials: %w", err)
	}

	// Validate required fields
	if gcpCreds.ProjectID == "" {
		return nil, fmt.Errorf("project_id is required for Google Cloud DNS")
	}
	if gcpCreds.ServiceAccountJSON == "" {
		return nil, fmt.Errorf("service_account_json is required for Google Cloud DNS")
	}

	// Use lego's NewDNSProviderServiceAccountKey which properly initializes the HTTP client
	// This handles all the OAuth2 authentication setup internally
	provider, err := gcloud.NewDNSProviderServiceAccountKey([]byte(gcpCreds.ServiceAccountJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to create Google Cloud DNS provider: %w", err)
	}

	m.logger.Infof("Created Google Cloud DNS provider for project: %s", gcpCreds.ProjectID)
	return provider, nil
}

// SetupGoDaddyDNSProvider creates a GoDaddy DNS provider
func (m *CertificateManager) SetupGoDaddyDNSProvider(ctx context.Context, credentialID primitive.ObjectID, project string) (DNSProvider, error) {
	// Get and decrypt credentials
	cred, decrypted, err := m.GetDNSCredential(ctx, credentialID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get DNS credential: %w", err)
	}

	// Validate provider type
	if cred.Provider != "godaddy" {
		return nil, fmt.Errorf("credential is not for GoDaddy (got: %s)", cred.Provider)
	}

	// Parse credentials
	var godaddyCreds GoDaddyCredentials
	if err := json.Unmarshal([]byte(decrypted), &godaddyCreds); err != nil {
		return nil, fmt.Errorf("failed to parse GoDaddy credentials: %w", err)
	}

	// Validate required fields
	if godaddyCreds.APIKey == "" {
		return nil, fmt.Errorf("api_key is required for GoDaddy")
	}
	if godaddyCreds.APISecret == "" {
		return nil, fmt.Errorf("api_secret is required for GoDaddy")
	}

	// Create Lego GoDaddy DNS provider config
	config := godaddy.NewDefaultConfig()
	config.APIKey = godaddyCreds.APIKey
	config.APISecret = godaddyCreds.APISecret
	config.PropagationTimeout = 4 * time.Minute // GoDaddy DNS propagation can be slow
	config.PollingInterval = 10 * time.Second
	config.TTL = 600

	// Create provider
	provider, err := godaddy.NewDNSProviderConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create GoDaddy DNS provider: %w", err)
	}

	m.logger.Infof("Created GoDaddy DNS provider with 4-minute propagation timeout")
	return provider, nil
}

// SetupCloudflareDNSProvider creates a Cloudflare DNS provider
func (m *CertificateManager) SetupCloudflareDNSProvider(ctx context.Context, credentialID primitive.ObjectID, project string) (DNSProvider, error) {
	// Get and decrypt credentials
	cred, decrypted, err := m.GetDNSCredential(ctx, credentialID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get DNS credential: %w", err)
	}

	// Validate provider type
	if cred.Provider != "cloudflare" {
		return nil, fmt.Errorf("credential is not for Cloudflare (got: %s)", cred.Provider)
	}

	// Parse credentials
	var cloudflareCreds CloudflareCredentials
	if err := json.Unmarshal([]byte(decrypted), &cloudflareCreds); err != nil {
		m.logger.Errorf("Failed to parse Cloudflare credentials. Raw decrypted: %s, Error: %v", decrypted, err)
		return nil, fmt.Errorf("failed to parse Cloudflare credentials: %w", err)
	}

	// Debug log to verify parsed values
	m.logger.Debugf("Parsed Cloudflare credentials: APIToken length=%d", len(cloudflareCreds.APIToken))

	// Validate required field
	if cloudflareCreds.APIToken == "" {
		m.logger.Errorf("Cloudflare APIToken is empty after parsing. Raw decrypted JSON: %s", decrypted)
		return nil, fmt.Errorf("api_token is required for Cloudflare")
	}

	// Create Lego Cloudflare DNS provider config
	config := cloudflare.NewDefaultConfig()
	config.AuthToken = cloudflareCreds.APIToken
	config.PropagationTimeout = 2 * time.Minute // Cloudflare DNS is usually fast
	config.PollingInterval = 10 * time.Second
	config.TTL = 120 // Cloudflare minimum TTL

	// Create provider
	provider, err := cloudflare.NewDNSProviderConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Cloudflare DNS provider: %w", err)
	}

	m.logger.Infof("Created Cloudflare DNS provider with API token authentication")
	return provider, nil
}

// SetupDigitalOceanDNSProvider creates a DigitalOcean DNS provider
func (m *CertificateManager) SetupDigitalOceanDNSProvider(ctx context.Context, credentialID primitive.ObjectID, project string) (DNSProvider, error) {
	// Get and decrypt credentials
	cred, decrypted, err := m.GetDNSCredential(ctx, credentialID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get DNS credential: %w", err)
	}

	// Validate provider type
	if cred.Provider != "digitalocean" {
		return nil, fmt.Errorf("credential is not for DigitalOcean (got: %s)", cred.Provider)
	}

	// Parse credentials
	var doCreds DigitalOceanCredentials
	if err := json.Unmarshal([]byte(decrypted), &doCreds); err != nil {
		m.logger.Errorf("Failed to parse DigitalOcean credentials. Raw decrypted: %s, Error: %v", decrypted, err)
		return nil, fmt.Errorf("failed to parse DigitalOcean credentials: %w", err)
	}

	// Debug log to verify parsed values
	m.logger.Debugf("Parsed DigitalOcean credentials: APIToken length=%d", len(doCreds.APIToken))

	// Validate required field
	if doCreds.APIToken == "" {
		m.logger.Errorf("DigitalOcean APIToken is empty after parsing. Raw decrypted JSON: %s", decrypted)
		return nil, fmt.Errorf("api_token is required for DigitalOcean")
	}

	// Create Lego DigitalOcean DNS provider config
	config := digitalocean.NewDefaultConfig()
	config.AuthToken = doCreds.APIToken
	config.PropagationTimeout = 2 * time.Minute // DigitalOcean DNS is usually fast
	config.PollingInterval = 10 * time.Second
	config.TTL = 120 // DigitalOcean minimum TTL

	// Create provider
	provider, err := digitalocean.NewDNSProviderConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create DigitalOcean DNS provider: %w", err)
	}

	m.logger.Infof("Created DigitalOcean DNS provider with API token authentication")
	return provider, nil
}

// SetupRoute53DNSProvider creates an AWS Route 53 DNS provider
func (m *CertificateManager) SetupRoute53DNSProvider(ctx context.Context, credentialID primitive.ObjectID, project string) (DNSProvider, error) {
	// Get and decrypt credentials
	cred, decrypted, err := m.GetDNSCredential(ctx, credentialID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get DNS credential: %w", err)
	}

	// Validate provider type
	if cred.Provider != "route53" {
		return nil, fmt.Errorf("credential is not for Route 53 (got: %s)", cred.Provider)
	}

	// Parse credentials
	var r53Creds Route53Credentials
	if err := json.Unmarshal([]byte(decrypted), &r53Creds); err != nil {
		m.logger.Errorf("Failed to parse Route 53 credentials. Raw decrypted: %s, Error: %v", decrypted, err)
		return nil, fmt.Errorf("failed to parse Route 53 credentials: %w", err)
	}

	// Validate required fields
	if r53Creds.AccessKeyID == "" {
		return nil, fmt.Errorf("access_key_id is required for Route 53")
	}
	if r53Creds.SecretAccessKey == "" {
		return nil, fmt.Errorf("secret_access_key is required for Route 53")
	}

	// Set default region if not provided
	if r53Creds.Region == "" {
		r53Creds.Region = "us-east-1"
	}

	// Create Lego Route 53 DNS provider config
	config := route53.NewDefaultConfig()
	config.AccessKeyID = r53Creds.AccessKeyID
	config.SecretAccessKey = r53Creds.SecretAccessKey
	config.Region = r53Creds.Region
	if r53Creds.HostedZoneID != "" {
		config.HostedZoneID = r53Creds.HostedZoneID
	}
	config.PropagationTimeout = 2 * time.Minute // Route 53 DNS is usually fast
	config.PollingInterval = 10 * time.Second
	config.TTL = 60 // Route 53 minimum TTL

	// Create provider
	provider, err := route53.NewDNSProviderConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Route 53 DNS provider: %w", err)
	}

	m.logger.Infof("Created Route 53 DNS provider for region: %s", r53Creds.Region)
	return provider, nil
}

// SetupLightsailDNSProvider creates an AWS Lightsail DNS provider
func (m *CertificateManager) SetupLightsailDNSProvider(ctx context.Context, credentialID primitive.ObjectID, project string) (DNSProvider, error) {
	// Get and decrypt credentials
	cred, decrypted, err := m.GetDNSCredential(ctx, credentialID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get DNS credential: %w", err)
	}

	// Validate provider type
	if cred.Provider != "lightsail" {
		return nil, fmt.Errorf("credential is not for Lightsail (got: %s)", cred.Provider)
	}

	// Parse credentials
	var lsCreds LightsailCredentials
	if err := json.Unmarshal([]byte(decrypted), &lsCreds); err != nil {
		m.logger.Errorf("Failed to parse Lightsail credentials. Raw decrypted: %s, Error: %v", decrypted, err)
		return nil, fmt.Errorf("failed to parse Lightsail credentials: %w", err)
	}

	// Validate required fields
	if lsCreds.AccessKeyID == "" {
		return nil, fmt.Errorf("access_key_id is required for Lightsail")
	}
	if lsCreds.SecretAccessKey == "" {
		return nil, fmt.Errorf("secret_access_key is required for Lightsail")
	}
	if lsCreds.DNSZone == "" {
		return nil, fmt.Errorf("dns_zone is required for Lightsail")
	}

	// Set default region if not provided
	if lsCreds.Region == "" {
		lsCreds.Region = "us-east-1"
	}

	// Lightsail provider uses environment variables for AWS credentials
	// Set them temporarily for this provider instance
	originalAccessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	originalSecretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	originalRegion := os.Getenv("AWS_REGION")

	os.Setenv("AWS_ACCESS_KEY_ID", lsCreds.AccessKeyID)
	os.Setenv("AWS_SECRET_ACCESS_KEY", lsCreds.SecretAccessKey)
	os.Setenv("AWS_REGION", lsCreds.Region)

	// Create Lego Lightsail DNS provider config
	config := lightsail.NewDefaultConfig()
	config.DNSZone = lsCreds.DNSZone // CRITICAL: Set the DNS zone for Lightsail
	config.PropagationTimeout = 2 * time.Minute // Lightsail DNS is usually fast
	config.PollingInterval = 10 * time.Second

	// Create provider
	provider, err := lightsail.NewDNSProviderConfig(config)

	// Restore original environment variables
	if originalAccessKey != "" {
		os.Setenv("AWS_ACCESS_KEY_ID", originalAccessKey)
	} else {
		os.Unsetenv("AWS_ACCESS_KEY_ID")
	}
	if originalSecretKey != "" {
		os.Setenv("AWS_SECRET_ACCESS_KEY", originalSecretKey)
	} else {
		os.Unsetenv("AWS_SECRET_ACCESS_KEY")
	}
	if originalRegion != "" {
		os.Setenv("AWS_REGION", originalRegion)
	} else {
		os.Unsetenv("AWS_REGION")
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create Lightsail DNS provider: %w", err)
	}

	m.logger.Infof("Created Lightsail DNS provider for region: %s", lsCreds.Region)
	return provider, nil
}

// SetupDNSProvider creates appropriate DNS provider based on credential type
func (m *CertificateManager) SetupDNSProvider(ctx context.Context, credentialID primitive.ObjectID, project string) (DNSProvider, error) {
	// Get credential to determine provider type
	cred, _, err := m.GetDNSCredential(ctx, credentialID, project)
	if err != nil {
		return nil, fmt.Errorf("failed to get DNS credential: %w", err)
	}

	// Create provider based on type
	switch cred.Provider {
	case "google":
		return m.SetupGoogleCloudDNSProvider(ctx, credentialID, project)
	case "godaddy":
		return m.SetupGoDaddyDNSProvider(ctx, credentialID, project)
	case "cloudflare":
		return m.SetupCloudflareDNSProvider(ctx, credentialID, project)
	case "digitalocean":
		return m.SetupDigitalOceanDNSProvider(ctx, credentialID, project)
	case "route53":
		return m.SetupRoute53DNSProvider(ctx, credentialID, project)
	case "lightsail":
		return m.SetupLightsailDNSProvider(ctx, credentialID, project)
	default:
		return nil, fmt.Errorf("unsupported DNS provider: %s", cred.Provider)
	}
}

// TestDNSCredentials tests DNS credentials by attempting to create and cleanup a test TXT record
func (m *CertificateManager) TestDNSCredentials(ctx context.Context, provider string, credentialsJSON string, testDomain string) error {
	// Validate provider
	if err := ValidateProvider(provider); err != nil {
		return err
	}

	// Parse credentials based on provider type
	var dnsProvider DNSProvider
	var err error

	switch provider {
	case "google":
		dnsProvider, err = m.createGoogleCloudDNSProviderFromJSON(credentialsJSON)
	case "godaddy":
		dnsProvider, err = m.createGoDaddyDNSProviderFromJSON(credentialsJSON)
	case "cloudflare":
		dnsProvider, err = m.createCloudflareDNSProviderFromJSON(credentialsJSON)
	case "digitalocean":
		dnsProvider, err = m.createDigitalOceanDNSProviderFromJSON(credentialsJSON)
	case "route53":
		dnsProvider, err = m.createRoute53DNSProviderFromJSON(credentialsJSON)
	case "lightsail":
		dnsProvider, err = m.createLightsailDNSProviderFromJSON(credentialsJSON)
	default:
		return fmt.Errorf("unsupported DNS provider: %s", provider)
	}

	if err != nil {
		return fmt.Errorf("failed to create DNS provider: %w", err)
	}

	// Test with a dummy challenge
	testToken := fmt.Sprintf("elchi-test-%d", time.Now().Unix())
	testKeyAuth := "test-key-auth-value"

	m.logger.Infof("Testing %s DNS credentials for domain: %s", provider, testDomain)

	// Try to create the TXT record
	if err := dnsProvider.Present(testDomain, testToken, testKeyAuth); err != nil {
		return fmt.Errorf("failed to create test DNS record: %w", err)
	}

	m.logger.Infof("Successfully created test DNS record for domain: %s", testDomain)

	// Wait a moment to ensure record was created
	time.Sleep(2 * time.Second)

	// Cleanup the test record
	if err := dnsProvider.CleanUp(testDomain, testToken, testKeyAuth); err != nil {
		m.logger.Warnf("Failed to cleanup test DNS record (non-critical): %v", err)
		// Don't return error on cleanup failure - credential test was successful
	} else {
		m.logger.Infof("Successfully cleaned up test DNS record for domain: %s", testDomain)
	}

	return nil
}

// createGoogleCloudDNSProviderFromJSON creates a Google Cloud DNS provider from JSON credentials
func (m *CertificateManager) createGoogleCloudDNSProviderFromJSON(credentialsJSON string) (DNSProvider, error) {
	// Parse credentials
	var gcpCreds GoogleCloudDNSCredentials
	if err := json.Unmarshal([]byte(credentialsJSON), &gcpCreds); err != nil {
		return nil, fmt.Errorf("failed to parse Google Cloud DNS credentials: %w", err)
	}

	// Validate required fields
	if gcpCreds.ProjectID == "" {
		return nil, fmt.Errorf("project_id is required for Google Cloud DNS")
	}
	if gcpCreds.ServiceAccountJSON == "" {
		return nil, fmt.Errorf("service_account_json is required for Google Cloud DNS")
	}

	// Use lego's NewDNSProviderServiceAccountKey which properly initializes the HTTP client
	provider, err := gcloud.NewDNSProviderServiceAccountKey([]byte(gcpCreds.ServiceAccountJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to create Google Cloud DNS provider: %w", err)
	}

	return provider, nil
}

// createGoDaddyDNSProviderFromJSON creates a GoDaddy DNS provider from JSON credentials
func (m *CertificateManager) createGoDaddyDNSProviderFromJSON(credentialsJSON string) (DNSProvider, error) {
	// Parse credentials
	var godaddyCreds GoDaddyCredentials
	if err := json.Unmarshal([]byte(credentialsJSON), &godaddyCreds); err != nil {
		return nil, fmt.Errorf("failed to parse GoDaddy credentials: %w", err)
	}

	// Validate required fields
	if godaddyCreds.APIKey == "" {
		return nil, fmt.Errorf("api_key is required for GoDaddy")
	}
	if godaddyCreds.APISecret == "" {
		return nil, fmt.Errorf("api_secret is required for GoDaddy")
	}

	// Create Lego GoDaddy DNS provider config
	config := godaddy.NewDefaultConfig()
	config.APIKey = godaddyCreds.APIKey
	config.APISecret = godaddyCreds.APISecret
	config.PropagationTimeout = 4 * time.Minute // GoDaddy DNS propagation can be slow
	config.PollingInterval = 10 * time.Second
	config.TTL = 600

	// Create provider
	provider, err := godaddy.NewDNSProviderConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create GoDaddy DNS provider: %w", err)
	}

	return provider, nil
}

// createCloudflareDNSProviderFromJSON creates a Cloudflare DNS provider from JSON credentials
func (m *CertificateManager) createCloudflareDNSProviderFromJSON(credentialsJSON string) (DNSProvider, error) {
	// Parse credentials
	var cloudflareCreds CloudflareCredentials
	if err := json.Unmarshal([]byte(credentialsJSON), &cloudflareCreds); err != nil {
		m.logger.Errorf("Failed to parse Cloudflare credentials during test. Raw JSON: %s, Error: %v", credentialsJSON, err)
		return nil, fmt.Errorf("failed to parse Cloudflare credentials: %w", err)
	}

	// Debug log to verify parsed values
	m.logger.Debugf("Test: Parsed Cloudflare credentials: APIToken length=%d", len(cloudflareCreds.APIToken))

	// Validate required field
	if cloudflareCreds.APIToken == "" {
		m.logger.Errorf("Test: Cloudflare APIToken is empty after parsing. Raw JSON: %s", credentialsJSON)
		return nil, fmt.Errorf("api_token is required for Cloudflare")
	}

	// Create Lego Cloudflare DNS provider config
	config := cloudflare.NewDefaultConfig()
	config.AuthToken = cloudflareCreds.APIToken
	config.PropagationTimeout = 2 * time.Minute
	config.PollingInterval = 10 * time.Second
	config.TTL = 120

	// Create provider
	provider, err := cloudflare.NewDNSProviderConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Cloudflare DNS provider: %w", err)
	}

	return provider, nil
}

// createDigitalOceanDNSProviderFromJSON creates a DigitalOcean DNS provider from JSON credentials
func (m *CertificateManager) createDigitalOceanDNSProviderFromJSON(credentialsJSON string) (DNSProvider, error) {
	// Parse credentials
	var doCreds DigitalOceanCredentials
	if err := json.Unmarshal([]byte(credentialsJSON), &doCreds); err != nil {
		m.logger.Errorf("Failed to parse DigitalOcean credentials during test. Raw JSON: %s, Error: %v", credentialsJSON, err)
		return nil, fmt.Errorf("failed to parse DigitalOcean credentials: %w", err)
	}

	// Debug log to verify parsed values
	m.logger.Debugf("Test: Parsed DigitalOcean credentials: APIToken length=%d", len(doCreds.APIToken))

	// Validate required field
	if doCreds.APIToken == "" {
		m.logger.Errorf("Test: DigitalOcean APIToken is empty after parsing. Raw JSON: %s", credentialsJSON)
		return nil, fmt.Errorf("api_token is required for DigitalOcean")
	}

	// Create Lego DigitalOcean DNS provider config
	config := digitalocean.NewDefaultConfig()
	config.AuthToken = doCreds.APIToken
	config.PropagationTimeout = 2 * time.Minute
	config.PollingInterval = 10 * time.Second
	config.TTL = 120

	// Create provider
	provider, err := digitalocean.NewDNSProviderConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create DigitalOcean DNS provider: %w", err)
	}

	return provider, nil
}

// createRoute53DNSProviderFromJSON creates a Route 53 DNS provider from JSON credentials
func (m *CertificateManager) createRoute53DNSProviderFromJSON(credentialsJSON string) (DNSProvider, error) {
	// Parse credentials
	var r53Creds Route53Credentials
	if err := json.Unmarshal([]byte(credentialsJSON), &r53Creds); err != nil {
		m.logger.Errorf("Failed to parse Route 53 credentials during test. Raw JSON: %s, Error: %v", credentialsJSON, err)
		return nil, fmt.Errorf("failed to parse Route 53 credentials: %w", err)
	}

	// Validate required fields
	if r53Creds.AccessKeyID == "" {
		return nil, fmt.Errorf("access_key_id is required for Route 53")
	}
	if r53Creds.SecretAccessKey == "" {
		return nil, fmt.Errorf("secret_access_key is required for Route 53")
	}

	// Set default region if not provided
	if r53Creds.Region == "" {
		r53Creds.Region = "us-east-1"
	}

	// Create Lego Route 53 DNS provider config
	config := route53.NewDefaultConfig()
	config.AccessKeyID = r53Creds.AccessKeyID
	config.SecretAccessKey = r53Creds.SecretAccessKey
	config.Region = r53Creds.Region
	if r53Creds.HostedZoneID != "" {
		config.HostedZoneID = r53Creds.HostedZoneID
	}
	config.PropagationTimeout = 2 * time.Minute
	config.PollingInterval = 10 * time.Second
	config.TTL = 60

	// Create provider
	provider, err := route53.NewDNSProviderConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Route 53 DNS provider: %w", err)
	}

	return provider, nil
}

// createLightsailDNSProviderFromJSON creates a Lightsail DNS provider from JSON credentials
func (m *CertificateManager) createLightsailDNSProviderFromJSON(credentialsJSON string) (DNSProvider, error) {
	// Parse credentials
	var lsCreds LightsailCredentials
	if err := json.Unmarshal([]byte(credentialsJSON), &lsCreds); err != nil {
		m.logger.Errorf("Failed to parse Lightsail credentials during test. Raw JSON: %s, Error: %v", credentialsJSON, err)
		return nil, fmt.Errorf("failed to parse Lightsail credentials: %w", err)
	}

	// Validate required fields
	if lsCreds.AccessKeyID == "" {
		return nil, fmt.Errorf("access_key_id is required for Lightsail")
	}
	if lsCreds.SecretAccessKey == "" {
		return nil, fmt.Errorf("secret_access_key is required for Lightsail")
	}
	if lsCreds.DNSZone == "" {
		return nil, fmt.Errorf("dns_zone is required for Lightsail")
	}

	// Set default region if not provided
	if lsCreds.Region == "" {
		lsCreds.Region = "us-east-1"
	}

	// Lightsail provider uses environment variables for AWS credentials
	// Set them temporarily for this test
	originalAccessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	originalSecretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	originalRegion := os.Getenv("AWS_REGION")

	os.Setenv("AWS_ACCESS_KEY_ID", lsCreds.AccessKeyID)
	os.Setenv("AWS_SECRET_ACCESS_KEY", lsCreds.SecretAccessKey)
	os.Setenv("AWS_REGION", lsCreds.Region)

	// Create Lego Lightsail DNS provider config
	config := lightsail.NewDefaultConfig()
	config.DNSZone = lsCreds.DNSZone // CRITICAL: Set the DNS zone for Lightsail
	config.PropagationTimeout = 2 * time.Minute
	config.PollingInterval = 10 * time.Second

	// Create provider
	provider, err := lightsail.NewDNSProviderConfig(config)

	// Restore original environment variables
	if originalAccessKey != "" {
		os.Setenv("AWS_ACCESS_KEY_ID", originalAccessKey)
	} else {
		os.Unsetenv("AWS_ACCESS_KEY_ID")
	}
	if originalSecretKey != "" {
		os.Setenv("AWS_SECRET_ACCESS_KEY", originalSecretKey)
	} else {
		os.Unsetenv("AWS_SECRET_ACCESS_KEY")
	}
	if originalRegion != "" {
		os.Setenv("AWS_REGION", originalRegion)
	} else {
		os.Unsetenv("AWS_REGION")
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create Lightsail DNS provider: %w", err)
	}

	return provider, nil
}

