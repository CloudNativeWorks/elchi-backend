package openstack

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"github.com/CloudNativeWorks/elchi-backend/controller/client/services"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// Handler handles OpenStack-related API requests
type Handler struct {
	Context       *db.AppContext
	Logger        *logger.Logger
	ClientService *services.ClientService
}

// NewHandler creates a new OpenStack handler
func NewHandler(context *db.AppContext, logger *logger.Logger, clientService *services.ClientService) *Handler {
	return &Handler{
		Context:       context,
		Logger:        logger,
		ClientService: clientService,
	}
}

// InterfaceInfo represents interface information for API response
type InterfaceInfo struct {
	ID                  string                 `json:"id"`
	Name                string                 `json:"name"`
	NetworkID           string                 `json:"network_id"`
	Status              string                 `json:"status"`
	AdminStateUp        bool                   `json:"admin_state_up"`
	MACAddress          string                 `json:"mac_address"`
	FixedIPs            []FixedIP              `json:"fixed_ips"`
	AllowedAddressPairs []AllowedAddressPair   `json:"allowed_address_pairs"`
	DeviceID            string                 `json:"device_id"`
	DeviceOwner         string                 `json:"device_owner"`
}

// InterfaceListResponse represents the response structure for interface listing
type InterfaceListResponse struct {
	Message    string          `json:"message"`
	Data       []InterfaceInfo `json:"data"`
	ClientInfo ClientInfo      `json:"client_info"`
}

// NetworkInfo represents network information for API response
type NetworkInfo struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	TenantID            string   `json:"tenant_id"`
	AdminStateUp        bool     `json:"admin_state_up"`
	Status              string   `json:"status"`
	Subnets             []string `json:"subnets"`
	Shared              bool     `json:"shared"`
	AvailabilityZones   []string `json:"availability_zones"`
	RouterExternal      bool     `json:"router_external"`
	DNSDomain           string   `json:"dns_domain"`
	MTU                 int      `json:"mtu"`
	PortSecurityEnabled bool     `json:"port_security_enabled"`
	Tags                []string `json:"tags"`
	Description         string   `json:"description"`
}

