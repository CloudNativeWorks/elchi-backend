package scenarios

const RouteWithDirectVirtualHost = `
{
	"general": {
		"name": "{{ .Data.name }}",
		"version": "{{ .Version }}",
		"type": "route",
		"gtype": "envoy.config.route.v3.RouteConfiguration",
		"project": "{{ .Project }}",
		"collection": "routes",
		"canonical_name": "config.route.v3.RouteConfiguration",
		"category": "route",
		"metadata": { "from_template": true },
		"config_discovery": [],
		"typed_config": [],
		"permissions": {
			"users": [],
			"groups": []
		}
	},
	"resource": {
		"version": "1",
		"resource": {
			"name": "{{ .Data.name }}",
			"virtual_hosts": [
				{
					"routes": [
						{
							"name": "route1",
							"match": {
								"{{ .Data.match_key }}": "{{ .Data.match_value }}"
							},
							"route": {
								"cluster": "{{ .Data.cluster }}"
							}
						}
					],
					"name": "virtualhost1",
					"domains": {{ .Data.domains | toJson }}
				}
			]
		}
	}
}
`

const RouteWithVHDS = `
{
	"general": {
		"name": "{{ .Data.name }}",
		"version": "{{ .Version }}",
		"type": "route",
		"gtype": "envoy.config.route.v3.RouteConfiguration",
		"project": "{{ .Project }}",
		"collection": "routes",
		"canonical_name": "config.route.v3.RouteConfiguration",
		"category": "route",
		"metadata": { "from_template": true },
		"permissions": {
			"users": [],
			"groups": []
		},
		"config_discovery": [
			{
				"parent_name": "{{ .Data.name }}",
				"gtype": "envoy.config.route.v3.VirtualHost",
				"name": "{{ .Data.vhds }}",
				"priority": 0,
				"category": "vhds",
				"canonical_name": "config.route.v3.VirtualHost"
			}
		],
		"typed_config": []
	},
	"resource": {
		"version": "1",
		"resource": {
			"name": "{{ .Data.name }}",
			"vhds": {
				"config_source": {
					"api_config_source": {
						"api_type": "DELTA_GRPC",
						"transport_api_version": "V3",
						"grpc_services": [
							{
								"envoy_grpc": {
									"cluster_name": "elchi-control-plane"
								},
								"timeout": "2.0s",
								"initial_metadata": [
									{
										"key": "nodeid",
										"value": "__NODEID__"
									}
								]
							}
						]
					},
					"initial_fetch_timeout": "2.0s",
					"resource_api_version": "V3"
				}
			}
		}
	}
}
`
