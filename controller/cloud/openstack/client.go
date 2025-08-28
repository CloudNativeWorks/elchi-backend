package openstack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// Network represents OpenStack network details
type Network struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	TenantID            string   `json:"tenant_id"`
	AdminStateUp        bool     `json:"admin_state_up"`
	Status              string   `json:"status"`
	Subnets             []string `json:"subnets"`
	Shared              bool     `json:"shared"`
	AvailabilityZones   []string `json:"availability_zones"`
	RouterExternal      bool     `json:"router:external"`
	DNSDomain           string   `json:"dns_domain"`
	MTU                 int      `json:"mtu"`
	PortSecurityEnabled bool     `json:"port_security_enabled"`
	Tags                []string `json:"tags"`
	Description         string   `json:"description"`
}

// Subnet represents OpenStack subnet details
type Subnet struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	TenantID         string            `json:"tenant_id"`
	NetworkID        string            `json:"network_id"`
	IPVersion        int               `json:"ip_version"`
	CIDR             string            `json:"cidr"`
	GatewayIP        string            `json:"gateway_ip"`
	DNSNameservers   []string          `json:"dns_nameservers"`
	AllocationPools  []AllocationPool  `json:"allocation_pools"`
	HostRoutes       []HostRoute       `json:"host_routes"`
	EnableDHCP       bool              `json:"enable_dhcp"`
	IPv6AddressMode  string            `json:"ipv6_address_mode"`
	IPv6RAMode       string            `json:"ipv6_ra_mode"`
	SubnetPoolID     string            `json:"subnetpool_id"`
	UseDefaultSubnet bool              `json:"use_default_subnetpool"`
	Tags             []string          `json:"tags"`
	Description      string            `json:"description"`
}

// AllocationPool represents IP allocation pool
type AllocationPool struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// HostRoute represents host route
type HostRoute struct {
	Destination string `json:"destination"`
	NextHop     string `json:"nexthop"`
}

// NetworkResponse represents the response from getting network details
type NetworkResponse struct {
	Network Network `json:"network"`
}

// SubnetResponse represents the response from getting subnet details
type SubnetResponse struct {
	Subnet Subnet `json:"subnet"`
}

// SubnetsResponse represents the response from listing subnets
type SubnetsResponse struct {
	Subnets []Subnet `json:"subnets"`
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
	
	// Parse URL to check DNS resolution
	parsedURL, err := url.Parse(authURL)
	if err == nil {
		c.Logger.Debugf("OpenStack Auth - Parsed URL - Host: %s, Scheme: %s, Path: %s", parsedURL.Host, parsedURL.Scheme, parsedURL.Path)
	}
	
	req, err := http.NewRequestWithContext(ctx, "POST", authURL, bytes.NewBuffer(jsonData))
	if err != nil {
		c.Logger.Errorf("OpenStack Auth - Failed to create auth request: %v", err)
		return fmt.Errorf("failed to create auth request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	c.Logger.Debugf("OpenStack Auth - Sending authentication request")
	
	// Log request size and headers for debugging
	c.Logger.Debugf("OpenStack Auth - Request payload size: %d bytes", len(jsonData))
	c.Logger.Debugf("OpenStack Auth - Request Content-Type: %s", req.Header.Get("Content-Type"))

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		c.Logger.Errorf("OpenStack Auth - Authentication request failed: %v", err)
		return fmt.Errorf("authentication request failed: %v", err)
	}
	defer resp.Body.Close()
	
	c.Logger.Debugf("OpenStack Auth - Received auth response with status: %d", resp.StatusCode)

	if resp.StatusCode != http.StatusCreated {
		// Read response body for more details
		bodyBytes, _ := io.ReadAll(resp.Body)
		c.Logger.Errorf("OpenStack Auth - Authentication failed with status: %d, Response: %s", resp.StatusCode, string(bodyBytes))
		
		// Log request details for debugging in K8s environment
		c.Logger.Debugf("OpenStack Auth - Auth URL: %s", authURL)
		
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
		// Read response body for error details
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			c.Logger.Errorf("OpenStack Ports List - Failed to read error response body: %v", err)
			return nil, fmt.Errorf("API request failed with status: %d (unable to read response body)", resp.StatusCode)
		}
		
		// Log detailed error information
		c.Logger.Errorf("OpenStack Ports List - API request failed with status: %d", resp.StatusCode)
		c.Logger.Errorf("OpenStack Ports List - Error response body: %s", string(bodyBytes))
		c.Logger.Debugf("OpenStack Ports List - Request URL: %s", reqURL)
		
		return nil, fmt.Errorf("API request failed with status: %d, response: %s", resp.StatusCode, string(bodyBytes))
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
		// Read response body for error details
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			c.Logger.Errorf("OpenStack Port Get - Failed to read error response body: %v", err)
			return nil, fmt.Errorf("API request failed with status: %d (unable to read response body)", resp.StatusCode)
		}
		
		// Log detailed error information
		c.Logger.Errorf("OpenStack Port Get - API request failed with status: %d", resp.StatusCode)
		c.Logger.Errorf("OpenStack Port Get - Error response body: %s", string(bodyBytes))
		c.Logger.Debugf("OpenStack Port Get - Request URL: %s", reqURL)
		
		return nil, fmt.Errorf("API request failed with status: %d, response: %s", resp.StatusCode, string(bodyBytes))
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
		// Read response body for error details
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			c.Logger.Errorf("OpenStack Port Update - Failed to read error response body: %v", err)
			return fmt.Errorf("API request failed with status: %d (unable to read response body)", resp.StatusCode)
		}
		
		// Log detailed error information
		c.Logger.Errorf("OpenStack Port Update - API request failed with status: %d", resp.StatusCode)
		c.Logger.Errorf("OpenStack Port Update - Error response body: %s", string(bodyBytes))
		c.Logger.Debugf("OpenStack Port Update - Request URL: %s", reqURL)
		c.Logger.Debugf("OpenStack Port Update - Request payload: %s", string(jsonData))
		
		return fmt.Errorf("API request failed with status: %d, response: %s", resp.StatusCode, string(bodyBytes))
	}

	c.Logger.Debugf("Successfully updated port %s", portID)
	return nil
}

