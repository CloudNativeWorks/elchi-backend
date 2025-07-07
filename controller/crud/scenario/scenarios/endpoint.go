package scenarios

const Endpoint = `
{
	"general": {
		"name": "{{ .Data.cluster_name }}",
		"version": "{{ .Version }}",
		"type": "endpoint",
		"gtype": "envoy.config.endpoint.v3.ClusterLoadAssignment",
		"project": "{{ .Project }}",
		"collection": "endpoints",
		"canonical_name": "config.endpoint.v3.Endpoint",
		"category": "cluster",
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
			"cluster_name": "{{ .Data.cluster_name }}",
			"endpoints": [
				{
					"lb_endpoints": [
						{{ range $index, $endpoint := .Data.lb_endpoints }}
						{{ if $index }},{{ end }}
						{
							"endpoint": {
								"address": {
									"socket_address": {
										"protocol": "TCP",
										"address": "{{ $endpoint.address }}",
										"port_value": {{ $endpoint.port }}
									}
								}
							}
						}
						{{ end }}
					]
				}
			]
		}
	}
}
`
