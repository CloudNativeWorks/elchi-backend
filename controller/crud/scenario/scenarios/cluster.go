package scenarios

const NonEdsCluster = `
{
	"general": {
		"name": "{{ .Data.cluster_name }}",
		"version": "{{ .Version }}",
		"type": "cluster",
		"gtype": "envoy.config.cluster.v3.Cluster",
		"project": "{{ .Project }}",
		"collection": "clusters",
		"canonical_name": "config.cluster.v3.Cluster",
		"category": "cluster",
		"metadata": { "from_template": true },
		"permissions": {
			"users": [],
			"groups": []
		},
		"config_discovery": [],
		"typed_config": []
	},
	"resource": {
		"version": "1",
		"resource": {
			"name": "{{ .Data.cluster_name }}",
			"type": "{{ .Data.type }}",
			"connect_timeout": "2s",
			"load_assignment": {
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
									"protocol": "{{ $.Data.protocol }}",
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
}
`

const EdsCluster = `
{
	"general": {
		"name": "{{ .Data.cluster_name }}",
		"version": "{{ .Version }}",
		"type": "cluster",
		"gtype": "envoy.config.cluster.v3.Cluster",
		"project": "{{ .Project }}",
		"collection": "clusters",
		"canonical_name": "config.cluster.v3.Cluster",
		"category": "cluster",
		"metadata": { "from_template": true },
		"permissions": {
			"users": [],
			"groups": []
		},
		"config_discovery": [],
		"typed_config": []
	},
	"resource": {
		"version": "1",
		"resource": {
			"name": "{{ .Data.cluster_name }}",
			"connect_timeout": "2s",
			"type": "EDS",
			"eds_cluster_config": {
				"eds_config": {
					"ads": {},
					"initial_fetch_timeout": "2.0s",
					"resource_api_version": "V3"
				},
				"service_name": "{{ .Data.eds_config }}"
			}
		}
	}
}
`
