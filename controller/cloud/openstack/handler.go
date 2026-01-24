package openstack

import (
	"context"
	"errors"
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
	ID                  string               `json:"id"`
	Name                string               `json:"name"`
	NetworkID           string               `json:"network_id"`
	NetworkInfo         *NetworkInfo         `json:"network,omitempty"` // Full network details including subnets
	Status              string               `json:"status"`
	AdminStateUp        bool                 `json:"admin_state_up"`
	MACAddress          string               `json:"mac_address"`
	FixedIPs            []FixedIP            `json:"fixed_ips"`
	AllowedAddressPairs []AllowedAddressPair `json:"allowed_address_pairs"`
	DeviceID            string               `json:"device_id"`
	DeviceOwner         string               `json:"device_owner"`
}

// InterfaceListResponse represents the response structure for interface listing
type InterfaceListResponse struct {
	Message    string          `json:"message"`
	Data       []InterfaceInfo `json:"data"`
	ClientInfo ClientInfo      `json:"client_info"`
}

// NetworkInfo represents network information for API response
type NetworkInfo struct {
	ID                  string       `json:"id"`
	Name                string       `json:"name"`
	TenantID            string       `json:"tenant_id"`
	AdminStateUp        bool         `json:"admin_state_up"`
	Status              string       `json:"status"`
	Subnets             []SubnetInfo `json:"subnets"` // Changed from []string to []SubnetInfo
	Shared              bool         `json:"shared"`
	AvailabilityZones   []string     `json:"availability_zones"`
	RouterExternal      bool         `json:"router_external"`
	DNSDomain           string       `json:"dns_domain"`
	MTU                 int          `json:"mtu"`
	PortSecurityEnabled bool         `json:"port_security_enabled"`
	Tags                []string     `json:"tags"`
	Description         string       `json:"description"`
}

// SubnetInfo represents subnet information for API response
type SubnetInfo struct {
	ID               string           `json:"id"`
	Name             string           `json:"name"`
	TenantID         string           `json:"tenant_id"`
	NetworkID        string           `json:"network_id"`
	IPVersion        int              `json:"ip_version"`
	CIDR             string           `json:"cidr"`
	GatewayIP        string           `json:"gateway_ip"`
	DNSNameservers   []string         `json:"dns_nameservers"`
	AllocationPools  []AllocationPool `json:"allocation_pools"`
	HostRoutes       []HostRoute      `json:"host_routes"`
	EnableDHCP       bool             `json:"enable_dhcp"`
	IPv6AddressMode  string           `json:"ipv6_address_mode"`
	IPv6RAMode       string           `json:"ipv6_ra_mode"`
	SubnetPoolID     string           `json:"subnetpool_id"`
	UseDefaultSubnet bool             `json:"use_default_subnetpool"`
	Tags             []string         `json:"tags"`
	Description      string           `json:"description"`
}

// AvailableIPsInfo represents available IP information for a subnet
type AvailableIPsInfo struct {
	SubnetID       string   `json:"subnet_id"`
	SubnetName     string   `json:"subnet_name"`
	NetworkID      string   `json:"network_id"`
	NetworkName    string   `json:"network_name"`
	CIDR           string   `json:"cidr"`
	GatewayIP      string   `json:"gateway_ip"`
	AvailableIPs   []string `json:"available_ips"`
	UsedIPs        []string `json:"used_ips"`
	TotalAvailable int      `json:"total_available"`
	TotalUsed      int      `json:"total_used"`
}

// AvailableIPsResponse represents the response structure for available IPs
type AvailableIPsResponse struct {
	Message string           `json:"message"`
	Data    AvailableIPsInfo `json:"data"`
}