// SubnetInfo represents subnet information for API response
type SubnetInfo struct {
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

// NetworkDetailsResponse represents the response structure for network details
type NetworkDetailsResponse struct {
	Message string      `json:"message"`
	Data    NetworkInfo `json:"data"`
}

// SubnetDetailsResponse represents the response structure for subnet details
type SubnetDetailsResponse struct {
	Message string     `json:"message"`
	Data    SubnetInfo `json:"data"`
}

// NetworkSubnetsResponse represents the response structure for network subnets
type NetworkSubnetsResponse struct {
	Message   string       `json:"message"`
	NetworkID string       `json:"network_id"`
	Data      []SubnetInfo `json:"data"`
}

// ClientInfo represents client information in the response
type ClientInfo struct {
	ClientID   string `json:"client_id"`
	ClientName string `json:"client_name"`
	Provider   string `json:"provider"`
	ServerID   string `json:"server_id,omitempty"`
}

// GetClientInterfaces lists all interfaces for an OpenStack client
// GET /api/op/clients/:client_id/openstack/interfaces
func (h *Handler) GetClientInterfaces(c *gin.Context) {
	clientID := c.Param("client_id")
	osUUID := c.Query("os_uuid")
	
	h.Logger.Debugf("OpenStack Interface API Request - ClientID: %s, OS_UUID: %s", clientID, osUUID)
	
	if clientID == "" {
		h.Logger.Errorf("OpenStack Interface API - Missing client_id parameter")
		c.JSON(http.StatusBadRequest, gin.H{"message": "client_id is required"})
		return
	}

	// Get client information
	h.Logger.Debugf("OpenStack Interface API - Fetching client info for: %s", clientID)
	clientInfo, err := h.ClientService.GetClientByClientID(context.Background(), clientID)
	if err != nil {
		h.Logger.Errorf("OpenStack Interface API - Failed to get client %s: %v", clientID, err)
		c.JSON(http.StatusNotFound, gin.H{"message": "Client not found"})
		return
	}
	
	h.Logger.Debugf("OpenStack Interface API - Client info retrieved: Name=%s, Provider=%s", clientInfo.Name, clientInfo.Provider)

	// Validate that client is OpenStack provider
	if clientInfo.Provider != "openstack" {
		h.Logger.Warnf("OpenStack Interface API - Client %s is not OpenStack provider (provider: %s)", clientID, clientInfo.Provider)
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "Client is not an OpenStack provider",
			"client_info": ClientInfo{
				ClientID:   clientInfo.ClientID,
				ClientName: clientInfo.Name,
				Provider:   clientInfo.Provider,
			},
		})
		return
	}
	
	h.Logger.Debugf("OpenStack Interface API - Client %s validated as OpenStack provider", clientID)

	// Get OpenStack project UUID from query parameter (required for OpenStack API)
	ospProject := c.Query("osp_project")
	if ospProject == "" {
		h.Logger.Errorf("OpenStack Interface API - Missing osp_project query parameter")
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "osp_project query parameter is required (OpenStack project UUID)",
			"example": "/api/op/clients/{client_id}/openstack/interfaces?os_uuid=server-uuid&osp_project=openstack-project-uuid&project=db-project-name",
			"client_info": ClientInfo{
				ClientID:   clientInfo.ClientID,
				ClientName: clientInfo.Name,
				Provider:   clientInfo.Provider,
			},
		})
		return
	}

	// Get our DB project from query parameter (required for cloud config lookup)
	dbProject := c.Query("project")
	if dbProject == "" {
		h.Logger.Errorf("OpenStack Interface API - Missing project query parameter")
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "project query parameter is required (DB project name for cloud config)",
			"example": "/api/op/clients/{client_id}/openstack/interfaces?os_uuid=server-uuid&osp_project=openstack-project-uuid&project=db-project-name",
			"client_info": ClientInfo{
				ClientID:   clientInfo.ClientID,
				ClientName: clientInfo.Name,
				Provider:   clientInfo.Provider,
			},
		})
		return
	}

	h.Logger.Debugf("OpenStack Interface API - OpenStack project UUID: %s, DB project: %s", ospProject, dbProject)

	// Get cloud configuration for the DB project
	h.Logger.Debugf("OpenStack Interface API - Fetching cloud config for DB project: %s", dbProject)
	cloudConfig, err := h.getCloudConfig(dbProject)
	if err != nil {
		h.Logger.Errorf("OpenStack Interface API - Failed to get cloud config for DB project %s: %v", dbProject, err)
		c.JSON(http.StatusBadRequest, gin.H{"message": fmt.Sprintf("Cloud configuration not found for DB project %s: %v", dbProject, err)})
		return
	}
	
	h.Logger.Debugf("OpenStack Interface API - Cloud config retrieved: Provider=%s, AuthURL=%s", cloudConfig.Provider, cloudConfig.Auth.AuthURL)

	// Create OpenStack client
	h.Logger.Debugf("OpenStack Interface API - Creating OpenStack client for DB project: %s", dbProject)
	osClient := NewOpenStackClient(cloudConfig, h.Logger)

	// Get server UUID directly from query parameter (frontend should pass os_uuid)
	serverID := c.Query("os_uuid")
	if serverID == "" {
		h.Logger.Errorf("OpenStack Interface API - Missing os_uuid query parameter for client: %s", clientID)
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "os_uuid query parameter is required",
			"client_info": ClientInfo{
				ClientID:   clientInfo.ClientID,
				ClientName: clientInfo.Name,
				Provider:   clientInfo.Provider,
			},
		})
		return
	}
	
	h.Logger.Debugf("OpenStack Interface API - Server UUID: %s", serverID)

	// List server interfaces
	h.Logger.Debugf("OpenStack Interface API - Listing interfaces for server: %s", serverID)
	ports, err := osClient.ListServerPorts(context.Background(), serverID)
	if err != nil {
		h.Logger.Errorf("OpenStack Interface API - Failed to list interfaces for server %s: %v", serverID, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": fmt.Sprintf("Failed to retrieve interfaces: %v", err),
			"client_info": ClientInfo{
				ClientID:   clientInfo.ClientID,
				ClientName: clientInfo.Name,
				Provider:   clientInfo.Provider,
				ServerID:   serverID,
			},
		})
		return
	}
	
	h.Logger.Debugf("OpenStack Interface API - Found %d interfaces for server %s", len(ports), serverID)

	// Convert to response format
	h.Logger.Debugf("OpenStack Interface API - Converting %d ports to response format", len(ports))
	interfaces := make([]InterfaceInfo, len(ports))
	for i, port := range ports {
		h.Logger.Debugf("OpenStack Interface API - Port[%d]: ID=%s, Name=%s, NetworkID=%s, Status=%s, MAC=%s, FixedIPs=%d, AllowedPairs=%d", 
			i, port.ID, port.Name, port.NetworkID, port.Status, port.MACAddress, len(port.FixedIPs), len(port.AllowedAddressPairs))
		
		interfaces[i] = InterfaceInfo{
			ID:                  port.ID,
			Name:                port.Name,
			NetworkID:           port.NetworkID,
			Status:              port.Status,
			AdminStateUp:        port.AdminStateUp,
			MACAddress:          port.MACAddress,
			FixedIPs:            port.FixedIPs,
			AllowedAddressPairs: port.AllowedAddressPairs,
			DeviceID:            port.DeviceID,
			DeviceOwner:         port.DeviceOwner,
		}
	}

	response := InterfaceListResponse{
		Message: "Success",
		Data:    interfaces,
		ClientInfo: ClientInfo{
			ClientID:   clientInfo.ClientID,
			ClientName: clientInfo.Name,
			Provider:   clientInfo.Provider,
			ServerID:   serverID,
		},
	}

	h.Logger.Infof("OpenStack Interface API - Successfully retrieved %d interfaces for client %s (server: %s)", len(interfaces), clientID, serverID)
	c.JSON(http.StatusOK, response)
}

