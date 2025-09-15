package catalog

import "github.com/CloudNativeWorks/elchi-backend/pkg/models"

// EndpointDefinition defines the endpoint component
var EndpointDefinition = models.ComponentDefinition{
	Name:          "endpoint",
	Label:         "Endpoint Configuration",
	Description:   "Service endpoint configuration for EDS clusters",
	Category:      "envoy.config.endpoint.v3.ClusterLoadAssignment",
	Collection:    "endpoints",
	CanonicalName: "config.endpoint.v3.ClusterLoadAssignment",
	GType:         "envoy.config.endpoint.v3.ClusterLoadAssignment",
	Priority:      10,
	AvailableFields: []models.AvailableField{
		{
			Name:                 "cluster_name",
			Label:                "Cluster Name",
			Description:          "Name of the cluster this endpoint belongs to",
			Type:                 models.FieldTypeString,
			RequiredForCreation:  true,
			RequiredForExecution: true,
			UseComponentName:     true,
			ValidationRules:      []string{"required"},
		},
		{
			Name:                 "endpoint_configuration",
			Label:                "Endpoint Configuration",
			Description:          "Choose between static endpoints or Kubernetes discovery",
			Type:                 models.FieldTypeNestedChoice,
			RequiredForCreation:  true,
			RequiredForExecution: true,
			NestedConfig: &models.NestedFieldConfig{
				Choices: []models.ConditionalChoice{
					{
						Value: "static",
						Label: "Static Endpoints",
						SubFields: []models.AvailableField{
							{
								Name:                 "lb_endpoints",
								Label:                "Load Balancing Endpoints",
								Description:          "List of load balancing endpoints with socket addresses",
								Type:                 models.FieldTypeArray,
								RequiredForCreation:  true,
								RequiredForExecution: true,
								ArraySchema: &models.ArraySchema{
									ItemType: models.FieldTypeObject,
									Properties: map[string]models.FieldDef{
										"address": {
											Type:        models.FieldTypeString,
											Label:       "Address",
											Description: "IP address or hostname",
											Required:    true,
										},
										"port": {
											Type:        models.FieldTypeInt,
											Label:       "Port",
											Description: "Port number",
											Required:    true,
										},
										"protocol": {
											Type:         models.FieldTypeSelect,
											Label:        "Protocol",
											Description:  "Network protocol",
											Required:     true,
											DefaultValue: "TCP",
											Options: []models.FieldOption{
												{Value: "TCP", Label: "TCP"},
												{Value: "UDP", Label: "UDP"},
											},
										},
									},
								},
								ValidationRules: []string{"required", "min_length_execution:1"},
							},
						},
					},
					{
						Value: "discovery",
						Label: "Elchi Discovery",
						SubFields: []models.AvailableField{
							{
								Name:                 "cluster_name",
								Label:                "Discovery Cluster",
								Description:          "Select Kubernetes cluster for endpoint discovery",
								Type:                 models.FieldTypeSelect,
								RequiredForCreation:  true,
								RequiredForExecution: true,
								ApiEndpoint:          "/api/discovery/clusters",
								HasDiscovery:         true,
								ValidationRules:      []string{"required"},
							},
							{
								Name:                 "port",
								Label:                "Service Port",
								Description:          "Port number for discovered endpoints",
								Type:                 models.FieldTypeInt,
								RequiredForCreation:  true,
								RequiredForExecution: true,
								DefaultValue:         80,
								ValidationRules:      []string{"required", "min:1", "max:65535"},
							},
							{
								Name:                 "protocol",
								Label:                "Service Protocol",
								Description:          "Protocol for discovered endpoints",
								Type:                 models.FieldTypeSelect,
								RequiredForCreation:  true,
								RequiredForExecution: true,
								DefaultValue:         "TCP",
								Options: []models.FieldOption{
									{Value: "TCP", Label: "TCP"},
									{Value: "UDP", Label: "UDP"},
								},
								ValidationRules: []string{"required"},
							},
							{
								Name:                 "address_type",
								Label:                "Node Address Type",
								Description:          "Which node IP address to use for endpoints",
								Type:                 models.FieldTypeSelect,
								RequiredForCreation:  true,
								RequiredForExecution: true,
								DefaultValue:         "ExternalIP",
								Options: []models.FieldOption{
									{Value: "ExternalIP", Label: "External IP (Public)"},
									{Value: "InternalIP", Label: "Internal IP (Private)"},
								},
								ValidationRules: []string{},
							},
							{
								Name:                 "roles",
								Label:                "Node Roles",
								Description:          "Which node roles to include in endpoints",
								Type:                 models.FieldTypeSelect,
								RequiredForCreation:  true,
								RequiredForExecution: true,
								DefaultValue:         "worker",
								Options: []models.FieldOption{
									{Value: "worker", Label: "Worker Nodes Only"},
									{Value: "master", Label: "Master Nodes Only"},
									{Value: "all", Label: "All Nodes (Master + Worker)"},
								},
								ValidationRules: []string{},
							},
						},
					},
				},
				DefaultChoice:     "static",
				MutuallyExclusive: true,
			},
			ValidationRules: []string{"required"},
		},
	},
	Rules: models.ComponentRule{
		RequiredWith: []string{"cluster"},
		MinCount:     0,
		MaxCount:     200,
	},
}
