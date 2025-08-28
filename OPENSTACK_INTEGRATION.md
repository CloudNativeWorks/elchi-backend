# OpenStack Integration - Allowed Address Pairs Management

## 📋 Overview

This document outlines the implementation of automatic OpenStack allowed address pairs management when deploying services to OpenStack cloud clients. When a service is successfully deployed to a client running on OpenStack, the system automatically configures allowed address pairs for the downstream IP addresses.

## 🏗️ Architecture

### Current Deploy Flow
1. Client receives deploy command
2. Client executes deployment 
3. Client sends success response
4. **DeployResponser.ValidateAndTransform()** processes response
5. Client is added to service in MongoDB
6. **NEW: OpenStack integration triggers here**

### Enhanced Flow with OpenStack Integration
1. Client receives deploy command
2. Client executes deployment
3. Client sends success response
4. **DeployResponser.ValidateAndTransform()** processes response
5. Client is added to service in MongoDB
6. **Check client provider** → If "openstack", trigger cloud integration
7. **OpenStack API call** → Add allowed address pair for downstream IP
8. Log success/failure of OpenStack operation

## 📁 Directory Structure

```
controller/
├── cloud/
│   └── openstack/
│       ├── client.go          # OpenStack API client
│       ├── allowed_pairs.go   # Allowed address pairs management
│       ├── config.go          # Configuration structures
│       └── interface.go       # Cloud provider interface
└── client/
    └── responser/
        └── deploy.go          # Enhanced with OpenStack integration
```

## 🔌 Integration Points

### 1. Deploy Response Processing
**File:** `controller/client/responser/deploy.go`

Enhanced `ValidateAndTransform()` method:
```go
func (p *DeployResponser) ValidateAndTransform(op models.OperationClass, response *pb.CommandResponse) any {
    if !p.validateResponse(response) {
        return response
    }

    clientID := response.Identity.ClientId
    projectName := op.GetCommandProject()
    serviceName := op.GetCommandName()
    downstreamAddress := op.GetExtend().DownstreamAddress

    // Add client to service (existing logic)
    if err := p.addClientToService(clientID, downstreamAddress, serviceName, projectName); err != nil {
        p.Logger.Warnf("Error while adding client to service: %v", err)
    } else {
        p.Logger.Infof("Client ID: %s successfully added to service: %s", clientID, serviceName)
        
        // NEW: OpenStack integration
        if err := p.handleCloudIntegration(clientID, downstreamAddress, projectName); err != nil {
            p.Logger.Warnf("Cloud integration failed for client %s: %v", clientID, err)
        }
    }

    return response
}
```

### 2. Cloud Integration Handler
```go
func (p *DeployResponser) handleCloudIntegration(clientID, downstreamIP, projectName string) error {
    // Get client info to check provider
    client, err := p.Service.GetClientByClientID(context.Background(), clientID)
    if err != nil {
        return fmt.Errorf("failed to get client info: %v", err)
    }
    
    // Only process OpenStack clients
    if client.Provider != "openstack" {
        return nil // Skip for non-OpenStack providers
    }
    
    // Get OpenStack cloud configuration
    cloudConfig, err := p.getCloudConfiguration(projectName, client.Cloud)
    if err != nil {
        return fmt.Errorf("failed to get cloud config: %v", err)
    }
    
    // Create OpenStack client and add allowed address pair
    osClient := openstack.NewClient(cloudConfig)
    return osClient.AddAllowedAddressPair(client.Hostname, downstreamIP)
}
```

## 🌐 OpenStack API Integration

### 1. Configuration Structure
**File:** `controller/cloud/openstack/config.go`

```go
type OpenStackConfig struct {
    AuthURL                     string `json:"auth_url"`
    ApplicationCredentialID     string `json:"application_credential_id"`
    ApplicationCredentialSecret string `json:"application_credential_secret"`
    RegionName                  string `json:"region_name"`
    Interface                   string `json:"interface"`
    IdentityAPIVersion          int    `json:"identity_api_version"`
}

func (c *OpenStackConfig) ToGoCloudConfig() map[string]string {
    return map[string]string{
        "OS_AUTH_URL":                     c.AuthURL,
        "OS_APPLICATION_CREDENTIAL_ID":     c.ApplicationCredentialID,
        "OS_APPLICATION_CREDENTIAL_SECRET": c.ApplicationCredentialSecret,
        "OS_REGION_NAME":                   c.RegionName,
        "OS_INTERFACE":                     c.Interface,
        "OS_IDENTITY_API_VERSION":          strconv.Itoa(c.IdentityAPIVersion),
    }
}
```

### 2. OpenStack Client (REST API Based)
**File:** `controller/cloud/openstack/client.go`