// AddAllowedAddressPair adds an allowed address pair to an OpenStack interface
func (h *Handler) AddAllowedAddressPair(ctx context.Context, interfaceID, ipAddress, projectName string) error {
	h.Logger.Debugf("OpenStack AddAllowedAddressPair - InterfaceID: %s, IP: %s, Project: %s", interfaceID, ipAddress, projectName)
	
	// Get cloud configuration for the project
	cloudConfig, err := h.getCloudConfig(projectName)
	if err != nil {
		h.Logger.Errorf("OpenStack AddAllowedAddressPair - Failed to get cloud config for project %s: %v", projectName, err)
		return fmt.Errorf("failed to get cloud config for project %s: %v", projectName, err)
	}
	
	h.Logger.Debugf("OpenStack AddAllowedAddressPair - Cloud config retrieved: Provider=%s, AuthURL=%s", cloudConfig.Provider, cloudConfig.Auth.AuthURL)

	// Create OpenStack client
	osClient := NewOpenStackClient(cloudConfig, h.Logger)
	
	// Pre-check: Verify interface exists before attempting to modify
	if err := osClient.authenticate(ctx); err != nil {
		h.Logger.Errorf("OpenStack AddAllowedAddressPair - Authentication failed: %v", err)
		return fmt.Errorf("OpenStack authentication failed: %v", err)
	}
	
	endpoint, err := osClient.getNetworkEndpoint()
	if err != nil {
		h.Logger.Errorf("OpenStack AddAllowedAddressPair - Failed to get network endpoint: %v", err)
		return fmt.Errorf("failed to get network endpoint: %v", err)
	}
	
	// Check if interface exists  
	_, err = osClient.getPort(ctx, endpoint, interfaceID)
	if err != nil {
		h.Logger.Errorf("OpenStack AddAllowedAddressPair - Interface %s not found: %v", interfaceID, err)
		return fmt.Errorf("interface %s not found or inaccessible: %v", interfaceID, err)
	}
	
	h.Logger.Debugf("OpenStack AddAllowedAddressPair - No CIDR validation required for AAP (allows cross-subnet IPs)")
	
	// Add allowed address pair
	if err := osClient.AddAllowedAddressPair(ctx, interfaceID, ipAddress); err != nil {
		h.Logger.Errorf("OpenStack AddAllowedAddressPair - Failed to add allowed address pair %s to interface %s: %v", ipAddress, interfaceID, err)
		return fmt.Errorf("failed to add allowed address pair %s to interface %s: %v", ipAddress, interfaceID, err)
	}
	
	h.Logger.Infof("OpenStack AddAllowedAddressPair - Successfully added allowed address pair %s to interface %s", ipAddress, interfaceID)
	return nil
}

