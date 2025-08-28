package openstack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// OpenStackClient handles OpenStack API interactions
type OpenStackClient struct {
	AuthURL      string
	Username     string
	Password     string
	TenantName   string
	DomainName   string
	RegionName   string
	HTTPClient   *http.Client
	Logger       *logger.Logger
	cachedToken  *AuthResponse
	tokenExpiry  time.Time
}

// AuthResponse represents OpenStack Identity v3 authentication response
type AuthResponse struct {
	Token struct {
		ExpiresAt string `json:"expires_at"`
		Catalog   []struct {
			Name      string `json:"name"`
			Type      string `json:"type"`
			Endpoints []struct {
				URL       string `json:"url"`
				Interface string `json:"interface"`
				Region    string `json:"region"`
			} `json:"endpoints"`
		} `json:"catalog"`
	} `json:"token"`
	tokenID string // Store token ID from X-Subject-Token header
}

// ServerPort represents OpenStack server port (interface)
type ServerPort struct {
	ID                string   `json:"id"`
	NetworkID         string   `json:"network_id"`
	Name              string   `json:"name"`
	AdminStateUp      bool     `json:"admin_state_up"`
	Status            string   `json:"status"`
	MACAddress        string   `json:"mac_address"`
	FixedIPs          []FixedIP `json:"fixed_ips"`
	AllowedAddressPairs []AllowedAddressPair `json:"allowed_address_pairs"`
	DeviceID          string   `json:"device_id"`
	DeviceOwner       string   `json:"device_owner"`
	TenantID          string   `json:"tenant_id"`
}

// FixedIP represents a fixed IP address
type FixedIP struct {
	SubnetID  string `json:"subnet_id"`
	IPAddress string `json:"ip_address"`
}

// AllowedAddressPair represents an allowed address pair
type AllowedAddressPair struct {
	IPAddress  string `json:"ip_address"`
	MACAddress string `json:"mac_address"`
}

// PortsResponse represents the response from listing ports
type PortsResponse struct {
	Ports []ServerPort `json:"ports"`
}

// NewOpenStackClient creates a new OpenStack client
func NewOpenStackClient(config *models.CloudConfig, logger *logger.Logger) *OpenStackClient {
	return &OpenStackClient{
		AuthURL:    config.Auth.AuthURL,
		Username:   config.Auth.ApplicationCredentialID,
		Password:   config.Auth.ApplicationCredentialSecret,
		TenantName: "", // Not needed for application credentials
		DomainName: "", // Not needed for application credentials
		RegionName: config.RegionName,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		Logger: logger,
	}
}

// authenticate authenticates with OpenStack and caches the token
func (c *OpenStackClient) authenticate(ctx context.Context) error {
	c.Logger.Debugf("OpenStack Auth - Starting authentication process")
	
	// Check if we have a valid cached token
	if c.cachedToken != nil && time.Now().Before(c.tokenExpiry) {
		c.Logger.Debugf("OpenStack Auth - Using cached token, expires at: %v", c.tokenExpiry)
		return nil
	}
	
	if c.cachedToken != nil {
		c.Logger.Debugf("OpenStack Auth - Cached token expired, getting new token")
	} else {
		c.Logger.Debugf("OpenStack Auth - No cached token, authenticating for the first time")
	}

	// Prepare authentication request using application credentials
	authPayload := map[string]interface{}{
		"auth": map[string]interface{}{
			"identity": map[string]interface{}{
				"methods": []string{"application_credential"},
				"application_credential": map[string]interface{}{
					"id":     c.Username,   // Application Credential ID
					"secret": c.Password,   // Application Credential Secret
				},
			},
		},
	}

	jsonData, err := json.Marshal(authPayload)
	if err != nil {
		return fmt.Errorf("failed to marshal auth payload: %v", err)
	}

	authURL := c.AuthURL + "/v3/auth/tokens"
	c.Logger.Debugf("OpenStack Auth - Making auth request to: %s", authURL)
	
	req, err := http.NewRequestWithContext(ctx, "POST", authURL, bytes.NewBuffer(jsonData))
	if err != nil {
		c.Logger.Errorf("OpenStack Auth - Failed to create auth request: %v", err)
		return fmt.Errorf("failed to create auth request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	c.Logger.Debugf("OpenStack Auth - Sending authentication request")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		c.Logger.Errorf("OpenStack Auth - Authentication request failed: %v", err)
		return fmt.Errorf("authentication request failed: %v", err)
	}
	defer resp.Body.Close()
	
	c.Logger.Debugf("OpenStack Auth - Received auth response with status: %d", resp.StatusCode)

	if resp.StatusCode != http.StatusCreated {
		c.Logger.Errorf("OpenStack Auth - Authentication failed with status: %d", resp.StatusCode)
		return fmt.Errorf("authentication failed with status: %d", resp.StatusCode)
	}

	// Get token from X-Subject-Token header
	tokenID := resp.Header.Get("X-Subject-Token")
	if tokenID == "" {
		c.Logger.Errorf("OpenStack Auth - No token returned in X-Subject-Token header")
		return fmt.Errorf("no token returned in response header")
	}
	
	c.Logger.Debugf("OpenStack Auth - Token ID received: %s...", tokenID[:10])

	var authResp AuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		c.Logger.Errorf("OpenStack Auth - Failed to decode auth response: %v", err)
		return fmt.Errorf("failed to decode auth response: %v", err)
	}

	c.Logger.Debugf("OpenStack Auth - Auth response decoded, expires_at: %s", authResp.Token.ExpiresAt)
	c.Logger.Debugf("OpenStack Auth - Found %d services in catalog", len(authResp.Token.Catalog))

	// Parse expires_at timestamp
	expiresAt, err := time.Parse("2006-01-02T15:04:05.000000Z", authResp.Token.ExpiresAt)
	if err != nil {
		// Try alternative format
		c.Logger.Debugf("OpenStack Auth - Trying alternative timestamp format")
		expiresAt, err = time.Parse("2006-01-02T15:04:05Z", authResp.Token.ExpiresAt)
		if err != nil {
			c.Logger.Errorf("OpenStack Auth - Failed to parse expires_at timestamp: %v", err)
			return fmt.Errorf("failed to parse expires_at timestamp: %v", err)
		}
	}

	c.cachedToken = &authResp
	c.cachedToken.tokenID = tokenID // Store token ID separately
	// Set token expiry to 5 minutes before actual expiry for safety
	c.tokenExpiry = expiresAt.Add(-5 * time.Minute)

	c.Logger.Infof("OpenStack Auth - Authentication successful, token expires at: %v", expiresAt)
	return nil
}