// GetNetwork retrieves network details by network ID
func (c *OpenStackClient) GetNetwork(ctx context.Context, networkID string) (*Network, error) {
	c.Logger.Debugf("OpenStack Network - Getting network details for: %s", networkID)
	
	// Authenticate first
	if err := c.authenticate(ctx); err != nil {
		c.Logger.Errorf("OpenStack Network - Authentication failed: %v", err)
		return nil, fmt.Errorf("authentication failed: %v", err)
	}

	// Get network endpoint
	endpoint, err := c.getNetworkEndpoint()
	if err != nil {
		c.Logger.Errorf("OpenStack Network - Failed to get network endpoint: %v", err)
		return nil, fmt.Errorf("failed to get network endpoint: %v", err)
	}

	// Build request URL
	reqURL := fmt.Sprintf("%s/v2.0/networks/%s", endpoint, networkID)
	c.Logger.Debugf("OpenStack Network - Making request to: %s", reqURL)
	
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		c.Logger.Errorf("OpenStack Network - Failed to create request: %v", err)
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	// Add authentication token
	req.Header.Set("X-Auth-Token", c.cachedToken.tokenID)
	req.Header.Set("Content-Type", "application/json")
	
	c.Logger.Debugf("OpenStack Network - Sending network details request")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		c.Logger.Errorf("OpenStack Network - Request failed: %v", err)
		return nil, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	c.Logger.Debugf("OpenStack Network - Received response with status: %d", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		// Read response body for error details
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			c.Logger.Errorf("OpenStack Network Get - Failed to read error response body: %v", err)
			return nil, fmt.Errorf("API request failed with status: %d (unable to read response body)", resp.StatusCode)
		}
		
		// Log detailed error information
		c.Logger.Errorf("OpenStack Network Get - API request failed with status: %d", resp.StatusCode)
		c.Logger.Errorf("OpenStack Network Get - Error response body: %s", string(bodyBytes))
		c.Logger.Debugf("OpenStack Network Get - Request URL: %s", reqURL)
		
		return nil, fmt.Errorf("API request failed with status: %d, response: %s", resp.StatusCode, string(bodyBytes))
	}

	var networkResp NetworkResponse
	if err := json.NewDecoder(resp.Body).Decode(&networkResp); err != nil {
		c.Logger.Errorf("OpenStack Network - Failed to decode response: %v", err)
		return nil, fmt.Errorf("failed to decode response: %v", err)
	}

	c.Logger.Infof("OpenStack Network - Successfully retrieved network %s (%s)", networkResp.Network.Name, networkID)
	return &networkResp.Network, nil
}

