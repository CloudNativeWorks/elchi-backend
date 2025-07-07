package models

type ArrayPath struct {
	ParentPath string
	IndexPath  string
}

type TypedConfigPath struct {
	ArrayPaths       []ArrayPath
	PathTemplate     string
	Kind             string
	IsPerTypedConfig bool
}

var (
	accessLog            = "access_log.%d"
	accessLogTypedConfig = "access_log.%d.typed_config"
	routes               = "routes.%d"
)

var BootstrapTypedConfigPaths = []TypedConfigPath{
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "admin.access_log", IndexPath: "admin.access_log.%d"},
		},
		PathTemplate: "admin.access_log.%d.typed_config",
		Kind:         "access_log",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "stats_sinks", IndexPath: "stats_sinks.%d"},
		},
		PathTemplate: "stats_sinks.%d.typed_config",
		Kind:         "stats_sinks",
	},
}

var ListenerTypedConfigPaths = []TypedConfigPath{
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "filter_chains", IndexPath: "filter_chains.%d"},
		},
		PathTemplate: "filter_chains.%d.transport_socket.typed_config",
		Kind:         "downstream_tls",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "access_log", IndexPath: accessLog},
		},
		PathTemplate: accessLogTypedConfig,
		Kind:         "access_log",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "filter_chains", IndexPath: "filter_chains.%d"},
			{ParentPath: "filter_chains.%d.filters", IndexPath: "filters.%d"},
		},
		PathTemplate: "filter_chains.%d.filters.%d.typed_config",
		Kind:         "network_filter",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "listener_filters", IndexPath: "listener_filters.%d"},
		},
		PathTemplate: "listener_filters.%d.typed_config",
		Kind:         "network_filter",
	},
}

var GeneralAccessLogTypedConfigPaths = []TypedConfigPath{
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "access_log", IndexPath: accessLog},
		},
		PathTemplate: accessLogTypedConfig,
		Kind:         "access_log",
	},
}

var ClusterTypedConfigPaths = []TypedConfigPath{
	{
		ArrayPaths:   []ArrayPath{},
		PathTemplate: "transport_socket.typed_config",
		Kind:         "upstream_tls",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "health_checks", IndexPath: "health_checks.%d"},
			{ParentPath: "health_checks.%d.event_logger", IndexPath: "event_logger.%d"},
		},
		PathTemplate: "health_checks.%d.event_logger.%d.typed_config",
		Kind:         "hcefs",
	},
	{
		ArrayPaths:       []ArrayPath{},
		PathTemplate:     "typed_extension_protocol_options",
		Kind:             "http_protocol",
		IsPerTypedConfig: true,
	},
}

var RouteTypedConfigPaths = []TypedConfigPath{
	{
		ArrayPaths:       []ArrayPath{},
		PathTemplate:     "typed_per_filter_config",
		Kind:             "route",
		IsPerTypedConfig: true,
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "virtual_hosts", IndexPath: "virtual_hosts.%d"},
		},
		PathTemplate:     "virtual_hosts.%d.typed_per_filter_config",
		Kind:             "route",
		IsPerTypedConfig: true,
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "virtual_hosts", IndexPath: "virtual_hosts.%d"},
			{ParentPath: "virtual_hosts.%d.routes", IndexPath: routes},
		},
		PathTemplate:     "virtual_hosts.%d.routes.%d.typed_per_filter_config",
		Kind:             "route",
		IsPerTypedConfig: true,
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "virtual_hosts", IndexPath: "virtual_hosts.%d"},
			{ParentPath: "virtual_hosts.%d.routes", IndexPath: routes},
		},
		PathTemplate: "virtual_hosts.%d.routes.%d.match.path_match_policy.typed_config",
		Kind:         "route",
	},
}

var VirtualHostTypedConfigPaths = []TypedConfigPath{
	{
		ArrayPaths:       []ArrayPath{},
		PathTemplate:     "typed_per_filter_config",
		Kind:             "virtual_hosts",
		IsPerTypedConfig: true,
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "routes", IndexPath: routes},
		},
		PathTemplate:     "routes.%d.typed_per_filter_config",
		Kind:             "virtual_hosts",
		IsPerTypedConfig: true,
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "routes", IndexPath: "routes.%d"},
		},
		PathTemplate: "routes.%d.match.path_match_policy.typed_config",
		Kind:         "virtual_hosts",
	},
}