```go
type Client struct {
    httpClient    *http.Client
    authToken     string
    computeURL    string
    networkURL    string
    config        *OpenStackConfig
    logger        *logger.Logger
}

func NewClient(config *OpenStackConfig) (*Client, error) {
    client := &Client{
        httpClient: &http.Client{Timeout: 30 * time.Second},
        config:     config,
        logger:     logger.NewLogger("openstack-client"),
    }
    
    // Authenticate and get service endpoints
    if err := client.authenticate(); err != nil {
        return nil, fmt.Errorf("authentication failed: %v", err)
    }
    
    return client, nil
}

func (c *Client) authenticate() error {
    authPayload := map[string]interface{}{
        "auth": map[string]interface{}{
            "identity": map[string]interface{}{
                "methods": []string{"application_credential"},
                "application_credential": map[string]interface{}{
                    "id":     c.config.ApplicationCredentialID,
                    "secret": c.config.ApplicationCredentialSecret,
                },
            },
        },
    }
    
    jsonPayload, _ := json.Marshal(authPayload)
    resp, err := c.httpClient.Post(
        c.config.AuthURL+"/v3/auth/tokens",
        "application/json",
        bytes.NewBuffer(jsonPayload),
    )
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != 201 {
        return fmt.Errorf("authentication failed with status: %d", resp.StatusCode)
    }
    
    // Extract token and service endpoints
    c.authToken = resp.Header.Get("X-Subject-Token")
    
    var authResp struct {
        Token struct {
            Catalog []struct {
                Type      string `json:"type"`
                Endpoints []struct {
                    Interface string `json:"interface"`
                    URL       string `json:"url"`
                    Region    string `json:"region"`
                } `json:"endpoints"`
            } `json:"catalog"`
        } `json:"token"`
    }
    
    if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
        return err
    }
    
    // Extract compute and network URLs
    for _, service := range authResp.Token.Catalog {
        for _, endpoint := range service.Endpoints {
            if endpoint.Interface == c.config.Interface && endpoint.Region == c.config.RegionName {
                switch service.Type {
                case "compute":
                    c.computeURL = endpoint.URL
                case "network":
                    c.networkURL = endpoint.URL
                }
            }
        }
    }
    
    return nil
}

func (c *Client) makeRequest(method, url string, payload interface{}) (*http.Response, error) {
    var body io.Reader
    if payload != nil {
        jsonData, err := json.Marshal(payload)
        if err != nil {
            return nil, err
        }
        body = bytes.NewBuffer(jsonData)
    }
    
    req, err := http.NewRequest(method, url, body)
    if err != nil {
        return nil, err
    }
    
    req.Header.Set("X-Auth-Token", c.authToken)
    req.Header.Set("Content-Type", "application/json")
    
    return c.httpClient.Do(req)
}
```

### 3. Allowed Address Pairs Management (REST API)
**File:** `controller/cloud/openstack/allowed_pairs.go`