// getNetworkEndpoint returns the network service endpoint
func (c *OpenStackClient) getNetworkEndpoint() (string, error) {
	c.Logger.Debugf("OpenStack Endpoint - Getting network service endpoint")
	
	if c.cachedToken == nil {
		c.Logger.Errorf("OpenStack Endpoint - No authentication token available")
		return "", fmt.Errorf("no authentication token available")
	}

	c.Logger.Debugf("OpenStack Endpoint - Searching through %d services in catalog", len(c.cachedToken.Token.Catalog))

	for _, service := range c.cachedToken.Token.Catalog {
		c.Logger.Debugf("OpenStack Endpoint - Checking service: %s (type: %s)", service.Name, service.Type)
		if service.Type == "network" {
			c.Logger.Debugf("OpenStack Endpoint - Found network service with %d endpoints", len(service.Endpoints))
			for _, endpoint := range service.Endpoints {
				c.Logger.Debugf("OpenStack Endpoint - Checking endpoint: interface=%s, region=%s, url=%s", 
					endpoint.Interface, endpoint.Region, endpoint.URL)
				// Check interface type (public, internal, admin) and region
				if endpoint.Interface == "public" && (endpoint.Region == c.RegionName || c.RegionName == "") {
					c.Logger.Infof("OpenStack Endpoint - Found matching network endpoint: %s", endpoint.URL)
					return endpoint.URL, nil
				}
			}
			// If no region match, use the first public endpoint
			for _, endpoint := range service.Endpoints {
				if endpoint.Interface == "public" {
					c.Logger.Infof("OpenStack Endpoint - Using first public network endpoint: %s", endpoint.URL)
					return endpoint.URL, nil
				}
			}
		}
	}

	c.Logger.Errorf("OpenStack Endpoint - Network service endpoint not found")
	return "", fmt.Errorf("network service endpoint not found")
}

// ListServerPorts lists all ports for a specific server (device_id)
func (c *OpenStackClient) ListServerPorts(ctx context.Context, serverID string) ([]ServerPort, error) {
	c.Logger.Debugf("OpenStack Ports - Listing ports for server: %s", serverID)
	
	// Authenticate first
	if err := c.authenticate(ctx); err != nil {
		c.Logger.Errorf("OpenStack Ports - Authentication failed: %v", err)
		return nil, fmt.Errorf("authentication failed: %v", err)
	}

	// Get network endpoint
	endpoint, err := c.getNetworkEndpoint()
	if err != nil {
		c.Logger.Errorf("OpenStack Ports - Failed to get network endpoint: %v", err)
		return nil, fmt.Errorf("failed to get network endpoint: %v", err)
	}

	// Build request URL with server filter
	reqURL := fmt.Sprintf("%s/v2.0/ports?device_id=%s", endpoint, url.QueryEscape(serverID))
	c.Logger.Debugf("OpenStack Ports - Making request to: %s", reqURL)
	
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		c.Logger.Errorf("OpenStack Ports - Failed to create request: %v", err)
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	// Add authentication token
	req.Header.Set("X-Auth-Token", c.cachedToken.tokenID)
	req.Header.Set("Content-Type", "application/json")
	
	c.Logger.Debugf("OpenStack Ports - Sending ports list request")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		c.Logger.Errorf("OpenStack Ports - Request failed: %v", err)
		return nil, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	c.Logger.Debugf("OpenStack Ports - Received response with status: %d", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		c.Logger.Errorf("OpenStack Ports - API request failed with status: %d", resp.StatusCode)
		return nil, fmt.Errorf("API request failed with status: %d", resp.StatusCode)
	}

	var portsResp PortsResponse
	if err := json.NewDecoder(resp.Body).Decode(&portsResp); err != nil {
		c.Logger.Errorf("OpenStack Ports - Failed to decode response: %v", err)
		return nil, fmt.Errorf("failed to decode response: %v", err)
	}

	c.Logger.Infof("OpenStack Ports - Found %d ports for server %s", len(portsResp.Ports), serverID)
	return portsResp.Ports, nil
}

