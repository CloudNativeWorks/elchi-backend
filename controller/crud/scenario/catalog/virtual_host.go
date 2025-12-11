package catalog

import "github.com/CloudNativeWorks/elchi-backend/pkg/models"

// VirtualHostDefinition defines the virtual host component
var VirtualHostDefinition = models.ComponentDefinition{
	Name:          "virtual_host",
	Label:         "Virtual Host",
	Description:   "Virtual host configuration for domain-based routing",
	Category:      "envoy.config.route.v3.VirtualHost",
	Collection:    "virtual_hosts",
	CanonicalName: "config.route.v3.VirtualHost",
	GType:         "envoy.config.route.v3.VirtualHost",
	Priority:      150,
	AvailableFields: []models.AvailableField{
		{
			Name:                 "name",
			Label:                "Virtual Host Name",
			Description:          "Name for the virtual host",
			Type:                 models.FieldTypeString,
			RequiredForCreation:  true,
			RequiredForExecution: true,
			ValidationRules:      []string{"required", "unique"},
		},
		{
			Name:                 "domains",
			Label:                "Domains",
			Description:          "List of domains that this virtual host handles",
			Type:                 models.FieldTypeArray,
			RequiredForCreation:  true,
			RequiredForExecution: true,
			ValidationRules:      []string{"required", "domains"},
		},
		{
			Name:                 "routes",
			Label:                "Routes",
			Description:          "HTTP routes for this virtual host",
			Type:                 models.FieldTypeArray,
			RequiredForCreation:  true,
			RequiredForExecution: true,
			ArraySchema: &models.ArraySchema{
				ItemType: models.FieldTypeObject,
				Properties: map[string]models.FieldDef{
					"name": {
						Type:         models.FieldTypeString,
						Label:        "Route Name",
						Required:     true,
						DefaultValue: "default-route",
					},
					"match_type": {
						Type:         models.FieldTypeSelect,
						Label:        "Match Type",
						Required:     true,
						DefaultValue: "prefix",
						Options: []models.FieldOption{
							{Value: "prefix", Label: "Prefix Match"},
							{Value: "path", Label: "Exact Path Match"},
							{Value: "safe_regex", Label: "Regex Match"},
						},
					},
					"match_value": {
						Type:         models.FieldTypeString,
						Label:        "Match Value",
						Required:     true,
						DefaultValue: "/",
					},
					"route_cluster": {
						Type:        models.FieldTypeSelect,
						Label:       "Target Cluster",
						Description: "Select cluster from scenario components",
						Required:    true,
						Connected: &models.ConnectedField{
							ComponentTypes: []string{"cluster"},
							FieldName:      "name",
						},
					},
					"timeout": {
						Type:         models.FieldTypeString,
						Label:        "Route Timeout",
						Required:     false,
						DefaultValue: "15s",
					},
				},
			},
			ValidationRules: []string{"required", "routes_array"},
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
							{Value: "ADD_IF_ABSENT", Label: "Add if absent"},
							{Value: "OVERWRITE_IF_EXISTS", Label: "Overwrite if exists"},
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
							{Value: "ADD_IF_ABSENT", Label: "Add if absent"},
							{Value: "OVERWRITE_IF_EXISTS", Label: "Overwrite if exists"},
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
			Name:                 "include_request_attempt_count",
			Label:                "Include Request Attempt Count",
			Description:          "Include request attempt count in headers",
			Type:                 models.FieldTypeBool,
			RequiredForCreation:  false,
			RequiredForExecution: false,
			DefaultValue:         false,
		},
		{
			Name:                 "include_attempt_count_in_response",
			Label:                "Include Attempt Count in Response",
			Description:          "Include attempt count in response headers",
			Type:                 models.FieldTypeBool,
			RequiredForCreation:  false,
			RequiredForExecution: false,
			DefaultValue:         false,
		},
		{
			Name:                 "include_is_timeout_retry_header",
			Label:                "Include Timeout Retry Header",
			Description:          "Include timeout retry header in requests",
			Type:                 models.FieldTypeBool,
			RequiredForCreation:  false,
			RequiredForExecution: false,
			DefaultValue:         false,
		},
		{
			Name:                 "per_request_buffer_limit_bytes",
			Label:                "Per Request Buffer Limit",
			Description:          "Buffer limit per request in bytes",
			Type:                 models.FieldTypeInt,
			RequiredForCreation:  false,
			RequiredForExecution: false,
		},
	},
	Rules: models.ComponentRule{
		MinCount: 0,
		MaxCount: 50,
		RequiredWith: []string{"route"}, // Virtual Host requires a Route component with VHDS configuration
	},
}