```go
func (c *Client) AddAllowedAddressPair(hostname, ipAddress string) error {
    // 1. Find server by hostname
    serverID, err := c.findServerByHostname(hostname)
    if err != nil {
        return fmt.Errorf("failed to find server: %v", err)
    }
    
    // 2. Get server interfaces
    interfaces, err := c.getServerInterfaces(serverID)
    if err != nil {
        return fmt.Errorf("failed to get server interfaces: %v", err)
    }
    
    // 3. Add allowed address pair to primary interface
    for _, iface := range interfaces {
        if err := c.addAllowedAddressPairToPort(iface.PortID, ipAddress); err != nil {
            c.logger.Warnf("Failed to add allowed address pair to port %s: %v", iface.PortID, err)
            continue
        }
        c.logger.Infof("Successfully added allowed address pair %s to port %s", ipAddress, iface.PortID)
        return nil
    }
    
    return fmt.Errorf("failed to add allowed address pair to any interface")
}

func (c *Client) findServerByHostname(hostname string) (string, error) {
    url := fmt.Sprintf("%s/servers?name=%s", c.computeURL, hostname)
    resp, err := c.makeRequest("GET", url, nil)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != 200 {
        return "", fmt.Errorf("failed to list servers: %d", resp.StatusCode)
    }
    
    var serversResp struct {
        Servers []struct {
            ID   string `json:"id"`
            Name string `json:"name"`
        } `json:"servers"`
    }
    
    if err := json.NewDecoder(resp.Body).Decode(&serversResp); err != nil {
        return "", err
    }
    
    if len(serversResp.Servers) == 0 {
        return "", fmt.Errorf("server with hostname %s not found", hostname)
    }
    
    return serversResp.Servers[0].ID, nil
}

func (c *Client) getServerInterfaces(serverID string) ([]ServerInterface, error) {
    url := fmt.Sprintf("%s/servers/%s/os-interface", c.computeURL, serverID)
    resp, err := c.makeRequest("GET", url, nil)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != 200 {
        return nil, fmt.Errorf("failed to get server interfaces: %d", resp.StatusCode)
    }
    
    var interfacesResp struct {
        InterfaceAttachments []struct {
            PortID string `json:"port_id"`
            NetID  string `json:"net_id"`
        } `json:"interfaceAttachments"`
    }
    
    if err := json.NewDecoder(resp.Body).Decode(&interfacesResp); err != nil {
        return nil, err
    }
    
    var interfaces []ServerInterface
    for _, iface := range interfacesResp.InterfaceAttachments {
        interfaces = append(interfaces, ServerInterface{
            PortID: iface.PortID,
            NetID:  iface.NetID,
        })
    }
    
    return interfaces, nil
}

func (c *Client) addAllowedAddressPairToPort(portID, ipAddress string) error {
    // 1. Get current port info
    url := fmt.Sprintf("%s/v2.0/ports/%s", c.networkURL, portID)
    resp, err := c.makeRequest("GET", url, nil)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != 200 {
        return fmt.Errorf("failed to get port info: %d", resp.StatusCode)
    }
    
    var portResp struct {
        Port struct {
            AllowedAddressPairs []struct {
                IPAddress  string `json:"ip_address"`
                MACAddress string `json:"mac_address,omitempty"`
            } `json:"allowed_address_pairs"`
        } `json:"port"`
    }
    
    if err := json.NewDecoder(resp.Body).Decode(&portResp); err != nil {
        return err
    }
    
    // 2. Check if allowed address pair already exists
    for _, pair := range portResp.Port.AllowedAddressPairs {
        if pair.IPAddress == ipAddress {
            c.logger.Infof("Allowed address pair %s already exists on port %s", ipAddress, portID)
            return nil
        }
    }
    
    // 3. Add new allowed address pair
    newPair := struct {
        IPAddress string `json:"ip_address"`
    }{
        IPAddress: ipAddress,
    }
    
    newPairs := append(portResp.Port.AllowedAddressPairs, newPair)
    
    updatePayload := map[string]interface{}{
        "port": map[string]interface{}{
            "allowed_address_pairs": newPairs,
        },
    }
    
    // 4. Update port
    resp, err = c.makeRequest("PUT", url, updatePayload)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != 200 {
        return fmt.Errorf("failed to update port: %d", resp.StatusCode)
    }
    
    return nil
}

type ServerInterface struct {
    PortID string
    NetID  string
}
```

## 🔄 Error Handling & Logging

### Error Scenarios
1. **Client not found** → Log warning, continue
2. **Non-OpenStack client** → Skip silently
3. **Cloud config not found** → Log warning, continue
4. **OpenStack auth failure** → Log error, continue
5. **Server not found in OpenStack** → Log warning, continue
6. **Network operation failure** → Log warning, continue

### Logging Strategy
- **INFO**: Successful allowed address pair additions
- **WARN**: Non-critical failures (missing server, network errors)  
- **ERROR**: Critical configuration issues
- **DEBUG**: Detailed OpenStack API interactions

## 🧪 Testing Strategy

### Unit Tests
- OpenStack client initialization
- Allowed address pair addition/removal
- Error handling scenarios
- Configuration parsing

### Integration Tests
- End-to-end deploy flow
- OpenStack API mocking
- Different client provider scenarios

### Manual Testing
1. Deploy service to OpenStack client
2. Verify allowed address pair in OpenStack
3. Deploy to non-OpenStack client (should skip)
4. Test with invalid cloud configuration

## 🚀 Implementation Plan

### Phase 1: Core Structure
1. Create `controller/cloud/openstack/` directory
2. Implement basic OpenStack client
3. Create configuration structures
4. Add cloud provider interface

### Phase 2: Integration
1. Enhance DeployResponser
2. Add cloud configuration retrieval
3. Implement allowed address pair logic
4. Add comprehensive logging

### Phase 3: Testing & Polish
1. Unit and integration tests
2. Error handling improvements
3. Performance optimization
4. Documentation completion

### Phase 4: Future Extensions
1. Support for undeploy operations
2. Other cloud providers (AWS, Azure, GCP)
3. Advanced networking features
4. Monitoring and metrics

## 📚 Dependencies

### New Dependencies
```go
// Standard HTTP client - No external dependencies needed!
"net/http"
"encoding/json"
"bytes"
```

### Configuration Requirements
- OpenStack cloud configurations in settings collection
- Application credentials for OpenStack access
- Proper network permissions for allowed address pairs

## 🔧 Configuration Example

### Settings Collection Entry
```json
{
  "project": "project-id",
  "clouds": {
    "osp-r2-test": {
      "provider": "openstack",
      "auth": {
        "auth_url": "https://osp-r2-test.hepsiburada.com:5000",
        "application_credential_id": "f6ed87b672e0d0c222ea",
        "application_credential_secret": "secret-here"
      },
      "region_name": "osp-r2-test",
      "interface": "public",
      "identity_api_version": 3
    }
  }
}
```

This integration provides seamless OpenStack networking automation while maintaining backward compatibility and extensibility for future cloud providers.