// NetworkDetailsResponse represents the response structure for network details
type NetworkDetailsResponse struct {
	Message string      `json:"message"`
	Data    NetworkInfo `json:"data"`
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

	// Convert to response format with network details
	h.Logger.Debugf("OpenStack Interface API - Converting %d ports to response format with network details", len(ports))
	interfaces := h.enrichPortsWithNetworkInfo(context.Background(), ports, osClient)

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

// enrichPortsWithNetworkInfo enriches port data with network and subnet details
func (h *Handler) enrichPortsWithNetworkInfo(ctx context.Context, ports []ServerPort, osClient *OpenStackClient) []InterfaceInfo {
	interfaces := make([]InterfaceInfo, len(ports))
	networkCache := make(map[string]*NetworkInfo)

	for i, port := range ports {
		h.Logger.Debugf("OpenStack Interface API - Port[%d]: ID=%s, Name=%s, NetworkID=%s",
			i, port.ID, port.Name, port.NetworkID)

		// Get network info from cache or fetch it
		networkInfo, err := h.getNetworkInfoWithCache(ctx, port.NetworkID, networkCache, osClient)
		if err != nil {
			h.Logger.Warnf("OpenStack Interface API - Failed to get network info for %s: %v", port.NetworkID, err)
			networkInfo = nil
		}

		// Generate interface name if empty
		interfaceName := port.Name
		if interfaceName == "" && networkInfo != nil {
			interfaceName = fmt.Sprintf("%s-port", networkInfo.Name)
		}

		interfaces[i] = InterfaceInfo{
			ID:                  port.ID,
			Name:                interfaceName,
			NetworkID:           port.NetworkID,
			NetworkInfo:         networkInfo,
			Status:              port.Status,
			AdminStateUp:        port.AdminStateUp,
			MACAddress:          port.MACAddress,
			FixedIPs:            port.FixedIPs,
			AllowedAddressPairs: port.AllowedAddressPairs,
			DeviceID:            port.DeviceID,
			DeviceOwner:         port.DeviceOwner,
		}

		networkName := "unknown"
		if networkInfo != nil {
			networkName = networkInfo.Name
		}
		h.Logger.Debugf("OpenStack Interface API - Interface[%d] completed: Name=%s, NetworkName=%s",
			i, interfaceName, networkName)
	}

	return interfaces
}

// getNetworkInfoWithCache gets network info using cache to avoid duplicate API calls
func (h *Handler) getNetworkInfoWithCache(ctx context.Context, networkID string, cache map[string]*NetworkInfo, osClient *OpenStackClient) (*NetworkInfo, error) {
	// Check cache first
	if cachedNetwork, exists := cache[networkID]; exists {
		h.Logger.Debugf("OpenStack Interface API - Using cached network details for: %s", networkID)
		return cachedNetwork, nil
	}

	// Fetch network details
	h.Logger.Debugf("OpenStack Interface API - Fetching network details for: %s", networkID)
	network, err := osClient.GetNetwork(ctx, networkID)
	if err != nil {
		return nil, fmt.Errorf("failed to get network %s: %w", networkID, err)
	}

	// Fetch subnet details
	h.Logger.Debugf("OpenStack Interface API - Fetching subnet details for network %s (%d subnets)", networkID, len(network.Subnets))
	subnetInfos := h.fetchSubnetDetails(ctx, network.Subnets, osClient)

	// Create NetworkInfo
	networkInfo := &NetworkInfo{
		ID:                  network.ID,
		Name:                network.Name,
		TenantID:            network.TenantID,
		AdminStateUp:        network.AdminStateUp,
		Status:              network.Status,
		Subnets:             subnetInfos,
		Shared:              network.Shared,
		AvailabilityZones:   network.AvailabilityZones,
		RouterExternal:      network.RouterExternal,
		DNSDomain:           network.DNSDomain,
		MTU:                 network.MTU,
		PortSecurityEnabled: network.PortSecurityEnabled,
		Tags:                network.Tags,
		Description:         network.Description,
	}

	// Cache and return
	cache[networkID] = networkInfo
	h.Logger.Debugf("OpenStack Interface API - Cached network details for: %s (%s, %d subnets)",
		network.ID, network.Name, len(subnetInfos))

	return networkInfo, nil
}

// fetchSubnetDetails fetches details for multiple subnets
func (h *Handler) fetchSubnetDetails(ctx context.Context, subnetIDs []string, osClient *OpenStackClient) []SubnetInfo {
	subnetInfos := make([]SubnetInfo, 0, len(subnetIDs))

	for _, subnetID := range subnetIDs {
		subnet, err := osClient.GetSubnet(ctx, subnetID)
		if err != nil {
			h.Logger.Warnf("OpenStack Interface API - Failed to get subnet %s details: %v (continuing with other subnets)", subnetID, err)
			continue
		}

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

		subnetInfos = append(subnetInfos, subnetInfo)
	}

	return subnetInfos
}

// calculateAvailableIPs calculates available IPs for a subnet
func (h *Handler) calculateAvailableIPs(ctx context.Context, subnet Subnet, networkID string, osClient *OpenStackClient) ([]string, []string, error) {
	h.Logger.Debugf("Calculating available IPs for subnet %s in network %s", subnet.ID, networkID)

	// Get all used IPs in the network
	usedIPs, err := osClient.GetNetworkUsedIPs(ctx, networkID)
	if err != nil {
		h.Logger.Warnf("Failed to get used IPs for network %s: %v", networkID, err)
		return nil, nil, err
	}

	// Create a map for quick lookup of used IPs
	usedIPMap := make(map[string]bool)
	var subnetUsedIPs []string

	for _, ip := range usedIPs {
		usedIPMap[ip] = true
		// Check if this IP belongs to current subnet by checking if it's in CIDR
		if h.isIPInCIDR(ip, subnet.CIDR) {
			subnetUsedIPs = append(subnetUsedIPs, ip)
		}
	}

	// Calculate available IPs from allocation pools
	var availableIPs []string
	for _, pool := range subnet.AllocationPools {
		poolIPs, err := h.generateIPRange(pool.Start, pool.End)
		if err != nil {
			h.Logger.Warnf("Failed to generate IP range %s-%s: %v", pool.Start, pool.End, err)
			continue
		}

		for _, ip := range poolIPs {
			if !usedIPMap[ip] && ip != subnet.GatewayIP {
				availableIPs = append(availableIPs, ip)
			}
		}
	}

	h.Logger.Debugf("Subnet %s: Available IPs: %d, Used IPs: %d", subnet.ID, len(availableIPs), len(subnetUsedIPs))
	return availableIPs, subnetUsedIPs, nil
}

// isIPInCIDR checks if an IP address is within a CIDR range
func (h *Handler) isIPInCIDR(ipStr, cidr string) bool {
	if cidr == "" {
		return false
	}

	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}

	return ipNet.Contains(ip)
}

