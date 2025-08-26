package catalog

import "github.com/CloudNativeWorks/elchi-backend/pkg/models"

// TcpProxyDefinition defines the TCP proxy component
var TcpProxyDefinition = models.ComponentDefinition{
	Name:          "tcp_proxy",
	Label:         "TCP Proxy Filter",
	Description:   "TCP proxy network filter for forwarding TCP connections",
	Category:      "envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy",
	Collection:    "filters",
	CanonicalName: "envoy.filters.network.tcp_proxy",
	GType:         "envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy",
	Priority:      600,
	AvailableFields: []models.AvailableField{
		{
			Name:            "stat_prefix",
			Label:           "Statistics Prefix",
			Description:     "Prefix for TCP proxy statistics",
			Type:            models.FieldTypeString,
			RequiredForCreation: true,
			RequiredForExecution: true,
			ValidationRules: []string{"required"},
		},
		{
			Name:            "cluster",
			Label:           "Target Cluster",
			Description:     "Name of the cluster to forward connections to - from scenario or system clusters",
			Type:            models.FieldTypeSelect,
			RequiredForCreation: true,
			RequiredForExecution: true,
			Connected: &models.ConnectedField{
				ComponentTypes: []string{"cluster"},
				FieldName:      "name",
			},
			ApiEndpoint:       "/api/v3/custom/resource_list?collection=clusters&type=clusters",
			UseApiAndScenario: true, // Allow selection from both API and scenario
			ValidationRules:   []string{"required", "cluster_exists"},
		},
		{
			Name:            "idle_timeout",
			Label:           "Idle Timeout",
			Description:     "The idle timeout for the TCP connection",
			Type:            models.FieldTypeString,
			RequiredForCreation: false,
			RequiredForExecution: false,
			DefaultValue:    "1h",
			ValidationRules: []string{"duration"},
		},
		{
			Name:            "max_connect_attempts",
			Label:           "Max Connect Attempts",
			Description:     "Maximum number of unsuccessful connection attempts",
			Type:            models.FieldTypeInt,
			RequiredForCreation: false,
			RequiredForExecution: false,
			DefaultValue:    1,
		},
		{
			Name:            "max_downstream_connection_duration",
			Label:           "Max Downstream Connection Duration",
			Description:     "Maximum duration of a downstream connection",
			Type:            models.FieldTypeString,
			RequiredForCreation: false,
			RequiredForExecution: false,
			ValidationRules: []string{"duration"},
		},
	},
	Rules: models.ComponentRule{
		ConflictWith: []string{"http_connection_manager"}, // Cannot coexist with HCM on same listener
		RequiredWith: []string{"cluster"}, // Must have a cluster
		MinCount:     0,
		MaxCount:     10,
	},
}