package catalog

import "github.com/CloudNativeWorks/elchi-backend/pkg/models"

// RouteDefinition defines the route component
var RouteDefinition = models.ComponentDefinition{
	Name:          "route",
	Label:         "Route Configuration",
	Description:   "HTTP route configuration for request routing",
	Category:      "envoy.config.route.v3.RouteConfiguration",
	Collection:    "routes",
	CanonicalName: "config.route.v3.RouteConfiguration",
	GType:         "envoy.config.route.v3.RouteConfiguration",
	Priority:      200,
	AvailableFields: []models.AvailableField{
		{
			Name:                 "name",
			Label:                "Route Name",
			Description:          "Unique name for the route configuration",
			Type:                 models.FieldTypeString,
			RequiredForCreation:  true,
			RequiredForExecution: true,
			UseComponentName:     true,
			ValidationRules:      []string{"required", "unique"},
		},
		{
			Name:                 "virtual_host_config",
			Label:                "Virtual Host Configuration",
			Description:          "Choose how to configure virtual hosts",
			Type:                 models.FieldTypeNestedChoice,
			RequiredForCreation:  true,
			RequiredForExecution: true,
			NestedConfig: &models.NestedFieldConfig{
				MutuallyExclusive: true,
				DefaultChoice:     "vhds",
				Choices: []models.ConditionalChoice{
					{
						Value: "vhds",
						Label: "VHDS (Virtual Host Discovery Service)",
						SubFields: []models.AvailableField{
							{
								Name:                 "vhds_config",
								Label:                "VHDS Configuration",
								Description:          "Virtual Host Discovery Service configuration",
								Type:                 models.FieldTypeSelect,
								RequiredForCreation:  true,
								RequiredForExecution: true,
								ApiEndpoint:          "/api/v3/custom/resource_list?collection=virtual_hosts&type=virtual_hosts",
								HasMetadata:          true,
								Connected: &models.ConnectedField{
									ComponentTypes: []string{"virtual_host"},
									FieldName:      ":componentname:",
								},
								ValidationRules: []string{"required"},
							},
						},
					},
					{
						Value: "inline_virtual_hosts",
						Label: "Inline Virtual Hosts",
						SubFields: []models.AvailableField{
							{
								Name:                 "virtual_hosts",
								Label:                "Virtual Hosts List",
								Description:          "Define virtual hosts inline",
								Type:                 models.FieldTypeArray,
								RequiredForCreation:  true,
								RequiredForExecution: true,
								ArraySchema: &models.ArraySchema{
									ItemType: models.FieldTypeObject,
									Properties: map[string]models.FieldDef{
										"name": {
											Type:     models.FieldTypeString,
											Label:    "Virtual Host Name",
											Required: true,
										},
										"domains": {
											Type:         models.FieldTypeArray,
											Label:        "Domains",
											Required:     true,
											DefaultValue: []string{"*"},
										},
										"routes": {
											Type:     models.FieldTypeArray,
											Label:    "Routes",
											Required: true,
											Properties: map[string]models.FieldDef{
												"name": {
													Type:     models.FieldTypeString,
													Label:    "Route Name",
													Required: false,
												},
												"match": {
													Type:     models.FieldTypeObject,
													Label:    "Route Match",
													Required: true,
													Properties: map[string]models.FieldDef{
														"prefix": {
															Type:         models.FieldTypeString,
															Label:        "Prefix Match",
															Required:     false,
															DefaultValue: "/",
														},
														"case_sensitive": {
															Type:         models.FieldTypeBool,
															Label:        "Case Sensitive",
															Required:     false,
															DefaultValue: false,
														},
													},
												},
												"route": {
													Type:     models.FieldTypeObject,
													Label:    "Route Action",
													Required: true,
													Properties: map[string]models.FieldDef{
														"cluster": {
															Type:        models.FieldTypeString,
															Label:       "Target Cluster",
															Description: "Select cluster from scenario components",
															Required:    true,
															Connected: &models.ConnectedField{
																ComponentTypes: []string{"cluster"},
																FieldName:      "name",
															},
														},
													},
												},
											},
										},
									},
								},
								ValidationRules: []string{"required", "virtual_hosts_array"},
							},
						},
					},
				},
			},
		},
		{
			Name:                 "internal_only_headers",
			Label:                "Internal Only Headers",
			Description:          "Headers that should not be forwarded to upstream",
			Type:                 models.FieldTypeArray,
			RequiredForCreation:  false,
			RequiredForExecution: false,
			ArraySchema: &models.ArraySchema{
				ItemType: models.FieldTypeString,
			},
		},
		{
			Name:                 "response_headers_to_add",
			Label:                "Response Headers to Add",
			Description:          "Headers to add to HTTP responses",
			Type:                 models.FieldTypeArray,
			RequiredForCreation:  false,
			RequiredForExecution: false,
			ArraySchema: &models.ArraySchema{
				ItemType: models.FieldTypeObject,
				Properties: map[string]models.FieldDef{
					"key": {
						Type:     models.FieldTypeString,
						Label:    "Header Key",
						Required: true,
					},
					"value": {
						Type:     models.FieldTypeString,
						Label:    "Header Value",
						Required: true,
					},
					"append_action": {
						Type:         models.FieldTypeSelect,
						Label:        "Append Action",
						Required:     false,
						DefaultValue: "APPEND_IF_EXISTS_OR_ADD",
						Options: []models.FieldOption{
							{Value: "APPEND_IF_EXISTS_OR_ADD", Label: "Append if exists or add"},
							{Value: "OVERWRITE_IF_EXISTS_OR_ADD", Label: "Overwrite if exists or add"},
						},
					},
					"keep_empty_value": {
						Type:         models.FieldTypeBool,
						Label:        "Keep Empty Value",
						Required:     false,
						DefaultValue: false,
					},
				},
			},
		},
		{
			Name:                 "response_headers_to_remove",
			Label:                "Response Headers to Remove",
			Description:          "Headers to remove from HTTP responses",
			Type:                 models.FieldTypeArray,
			RequiredForCreation:  false,
			RequiredForExecution: false,
			ArraySchema: &models.ArraySchema{
				ItemType: models.FieldTypeString,
			},
		},
		{
			Name:                 "request_headers_to_add",
			Label:                "Request Headers to Add",
			Description:          "Headers to add to HTTP requests",
			Type:                 models.FieldTypeArray,
			RequiredForCreation:  false,
			RequiredForExecution: false,
			ArraySchema: &models.ArraySchema{
				ItemType: models.FieldTypeObject,
				Properties: map[string]models.FieldDef{
					"key": {
						Type:     models.FieldTypeString,
						Label:    "Header Key",
						Required: true,
					},
					"value": {
						Type:     models.FieldTypeString,
						Label:    "Header Value",
						Required: true,
					},
					"append_action": {
						Type:         models.FieldTypeSelect,
						Label:        "Append Action",
						Required:     false,
						DefaultValue: "APPEND_IF_EXISTS_OR_ADD",
						Options: []models.FieldOption{
							{Value: "APPEND_IF_EXISTS_OR_ADD", Label: "Append if exists or add"},
							{Value: "OVERWRITE_IF_EXISTS_OR_ADD", Label: "Overwrite if exists or add"},
						},
					},
					"keep_empty_value": {
						Type:         models.FieldTypeBool,
						Label:        "Keep Empty Value",
						Required:     false,
						DefaultValue: false,
					},
				},
			},
		},
		{
			Name:                 "request_headers_to_remove",
			Label:                "Request Headers to Remove",
			Description:          "Headers to remove from HTTP requests",
			Type:                 models.FieldTypeArray,
			RequiredForCreation:  false,
			RequiredForExecution: false,
			ArraySchema: &models.ArraySchema{
				ItemType: models.FieldTypeString,
			},
		},
		{
			Name:                 "most_specific_header_mutations_wins",
			Label:                "Most Specific Header Mutations Wins",
			Description:          "Whether most specific header mutations win",
			Type:                 models.FieldTypeBool,
			RequiredForCreation:  false,
			RequiredForExecution: false,
			DefaultValue:         false,
		},
		{
			Name:                 "validate_clusters",
			Label:                "Validate Clusters",
			Description:          "Validate that referenced clusters exist",
			Type:                 models.FieldTypeBool,
			RequiredForCreation:  false,
			RequiredForExecution: false,
			DefaultValue:         true,
		},
		{
			Name:                 "max_direct_response_body_size_bytes",
			Label:                "Max Direct Response Body Size",
			Description:          "Maximum size of direct response body",
			Type:                 models.FieldTypeInt,
			RequiredForCreation:  false,
			RequiredForExecution: false,
		},
		{
			Name:                 "ignore_port_in_host_matching",
			Label:                "Ignore Port in Host Matching",
			Description:          "Ignore port when matching host header",
			Type:                 models.FieldTypeBool,
			RequiredForCreation:  false,
			RequiredForExecution: false,
			DefaultValue:         false,
		},
		{
			Name:                 "ignore_path_parameters_in_path_matching",
			Label:                "Ignore Path Parameters in Path Matching",
			Description:          "Ignore path parameters when matching paths",
			Type:                 models.FieldTypeBool,
			RequiredForCreation:  false,
			RequiredForExecution: false,
			DefaultValue:         false,
		},
	},
	Rules: models.ComponentRule{},
}
