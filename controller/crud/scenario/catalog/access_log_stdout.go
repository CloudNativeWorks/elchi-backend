package catalog

import "github.com/CloudNativeWorks/elchi-backend/pkg/models"

// StdoutAccessLogDefinition defines the stdout access log component
var StdoutAccessLogDefinition = models.ComponentDefinition{
	Name:          "access_log_stdout",
	Label:         "Stdout Access Log",
	Description:   "Access logger that writes logs to stdout",
	Category:      "envoy.access_loggers",
	Collection:    "extensions",
	CanonicalName: "envoy.access_loggers.stdout",
	GType:         "envoy.extensions.access_loggers.stream.v3.StdoutAccessLog",
	Priority:      410,
	AvailableFields: []models.AvailableField{
		{
			Name:                 "name",
			Label:                "Access Log Name",
			Description:          "Unique name for the access log configuration",
			Type:                 models.FieldTypeString,
			RequiredForCreation:  true,
			RequiredForExecution: true,
			UseComponentName:     true,
			ValidationRules:      []string{"required", "unique"},
		},
		{
			Name:                 "log_format",
			Label:                "Log Format Configuration",
			Description:          "Choose between text or JSON log format",
			Type:                 models.FieldTypeNestedChoice,
			RequiredForCreation:  false,
			RequiredForExecution: false,
			NestedConfig: &models.NestedFieldConfig{
				MutuallyExclusive: true,
				DefaultChoice:     "text_format",
				Choices: []models.ConditionalChoice{
					{
						Value: "text_format",
						Label: "Text Format",
						SubFields: []models.AvailableField{
							{
								Name:                 "text_format",
								Label:                "Format String",
								Description:          "Log format string with Envoy command operators",
								Type:                 models.FieldTypeString,
								RequiredForCreation:  false,
								RequiredForExecution: false,
								DefaultValue:         `[%START_TIME%] "%REQ(:METHOD)% %REQ(X-ENVOY-ORIGINAL-PATH?:PATH)% %PROTOCOL%" %RESPONSE_CODE% %RESPONSE_FLAGS% %BYTES_RECEIVED% %BYTES_SENT% %DURATION% %RESP(X-ENVOY-UPSTREAM-SERVICE-TIME)% "%REQ(X-FORWARDED-FOR)%" "%REQ(USER-AGENT)%" "%REQ(X-REQUEST-ID)%" "%REQ(:AUTHORITY)%" "%UPSTREAM_HOST%"\n`,
							},
						},
					},
					{
						Value: "json_format",
						Label: "JSON Format",
						SubFields: []models.AvailableField{
							{
								Name:                 "json_format",
								Label:                "JSON Fields",
								Description:          "Key-value pairs for JSON log format",
								Type:                 models.FieldTypeObject,
								RequiredForCreation:  false,
								RequiredForExecution: false,
								DefaultValue: map[string]string{
									"protocol":                  "%PROTOCOL%",
									"duration":                  "%DURATION%",
									"request_method":            "%REQ(:METHOD)%",
									"request_path":              "%REQ(X-ENVOY-ORIGINAL-PATH?:PATH)%",
									"response_code":             "%RESPONSE_CODE%",
									"response_flags":            "%RESPONSE_FLAGS%",
									"bytes_received":            "%BYTES_RECEIVED%",
									"bytes_sent":                "%BYTES_SENT%",
									"upstream_host":             "%UPSTREAM_HOST%",
									"upstream_cluster":          "%UPSTREAM_CLUSTER%",
									"user_agent":                "%REQ(USER-AGENT)%",
									"downstream_remote_address": "%DOWNSTREAM_REMOTE_ADDRESS%",
									"x_forwarded_for":           "%REQ(X-FORWARDED-FOR)%",
									"request_id":                "%REQ(X-REQUEST-ID)%",
									"start_time":                "%START_TIME%",
								},
							},
						},
					},
				},
			},
		},
	},
	Rules: models.ComponentRule{
		ConflictWith: []string{"access_log_file", "access_log_fluentd"}, // Only one access log type allowed per scenario
		MinCount:     0,
		MaxCount:     10,
	},
}