// RemoveAllowedAddressPair removes an allowed address pair from an OpenStack interface
func (h *Handler) RemoveAllowedAddressPair(ctx context.Context, interfaceID, ipAddress, projectName string) error {
	h.Logger.Debugf("OpenStack RemoveAllowedAddressPair - InterfaceID: %s, IP: %s, Project: %s", interfaceID, ipAddress, projectName)
	
	// Get cloud configuration for the project
	cloudConfig, err := h.getCloudConfig(projectName)
	if err != nil {
		h.Logger.Errorf("OpenStack RemoveAllowedAddressPair - Failed to get cloud config for project %s: %v", projectName, err)
		return fmt.Errorf("failed to get cloud config for project %s: %v", projectName, err)
	}
	
	h.Logger.Debugf("OpenStack RemoveAllowedAddressPair - Cloud config retrieved: Provider=%s, AuthURL=%s", cloudConfig.Provider, cloudConfig.Auth.AuthURL)

	// Create OpenStack client
	osClient := NewOpenStackClient(cloudConfig, h.Logger)
	
	// Remove allowed address pair
	if err := osClient.RemoveAllowedAddressPair(ctx, interfaceID, ipAddress); err != nil {
		h.Logger.Errorf("OpenStack RemoveAllowedAddressPair - Failed to remove allowed address pair %s from interface %s: %v", ipAddress, interfaceID, err)
		return fmt.Errorf("failed to remove allowed address pair %s from interface %s: %v", ipAddress, interfaceID, err)
	}
	
	h.Logger.Infof("OpenStack RemoveAllowedAddressPair - Successfully removed allowed address pair %s from interface %s", ipAddress, interfaceID)
	return nil
}

// AddFixedIPWithAutoSubnet adds a fixed IP to an interface using the first existing fixed IP's subnet
func (h *Handler) AddFixedIPWithAutoSubnet(ctx context.Context, interfaceID, ipAddress, projectName string) error {
	h.Logger.Debugf("OpenStack AddFixedIPWithAutoSubnet - InterfaceID: %s, IP: %s, Project: %s", interfaceID, ipAddress, projectName)
	
	// Get cloud configuration for the project
	cloudConfig, err := h.getCloudConfig(projectName)
	if err != nil {
		h.Logger.Errorf("OpenStack AddFixedIPWithAutoSubnet - Failed to get cloud config for project %s: %v", projectName, err)
		return fmt.Errorf("failed to get cloud config for project %s: %v", projectName, err)
	}
	
	// Create OpenStack client
	osClient := NewOpenStackClient(cloudConfig, h.Logger)
	
	// Authenticate first to get endpoint
	if err := osClient.authenticate(ctx); err != nil {
		h.Logger.Errorf("OpenStack AddFixedIPWithAutoSubnet - Authentication failed: %v", err)
		return fmt.Errorf("authentication failed: %v", err)
	}
	
	// Get network endpoint
	endpoint, err := osClient.getNetworkEndpoint()
	if err != nil {
		h.Logger.Errorf("OpenStack AddFixedIPWithAutoSubnet - Failed to get network endpoint: %v", err)
		return fmt.Errorf("failed to get network endpoint: %v", err)
	}
	
	// Get port details to find existing subnet ID
	port, err := osClient.getPort(ctx, endpoint, interfaceID)
	if err != nil {
		h.Logger.Errorf("OpenStack AddFixedIPWithAutoSubnet - Failed to get port details for %s: %v", interfaceID, err)
		return fmt.Errorf("failed to get port details: %v", err)
	}
	
	// Use the first existing fixed IP's subnet ID
	if len(port.FixedIPs) == 0 {
		h.Logger.Errorf("OpenStack AddFixedIPWithAutoSubnet - Port %s has no existing fixed IPs to determine subnet", interfaceID)
		return fmt.Errorf("port %s has no existing fixed IPs to determine subnet", interfaceID)
	}
	
	subnetID := port.FixedIPs[0].SubnetID
	h.Logger.Debugf("OpenStack AddFixedIPWithAutoSubnet - Using subnet ID: %s from existing fixed IP", subnetID)
	
	// Validate IP is compatible with subnet (get subnet info and check CIDR)
	subnet, err := osClient.GetSubnet(ctx, subnetID)
	if err != nil {
		h.Logger.Errorf("OpenStack AddFixedIPWithAutoSubnet - Failed to get subnet %s details: %v", subnetID, err)
		return fmt.Errorf("failed to validate subnet compatibility: %v", err)
	}
	
	// Check if IP is within subnet CIDR
	if subnet.CIDR != "" {
		_, ipNet, err := net.ParseCIDR(subnet.CIDR)
		if err != nil {
			h.Logger.Warnf("OpenStack AddFixedIPWithAutoSubnet - Invalid subnet CIDR %s, skipping validation", subnet.CIDR)
		} else {
			ip := net.ParseIP(ipAddress)
			if ip != nil && !ipNet.Contains(ip) {
				h.Logger.Errorf("OpenStack AddFixedIPWithAutoSubnet - IP %s is not within subnet CIDR %s", ipAddress, subnet.CIDR)
				return fmt.Errorf("IP address %s is not within subnet CIDR %s", ipAddress, subnet.CIDR)
			}
			h.Logger.Debugf("OpenStack AddFixedIPWithAutoSubnet - IP %s is within subnet CIDR %s", ipAddress, subnet.CIDR)
		}
	}
	
	// Add fixed IP
	if err := osClient.AddFixedIP(ctx, interfaceID, ipAddress, subnetID); err != nil {
		h.Logger.Errorf("OpenStack AddFixedIPWithAutoSubnet - Failed to add fixed IP %s to interface %s: %v", ipAddress, interfaceID, err)
		return fmt.Errorf("failed to add fixed IP %s to interface %s: %v", ipAddress, interfaceID, err)
	}
	
	h.Logger.Infof("OpenStack AddFixedIPWithAutoSubnet - Successfully added fixed IP %s to interface %s (subnet: %s)", ipAddress, interfaceID, subnetID)
	return nil
}

