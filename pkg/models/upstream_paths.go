package models

var ClusterUpstreams = map[string]GType{
	"eds_cluster_config.service_name": Endpoint,
}

var TCPProxyUpstreams = map[string]GType{
	"cluster":                           Cluster,
	"weighted_clusters.clusters.#.name": Cluster,
}

var HTTPConnectionManagerUpstreams = map[string]GType{
	"rds.route_config_name":                                                         Route,
	"route_config.virtual_hosts.#.routes.#.route.cluster":                           Cluster,
	"route_config.virtual_hosts.#.routes.#.route.weighted_clusters.clusters.#.name": Cluster,
	"route_config.virtual_hosts.#.request_mirror_policies.#.cluster":                Cluster,
	"route_config.request_mirror_policies.#.cluster":                                Cluster,
}

var RouteUpstreams = map[string]GType{
	"virtual_hosts.#.routes.#.route.cluster":                           Cluster,
	"virtual_hosts.#.routes.#.route.weighted_clusters.clusters.#.name": Cluster,
	"virtual_hosts.#.request_mirror_policies.#.cluster":                Cluster,
	"request_mirror_policies.#.cluster":                                Cluster,
}

var VirtualHostUpstreams = map[string]GType{
	"routes.#.route.cluster":                           Cluster,
	"routes.#.route.weighted_clusters.clusters.#.name": Cluster,
	"request_mirror_policies.#.cluster":                Cluster,
}

var FluentdAccessLogUpstreams = map[string]GType{
	"cluster": Cluster,
}

var BootstrapUpstreams = map[string]GType{
	"static_resources.clusters.#.name": Cluster,
}

var DownstreamTLSContextUpstreams = map[string]GType{
	"common_tls_context.tls_certificate_sds_secret_configs.#.name": TLSCertificate,
	"common_tls_context.validation_context_sds_secret_config.name": CertificateValidationContext,
}

var UpstreamTLSContextUpstreams = map[string]GType{
	"common_tls_context.tls_certificate_sds_secret_configs.#.name": TLSCertificate,
	"common_tls_context.validation_context_sds_secret_config.name": CertificateValidationContext,
}

var GenericGRPCServiceUpstreams = map[string]GType{
	"grpc_service.envoy_grpc.cluster_name": Cluster,
}

var OAuth2Upstreams = map[string]GType{
	"config.token_endpoint.cluster":        Cluster,
	"config.credentials.hmac_secret.name":  GenericSecret,
	"config.credentials.token_secret.name": GenericSecret,
}

var ExtProcUpstreams = map[string]GType{
	"grpc_service.envoy_grpc.cluster_name": Cluster,
}

var ExtProcPerRouteUpstreams = map[string]GType{
	"overrides.grpc_service.envoy_grpc.cluster_name": Cluster,
}

var ExtAuthzUpstreams = map[string]GType{
	"grpc_service.envoy_grpc.cluster_name": Cluster,
	"http_service.server_uri.cluster":      Cluster,
}

var ExtAuthzPerRouteUpstreams = map[string]GType{
	"check_settings.grpc_service.envoy_grpc.cluster_name": Cluster,
	"check_settings.http_service.server_uri.cluster":      Cluster,
}

var JWTAuthenticationUpstreams = map[string]GType{
	"providers.*.remote_jwks.http_uri.cluster": Cluster,
}

var UDPProxyUpstreams = map[string]GType{
	"cluster":                      Cluster,
	"matcher.on_no_match.action.0": Cluster, // Route action cluster
}

var DynamicForwardProxyHTTPUpstreams = map[string]GType{
	// No upstream cluster dependencies - dns_cache_config references a shared cache config
}

var QuicDownstreamTransportUpstreams = map[string]GType{
	"downstream_tls_context.common_tls_context.tls_certificate_sds_secret_configs.#.name": TLSCertificate,
	"downstream_tls_context.common_tls_context.validation_context_sds_secret_config.name": CertificateValidationContext,
}
