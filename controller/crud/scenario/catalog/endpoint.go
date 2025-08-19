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
			Name:             "cluster_name",
			Label:            "Cluster Name",
			Description:      "Name of the cluster this endpoint belongs to",
			Type:             models.FieldTypeString,
			RequiredForCreation:  true,
			RequiredForExecution: true,
			UseComponentName: true,
			ValidationRules:  []string{"required"},
		},
		{
			Name:            "lb_endpoints",
			Label:           "Load Balancing Endpoints",
			Description:     "List of load balancing endpoints with socket addresses",
			Type:            models.FieldTypeArray,
			RequiredForCreation: true,
			RequiredForExecution: true,
			ArraySchema: &models.ArraySchema{
				ItemType: models.FieldTypeObject,
				Properties: map[string]models.FieldDef{
					"address": {
						Type:     models.FieldTypeString,
						Label:    "Address",
						Description: "IP address or hostname",
						Required: true,
					},
					"port": {
						Type:     models.FieldTypeInt,
						Label:    "Port",
						Description: "Port number",
						Required: true,
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
	Rules: models.ComponentRule{
		RequiredWith: []string{"cluster"},
		MinCount:     0,
		MaxCount:     200,
	},
}