var HTTPConnectionManagerTypedConfigPaths = []TypedConfigPath{
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "access_log", IndexPath: accessLog},
		},
		PathTemplate: accessLogTypedConfig,
		Kind:         "access_log",
	},
	{
		ArrayPaths:       []ArrayPath{},
		PathTemplate:     "route_config.typed_per_filter_config",
		Kind:             "hcm",
		IsPerTypedConfig: true,
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "route_config.virtual_hosts", IndexPath: "route_config.virtual_hosts.%d"},
		},
		PathTemplate:     "route_config.virtual_hosts.%d.typed_per_filter_config",
		Kind:             "hcm",
		IsPerTypedConfig: true,
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "route_config.virtual_hosts", IndexPath: "route_config.virtual_hosts.%d"},
			{ParentPath: "route_config.virtual_hosts.%d.routes", IndexPath: routes},
		},
		PathTemplate:     "route_config.virtual_hosts.%d.routes.%d.typed_per_filter_config",
		Kind:             "hcm",
		IsPerTypedConfig: true,
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "route_config.virtual_hosts", IndexPath: "route_config.virtual_hosts.%d"},
			{ParentPath: "route_config.virtual_hosts.%d.routes", IndexPath: routes},
		},
		PathTemplate: "route_config.virtual_hosts.%d.routes.%d.match.path_match_policy.typed_config",
		Kind:         "hcm",
	},
}

var StatefulSessionTypedConfigPaths = []TypedConfigPath{
	{
		ArrayPaths:   []ArrayPath{},
		PathTemplate: "session_state.typed_config",
		Kind:         "session_state",
	},
}

var StatefulSessionPerRouteTypedConfigPaths = []TypedConfigPath{
	{
		ArrayPaths:   []ArrayPath{},
		PathTemplate: "stateful_session.session_state.typed_config",
		Kind:         "session_state",
	},
}

var CompressorTypedConfigPaths = []TypedConfigPath{
	{
		ArrayPaths:   []ArrayPath{},
		PathTemplate: "compressor_library.typed_config",
		Kind:         "compressor_library",
	},
}