// RemoveFixedIP removes a fixed IP from an OpenStack interface
func (h *Handler) RemoveFixedIP(ctx context.Context, interfaceID, ipAddress, projectName string) error {
	h.Logger.Debugf("OpenStack RemoveFixedIP - InterfaceID: %s, IP: %s, Project: %s", interfaceID, ipAddress, projectName)
	
	// Get cloud configuration for the project
	cloudConfig, err := h.getCloudConfig(projectName)
	if err != nil {
		h.Logger.Errorf("OpenStack RemoveFixedIP - Failed to get cloud config for project %s: %v", projectName, err)
		return fmt.Errorf("failed to get cloud config for project %s: %v", projectName, err)
	}
	
	// Create OpenStack client
	osClient := NewOpenStackClient(cloudConfig, h.Logger)
	
	// Remove fixed IP
	if err := osClient.RemoveFixedIP(ctx, interfaceID, ipAddress); err != nil {
		h.Logger.Errorf("OpenStack RemoveFixedIP - Failed to remove fixed IP %s from interface %s: %v", ipAddress, interfaceID, err)
		return fmt.Errorf("failed to remove fixed IP %s from interface %s: %v", ipAddress, interfaceID, err)
	}
	
	h.Logger.Infof("OpenStack RemoveFixedIP - Successfully removed fixed IP %s from interface %s", ipAddress, interfaceID)
	return nil
}

// getCloudConfig retrieves cloud configuration for a project
func (h *Handler) getCloudConfig(projectID string) (*models.CloudConfig, error) {
	h.Logger.Debugf("OpenStack getCloudConfig - Searching cloud config for project: %s", projectID)
	
	settingsCollection := h.Context.Client.Collection("settings")
	
	var settings models.Settings
	filter := bson.M{"project": projectID}
	
	h.Logger.Debugf("OpenStack getCloudConfig - DB query filter: %+v", filter)
	err := settingsCollection.FindOne(context.Background(), filter).Decode(&settings)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			h.Logger.Errorf("OpenStack getCloudConfig - No settings found for project %s", projectID)
			return nil, fmt.Errorf("no settings found for project %s", projectID)
		}
		h.Logger.Errorf("OpenStack getCloudConfig - Database error for project %s: %v", projectID, err)
		return nil, fmt.Errorf("database error: %v", err)
	}

	h.Logger.Debugf("OpenStack getCloudConfig - Settings retrieved for project %s", projectID)

	// Look for OpenStack cloud configuration
	if settings.Clouds == nil {
		h.Logger.Warnf("OpenStack getCloudConfig - No cloud configurations found for project %s", projectID)
		return nil, fmt.Errorf("no cloud configurations found")
	}

	h.Logger.Debugf("OpenStack getCloudConfig - Found %d cloud configurations", len(settings.Clouds))

	for cloudName, cloudConfig := range settings.Clouds {
		h.Logger.Debugf("OpenStack getCloudConfig - Checking cloud config: %s (provider: %s)", cloudName, cloudConfig.Provider)
		if cloudConfig.Provider == "openstack" {
			h.Logger.Infof("OpenStack getCloudConfig - Found OpenStack cloud config: %s for project %s", cloudName, projectID)
			
			// Validate required auth fields
			if cloudConfig.Auth.AuthURL == "" {
				h.Logger.Errorf("OpenStack getCloudConfig - Missing auth_url in OpenStack config for project %s", projectID)
				return nil, fmt.Errorf("OpenStack auth_url not configured for project %s", projectID)
			}
			
			if cloudConfig.Auth.ApplicationCredentialID == "" || cloudConfig.Auth.ApplicationCredentialSecret == "" {
				h.Logger.Errorf("OpenStack getCloudConfig - Missing application credentials in OpenStack config for project %s", projectID)
				return nil, fmt.Errorf("OpenStack application credentials not configured for project %s", projectID)
			}
			
			h.Logger.Debugf("OpenStack getCloudConfig - Auth validation passed for project %s (auth_url: %s)", projectID, cloudConfig.Auth.AuthURL)
			return &cloudConfig, nil
		}
	}

	h.Logger.Warnf("OpenStack getCloudConfig - No OpenStack cloud configuration found for project %s", projectID)
	return nil, fmt.Errorf("no OpenStack cloud configuration found")
}