// AddAllowedAddressPair adds an allowed address pair to a port
func (c *OpenStackClient) AddAllowedAddressPair(ctx context.Context, portID, ipAddress string) error {
	// Authenticate first
	if err := c.authenticate(ctx); err != nil {
		return fmt.Errorf("authentication failed: %v", err)
	}

	// Get network endpoint
	endpoint, err := c.getNetworkEndpoint()
	if err != nil {
		return fmt.Errorf("failed to get network endpoint: %v", err)
	}

	// First, get current port details to preserve existing allowed address pairs
	port, err := c.getPort(ctx, endpoint, portID)
	if err != nil {
		return fmt.Errorf("failed to get port details: %v", err)
	}

	// Check if the IP address is already in allowed address pairs
	for _, pair := range port.AllowedAddressPairs {
		if pair.IPAddress == ipAddress {
			c.Logger.Debugf("IP address %s already exists in allowed address pairs for port %s", ipAddress, portID)
			return nil
		}
	}

	// Add the new allowed address pair
	newPair := AllowedAddressPair{
		IPAddress: ipAddress,
	}
	port.AllowedAddressPairs = append(port.AllowedAddressPairs, newPair)

	// Update the port
	updatePayload := map[string]interface{}{
		"port": map[string]interface{}{
			"allowed_address_pairs": port.AllowedAddressPairs,
		},
	}

	return c.updatePort(ctx, endpoint, portID, updatePayload)
}

// RemoveAllowedAddressPair removes an allowed address pair from a port
func (c *OpenStackClient) RemoveAllowedAddressPair(ctx context.Context, portID, ipAddress string) error {
	// Authenticate first
	if err := c.authenticate(ctx); err != nil {
		return fmt.Errorf("authentication failed: %v", err)
	}

	// Get network endpoint
	endpoint, err := c.getNetworkEndpoint()
	if err != nil {
		return fmt.Errorf("failed to get network endpoint: %v", err)
	}

	// Get current port details
	port, err := c.getPort(ctx, endpoint, portID)
	if err != nil {
		return fmt.Errorf("failed to get port details: %v", err)
	}

	// Remove the allowed address pair
	var updatedPairs []AllowedAddressPair
	found := false
	for _, pair := range port.AllowedAddressPairs {
		if pair.IPAddress != ipAddress {
			updatedPairs = append(updatedPairs, pair)
		} else {
			found = true
		}
	}

	if !found {
		c.Logger.Debugf("IP address %s not found in allowed address pairs for port %s", ipAddress, portID)
		return nil
	}

	// Update the port
	updatePayload := map[string]interface{}{
		"port": map[string]interface{}{
			"allowed_address_pairs": updatedPairs,
		},
	}

	return c.updatePort(ctx, endpoint, portID, updatePayload)
}

// getPort retrieves port details
func (c *OpenStackClient) getPort(ctx context.Context, endpoint, portID string) (*ServerPort, error) {
	reqURL := fmt.Sprintf("%s/v2.0/ports/%s", endpoint, portID)
	
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("X-Auth-Token", c.cachedToken.tokenID)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API request failed with status: %d", resp.StatusCode)
	}

	var portResp struct {
		Port ServerPort `json:"port"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&portResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %v", err)
	}

	return &portResp.Port, nil
}

// updatePort updates port configuration
func (c *OpenStackClient) updatePort(ctx context.Context, endpoint, portID string, payload map[string]interface{}) error {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal update payload: %v", err)
	}

	reqURL := fmt.Sprintf("%s/v2.0/ports/%s", endpoint, portID)
	
	req, err := http.NewRequestWithContext(ctx, "PUT", reqURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("X-Auth-Token", c.cachedToken.tokenID)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API request failed with status: %d", resp.StatusCode)
	}

	c.Logger.Debugf("Successfully updated port %s", portID)
	return nil
}