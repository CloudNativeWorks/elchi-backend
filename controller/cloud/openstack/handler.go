package openstack

import (
	"context"
	"fmt"
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
			return &cloudConfig, nil
		}
	}

	h.Logger.Warnf("OpenStack getCloudConfig - No OpenStack cloud configuration found for project %s", projectID)
	return nil, fmt.Errorf("no OpenStack cloud configuration found")
}