// GetNetworkDetails retrieves OpenStack network details by network ID
// GET /api/op/openstack/networks/:network_id
func (h *Handler) GetNetworkDetails(c *gin.Context) {
	networkID := c.Param("network_id")
	
	h.Logger.Debugf("OpenStack Network API Request - NetworkID: %s", networkID)
	
	if networkID == "" {
		h.Logger.Errorf("OpenStack Network API - Missing network_id parameter")
		c.JSON(http.StatusBadRequest, gin.H{"message": "network_id is required"})
		return
	}

	// Get OpenStack project UUID from query parameter (required for OpenStack API)
	ospProject := c.Query("osp_project")
	if ospProject == "" {
		h.Logger.Errorf("OpenStack Network API - Missing osp_project query parameter")
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "osp_project query parameter is required (OpenStack project UUID)",
			"example": "/api/op/openstack/networks/{network_id}?osp_project=openstack-project-uuid",
		})
		return
	}

	// Get our DB project from query parameter (required for cloud config lookup)
	dbProject := c.Query("project")
	if dbProject == "" {
		h.Logger.Errorf("OpenStack Network API - Missing project query parameter")
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "project query parameter is required (DB project name for cloud config)",
			"example": "/api/op/openstack/networks/{network_id}?osp_project=openstack-project-uuid&project=db-project-name",
		})
		return
	}

	h.Logger.Debugf("OpenStack Network API - OpenStack project UUID: %s, DB project: %s", ospProject, dbProject)

	// Get cloud configuration for the DB project
	h.Logger.Debugf("OpenStack Network API - Fetching cloud config for DB project: %s", dbProject)
	cloudConfig, err := h.getCloudConfig(dbProject)
	if err != nil {
		h.Logger.Errorf("OpenStack Network API - Failed to get cloud config for DB project %s: %v", dbProject, err)
		c.JSON(http.StatusBadRequest, gin.H{"message": fmt.Sprintf("Cloud configuration not found for DB project %s: %v", dbProject, err)})
		return
	}
	
	h.Logger.Debugf("OpenStack Network API - Cloud config retrieved: Provider=%s, AuthURL=%s", cloudConfig.Provider, cloudConfig.Auth.AuthURL)

	// Create OpenStack client
	h.Logger.Debugf("OpenStack Network API - Creating OpenStack client for DB project: %s", dbProject)
	osClient := NewOpenStackClient(cloudConfig, h.Logger)

	// Get network details
	h.Logger.Debugf("OpenStack Network API - Getting network details for: %s", networkID)
	network, err := osClient.GetNetwork(context.Background(), networkID)
	if err != nil {
		h.Logger.Errorf("OpenStack Network API - Failed to get network details for %s: %v", networkID, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": fmt.Sprintf("Failed to retrieve network details: %v", err),
		})
		return
	}

	// Convert to response format
	networkInfo := NetworkInfo{
		ID:                  network.ID,
		Name:                network.Name,
		TenantID:            network.TenantID,
		AdminStateUp:        network.AdminStateUp,
		Status:              network.Status,
		Subnets:             network.Subnets,
		Shared:              network.Shared,
		AvailabilityZones:   network.AvailabilityZones,
		RouterExternal:      network.RouterExternal,
		DNSDomain:           network.DNSDomain,
		MTU:                 network.MTU,
		PortSecurityEnabled: network.PortSecurityEnabled,
		Tags:                network.Tags,
		Description:         network.Description,
	}

	response := NetworkDetailsResponse{
		Message: "Success",
		Data:    networkInfo,
	}

	h.Logger.Infof("OpenStack Network API - Successfully retrieved network details for: %s (%s)", networkID, network.Name)
	c.JSON(http.StatusOK, response)
}