// GetSubnet retrieves subnet details by subnet ID
func (c *OpenStackClient) GetSubnet(ctx context.Context, subnetID string) (*Subnet, error) {
	c.Logger.Debugf("OpenStack Subnet - Getting subnet details for: %s", subnetID)
	
	// Authenticate first
	if err := c.authenticate(ctx); err != nil {
		c.Logger.Errorf("OpenStack Subnet - Authentication failed: %v", err)
		return nil, fmt.Errorf("authentication failed: %v", err)
	}

	// Get network endpoint
	endpoint, err := c.getNetworkEndpoint()
	if err != nil {
		c.Logger.Errorf("OpenStack Subnet - Failed to get network endpoint: %v", err)
		return nil, fmt.Errorf("failed to get network endpoint: %v", err)
	}

	// Build request URL
	reqURL := fmt.Sprintf("%s/v2.0/subnets/%s", endpoint, subnetID)
	c.Logger.Debugf("OpenStack Subnet - Making request to: %s", reqURL)
	
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		c.Logger.Errorf("OpenStack Subnet - Failed to create request: %v", err)
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	// Add authentication token
	req.Header.Set("X-Auth-Token", c.cachedToken.tokenID)
	req.Header.Set("Content-Type", "application/json")
	
	c.Logger.Debugf("OpenStack Subnet - Sending subnet details request")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		c.Logger.Errorf("OpenStack Subnet - Request failed: %v", err)
		return nil, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	c.Logger.Debugf("OpenStack Subnet - Received response with status: %d", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		// Read response body for error details
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			c.Logger.Errorf("OpenStack Subnet Get - Failed to read error response body: %v", err)
			return nil, fmt.Errorf("API request failed with status: %d (unable to read response body)", resp.StatusCode)
		}
		
		// Log detailed error information
		c.Logger.Errorf("OpenStack Subnet Get - API request failed with status: %d", resp.StatusCode)
		c.Logger.Errorf("OpenStack Subnet Get - Error response body: %s", string(bodyBytes))
		c.Logger.Debugf("OpenStack Subnet Get - Request URL: %s", reqURL)
		
		return nil, fmt.Errorf("API request failed with status: %d, response: %s", resp.StatusCode, string(bodyBytes))
	}

	var subnetResp SubnetResponse
	if err := json.NewDecoder(resp.Body).Decode(&subnetResp); err != nil {
		c.Logger.Errorf("OpenStack Subnet - Failed to decode response: %v", err)
		return nil, fmt.Errorf("failed to decode response: %v", err)
	}

	c.Logger.Infof("OpenStack Subnet - Successfully retrieved subnet %s (%s)", subnetResp.Subnet.Name, subnetID)
	return &subnetResp.Subnet, nil
}

// ListNetworkSubnets lists all subnets for a specific network
func (c *OpenStackClient) ListNetworkSubnets(ctx context.Context, networkID string) ([]Subnet, error) {
	c.Logger.Debugf("OpenStack Subnets - Listing subnets for network: %s", networkID)
	
	// Authenticate first
	if err := c.authenticate(ctx); err != nil {
		c.Logger.Errorf("OpenStack Subnets - Authentication failed: %v", err)
		return nil, fmt.Errorf("authentication failed: %v", err)
	}

	// Get network endpoint
	endpoint, err := c.getNetworkEndpoint()
	if err != nil {
		c.Logger.Errorf("OpenStack Subnets - Failed to get network endpoint: %v", err)
		return nil, fmt.Errorf("failed to get network endpoint: %v", err)
	}

	// Build request URL with network filter
	reqURL := fmt.Sprintf("%s/v2.0/subnets?network_id=%s", endpoint, url.QueryEscape(networkID))
	c.Logger.Debugf("OpenStack Subnets - Making request to: %s", reqURL)
	
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		c.Logger.Errorf("OpenStack Subnets - Failed to create request: %v", err)
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	// Add authentication token
	req.Header.Set("X-Auth-Token", c.cachedToken.tokenID)
	req.Header.Set("Content-Type", "application/json")
	
	c.Logger.Debugf("OpenStack Subnets - Sending subnets list request")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		c.Logger.Errorf("OpenStack Subnets - Request failed: %v", err)
		return nil, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	c.Logger.Debugf("OpenStack Subnets - Received response with status: %d", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		// Read response body for error details
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			c.Logger.Errorf("OpenStack Subnets List - Failed to read error response body: %v", err)
			return nil, fmt.Errorf("API request failed with status: %d (unable to read response body)", resp.StatusCode)
		}
		
		// Log detailed error information
		c.Logger.Errorf("OpenStack Subnets List - API request failed with status: %d", resp.StatusCode)
		c.Logger.Errorf("OpenStack Subnets List - Error response body: %s", string(bodyBytes))
		c.Logger.Debugf("OpenStack Subnets List - Request URL: %s", reqURL)
		
		return nil, fmt.Errorf("API request failed with status: %d, response: %s", resp.StatusCode, string(bodyBytes))
	}

	var subnetsResp SubnetsResponse
	if err := json.NewDecoder(resp.Body).Decode(&subnetsResp); err != nil {
		c.Logger.Errorf("OpenStack Subnets - Failed to decode response: %v", err)
		return nil, fmt.Errorf("failed to decode response: %v", err)
	}

	c.Logger.Infof("OpenStack Subnets - Found %d subnets for network %s", len(subnetsResp.Subnets), networkID)
	return subnetsResp.Subnets, nil
}