var RBACTypedConfigPaths = []TypedConfigPath{
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rules.policies.*.permissions", IndexPath: "rules.policies.*.permissions.%d"},
		},
		PathTemplate: "rules.policies.*.permissions.%d.uri_template.typed_config",
		Kind:         "uri_template",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rules.policies.*.permissions", IndexPath: "rules.policies.*.permissions.%d"},
		},
		PathTemplate: "rules.policies.*.permissions.%d.not_rule.uri_template.typed_config",
		Kind:         "uri_template",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rules.policies.*.permissions", IndexPath: "rules.policies.*.permissions.%d"},
			{ParentPath: "rules.policies.*.permissions.%d.and_rules.rules", IndexPath: "rules.%d"},
		},
		PathTemplate: "rules.policies.*.permissions.%d.and_rules.rules.%d.uri_template.typed_config",
		Kind:         "uri_template",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rules.policies.*.permissions", IndexPath: "rules.policies.*.permissions.%d"},
			{ParentPath: "rules.policies.*.permissions.%d.or_rules.rules", IndexPath: "rules.%d"},
		},
		PathTemplate: "rules.policies.*.permissions.%d.or_rules.rules.%d.uri_template.typed_config",
		Kind:         "uri_template",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rules.policies.*.permissions", IndexPath: "rules.policies.*.permissions.%d"},
			{ParentPath: "rules.policies.*.permissions.%d.not_rule.and_rules.rules", IndexPath: "rules.%d"},
		},
		PathTemplate: "rules.policies.*.permissions.%d.not_rule.and_rules.rules.%d.uri_template.typed_config",
		Kind:         "uri_template",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rules.policies.*.permissions", IndexPath: "rules.policies.*.permissions.%d"},
			{ParentPath: "rules.policies.*.permissions.%d.not_rule.or_rules.rules", IndexPath: "rules.%d"},
		},
		PathTemplate: "rules.policies.*.permissions.%d.not_rule.or_rules.rules.%d.uri_template.typed_config",
		Kind:         "uri_template",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rules.policies.*.permissions", IndexPath: "rules.policies.*.permissions.%d"},
			{ParentPath: "rules.policies.*.permissions.%d.and_rules.rules", IndexPath: "rules.%d"},
			{ParentPath: "rules.policies.*.permissions.%d.and_rules.rules.%d.and_rules.rules", IndexPath: "rules.%d"},
		},
		PathTemplate: "rules.policies.*.permissions.%d.and_rules.rules.%d.and_rules.rules.%d.uri_template.typed_config",
		Kind:         "uri_template",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rules.policies.*.permissions", IndexPath: "rules.policies.*.permissions.%d"},
			{ParentPath: "rules.policies.*.permissions.%d.or_rules.rules", IndexPath: "rules.%d"},
			{ParentPath: "rules.policies.*.permissions.%d.or_rules.rules.%d.or_rules.rules", IndexPath: "rules.%d"},
		},
		PathTemplate: "rules.policies.*.permissions.%d.or_rules.rules.%d.or_rules.rules.%d.uri_template.typed_config",
		Kind:         "uri_template",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rules.policies.*.permissions", IndexPath: "rules.policies.*.permissions.%d"},
			{ParentPath: "rules.policies.*.permissions.%d.not_rule.and_rules.rules", IndexPath: "rules.%d"},
			{ParentPath: "rules.policies.*.permissions.%d.not_rule.and_rules.rules.%d.or_rules.rules", IndexPath: "rules.%d"},
		},
		PathTemplate: "rules.policies.*.permissions.%d.not_rule.and_rules.rules.%d.or_rules.rules.%d.uri_template.typed_config",
		Kind:         "uri_template",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rules.policies.*.permissions", IndexPath: "rules.policies.*.permissions.%d"},
			{ParentPath: "rules.policies.*.permissions.%d.and_rules.rules", IndexPath: "rules.%d"},
			{ParentPath: "rules.policies.*.permissions.%d.and_rules.rules.%d.not_rule.and_rules.rules", IndexPath: "rules.%d"},
		},
		PathTemplate: "rules.policies.*.permissions.%d.and_rules.rules.%d.not_rule.and_rules.rules.%d.uri_template.typed_config",
		Kind:         "uri_template",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rules.policies.*.permissions", IndexPath: "rules.policies.*.permissions.%d"},
			{ParentPath: "rules.policies.*.permissions.%d.or_rules.rules", IndexPath: "rules.%d"},
			{ParentPath: "rules.policies.*.permissions.%d.or_rules.rules.%d.not_rule.uri_template.typed_config", IndexPath: "rules.%d"},
		},
		PathTemplate: "rules.policies.*.permissions.%d.or_rules.rules.%d.not_rule.uri_template.typed_config",
		Kind:         "uri_template",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rules.policies.*.permissions", IndexPath: "rules.policies.*.permissions.%d"},
			{ParentPath: "rules.policies.*.permissions.%d.not_rule.or_rules.rules", IndexPath: "rules.%d"},
			{ParentPath: "rules.policies.*.permissions.%d.not_rule.or_rules.rules.%d.not_rule.uri_template.typed_config", IndexPath: "rules.%d"},
		},
		PathTemplate: "rules.policies.*.permissions.%d.not_rule.or_rules.rules.%d.not_rule.uri_template.typed_config",
		Kind:         "uri_template",
	},
}