// GetSubnetDetails retrieves OpenStack subnet details by subnet ID
// GET /api/op/openstack/subnets/:subnet_id
func (h *Handler) GetSubnetDetails(c *gin.Context) {
	subnetID := c.Param("subnet_id")
	
	h.Logger.Debugf("OpenStack Subnet API Request - SubnetID: %s", subnetID)
	
	if subnetID == "" {
		h.Logger.Errorf("OpenStack Subnet API - Missing subnet_id parameter")
		c.JSON(http.StatusBadRequest, gin.H{"message": "subnet_id is required"})
		return
	}

	// Get OpenStack project UUID from query parameter (required for OpenStack API)
	ospProject := c.Query("osp_project")
	if ospProject == "" {
		h.Logger.Errorf("OpenStack Subnet API - Missing osp_project query parameter")
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "osp_project query parameter is required (OpenStack project UUID)",
			"example": "/api/op/openstack/subnets/{subnet_id}?osp_project=openstack-project-uuid&project=db-project-name",
		})
		return
	}

	// Get our DB project from query parameter (required for cloud config lookup)
	dbProject := c.Query("project")
	if dbProject == "" {
		h.Logger.Errorf("OpenStack Subnet API - Missing project query parameter")
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "project query parameter is required (DB project name for cloud config)",
			"example": "/api/op/openstack/subnets/{subnet_id}?osp_project=openstack-project-uuid&project=db-project-name",
		})
		return
	}

	h.Logger.Debugf("OpenStack Subnet API - OpenStack project UUID: %s, DB project: %s", ospProject, dbProject)

	// Get cloud configuration for the DB project
	h.Logger.Debugf("OpenStack Subnet API - Fetching cloud config for DB project: %s", dbProject)
	cloudConfig, err := h.getCloudConfig(dbProject)
	if err != nil {
		h.Logger.Errorf("OpenStack Subnet API - Failed to get cloud config for DB project %s: %v", dbProject, err)
		c.JSON(http.StatusBadRequest, gin.H{"message": fmt.Sprintf("Cloud configuration not found for DB project %s: %v", dbProject, err)})
		return
	}
	
	h.Logger.Debugf("OpenStack Subnet API - Cloud config retrieved: Provider=%s, AuthURL=%s", cloudConfig.Provider, cloudConfig.Auth.AuthURL)

	// Create OpenStack client
	h.Logger.Debugf("OpenStack Subnet API - Creating OpenStack client for DB project: %s", dbProject)
	osClient := NewOpenStackClient(cloudConfig, h.Logger)

	// Get subnet details
	h.Logger.Debugf("OpenStack Subnet API - Getting subnet details for: %s", subnetID)
	subnet, err := osClient.GetSubnet(context.Background(), subnetID)
	if err != nil {
		h.Logger.Errorf("OpenStack Subnet API - Failed to get subnet details for %s: %v", subnetID, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": fmt.Sprintf("Failed to retrieve subnet details: %v", err),
		})
		return
	}

	// Convert to response format
	subnetInfo := SubnetInfo{
		ID:               subnet.ID,
		Name:             subnet.Name,
		TenantID:         subnet.TenantID,
		NetworkID:        subnet.NetworkID,
		IPVersion:        subnet.IPVersion,
		CIDR:             subnet.CIDR,
		GatewayIP:        subnet.GatewayIP,
		DNSNameservers:   subnet.DNSNameservers,
		AllocationPools:  subnet.AllocationPools,
		HostRoutes:       subnet.HostRoutes,
		EnableDHCP:       subnet.EnableDHCP,
		IPv6AddressMode:  subnet.IPv6AddressMode,
		IPv6RAMode:       subnet.IPv6RAMode,
		SubnetPoolID:     subnet.SubnetPoolID,
		UseDefaultSubnet: subnet.UseDefaultSubnet,
		Tags:             subnet.Tags,
		Description:      subnet.Description,
	}

	response := SubnetDetailsResponse{
		Message: "Success",
		Data:    subnetInfo,
	}

	h.Logger.Infof("OpenStack Subnet API - Successfully retrieved subnet details for: %s (%s)", subnetID, subnet.Name)
	c.JSON(http.StatusOK, response)
}

