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
		"config_discovery": [],
		"typed_config": [
			{
				"name": "{{ .Data.hcm }}",
				"canonical_name": "envoy.filters.network.http_connection_manager",
				"gtype": "envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager",
				"type": "network_filter",
				"category": "envoy.filters.network",
				"collection": "filters",
				"disabled": false,
				"priority": 0,
				"parent_name": ""
			}
		]
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
								"typed_config": {
									"type_url": "envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager",
									"value": "{{ .Data.hcm_base64 }}"
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
		"config_discovery": [],
		"typed_config": [
			{
				"name": "{{ .Data.tcp_proxy }}",
				"canonical_name": "envoy.filters.network.tcp_proxy",
				"gtype": "envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy",
				"type": "network_filter",
				"category": "envoy.filters.network",
				"collection": "filters",
				"disabled": false,
				"priority": 0,
				"parent_name": ""
			}
		]
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
								"typed_config": {
									"type_url": "envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy",
									"value": "{{ .Data.tcp_proxy_base64 }}"
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
