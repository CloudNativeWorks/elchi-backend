package helper

// DiscoveryUtils provides common functionality for node filtering and IP selection
// Used by both discovery service and XDS endpoint population

// NodeInfo represents a Kubernetes node with role and address information
type NodeInfo struct {
	Status    string            `json:"status" bson:"status"`
	Addresses map[string]string `json:"addresses" bson:"addresses"`
	Roles     []string          `json:"roles" bson:"roles"`
}

// DiscoveryConfig represents the configuration for node filtering and IP selection
type DiscoveryConfig struct {
	ClusterName string   `json:"cluster_name,omitempty" bson:"cluster_name,omitempty"`
	Port        int32    `json:"port,omitempty" bson:"port,omitempty"`
	Protocol    string   `json:"protocol,omitempty" bson:"protocol,omitempty"`
	AddressType string   `json:"address_type,omitempty" bson:"address_type,omitempty"`
	Roles       []string `json:"roles,omitempty" bson:"roles,omitempty"`
}

// GetRequiredRoles returns the required roles from configuration, defaulting to ["worker"] if empty
func GetRequiredRoles(config DiscoveryConfig) []string {
	if len(config.Roles) == 0 {
		return []string{"worker"} // Default to worker nodes
	}
	return config.Roles
}

// NodeMatchesRoles checks if a node has any of the required roles
func NodeMatchesRoles(node NodeInfo, requiredRoles []string) bool {
	for _, requiredRole := range requiredRoles {
		for _, nodeRole := range node.Roles {
			if nodeRole == requiredRole {
				return true
			}
		}
	}
	return false
}

// CountMatchingNodes counts how many nodes match the required roles
func CountMatchingNodes(nodes []NodeInfo, requiredRoles []string) int {
	count := 0
	for _, node := range nodes {
		if NodeMatchesRoles(node, requiredRoles) {
			count++
		}
	}
	return count
}

// GetNodeIP extracts IP address from node based on AddressType preference
// Returns empty string if no suitable address is found
func GetNodeIP(node NodeInfo, addressType string) string {
	switch addressType {
	case "ExternalIP":
		if externalIP, exists := node.Addresses["ExternalIP"]; exists && externalIP != "" {
			return externalIP
		}
		return ""
	case "InternalIP":
		if internalIP, exists := node.Addresses["InternalIP"]; exists && internalIP != "" {
			return internalIP
		}
		return ""
	default:
		// Default behavior: prefer ExternalIP, fallback to InternalIP
		if externalIP, exists := node.Addresses["ExternalIP"]; exists && externalIP != "" {
			return externalIP
		} else if internalIP, exists := node.Addresses["InternalIP"]; exists && internalIP != "" {
			return internalIP
		}
		return ""
	}
}

// FilterAndExtractIPs filters nodes by roles and extracts IPs based on addressType
// Returns slice of IP addresses for nodes that match criteria
func FilterAndExtractIPs(nodes []NodeInfo, config DiscoveryConfig) []string {
	requiredRoles := GetRequiredRoles(config)
	var ips []string

	for _, node := range nodes {
		// Only process Ready nodes
		if node.Status != "Ready" {
			continue
		}

		// Check if node matches required roles
		if !NodeMatchesRoles(node, requiredRoles) {
			continue
		}

		// Get IP based on address type preference
		if nodeIP := GetNodeIP(node, config.AddressType); nodeIP != "" {
			ips = append(ips, nodeIP)
		}
	}

	return ips
}

// NodeFilterResult contains the result of node filtering operation
type NodeFilterResult struct {
	TotalNodes        int      `json:"total_nodes"`
	MatchingNodes     int      `json:"matching_nodes"`
	RequiredRoles     []string `json:"required_roles"`
	ExtractedIPs      []string `json:"extracted_ips"`
	FilteredNodeCount int      `json:"filtered_node_count"`
}

// FilterNodesWithResult performs complete node filtering and returns detailed results
func FilterNodesWithResult(nodes []NodeInfo, config DiscoveryConfig) NodeFilterResult {
	requiredRoles := GetRequiredRoles(config)
	totalNodes := len(nodes)
	matchingNodes := CountMatchingNodes(nodes, requiredRoles)
	extractedIPs := FilterAndExtractIPs(nodes, config)

	return NodeFilterResult{
		TotalNodes:        totalNodes,
		MatchingNodes:     matchingNodes,
		RequiredRoles:     requiredRoles,
		ExtractedIPs:      extractedIPs,
		FilteredNodeCount: len(extractedIPs),
	}
}
