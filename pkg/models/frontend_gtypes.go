package models

// Frontend GTypes mapping imported from elchi/src/common/statics/gtypes.ts
var FrontendGTypesMapping = map[string]string{
	"bootstrap":                  "envoy.config.bootstrap.v3.Bootstrap",
	"listener":                   "envoy.config.listener.v3.Listener", 
	"cluster":                    "envoy.config.cluster.v3.Cluster",
	"endpoint":                   "envoy.config.endpoint.v3.ClusterLoadAssignment",
	"route":                      "envoy.config.route.v3.RouteConfiguration",
	"virtual_host":               "envoy.config.route.v3.VirtualHost",
	"http_connection_manager":    "envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager",
	"http_router":                "envoy.extensions.filters.http.router.v3.Router",
	"tcp_proxy":                  "envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy",
	"http_rbac":                  "envoy.extensions.filters.http.rbac.v3.RBAC",
	"http_rbac_per_route":        "envoy.extensions.filters.http.rbac.v3.RBACPerRoute",
	"network_rbac":               "envoy.extensions.filters.network.rbac.v3.RBAC",
	"basic_auth":                 "envoy.extensions.filters.http.basic_auth.v3.BasicAuth",
	"tls":                        "envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext",
	"secret":                     "envoy.extensions.transport_sockets.tls.v3.TlsCertificate",
	"access_log":                 "envoy.extensions.access_loggers.file.v3.FileAccessLog",
	"cors":                       "envoy.extensions.filters.http.cors.v3.Cors",
	"bandwidth_limit":            "envoy.extensions.filters.http.bandwidth_limit.v3.BandwidthLimit",
	"compressor":                 "envoy.extensions.filters.http.compressor.v3.Compressor",
	"lua":                        "envoy.extensions.filters.http.lua.v3.Lua",
	"buffer":                     "envoy.extensions.filters.http.buffer.v3.Buffer",
}

// GetFrontendGType returns the GType for a component type using frontend mapping
func GetFrontendGType(componentType string) string {
	if gtype, exists := FrontendGTypesMapping[componentType]; exists {
		return gtype
	}
	return ""
}