// generateIPRange generates all IPs between start and end (inclusive)
func (h *Handler) generateIPRange(startIP, endIP string) ([]string, error) {
	start := net.ParseIP(startIP)
	end := net.ParseIP(endIP)

	if start == nil || end == nil {
		return nil, fmt.Errorf("invalid IP format: start=%s, end=%s", startIP, endIP)
	}

	// Convert to 4-byte representation for easier arithmetic
	start4 := start.To4()
	end4 := end.To4()

	if start4 == nil || end4 == nil {
		return nil, fmt.Errorf("only IPv4 is supported")
	}

	// Convert to uint32 for arithmetic
	startUint := uint32(start4[0])<<24 | uint32(start4[1])<<16 | uint32(start4[2])<<8 | uint32(start4[3])
	endUint := uint32(end4[0])<<24 | uint32(end4[1])<<16 | uint32(end4[2])<<8 | uint32(end4[3])

	if startUint > endUint {
		return nil, fmt.Errorf("start IP %s is greater than end IP %s", startIP, endIP)
	}

	// Generate all IPs in range
	var ips []string
	for i := startUint; i <= endUint; i++ {
		ip := make(net.IP, 4)
		ip[0] = byte(i >> 24)
		ip[1] = byte(i >> 16)
		ip[2] = byte(i >> 8)
		ip[3] = byte(i)
		ips = append(ips, ip.String())
	}

	return ips, nil
}

