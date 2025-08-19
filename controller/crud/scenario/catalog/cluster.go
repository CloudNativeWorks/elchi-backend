package catalog

import "github.com/CloudNativeWorks/elchi-backend/pkg/models"

// ClusterDefinition defines the cluster component
var ClusterDefinition = models.ComponentDefinition{
	Name:          "cluster",
	Label:         "Cluster Configuration",
	Description:   "Upstream cluster configuration for backend services",
	Category:      "envoy.config.cluster.v3.Cluster",
	Collection:    "clusters",
	CanonicalName: "config.cluster.v3.Cluster",
	GType:         "envoy.config.cluster.v3.Cluster",
	Priority:      100,
	AvailableFields: []models.AvailableField{
		{
			Name:                 "name",
			Label:                "Cluster Name",
			Description:          "Unique name for the cluster",
			Type:                 models.FieldTypeString,
			RequiredForCreation:  true, // Name must be selected when creating scenario
			RequiredForExecution: true, // Name must have value when executing
			UseComponentName:     true,
			ValidationRules:      []string{"required", "unique"},
		},
		{
			Name:                 "connect_timeout",
			Label:                "Connection Timeout",
			Description:          "Timeout for new network connections in seconds (e.g., 2s, 1.5s, 0.5s)",
			Type:                 models.FieldTypeString,
			RequiredForCreation:  false, // Optional to select
			RequiredForExecution: true,  // If selected, must have value
			DefaultValue:         "2s",
			ValidationRules:      []string{"duration"},
		},
		{
			Name:                 "type",
			Label:                "Discovery Type",
			Description:          "Service discovery type for the cluster",
			Type:                 models.FieldTypeSelect,
			RequiredForCreation:  true, // Type must be selected when creating scenario
			RequiredForExecution: true, // Type must have value when executing
			DefaultValue:         "EDS",
			Options: []models.FieldOption{
				{Value: "STATIC", Label: "Static - Manual endpoint configuration"},
				{Value: "STRICT_DNS", Label: "Strict DNS - DNS resolution"},
				{Value: "LOGICAL_DNS", Label: "Logical DNS - DNS with load balancing"},
				{Value: "EDS", Label: "EDS - Endpoint Discovery Service"},
			},
			ValidationRules: []string{"required", "cluster_type_endpoint_consistency"},
		},
		{
			Name:                 "lb_policy",
			Label:                "Load Balancing Policy",
			Description:          "Load balancing algorithm to use",
			Type:                 models.FieldTypeSelect,
			RequiredForCreation:  false, // Optional to select
			RequiredForExecution: true,  // If selected, must have value
			DefaultValue:         "LEAST_REQUEST",
			Options: []models.FieldOption{
				{Value: "ROUND_ROBIN", Label: "Round Robin"},
				{Value: "LEAST_REQUEST", Label: "Least Request"},
				{Value: "RANDOM", Label: "Random"},
			},
		},
		{
			Name:                 "alt_stat_name",
			Label:                "Alternative Stat Name",
			Description:          "Alternative name for stats collection",
			Type:                 models.FieldTypeString,
			RequiredForCreation:  false,
			RequiredForExecution: false,
		},
		{
			Name:                 "per_connection_buffer_limit_bytes",
			Label:                "Per Connection Buffer Limit",
			Description:          "Soft limit on size of the cluster's new connection read and write buffers",
			Type:                 models.FieldTypeInt,
			RequiredForCreation:  false,
			RequiredForExecution: false,
		},
		{
			Name:                 "wait_for_warm_on_init",
			Label:                "Wait for Warm on Init",
			Description:          "If true, upstream hosts are considered not healthy until initial health check completes",
			Type:                 models.FieldTypeBool,
			RequiredForCreation:  false,
			RequiredForExecution: false,
			DefaultValue:         false,
		},
		{
			Name:                 "cleanup_interval",
			Label:                "Cleanup Interval",
			Description:          "The interval for cleanup of unused hosts in seconds (e.g., 30s, 60s, 1.5s)",
			Type:                 models.FieldTypeString,
			RequiredForCreation:  false,
			RequiredForExecution: false,
			ValidationRules:      []string{"duration"},
			DefaultValue:         "30s",
		},
		{
			Name:                 "close_connections_on_host_health_failure",
			Label:                "Close Connections on Host Health Failure",
			Description:          "Close connections to upstream hosts when they become unhealthy",
			Type:                 models.FieldTypeBool,
			RequiredForCreation:  false,
			RequiredForExecution: false,
			DefaultValue:         false,
		},
		{
			Name:                 "ignore_health_on_host_removal",
			Label:                "Ignore Health on Host Removal",
			Description:          "Ignore health status when removing hosts from load balancing",
			Type:                 models.FieldTypeBool,
			RequiredForCreation:  false,
			RequiredForExecution: false,
			DefaultValue:         false,
		},
		{
			Name:                 "connection_pool_per_downstream_connection",
			Label:                "Connection Pool per Downstream Connection",
			Description:          "Create separate connection pool for each downstream connection",
			Type:                 models.FieldTypeBool,
			RequiredForCreation:  false,
			RequiredForExecution: false,
			DefaultValue:         false,
		},
		{
			Name:                 "health_check",
			Label:                "Health Check",
			Description:          "Configure health check for the cluster",
			Type:                 models.FieldTypeObject,
			RequiredForCreation:  false,
			RequiredForExecution: false,
			ArraySchema: &models.ArraySchema{
				ItemType: models.FieldTypeObject,
				Properties: map[string]models.FieldDef{
					"timeout": {
						Type:            models.FieldTypeString,
						Label:           "Timeout",
						Description:     "Health check timeout in seconds (e.g., 1s, 2s, 0.5s)",
						Required:        true,
						DefaultValue:    "2s",
						ValidationRules: []string{"duration"},
					},
					"interval": {
						Type:            models.FieldTypeString,
						Label:           "Interval",
						Description:     "Health check interval in seconds (e.g., 5s, 10s, 1.5s)",
						Required:        true,
						DefaultValue:    "3s",
						ValidationRules: []string{"duration"},
					},
					"unhealthy_threshold": {
						Type:         models.FieldTypeInt,
						Label:        "Unhealthy Threshold",
						Description:  "Number of unhealthy checks before marking unhealthy",
						Required:     true,
						DefaultValue: 2,
					},
					"healthy_threshold": {
						Type:         models.FieldTypeInt,
						Label:        "Healthy Threshold",
						Description:  "Number of healthy checks before marking healthy",
						Required:     true,
						DefaultValue: 1,
					},
					"type": {
						Type:         models.FieldTypeConditional,
						Label:        "Health Check Type",
						Description:  "Type of health check to perform",
						Required:     true,
						DefaultValue: "tcp",
						Options: []models.FieldOption{
							{Value: "tcp", Label: "TCP Health Check"},
							{Value: "http", Label: "HTTP Health Check"},
						},
						Properties: map[string]models.FieldDef{
							"http": {
								Type: models.FieldTypeObject,
								Properties: map[string]models.FieldDef{
									"path": {
										Type:         models.FieldTypeString,
										Label:        "HTTP Path",
										Description:  "HTTP path for health check",
										Required:     true,
										DefaultValue: "/",
									},
									"host": {
										Type:        models.FieldTypeString,
										Label:       "HTTP Host",
										Description: "HTTP host header for health check",
										Required:    false,
									},
								},
							},
						},
					},
				},
			},
		},
		{
			Name:                 "endpoint_discovery_config",
			Label:                "Endpoint Discovery Configuration",
			Description:          "Choose between EDS (External Discovery Service) or static endpoints",
			Type:                 models.FieldTypeNestedChoice,
			RequiredForCreation:  true, // Must choose one of the options
			RequiredForExecution: true,
			NestedConfig: &models.NestedFieldConfig{
				MutuallyExclusive: true,
				DefaultChoice:     "static_endpoints",
				Choices: []models.ConditionalChoice{
					{
						Value: "eds",
						Label: "EDS (External Discovery Service)",
						SubFields: []models.AvailableField{
							{
								Name:                 "eds_service_name",
								Label:                "EDS Service Name",
								Description:          "Service name for EDS clusters",
								Type:                 models.FieldTypeString,
								RequiredForCreation:  true,
								RequiredForExecution: true,
								Connected: &models.ConnectedField{
									ComponentTypes: []string{"endpoint"},
									FieldName:      "cluster_name",
								},
								UseApiAndScenario: true,
								ApiEndpoint:       "/api/v3/custom/resource_list?collection=endpoints&type=endpoints",
							},
						},
					},
					{
						Value: "static_endpoints",
						Label: "Static Endpoints",
						SubFields: []models.AvailableField{
							{
								Name:                 "endpoints",
								Label:                "Endpoint List",
								Description:          "List of static endpoints",
								Type:                 models.FieldTypeArray,
								RequiredForCreation:  true,
								RequiredForExecution: true,
								ValidationRules:      []string{"array_of_endpoints", "min_length_execution:1"},
								ArraySchema: &models.ArraySchema{
									ItemType: models.FieldTypeObject,
									Properties: map[string]models.FieldDef{
										"address": {
											Type:     models.FieldTypeString,
											Label:    "Address",
											Required: true,
										},
										"port": {
											Type:     models.FieldTypeInt,
											Label:    "Port",
											Required: true,
										},
										"protocol": {
											Type:         models.FieldTypeSelect,
											Label:        "Protocol",
											Required:     true,
											DefaultValue: "TCP",
											Options: []models.FieldOption{
												{Value: "TCP", Label: "TCP"},
												{Value: "UDP", Label: "UDP"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	},
	Rules: models.ComponentRule{
		MinCount: 0,
		MaxCount: 100,
		// No field conflicts needed - choice structure handles mutual exclusivity
		ValidationRulesForCreation:  []string{},                                // No component-level validation during creation
		ValidationRulesForExecution: []string{"cluster_endpoint_relationship"}, // Validate cluster-endpoint compatibility during execution
	},
}
