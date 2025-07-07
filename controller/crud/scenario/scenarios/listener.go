package scenarios

const SingleListenerHTTP = `
{
	"general": {
		"name": "{{ .Data.name }}",
		"version": "{{ .Version }}",
		"type": "listener",
		"gtype": "envoy.config.listener.v3.Listener",
		"project": "{{ .Project }}",
		"collection": "listeners",
		"canonical_name": "config.listener.v3.Listener",
		"category": "listener",
		"metadata": { "from_template": true },
		"managed": {{ .Managed }},
		"permissions": {
			"users": [],
			"groups": []
		},
		"config_discovery": [
			{
				"parent_name": "{{ .Data.name }}{{ .Listener.UniqListenerNameID}}-fc{{ .Listener.UniqFilterChainNameID }}-filter{{ .Listener.UniqFilterNameID }}",
				"gtype": "envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager",
				"name": "{{ .Data.hcm }}",
				"priority": 0,
				"category": "envoy.filters.network",
				"canonical_name": "envoy.filters.network.http_connection_manager"
			}
		],
		"typed_config": []
	},
	"resource": {
		"version": "1",
		"resource": [
			{
				"name": "{{ .Data.name }}{{ .Listener.UniqListenerNameID}}",
				"address": {
					"socket_address": {
						"protocol": "{{ .Data.protocol }}",
						"address": "{{ .Data.address }}",
						"port_value": {{ .Data.port }}
					}
				},
				"filter_chains": [
					{
						"name": "{{ .Data.name }}{{ .Listener.UniqListenerNameID}}-fc{{ .Listener.UniqFilterChainNameID }}",
						"filters": [
							{
								"name": "{{ .Data.name }}{{ .Listener.UniqListenerNameID}}-fc{{ .Listener.UniqFilterChainNameID }}-filter{{ .Listener.UniqFilterNameID }}",
								"config_discovery": {
									"config_source": {
										"resource_api_version": "V3",
										"ads": {},
										"initial_fetch_timeout": "2.0s"
									},
									"type_urls": ["envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager"]
								}
							}
						]
					}
				]
			}
		]
	}
}
`

const SingleListenerTCP = `
{
	"general": {
		"name": "{{ .Data.name }}",
		"version": "{{ .Version }}",
		"type": "listener",
		"gtype": "envoy.config.listener.v3.Listener",
		"project": "{{ .Project }}",
		"collection": "listeners",
		"canonical_name": "config.listener.v3.Listener",
		"category": "listener",
		"metadata": { "from_template": true },
		"managed": {{ .Managed }},
		"permissions": {
			"users": [],
			"groups": []
		},
		"config_discovery": [
			{
				"parent_name": "{{ .Data.name }}{{ .Listener.UniqListenerNameID}}-fc{{ .Listener.UniqFilterChainNameID }}-filter{{ .Listener.UniqFilterNameID }}",
				"gtype": "envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy",
				"name": "{{ .Data.tcp_proxy }}",
				"priority": 0,
				"category": "envoy.filters.network",
				"canonical_name": "envoy.filters.network.tcp_proxy"
			}
		],
		"typed_config": []
	},
	"resource": {
		"version": "1",
		"resource": [
			{
				"name": "{{ .Data.name }}{{ .Listener.UniqListenerNameID}}",
				"address": {
					"socket_address": {
						"protocol": "{{ .Data.protocol }}",
						"address": "{{ .Data.address }}",
						"port_value": {{ .Data.port }}
					}
				},
				"filter_chains": [
					{
						"name": "{{ .Data.name }}{{ .Listener.UniqListenerNameID}}-fc{{ .Listener.UniqFilterChainNameID }}",
						"filters": [
							{
								"name": "{{ .Data.name }}{{ .Listener.UniqListenerNameID}}-fc{{ .Listener.UniqFilterChainNameID }}-filter{{ .Listener.UniqFilterNameID }}",
								"config_discovery": {
									"config_source": {
										"resource_api_version": "V3",
										"ads": {},
										"initial_fetch_timeout": "2.0s"
									},
									"type_urls": ["envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy"]
								}
							}
						]
					}
				]
			}
		]
	}
}
`