// GetNetworkSubnets retrieves all subnets for a specific network
// GET /api/op/openstack/networks/:network_id/subnets
func (h *Handler) GetNetworkSubnets(c *gin.Context) {
	networkID := c.Param("network_id")
	
	h.Logger.Debugf("OpenStack Network Subnets API Request - NetworkID: %s", networkID)
	
	if networkID == "" {
		h.Logger.Errorf("OpenStack Network Subnets API - Missing network_id parameter")
		c.JSON(http.StatusBadRequest, gin.H{"message": "network_id is required"})
		return
	}

	// Get OpenStack project UUID from query parameter (required for OpenStack API)
	ospProject := c.Query("osp_project")
	if ospProject == "" {
		h.Logger.Errorf("OpenStack Network Subnets API - Missing osp_project query parameter")
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "osp_project query parameter is required (OpenStack project UUID)",
			"example": "/api/op/openstack/networks/{network_id}/subnets?osp_project=openstack-project-uuid&project=db-project-name",
		})
		return
	}

	// Get our DB project from query parameter (required for cloud config lookup)
	dbProject := c.Query("project")
	if dbProject == "" {
		h.Logger.Errorf("OpenStack Network Subnets API - Missing project query parameter")
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "project query parameter is required (DB project name for cloud config)",
			"example": "/api/op/openstack/networks/{network_id}/subnets?osp_project=openstack-project-uuid&project=db-project-name",
		})
		return
	}

	h.Logger.Debugf("OpenStack Network Subnets API - OpenStack project UUID: %s, DB project: %s", ospProject, dbProject)

	// Get cloud configuration for the DB project
	h.Logger.Debugf("OpenStack Network Subnets API - Fetching cloud config for DB project: %s", dbProject)
	cloudConfig, err := h.getCloudConfig(dbProject)
	if err != nil {
		h.Logger.Errorf("OpenStack Network Subnets API - Failed to get cloud config for DB project %s: %v", dbProject, err)
		c.JSON(http.StatusBadRequest, gin.H{"message": fmt.Sprintf("Cloud configuration not found for DB project %s: %v", dbProject, err)})
		return
	}
	
	h.Logger.Debugf("OpenStack Network Subnets API - Cloud config retrieved: Provider=%s, AuthURL=%s", cloudConfig.Provider, cloudConfig.Auth.AuthURL)

	// Create OpenStack client
	h.Logger.Debugf("OpenStack Network Subnets API - Creating OpenStack client for DB project: %s", dbProject)
	osClient := NewOpenStackClient(cloudConfig, h.Logger)

	// Get network subnets
	h.Logger.Debugf("OpenStack Network Subnets API - Getting subnets for network: %s", networkID)
	subnets, err := osClient.ListNetworkSubnets(context.Background(), networkID)
	if err != nil {
		h.Logger.Errorf("OpenStack Network Subnets API - Failed to get subnets for network %s: %v", networkID, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": fmt.Sprintf("Failed to retrieve network subnets: %v", err),
		})
		return
	}

	h.Logger.Debugf("OpenStack Network Subnets API - Found %d subnets for network %s", len(subnets), networkID)

	// Convert to response format
	subnetInfos := make([]SubnetInfo, len(subnets))
	for i, subnet := range subnets {
		h.Logger.Debugf("OpenStack Network Subnets API - Subnet[%d]: ID=%s, Name=%s, CIDR=%s, GatewayIP=%s", 
			i, subnet.ID, subnet.Name, subnet.CIDR, subnet.GatewayIP)
		
		subnetInfos[i] = SubnetInfo{
			ID:               subnet.ID,
			Name:             subnet.Name,
			TenantID:         subnet.TenantID,
			NetworkID:        subnet.NetworkID,
			IPVersion:        subnet.IPVersion,
			CIDR:             subnet.CIDR,
			GatewayIP:        subnet.GatewayIP,
			DNSNameservers:   subnet.DNSNameservers,
			AllocationPools:  subnet.AllocationPools,
			HostRoutes:       subnet.HostRoutes,
			EnableDHCP:       subnet.EnableDHCP,
			IPv6AddressMode:  subnet.IPv6AddressMode,
			IPv6RAMode:       subnet.IPv6RAMode,
			SubnetPoolID:     subnet.SubnetPoolID,
			UseDefaultSubnet: subnet.UseDefaultSubnet,
			Tags:             subnet.Tags,
			Description:      subnet.Description,
		}
	}

	response := NetworkSubnetsResponse{
		Message:   "Success",
		NetworkID: networkID,
		Data:      subnetInfos,
	}

	h.Logger.Infof("OpenStack Network Subnets API - Successfully retrieved %d subnets for network: %s", len(subnetInfos), networkID)
	c.JSON(http.StatusOK, response)
}