// findSubnetForIP finds the appropriate subnet for an IP address within a network
func (h *Handler) findSubnetForIP(ctx context.Context, networkID, ipAddress string, osClient *OpenStackClient) (string, error) {
	h.Logger.Debugf("Finding subnet for IP %s in network %s", ipAddress, networkID)

	// Parse the IP address
	ip := net.ParseIP(ipAddress)
	if ip == nil {
		return "", fmt.Errorf("invalid IP address format: %s", ipAddress)
	}

	// Get all subnets for the network
	subnets, err := osClient.ListNetworkSubnets(ctx, networkID)
	if err != nil {
		return "", fmt.Errorf("failed to list network subnets: %w", err)
	}

	h.Logger.Debugf("Found %d subnets in network %s", len(subnets), networkID)

	// Check each subnet's CIDR to find which one contains the IP
	for _, subnet := range subnets {
		h.Logger.Debugf("Checking subnet %s (%s) with CIDR %s", subnet.ID, subnet.Name, subnet.CIDR)

		if subnet.CIDR == "" {
			h.Logger.Warnf("Subnet %s has empty CIDR, skipping", subnet.ID)
			continue
		}

		// Parse the CIDR
		_, ipNet, err := net.ParseCIDR(subnet.CIDR)
		if err != nil {
			h.Logger.Warnf("Invalid CIDR format for subnet %s: %s, skipping", subnet.ID, subnet.CIDR)
			continue
		}

		// Check if the IP is within this subnet's CIDR
		if ipNet.Contains(ip) {
			h.Logger.Infof("Found matching subnet for IP %s: %s (%s, CIDR: %s)",
				ipAddress, subnet.ID, subnet.Name, subnet.CIDR)
			return subnet.ID, nil
		}
	}

	// No matching subnet found
	return "", fmt.Errorf("IP address %s does not belong to any subnet in network %s", ipAddress, networkID)
}

// AddAllowedAddressPair adds an allowed address pair to an OpenStack interface
func (h *Handler) AddAllowedAddressPair(ctx context.Context, interfaceID, ipAddress, projectName string) error {
	h.Logger.Debugf("OpenStack AddAllowedAddressPair - InterfaceID: %s, IP: %s, Project: %s", interfaceID, ipAddress, projectName)

	// Get cloud configuration for the project
	cloudConfig, err := h.getCloudConfig(projectName)
	if err != nil {
		h.Logger.Errorf("OpenStack AddAllowedAddressPair - Failed to get cloud config for project %s: %v", projectName, err)
		return fmt.Errorf("failed to get cloud config for project %s: %w", projectName, err)
	}

	h.Logger.Debugf("OpenStack AddAllowedAddressPair - Cloud config retrieved: Provider=%s, AuthURL=%s", cloudConfig.Provider, cloudConfig.Auth.AuthURL)

	// Create OpenStack client
	osClient := NewOpenStackClient(cloudConfig, h.Logger)

	// Pre-check: Verify interface exists before attempting to modify
	if err := osClient.authenticate(ctx); err != nil {
		h.Logger.Errorf("OpenStack AddAllowedAddressPair - Authentication failed: %v", err)
		return fmt.Errorf("OpenStack authentication failed: %w", err)
	}

	endpoint, err := osClient.getNetworkEndpoint()
	if err != nil {
		h.Logger.Errorf("OpenStack AddAllowedAddressPair - Failed to get network endpoint: %v", err)
		return fmt.Errorf("failed to get network endpoint: %w", err)
	}

	// Check if interface exists
	_, err = osClient.getPort(ctx, endpoint, interfaceID)
	if err != nil {
		h.Logger.Errorf("OpenStack AddAllowedAddressPair - Interface %s not found: %v", interfaceID, err)
		return fmt.Errorf("interface %s not found or inaccessible: %w", interfaceID, err)
	}

	h.Logger.Debugf("OpenStack AddAllowedAddressPair - No CIDR validation required for AAP (allows cross-subnet IPs)")

	// Add allowed address pair
	if err := osClient.AddAllowedAddressPair(ctx, interfaceID, ipAddress); err != nil {
		h.Logger.Errorf("OpenStack AddAllowedAddressPair - Failed to add allowed address pair %s to interface %s: %v", ipAddress, interfaceID, err)
		return fmt.Errorf("failed to add allowed address pair %s to interface %s: %w", ipAddress, interfaceID, err)
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
		return fmt.Errorf("failed to get cloud config for project %s: %w", projectName, err)
	}

	h.Logger.Debugf("OpenStack RemoveAllowedAddressPair - Cloud config retrieved: Provider=%s, AuthURL=%s", cloudConfig.Provider, cloudConfig.Auth.AuthURL)

	// Create OpenStack client
	osClient := NewOpenStackClient(cloudConfig, h.Logger)

	// Remove allowed address pair
	if err := osClient.RemoveAllowedAddressPair(ctx, interfaceID, ipAddress); err != nil {
		h.Logger.Errorf("OpenStack RemoveAllowedAddressPair - Failed to remove allowed address pair %s from interface %s: %v", ipAddress, interfaceID, err)
		return fmt.Errorf("failed to remove allowed address pair %s from interface %s: %w", ipAddress, interfaceID, err)
	}

	h.Logger.Infof("OpenStack RemoveAllowedAddressPair - Successfully removed allowed address pair %s from interface %s", ipAddress, interfaceID)
	return nil
}