var RBACPerRouteTypedConfigPaths = []TypedConfigPath{
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rbac.rules.policies.*.permissions", IndexPath: "rbac.rules.policies.*.permissions.%d"},
		},
		PathTemplate: "rbac.rules.policies.*.permissions.%d.uri_template.typed_config",
		Kind:         "uri_template",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rbac.rules.policies.*.permissions", IndexPath: "rbac.rules.policies.*.permissions.%d"},
		},
		PathTemplate: "rbac.rules.policies.*.permissions.%d.not_rule.uri_template.typed_config",
		Kind:         "uri_template",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rbac.rules.policies.*.permissions", IndexPath: "rbac.rules.policies.*.permissions.%d"},
			{ParentPath: "rbac.rules.policies.*.permissions.%d.and_rules.rules", IndexPath: "rules.%d"},
		},
		PathTemplate: "rbac.rules.policies.*.permissions.%d.and_rules.rules.%d.uri_template.typed_config",
		Kind:         "uri_template",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rbac.rules.policies.*.permissions", IndexPath: "rbac.rules.policies.*.permissions.%d"},
			{ParentPath: "rbac.rules.policies.*.permissions.%d.or_rules.rules", IndexPath: "rules.%d"},
		},
		PathTemplate: "rbac.rules.policies.*.permissions.%d.or_rules.rules.%d.uri_template.typed_config",
		Kind:         "uri_template",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rbac.rules.policies.*.permissions", IndexPath: "rbac.rules.policies.*.permissions.%d"},
			{ParentPath: "rbac.rules.policies.*.permissions.%d.not_rule.and_rules.rules", IndexPath: "rules.%d"},
		},
		PathTemplate: "rbac.rules.policies.*.permissions.%d.not_rule.and_rules.rules.%d.uri_template.typed_config",
		Kind:         "uri_template",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rbac.rules.policies.*.permissions", IndexPath: "rbac.rules.policies.*.permissions.%d"},
			{ParentPath: "rbac.rules.policies.*.permissions.%d.not_rule.or_rules.rules", IndexPath: "rules.%d"},
		},
		PathTemplate: "rbac.rules.policies.*.permissions.%d.not_rule.or_rules.rules.%d.uri_template.typed_config",
		Kind:         "uri_template",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rbac.rules.policies.*.permissions", IndexPath: "rbac.rules.policies.*.permissions.%d"},
			{ParentPath: "rbac.rules.policies.*.permissions.%d.and_rules.rules", IndexPath: "rules.%d"},
			{ParentPath: "rbac.rules.policies.*.permissions.%d.and_rules.rules.%d.and_rules.rules", IndexPath: "rules.%d"},
		},
		PathTemplate: "rbac.rules.policies.*.permissions.%d.and_rules.rules.%d.and_rules.rules.%d.uri_template.typed_config",
		Kind:         "uri_template",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rbac.rules.policies.*.permissions", IndexPath: "rbac.rules.policies.*.permissions.%d"},
			{ParentPath: "rbac.rules.policies.*.permissions.%d.or_rules.rules", IndexPath: "rules.%d"},
			{ParentPath: "rbac.rules.policies.*.permissions.%d.or_rules.rules.%d.or_rules.rules", IndexPath: "rules.%d"},
		},
		PathTemplate: "rbac.rules.policies.*.permissions.%d.or_rules.rules.%d.or_rules.rules.%d.uri_template.typed_config",
		Kind:         "uri_template",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rbac.rules.policies.*.permissions", IndexPath: "rbac.rules.policies.*.permissions.%d"},
			{ParentPath: "rbac.rules.policies.*.permissions.%d.not_rule.and_rules.rules", IndexPath: "rules.%d"},
			{ParentPath: "rbac.rules.policies.*.permissions.%d.not_rule.and_rules.rules.%d.or_rules.rules", IndexPath: "rules.%d"},
		},
		PathTemplate: "rbac.rules.policies.*.permissions.%d.not_rule.and_rules.rules.%d.or_rules.rules.%d.uri_template.typed_config",
		Kind:         "uri_template",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rbac.rules.policies.*.permissions", IndexPath: "rbac.rules.policies.*.permissions.%d"},
			{ParentPath: "rbac.rules.policies.*.permissions.%d.and_rules.rules", IndexPath: "rules.%d"},
			{ParentPath: "rbac.rules.policies.*.permissions.%d.and_rules.rules.%d.not_rule.and_rules.rules", IndexPath: "rules.%d"},
		},
		PathTemplate: "rbac.rules.policies.*.permissions.%d.and_rules.rules.%d.not_rule.and_rules.rules.%d.uri_template.typed_config",
		Kind:         "uri_template",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rbac.rules.policies.*.permissions", IndexPath: "rbac.rules.policies.*.permissions.%d"},
			{ParentPath: "rbac.rules.policies.*.permissions.%d.or_rules.rules", IndexPath: "rules.%d"},
			{ParentPath: "rbac.rules.policies.*.permissions.%d.or_rules.rules.%d.not_rule.uri_template.typed_config", IndexPath: "rules.%d"},
		},
		PathTemplate: "rbac.rules.policies.*.permissions.%d.or_rules.rules.%d.not_rule.uri_template.typed_config",
		Kind:         "uri_template",
	},
	{
		ArrayPaths: []ArrayPath{
			{ParentPath: "rbac.rules.policies.*.permissions", IndexPath: "rbac.rules.policies.*.permissions.%d"},
			{ParentPath: "rbac.rules.policies.*.permissions.%d.not_rule.or_rules.rules", IndexPath: "rules.%d"},
			{ParentPath: "rbac.rules.policies.*.permissions.%d.not_rule.or_rules.rules.%d.not_rule.uri_template.typed_config", IndexPath: "rules.%d"},
		},
		PathTemplate: "rbac.rules.policies.*.permissions.%d.not_rule.or_rules.rules.%d.not_rule.uri_template.typed_config",
		Kind:         "uri_template",
	},
}
