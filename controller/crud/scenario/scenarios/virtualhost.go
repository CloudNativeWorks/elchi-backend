package scenarios

const virtualHost = `
{
	"general": {
		"name": "{{ .Data.name }}",
		"version": "{{ .Version }}",
		"type": "virtual_host",
		"gtype": "envoy.config.route.v3.VirtualHost",
		"project": "{{ .Project }}",
		"collection": "virtual_hosts",
		"canonical_name": "config.route.v3.VirtualHost",
		"category": "virtual_host",
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
		"resource": [
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
`