// AddFixedIPWithAutoSubnet adds a fixed IP to an interface with smart subnet detection
func (h *Handler) AddFixedIPWithAutoSubnet(ctx context.Context, interfaceID, ipAddress, projectName string) error {
	h.Logger.Debugf("OpenStack AddFixedIPWithAutoSubnet - InterfaceID: %s, IP: %s, Project: %s", interfaceID, ipAddress, projectName)

	// Get cloud configuration for the project
	cloudConfig, err := h.getCloudConfig(projectName)
	if err != nil {
		h.Logger.Errorf("OpenStack AddFixedIPWithAutoSubnet - Failed to get cloud config for project %s: %v", projectName, err)
		return fmt.Errorf("failed to get cloud config for project %s: %w", projectName, err)
	}

	// Create OpenStack client
	osClient := NewOpenStackClient(cloudConfig, h.Logger)

	// Authenticate first to get endpoint
	if err := osClient.authenticate(ctx); err != nil {
		h.Logger.Errorf("OpenStack AddFixedIPWithAutoSubnet - Authentication failed: %v", err)
		return fmt.Errorf("authentication failed: %w", err)
	}

	// Get network endpoint
	endpoint, err := osClient.getNetworkEndpoint()
	if err != nil {
		h.Logger.Errorf("OpenStack AddFixedIPWithAutoSubnet - Failed to get network endpoint: %v", err)
		return fmt.Errorf("failed to get network endpoint: %w", err)
	}

	// Get port details to find network ID
	port, err := osClient.getPort(ctx, endpoint, interfaceID)
	if err != nil {
		h.Logger.Errorf("OpenStack AddFixedIPWithAutoSubnet - Failed to get port details for %s: %v", interfaceID, err)
		return fmt.Errorf("failed to get port details: %w", err)
	}

	var subnetID string

	// Strategy 1: If port has existing fixed IPs, use the first one's subnet
	if len(port.FixedIPs) > 0 {
		subnetID = port.FixedIPs[0].SubnetID
		h.Logger.Debugf("OpenStack AddFixedIPWithAutoSubnet - Using subnet ID from existing fixed IP: %s", subnetID)
	} else {
		// Strategy 2: No existing fixed IPs - find subnet by IP address
		h.Logger.Debugf("OpenStack AddFixedIPWithAutoSubnet - No existing fixed IPs, finding subnet for IP %s in network %s", ipAddress, port.NetworkID)

		subnetID, err = h.findSubnetForIP(ctx, port.NetworkID, ipAddress, osClient)
		if err != nil {
			h.Logger.Errorf("OpenStack AddFixedIPWithAutoSubnet - Failed to find subnet for IP %s: %v", ipAddress, err)
			return fmt.Errorf("failed to determine subnet for IP %s: %w", ipAddress, err)
		}

		h.Logger.Infof("OpenStack AddFixedIPWithAutoSubnet - Auto-detected subnet ID: %s for IP %s", subnetID, ipAddress)
	}

	// Validate IP is within the chosen subnet (extra safety check)
	subnet, err := osClient.GetSubnet(ctx, subnetID)
	if err != nil {
		h.Logger.Warnf("OpenStack AddFixedIPWithAutoSubnet - Could not validate subnet %s: %v (proceeding anyway)", subnetID, err)
	} else if subnet.CIDR != "" {
		if !h.isIPInCIDR(ipAddress, subnet.CIDR) {
			return fmt.Errorf("IP address %s is not within subnet CIDR %s", ipAddress, subnet.CIDR)
		}
		h.Logger.Debugf("OpenStack AddFixedIPWithAutoSubnet - IP validation passed: %s is within subnet CIDR %s", ipAddress, subnet.CIDR)
	}

	// Add fixed IP
	if err := osClient.AddFixedIP(ctx, interfaceID, ipAddress, subnetID); err != nil {
		h.Logger.Errorf("OpenStack AddFixedIPWithAutoSubnet - Failed to add fixed IP %s to interface %s: %v", ipAddress, interfaceID, err)
		return fmt.Errorf("failed to add fixed IP %s to interface %s: %w", ipAddress, interfaceID, err)
	}

	h.Logger.Infof("OpenStack AddFixedIPWithAutoSubnet - Successfully added fixed IP %s to interface %s (subnet: %s)", ipAddress, interfaceID, subnetID)
	return nil
}

