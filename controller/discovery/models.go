package discovery

import "time"

// K8sDiscoveryRequest represents incoming discovery data from K8s agents
type K8sDiscoveryRequest struct {
	Project string           `json:"project"`
	Data    K8sDiscoveryData `json:"data"`
}

// K8sDiscoveryData represents the actual discovery data
type K8sDiscoveryData struct {
	Timestamp         time.Time   `json:"timestamp"`
	ClusterInfo       ClusterInfo `json:"cluster_info"`
	NodeCount         int         `json:"node_count"`
	Nodes             []NodeInfo  `json:"nodes"`
	DiscoveryDuration string      `json:"discovery_duration"`
	Initial           bool        `json:"initial,omitempty"`
}

type ClusterInfo struct {
	ClusterName    string `json:"cluster_name"`
	ClusterVersion string `json:"cluster_version"`
}

type NodeInfo struct {
	Name      string            `json:"name"`
	Status    string            `json:"status"`
	Version   string            `json:"version"`
	Addresses map[string]string `json:"addresses"`
	Roles     []string          `json:"roles,omitempty"` // ["master", "control-plane", "worker"] etc.
}

// DiscoveryToken represents authentication token for discovery agents
type DiscoveryToken struct {
	Project     string    `json:"project" bson:"project"`
	Token       string    `json:"token" bson:"token"`
	Description string    `json:"description,omitempty" bson:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" bson:"updated_at"`
	Enabled     bool      `json:"enabled" bson:"enabled"`
}

// EndpointUpdateResult represents the result of endpoint updates
type EndpointUpdateResult struct {
	EndpointName string   `json:"endpoint_name"`
	ClusterName  string   `json:"cluster_name"`
	UpdatedIPs   []string `json:"updated_ips"`
	AddedNodes   int      `json:"added_nodes"`
	RemovedNodes int      `json:"removed_nodes"`
	Changed      bool     `json:"changed"`
}

// DiscoveryProcessResult represents the overall processing result
type DiscoveryProcessResult struct {
	ProcessedAt    time.Time              `json:"processed_at"`
	ClusterName    string                 `json:"cluster_name"`
	NodesReceived  int                    `json:"nodes_received"`
	EndpointsFound int                    `json:"endpoints_found"`
	Updates        []EndpointUpdateResult `json:"updates"`
	Success        bool                   `json:"success"`
	Message        string                 `json:"message,omitempty"`
}

// ClusterDiscovery represents cluster registration in discovery collection
type ClusterDiscovery struct {
	ID                string     `json:"id" bson:"_id,omitempty"`
	ClusterName       string     `json:"cluster_name" bson:"cluster_name"`
	Project           string     `json:"project" bson:"project"`
	Nodes             []NodeInfo `json:"nodes" bson:"nodes"`
	LastSeen          time.Time  `json:"last_seen" bson:"last_seen"`
	CreatedAt         time.Time  `json:"created_at" bson:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at" bson:"updated_at"`
	NodeCount         int        `json:"node_count" bson:"node_count"`
	ClusterVersion    string     `json:"cluster_version" bson:"cluster_version"`
	DiscoveryDuration string     `json:"discovery_duration" bson:"discovery_duration"`
	LastRequestTime   time.Time  `json:"last_request_time" bson:"last_request_time"`
}

// ClusterUsageInfo represents endpoint usage information for a specific cluster
type ClusterUsageInfo struct {
	EndpointName string    `json:"endpoint_name"`
	ResourceID   string    `json:"resource_id"`
	Version      string    `json:"version"`
	Project      string    `json:"project"`
	UpdatedAt    time.Time `json:"updated_at"`
	IPCount      int       `json:"ip_count"`
	IPs          []string  `json:"ips"`
}