// ValidateFixedIPInSubnet validates that an IP address is within a valid subnet for the interface
func (h *Handler) ValidateFixedIPInSubnet(ctx context.Context, interfaceID, ipAddress, dbProject string) error {
	h.Logger.Debugf("OpenStack ValidateFixedIPInSubnet - InterfaceID: %s, IP: %s, DB Project: %s", interfaceID, ipAddress, dbProject)

	// Get cloud configuration for the DB project
	cloudConfig, err := h.getCloudConfig(dbProject)
	if err != nil {
		h.Logger.Errorf("OpenStack ValidateFixedIPInSubnet - Failed to get cloud config for project %s: %v", dbProject, err)
		return fmt.Errorf("failed to get cloud config for project %s: %w", dbProject, err)
	}

	// Create OpenStack client
	osClient := NewOpenStackClient(cloudConfig, h.Logger)

	// Authenticate first to get endpoint
	if err := osClient.authenticate(ctx); err != nil {
		h.Logger.Errorf("OpenStack ValidateFixedIPInSubnet - Authentication failed: %v", err)
		return fmt.Errorf("authentication failed: %w", err)
	}

	// Get network endpoint
	endpoint, err := osClient.getNetworkEndpoint()
	if err != nil {
		h.Logger.Errorf("OpenStack ValidateFixedIPInSubnet - Failed to get network endpoint: %v", err)
		return fmt.Errorf("failed to get network endpoint: %w", err)
	}

	// Get port details to find network ID
	port, err := osClient.getPort(ctx, endpoint, interfaceID)
	if err != nil {
		h.Logger.Errorf("OpenStack ValidateFixedIPInSubnet - Failed to get port details for %s: %v", interfaceID, err)
		return fmt.Errorf("failed to get port details: %w", err)
	}

	var subnetID string

	// Strategy 1: If port has existing fixed IPs, use the first one's subnet for validation
	if len(port.FixedIPs) > 0 {
		subnetID = port.FixedIPs[0].SubnetID
		h.Logger.Debugf("OpenStack ValidateFixedIPInSubnet - Using subnet ID from existing fixed IP: %s", subnetID)
	} else {
		// Strategy 2: No existing fixed IPs - find the appropriate subnet for this IP
		h.Logger.Debugf("OpenStack ValidateFixedIPInSubnet - No existing fixed IPs, finding subnet for IP %s in network %s", ipAddress, port.NetworkID)

		subnetID, err = h.findSubnetForIP(ctx, port.NetworkID, ipAddress, osClient)
		if err != nil {
			h.Logger.Errorf("OpenStack ValidateFixedIPInSubnet - Failed to find subnet for IP %s: %v", ipAddress, err)
			return fmt.Errorf("IP address %s does not belong to any subnet in network %s: %w", ipAddress, port.NetworkID, err)
		}

		h.Logger.Debugf("OpenStack ValidateFixedIPInSubnet - Found appropriate subnet ID: %s for IP %s", subnetID, ipAddress)
	}

	// Get subnet details for CIDR validation
	subnet, err := osClient.GetSubnet(ctx, subnetID)
	if err != nil {
		h.Logger.Errorf("OpenStack ValidateFixedIPInSubnet - Failed to get subnet %s details: %v", subnetID, err)
		return fmt.Errorf("failed to get subnet %s details: %w", subnetID, err)
	}

	// Validate IP is within subnet CIDR
	if subnet.CIDR != "" {
		if !h.isIPInCIDR(ipAddress, subnet.CIDR) {
			h.Logger.Errorf("OpenStack ValidateFixedIPInSubnet - IP %s is not within subnet CIDR %s", ipAddress, subnet.CIDR)
			return fmt.Errorf("IP address %s is not within subnet CIDR %s", ipAddress, subnet.CIDR)
		}

		h.Logger.Debugf("OpenStack ValidateFixedIPInSubnet - IP %s is within subnet CIDR %s", ipAddress, subnet.CIDR)
	}

	h.Logger.Infof("OpenStack ValidateFixedIPInSubnet - Validation successful for IP %s (subnet: %s)", ipAddress, subnetID)
	return nil
}

// RemoveFixedIP removes a fixed IP from an OpenStack interface
func (h *Handler) RemoveFixedIP(ctx context.Context, interfaceID, ipAddress, projectName string) error {
	h.Logger.Debugf("OpenStack RemoveFixedIP - InterfaceID: %s, IP: %s, Project: %s", interfaceID, ipAddress, projectName)

	// Get cloud configuration for the project
	cloudConfig, err := h.getCloudConfig(projectName)
	if err != nil {
		h.Logger.Errorf("OpenStack RemoveFixedIP - Failed to get cloud config for project %s: %v", projectName, err)
		return fmt.Errorf("failed to get cloud config for project %s: %w", projectName, err)
	}

	// Create OpenStack client
	osClient := NewOpenStackClient(cloudConfig, h.Logger)

	// Remove fixed IP
	if err := osClient.RemoveFixedIP(ctx, interfaceID, ipAddress); err != nil {
		h.Logger.Errorf("OpenStack RemoveFixedIP - Failed to remove fixed IP %s from interface %s: %v", ipAddress, interfaceID, err)
		return fmt.Errorf("failed to remove fixed IP %s from interface %s: %w", ipAddress, interfaceID, err)
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
		if errors.Is(err, mongo.ErrNoDocuments) {
			h.Logger.Errorf("OpenStack getCloudConfig - No settings found for project %s", projectID)
			return nil, fmt.Errorf("no settings found for project %s", projectID)
		}
		h.Logger.Errorf("OpenStack getCloudConfig - Database error for project %s: %v", projectID, err)
		return nil, fmt.Errorf("database error: %w", err)
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

	// Fetch subnet details for each subnet ID
	h.Logger.Debugf("OpenStack Network API - Fetching details for %d subnets", len(network.Subnets))
	subnetInfos := make([]SubnetInfo, 0, len(network.Subnets))

	for _, subnetID := range network.Subnets {
		h.Logger.Debugf("OpenStack Network API - Getting subnet details for: %s", subnetID)
		subnet, err := osClient.GetSubnet(context.Background(), subnetID)
		if err != nil {
			h.Logger.Warnf("OpenStack Network API - Failed to get subnet %s details: %v (continuing with other subnets)", subnetID, err)
			continue // Skip this subnet but continue with others
		}

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

		subnetInfos = append(subnetInfos, subnetInfo)
		h.Logger.Debugf("OpenStack Network API - Added subnet: %s (%s, CIDR: %s)", subnet.ID, subnet.Name, subnet.CIDR)
	}

	// Convert to response format
	networkInfo := NetworkInfo{
		ID:                  network.ID,
		Name:                network.Name,
		TenantID:            network.TenantID,
		AdminStateUp:        network.AdminStateUp,
		Status:              network.Status,
		Subnets:             subnetInfos, // Now includes full subnet details
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

// GetSubnetAvailableIPs retrieves available IP addresses for a specific subnet
// GET /api/op/openstack/subnets/:subnet_id/available_ips
func (h *Handler) GetSubnetAvailableIPs(c *gin.Context) {
	subnetID := c.Param("subnet_id")

	h.Logger.Debugf("OpenStack Available IPs API Request - SubnetID: %s", subnetID)

	if subnetID == "" {
		h.Logger.Errorf("OpenStack Available IPs API - Missing subnet_id parameter")
		c.JSON(http.StatusBadRequest, gin.H{"message": "subnet_id is required"})
		return
	}

	// Get OpenStack project UUID from query parameter (required for OpenStack API)
	ospProject := c.Query("osp_project")
	if ospProject == "" {
		h.Logger.Errorf("OpenStack Available IPs API - Missing osp_project query parameter")
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "osp_project query parameter is required (OpenStack project UUID)",
			"example": "/api/op/openstack/subnets/{subnet_id}/available_ips?osp_project=openstack-project-uuid&project=db-project-name",
		})
		return
	}

	// Get our DB project from query parameter (required for cloud config lookup)
	dbProject := c.Query("project")
	if dbProject == "" {
		h.Logger.Errorf("OpenStack Available IPs API - Missing project query parameter")
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "project query parameter is required (DB project name for cloud config)",
			"example": "/api/op/openstack/subnets/{subnet_id}/available_ips?osp_project=openstack-project-uuid&project=db-project-name",
		})
		return
	}

	h.Logger.Debugf("OpenStack Available IPs API - OpenStack project UUID: %s, DB project: %s", ospProject, dbProject)

	// Get cloud configuration for the DB project
	h.Logger.Debugf("OpenStack Available IPs API - Fetching cloud config for DB project: %s", dbProject)
	cloudConfig, err := h.getCloudConfig(dbProject)
	if err != nil {
		h.Logger.Errorf("OpenStack Available IPs API - Failed to get cloud config for DB project %s: %v", dbProject, err)
		c.JSON(http.StatusBadRequest, gin.H{"message": fmt.Sprintf("Cloud configuration not found for DB project %s: %v", dbProject, err)})
		return
	}

	h.Logger.Debugf("OpenStack Available IPs API - Cloud config retrieved: Provider=%s, AuthURL=%s", cloudConfig.Provider, cloudConfig.Auth.AuthURL)

	// Create OpenStack client
	h.Logger.Debugf("OpenStack Available IPs API - Creating OpenStack client for DB project: %s", dbProject)
	osClient := NewOpenStackClient(cloudConfig, h.Logger)

	// Get subnet details
	h.Logger.Debugf("OpenStack Available IPs API - Getting subnet details for: %s", subnetID)
	subnet, err := osClient.GetSubnet(context.Background(), subnetID)
	if err != nil {
		h.Logger.Errorf("OpenStack Available IPs API - Failed to get subnet details for %s: %v", subnetID, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": fmt.Sprintf("Failed to retrieve subnet details: %v", err),
		})
		return
	}

	// Get network details for network name
	h.Logger.Debugf("OpenStack Available IPs API - Getting network details for: %s", subnet.NetworkID)
	network, err := osClient.GetNetwork(context.Background(), subnet.NetworkID)
	if err != nil {
		h.Logger.Errorf("OpenStack Available IPs API - Failed to get network details for %s: %v", subnet.NetworkID, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": fmt.Sprintf("Failed to retrieve network details: %v", err),
		})
		return
	}

	// Calculate available and used IPs
	h.Logger.Debugf("OpenStack Available IPs API - Calculating available IPs for subnet %s", subnetID)
	availableIPs, usedIPs, err := h.calculateAvailableIPs(context.Background(), *subnet, subnet.NetworkID, osClient)
	if err != nil {
		h.Logger.Errorf("OpenStack Available IPs API - Failed to calculate available IPs for subnet %s: %v", subnetID, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": fmt.Sprintf("Failed to calculate available IPs: %v", err),
		})
		return
	}

	// Create response
	availableIPsInfo := AvailableIPsInfo{
		SubnetID:       subnet.ID,
		SubnetName:     subnet.Name,
		NetworkID:      subnet.NetworkID,
		NetworkName:    network.Name,
		CIDR:           subnet.CIDR,
		GatewayIP:      subnet.GatewayIP,
		AvailableIPs:   availableIPs,
		UsedIPs:        usedIPs,
		TotalAvailable: len(availableIPs),
		TotalUsed:      len(usedIPs),
	}

	response := AvailableIPsResponse{
		Message: "Success",
		Data:    availableIPsInfo,
	}

	h.Logger.Infof("OpenStack Available IPs API - Successfully calculated IPs for subnet %s (%s): Available=%d, Used=%d",
		subnetID, subnet.Name, len(availableIPs), len(usedIPs))
	c.JSON(http.StatusOK, response)
}
