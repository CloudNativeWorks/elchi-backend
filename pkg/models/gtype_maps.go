package models

import (
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models/downstreamfilters"
	bootstrap "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/bootstrap/v3"
	cluster "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/cluster/v3"
	endpoint "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/endpoint/v3"
	listener "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/listener/v3"
	route "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/route/v3"
	al_file "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/access_loggers/file/v3"
	al_fluentd "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/access_loggers/fluentd/v3"
	al_stream "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/access_loggers/stream/v3"
	brotli_compressor "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/compression/brotli/compressor/v3"
	gzip_compressor "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/compression/gzip/compressor/v3"
	zstd_compressor "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/compression/zstd/compressor/v3"
	adaptive_concurrency "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/http/adaptive_concurrency/v3"
	admission_control "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/http/admission_control/v3"
	bandwidth_limit "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/http/bandwidth_limit/v3"
	basic_auth "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/http/basic_auth/v3"
	buffer "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/http/buffer/v3"
	compressor "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/http/compressor/v3"
	cors "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/http/cors/v3"
	csrf_policy "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/http/csrf/v3"
	h_local_ratelimit "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/http/local_ratelimit/v3"
	lua "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/http/lua/v3"
	oauth2 "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/http/oauth2/v3"
	h_rbac "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/http/rbac/v3"
	router "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/http/router/v3"
	stateful_session "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/http/stateful_session/v3"
	l_http_inspector "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/listener/http_inspector/v3"
	l_local_ratelimit "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/listener/local_ratelimit/v3"
	l_original_dst "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/listener/original_dst/v3"
	l_original_src "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/listener/original_src/v3"
	l_proxy_protocol "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/listener/proxy_protocol/v3"
	l_tls_inspector "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/listener/tls_inspector/v3"
	connection_limit "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/network/connection_limit/v3"
	hcm "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	n_local_ratelimit "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/network/local_ratelimit/v3"
	n_rbac "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/network/rbac/v3"
	tcp "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/network/tcp_proxy/v3"
	l_dns_filter "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/udp/dns_filter/v3"
	hcefs "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/health_check/event_sinks/file/v3"
	stateful_session_cookie "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/http/stateful_session/cookie/v3"
	stateful_session_header "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/http/stateful_session/header/v3"
	utm "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/path/match/uri_template/v3"
	stat_sink_otel "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/stat_sinks/open_telemetry/v3"
	tls "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	http_protocol_options "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/upstreams/http/v3"
)

type GTypeMapping struct {
	Collection                    string
	URL                           string
	PrettyName                    string
	Type                          string
	CanonicalName                 string
	Category                      string
	Message                       proto.Message
	DownstreamFiltersFunc         func(downstreamfilters.DownstreamFilter) []downstreamfilters.MongoFilters
	TemplateDownstreamFiltersFunc func(name, project, version string) []downstreamfilters.MongoFilters
	TypedConfigPaths              []TypedConfigPath
	UpstreamPaths                 map[string]GType
}

const unknown = "unknown"

// createTemplateFilterFunc creates a template filter function for a given gtype
func createTemplateFilterFunc(gtype string) func(name, project, version string) []downstreamfilters.MongoFilters {
	return func(name, project, version string) []downstreamfilters.MongoFilters {
		return downstreamfilters.TemplateDownstreamFiltersForGType(name, project, version, gtype)
	}
}

var URLs = map[string]string{
	"bootstrap":             "/resource/bootstrap/",
	"clusters":              "/resource/cluster/",
	"endpoints":             "/resource/endpoint/",
	"listeners":             "/resource/listener/",
	"routes":                "/resource/route/",
	"virtual_hosts":         "/resource/virtual_host/",
	"tcp_proxy":             "/filters/network/tcp_proxy/",
	"hcm":                   "/filters/network/hcm/",
	"n_rbac":                "/filters/network/rbac/",
	"h_rbac":                "/filters/http/rbac/",
	"secrets":               "/resource/secret/",
	"access_log":            "/extensions/access_log/",
	"http_router":           "/filters/http/http_router/",
	"hcefs":                 "/extensions/hcefs/",
	"utm":                   "/extensions/utm/",
	"basic_auth":            "/filters/http/basic_auth/",
	"cors":                  "/filters/http/cors/",
	"bandwidth_limit":       "/filters/http/bandwidth_limit/",
	"compressor":            "/filters/http/compressor/",
	"compressor_library":    "/extensions/compressor_library/",
	"http_protocol_options": "/extensions/http_protocol_options/",
	"lua":                   "/filters/http/lua/",
	"buffer":                "/filters/http/buffer/",
	"adaptive_concurrency":  "/filters/http/adaptive_concurrency/",
	"admission_control":     "/filters/http/admission_control/",
	"session_state":         "/extensions/session_state/",
	"stateful_session":      "/filters/http/stateful_session/",
	"csrf_policy":           "/filters/http/csrf_policy/",
	"l_local_ratelimit":     "/filters/listener/l_local_ratelimit/",
	"l_http_inspector":      "/filters/listener/l_http_inspector/",
	"l_original_dst":        "/filters/listener/l_original_dst/",
	"l_original_src":        "/filters/listener/l_original_src/",
	"l_tls_inspector":       "/filters/listener/l_tls_inspector/",
	"l_dns_filter":          "/filters/listener/l_dns_filter/",
	"l_proxy_protocol":      "/filters/listener/l_proxy_protocol/",
	"connection_limit":      "/filters/network/connection_limit/",
	"n_local_ratelimit":     "/filters/network/n_local_ratelimit/",
	"h_local_ratelimit":     "/filters/http/h_local_ratelimit/",
	"oauth2":                "/filters/http/oauth2/",
	"tls":                   "/resource/tls/",
	"stat_sinks":            "/extensions/stat_sinks/",
}

var gTypeMappings = map[GType]GTypeMapping{
	BootStrap: {
		PrettyName:                    "Bootstrap",
		Collection:                    "bootstrap",
		Type:                          "bootstrap",
		CanonicalName:                 "config.bootstrap.v3.Bootstrap",
		Category:                      "bootstrap",
		URL:                           URLs["bootstrap"],
		Message:                       &bootstrap.Bootstrap{},
		DownstreamFiltersFunc:         nil,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(BootStrap.String()),
		TypedConfigPaths:              BootstrapTypedConfigPaths,
		UpstreamPaths:                 BootstrapUpstreams,
	},
	HTTPConnectionManager: {
		PrettyName:                    "Http Connection Manager",
		Collection:                    "filters",
		Type:                          "network_filter",
		CanonicalName:                 "envoy.filters.network.http_connection_manager",
		Category:                      "envoy.filters.network",
		URL:                           URLs["hcm"],
		Message:                       &hcm.HttpConnectionManager{},
		DownstreamFiltersFunc:         downstreamfilters.DownstreamTypedFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(HTTPConnectionManager.String()),
		TypedConfigPaths:              HTTPConnectionManagerTypedConfigPaths,
		UpstreamPaths:                 HTTPConnectionManagerUpstreams,
	},
	RBAC: {
		PrettyName:                    "RBAC",
		Collection:                    "filters",
		Type:                          "network_filter",
		CanonicalName:                 "envoy.filters.network.rbac",
		Category:                      "envoy.filters.network",
		URL:                           URLs["n_rbac"],
		Message:                       &n_rbac.RBAC{},
		DownstreamFiltersFunc:         downstreamfilters.DownstreamTypedFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(RBAC.String()),
		TypedConfigPaths:              RBACTypedConfigPaths,
		UpstreamPaths:                 nil,
	},
	HTTPRBAC: {
		PrettyName:                    "Http RBAC",
		Collection:                    "filters",
		Type:                          "http_filter",
		CanonicalName:                 "envoy.filters.http.rbac",
		Category:                      "envoy.filters.http",
		URL:                           URLs["h_rbac"],
		Message:                       &h_rbac.RBAC{},
		DownstreamFiltersFunc:         downstreamfilters.ConfigDiscoveryHTTPFilterDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(HTTPRBAC.String()),
		TypedConfigPaths:              RBACTypedConfigPaths,
		UpstreamPaths:                 nil,
	},
	HTTPRBACPerRoute: {
		PrettyName:                    "Http RBAC Per Route",
		Collection:                    "filters",
		Type:                          "http_filter",
		CanonicalName:                 "envoy.filters.http.rbac",
		Category:                      "envoy.filters.http",
		URL:                           URLs["h_rbac"],
		Message:                       &h_rbac.RBACPerRoute{},
		DownstreamFiltersFunc:         downstreamfilters.TypedHTTPFilterDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(HTTPRBACPerRoute.String()),
		TypedConfigPaths:              RBACPerRouteTypedConfigPaths,
		UpstreamPaths:                 nil,
	},
	Router: {
		PrettyName:                    "Router",
		Collection:                    "filters",
		Type:                          "http_filter",
		CanonicalName:                 "envoy.filters.http.router",
		Category:                      "envoy.filters.http",
		URL:                           URLs["http_router"],
		Message:                       &router.Router{},
		DownstreamFiltersFunc:         downstreamfilters.ConfigDiscoveryHTTPFilterDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(Router.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	Cluster: {
		PrettyName:                    "Cluster",
		Collection:                    "clusters",
		Type:                          "cluster",
		CanonicalName:                 "config.cluster.v3.Cluster",
		Category:                      "cluster",
		URL:                           URLs["clusters"],
		Message:                       &cluster.Cluster{},
		DownstreamFiltersFunc:         downstreamfilters.ClusterDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(Cluster.String()),
		TypedConfigPaths:              ClusterTypedConfigPaths,
		UpstreamPaths:                 ClusterUpstreams,
	},
	Listener: {
		PrettyName:                    "Listener",
		Collection:                    "listeners",
		Type:                          "listener",
		CanonicalName:                 "config.listener.v3.Listener",
		Category:                      "listener",
		URL:                           URLs["listeners"],
		Message:                       &listener.Listener{},
		DownstreamFiltersFunc:         nil,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(Listener.String()),
		TypedConfigPaths:              ListenerTypedConfigPaths,
		UpstreamPaths:                 nil,
	},
	Endpoint: {
		PrettyName:                    "Endpoint",
		Collection:                    "endpoints",
		Type:                          "endpoint",
		CanonicalName:                 "config.endpoint.v3.Endpoint",
		Category:                      "endpoint",
		URL:                           URLs["endpoints"],
		Message:                       &endpoint.ClusterLoadAssignment{},
		DownstreamFiltersFunc:         downstreamfilters.EdsDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(Endpoint.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	Route: {
		PrettyName:                    "Route",
		Collection:                    "routes",
		Type:                          "route",
		CanonicalName:                 "config.route.v3.RouteConfiguration",
		Category:                      "route",
		URL:                           URLs["routes"],
		Message:                       &route.RouteConfiguration{},
		DownstreamFiltersFunc:         downstreamfilters.RouteDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(Route.String()),
		TypedConfigPaths:              RouteTypedConfigPaths,
		UpstreamPaths:                 RouteUpstreams,
	},
	VirtualHost: {
		PrettyName:                    "Virtual Host",
		Collection:                    "virtual_hosts",
		Type:                          "virtual_host",
		CanonicalName:                 "config.route.v3.VirtualHost",
		Category:                      "virtual_host",
		URL:                           URLs["virtual_hosts"],
		Message:                       &route.VirtualHost{},
		DownstreamFiltersFunc:         downstreamfilters.VirtualHostDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(VirtualHost.String()),
		TypedConfigPaths:              VirtualHostTypedConfigPaths,
		UpstreamPaths:                 VirtualHostUpstreams,
	},
	TCPProxy: {
		PrettyName:                    "Tcp Proxy",
		Collection:                    "filters",
		Type:                          "network_filter",
		CanonicalName:                 "envoy.filters.network.tcp_proxy",
		Category:                      "envoy.filters.network",
		URL:                           URLs["tcp_proxy"],
		Message:                       &tcp.TcpProxy{},
		DownstreamFiltersFunc:         downstreamfilters.DownstreamTypedFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(TCPProxy.String()),
		TypedConfigPaths:              GeneralAccessLogTypedConfigPaths,
		UpstreamPaths:                 TCPProxyUpstreams,
	},
	FluentdAccessLog: {
		PrettyName:                    "Access Log(Fluentd)",
		Collection:                    "extensions",
		Type:                          "access_log",
		CanonicalName:                 "envoy.access_loggers.fluentd",
		Category:                      "envoy.access_loggers",
		URL:                           URLs["access_log"],
		Message:                       &al_fluentd.FluentdAccessLogConfig{},
		DownstreamFiltersFunc:         downstreamfilters.ALSDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(FluentdAccessLog.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 FluentdAccessLogUpstreams,
	},
	FileAccessLog: {
		PrettyName:                    "Access Log(File)",
		Collection:                    "extensions",
		Type:                          "access_log",
		CanonicalName:                 "envoy.access_loggers.file",
		Category:                      "envoy.access_loggers",
		URL:                           URLs["access_log"],
		Message:                       &al_file.FileAccessLog{},
		DownstreamFiltersFunc:         downstreamfilters.ALSDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(FileAccessLog.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	StdoutAccessLog: {
		PrettyName:                    "Access Log(StdOut)",
		Collection:                    "extensions",
		Type:                          "access_log",
		CanonicalName:                 "envoy.access_loggers.stdout",
		Category:                      "envoy.access_loggers",
		URL:                           URLs["access_log"],
		Message:                       &al_stream.StdoutAccessLog{},
		DownstreamFiltersFunc:         downstreamfilters.ALSDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(StdoutAccessLog.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	StdErrAccessLog: {
		PrettyName:                    "Access Log(StdErr)",
		Collection:                    "extensions",
		Type:                          "access_log",
		CanonicalName:                 "envoy.access_loggers.stderr",
		Category:                      "envoy.access_loggers",
		URL:                           URLs["access_log"],
		Message:                       &al_stream.StderrAccessLog{},
		DownstreamFiltersFunc:         downstreamfilters.ALSDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(StdErrAccessLog.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	DownstreamTLSContext: {
		PrettyName:                    "Downstream TLS",
		Collection:                    "tls",
		Type:                          "tls",
		CanonicalName:                 "envoy.transport_sockets.downstream",
		Category:                      "envoy.transport_sockets.tls",
		URL:                           URLs["tls"],
		Message:                       &tls.DownstreamTlsContext{},
		DownstreamFiltersFunc:         downstreamfilters.DownstreamTypedFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(DownstreamTLSContext.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 DownstreamTLSContextUpstreams,
	},
	UpstreamTLSContext: {
		PrettyName:                    "Upstream TLS",
		Collection:                    "tls",
		Type:                          "secret",
		CanonicalName:                 "envoy.transport_sockets.upstream",
		Category:                      "envoy.transport_sockets.tls",
		URL:                           URLs["tls"],
		Message:                       &tls.UpstreamTlsContext{},
		DownstreamFiltersFunc:         downstreamfilters.UpstreamTLSDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(UpstreamTLSContext.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 UpstreamTLSContextUpstreams,
	},
	TLSCertificate: {
		PrettyName:                    "TLS Certificate",
		Collection:                    "secrets",
		Type:                          "secret",
		CanonicalName:                 "envoy.transport_sockets.tls_certificate",
		Category:                      "envoy.transport_sockets.tls",
		URL:                           URLs["secrets"],
		Message:                       &tls.TlsCertificate{},
		DownstreamFiltersFunc:         downstreamfilters.TLSCertificateDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(TLSCertificate.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	CertificateValidationContext: {
		PrettyName:                    "Certificate Validation",
		Collection:                    "secrets",
		Type:                          "secret",
		CanonicalName:                 "envoy.transport_sockets.validation_context",
		Category:                      "envoy.transport_sockets.tls",
		URL:                           URLs["secrets"],
		Message:                       &tls.CertificateValidationContext{},
		DownstreamFiltersFunc:         downstreamfilters.ContextValidateDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(CertificateValidationContext.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	HealthCheckEventFileSink: {
		PrettyName:                    "Health Check Event File Sink",
		Collection:                    "extensions",
		Type:                          "hcefs",
		CanonicalName:                 "envoy.health_check.event_sinks",
		Category:                      "envoy.health_check.event_sinks",
		URL:                           URLs["hcefs"],
		Message:                       &hcefs.HealthCheckEventFileSink{},
		DownstreamFiltersFunc:         downstreamfilters.HCEFSDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(HealthCheckEventFileSink.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	URITemplateMatch: {
		PrettyName:                    "Uri Template Match",
		Collection:                    "extensions",
		Type:                          "utm",
		CanonicalName:                 "envoy.path.match.uri_template.uri_template_matcher",
		Category:                      "envoy.path.match.uri_template.uri_template_matcher",
		URL:                           URLs["utm"],
		Message:                       &utm.UriTemplateMatchConfig{},
		DownstreamFiltersFunc:         downstreamfilters.UTMDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(URITemplateMatch.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	BasicAuth: {
		PrettyName:                    "Basic Auth",
		Collection:                    "filters",
		Type:                          "http_filter",
		CanonicalName:                 "envoy.filters.http.basic_auth",
		Category:                      "envoy.filters.http",
		URL:                           URLs["basic_auth"],
		Message:                       &basic_auth.BasicAuth{},
		DownstreamFiltersFunc:         downstreamfilters.ConfigDiscoveryHTTPFilterDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(BasicAuth.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	BasicAuthPerRoute: {
		PrettyName:                    "Basic Auth Per Route",
		Collection:                    "filters",
		Type:                          "http_filter",
		CanonicalName:                 "envoy.filters.http.basic_auth",
		Category:                      "envoy.filters.http",
		URL:                           URLs["basic_auth"],
		Message:                       &basic_auth.BasicAuthPerRoute{},
		DownstreamFiltersFunc:         downstreamfilters.TypedHTTPFilterDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(BasicAuthPerRoute.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	Cors: {
		PrettyName:                    "Cors",
		Collection:                    "filters",
		Type:                          "http_filter",
		CanonicalName:                 "envoy.filters.http.cors",
		Category:                      "envoy.filters.http",
		URL:                           URLs["cors"],
		Message:                       &cors.Cors{},
		DownstreamFiltersFunc:         downstreamfilters.ConfigDiscoveryHTTPFilterDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(Cors.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	CorsPolicy: {
		PrettyName:                    "Cors Policy",
		Collection:                    "filters",
		Type:                          "http_filter",
		CanonicalName:                 "envoy.filters.http.cors",
		Category:                      "envoy.filters.http",
		URL:                           URLs["cors"],
		Message:                       &cors.CorsPolicy{},
		DownstreamFiltersFunc:         downstreamfilters.TypedHTTPFilterDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(CorsPolicy.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	BandwidthLimit: {
		PrettyName:                    "Bandwidth Limit",
		Collection:                    "filters",
		Type:                          "http_filter",
		CanonicalName:                 "envoy.filters.http.bandwidth_limit",
		Category:                      "envoy.filters.http",
		URL:                           URLs["bandwidth_limit"],
		Message:                       &bandwidth_limit.BandwidthLimit{},
		DownstreamFiltersFunc:         downstreamfilters.DiscoverAndTypedHTTPFilterDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(BandwidthLimit.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	Compressor: {
		PrettyName:                    "Compressor",
		Collection:                    "filters",
		Type:                          "http_filter",
		CanonicalName:                 "envoy.filters.http.compressor",
		Category:                      "envoy.filters.http",
		URL:                           URLs["compressor"],
		Message:                       &compressor.Compressor{},
		DownstreamFiltersFunc:         downstreamfilters.ConfigDiscoveryHTTPFilterDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(Compressor.String()),
		TypedConfigPaths:              CompressorTypedConfigPaths,
		UpstreamPaths:                 nil,
	},
	CompressorPerRoute: {
		PrettyName:                    "Compressor Per Route",
		Collection:                    "filters",
		Type:                          "http_filter",
		CanonicalName:                 "envoy.filters.http.compressor",
		Category:                      "envoy.filters.http",
		URL:                           URLs["compressor"],
		Message:                       &compressor.CompressorPerRoute{},
		DownstreamFiltersFunc:         downstreamfilters.TypedHTTPFilterDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(CompressorPerRoute.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	GzipCompressor: {
		PrettyName:                    "Gzip Compressor",
		Collection:                    "extensions",
		Type:                          "compressor_library",
		CanonicalName:                 "envoy.compression.compressor",
		Category:                      "envoy.compression.compressor",
		URL:                           URLs["compressor_library"],
		Message:                       &gzip_compressor.Gzip{},
		DownstreamFiltersFunc:         downstreamfilters.TypedConfigDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(GzipCompressor.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	BrotliCompressor: {
		PrettyName:                    "Brotli Compressor",
		Collection:                    "extensions",
		Type:                          "compressor_library",
		CanonicalName:                 "envoy.compression.compressor",
		Category:                      "envoy.compression.compressor",
		URL:                           URLs["compressor_library"],
		Message:                       &brotli_compressor.Brotli{},
		DownstreamFiltersFunc:         downstreamfilters.TypedConfigDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(BrotliCompressor.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	ZstdCompressor: {
		PrettyName:                    "Zstd Compressor",
		Collection:                    "extensions",
		Type:                          "compressor_library",
		CanonicalName:                 "envoy.compression.compressor",
		Category:                      "envoy.compression.compressor",
		URL:                           URLs["compressor_library"],
		Message:                       &zstd_compressor.Zstd{},
		DownstreamFiltersFunc:         downstreamfilters.TypedConfigDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(ZstdCompressor.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	HTTPProtocolOptions: {
		PrettyName:                    "Http Protocol Options",
		Collection:                    "extensions",
		Type:                          "http_protocol_options",
		CanonicalName:                 "envoy.upstreams.http.http_protocol_options",
		Category:                      "envoy.upstreams.http.http_protocol_options",
		URL:                           URLs["http_protocol_options"],
		Message:                       &http_protocol_options.HttpProtocolOptions{},
		DownstreamFiltersFunc:         downstreamfilters.TypedHTTPProtocolDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(HTTPProtocolOptions.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	Lua: {
		PrettyName:                    "Lua",
		Collection:                    "filters",
		Type:                          "http_filter",
		CanonicalName:                 "envoy.filters.http.lua",
		Category:                      "envoy.filters.http",
		URL:                           URLs["lua"],
		Message:                       &lua.Lua{},
		DownstreamFiltersFunc:         downstreamfilters.ConfigDiscoveryHTTPFilterDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(Lua.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	LuaPerRoute: {
		PrettyName:                    "Lua Per Route",
		Collection:                    "filters",
		Type:                          "http_filter",
		CanonicalName:                 "envoy.filters.http.lua",
		Category:                      "envoy.filters.http",
		URL:                           URLs["lua"],
		Message:                       &lua.LuaPerRoute{},
		DownstreamFiltersFunc:         downstreamfilters.TypedHTTPFilterDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(LuaPerRoute.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	Buffer: {
		PrettyName:                    "Buffer",
		Collection:                    "filters",
		Type:                          "http_filter",
		CanonicalName:                 "envoy.filters.http.buffer",
		Category:                      "envoy.filters.http",
		URL:                           URLs["buffer"],
		Message:                       &buffer.Buffer{},
		DownstreamFiltersFunc:         downstreamfilters.ConfigDiscoveryHTTPFilterDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(Buffer.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	BufferPerRoute: {
		PrettyName:                    "Buffer Per Route",
		Collection:                    "filters",
		Type:                          "http_filter",
		CanonicalName:                 "envoy.filters.http.buffer",
		Category:                      "envoy.filters.http",
		URL:                           URLs["buffer"],
		Message:                       &buffer.BufferPerRoute{},
		DownstreamFiltersFunc:         downstreamfilters.TypedHTTPFilterDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(BufferPerRoute.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	AdaptiveConcurrency: {
		PrettyName:                    "Adaptive Concurrency",
		Collection:                    "filters",
		Type:                          "http_filter",
		CanonicalName:                 "envoy.filters.http.adaptive_concurrency",
		Category:                      "envoy.filters.http",
		URL:                           URLs["adaptive_concurrency"],
		Message:                       &adaptive_concurrency.AdaptiveConcurrency{},
		DownstreamFiltersFunc:         downstreamfilters.DiscoverAndTypedHTTPFilterDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(AdaptiveConcurrency.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	AdmissionControl: {
		PrettyName:                    "Admission Control",
		Collection:                    "filters",
		Type:                          "http_filter",
		CanonicalName:                 "envoy.filters.http.admission_control",
		Category:                      "envoy.filters.http",
		URL:                           URLs["admission_control"],
		Message:                       &admission_control.AdmissionControl{},
		DownstreamFiltersFunc:         downstreamfilters.ConfigDiscoveryHTTPFilterDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(AdmissionControl.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	CookieBasedSessionState: {
		PrettyName:                    "Cookie Based Session State",
		Collection:                    "extensions",
		Type:                          "session_state",
		CanonicalName:                 "envoy.http.stateful_session.cookie",
		Category:                      "envoy.http.stateful_session.cookie",
		URL:                           URLs["session_state"],
		Message:                       &stateful_session_cookie.CookieBasedSessionState{},
		DownstreamFiltersFunc:         downstreamfilters.TypedConfigDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(CookieBasedSessionState.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	HeaderBasedSessionState: {
		PrettyName:                    "Header Based Session State",
		Collection:                    "extensions",
		Type:                          "session_state",
		CanonicalName:                 "envoy.http.stateful_session.header",
		Category:                      "envoy.http.stateful_session.header",
		URL:                           URLs["session_state"],
		Message:                       &stateful_session_header.HeaderBasedSessionState{},
		DownstreamFiltersFunc:         downstreamfilters.TypedConfigDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(HeaderBasedSessionState.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	StatefulSession: {
		PrettyName:                    "Stateful Session",
		Collection:                    "filters",
		Type:                          "http_filter",
		CanonicalName:                 "envoy.filters.http.stateful_session",
		Category:                      "envoy.filters.http",
		URL:                           URLs["stateful_session"],
		Message:                       &stateful_session.StatefulSession{},
		DownstreamFiltersFunc:         downstreamfilters.ConfigDiscoveryHTTPFilterDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(StatefulSession.String()),
		TypedConfigPaths:              StatefulSessionTypedConfigPaths,
		UpstreamPaths:                 nil,
	},
	StatefulSessionPerRoute: {
		PrettyName:                    "Stateful Session Per Route",
		Collection:                    "filters",
		Type:                          "http_filter",
		CanonicalName:                 "envoy.filters.http.stateful_session",
		Category:                      "envoy.filters.http",
		URL:                           URLs["stateful_session"],
		Message:                       &stateful_session.StatefulSessionPerRoute{},
		DownstreamFiltersFunc:         downstreamfilters.TypedHTTPFilterDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(StatefulSessionPerRoute.String()),
		TypedConfigPaths:              StatefulSessionPerRouteTypedConfigPaths,
		UpstreamPaths:                 nil,
	},
	CsrfPolicy: {
		PrettyName:                    "Csrf Policy",
		Collection:                    "filters",
		Type:                          "http_filter",
		CanonicalName:                 "envoy.filters.http.csrf",
		Category:                      "envoy.filters.http",
		URL:                           URLs["csrf_policy"],
		Message:                       &csrf_policy.CsrfPolicy{},
		DownstreamFiltersFunc:         downstreamfilters.ConfigDiscoveryHTTPFilterDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(CsrfPolicy.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	ListenerLocalRatelimit: {
		PrettyName:                    "Local Ratelimit",
		Collection:                    "filters",
		Type:                          "listener_filter",
		CanonicalName:                 "envoy.filters.listener.local_ratelimit",
		Category:                      "envoy.filters.listener",
		URL:                           URLs["l_local_ratelimit"],
		Message:                       &l_local_ratelimit.LocalRateLimit{},
		DownstreamFiltersFunc:         downstreamfilters.DownstreamTypedFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(ListenerLocalRatelimit.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	ListenerHTTPInspector: {
		PrettyName:                    "Http Inspector",
		Collection:                    "filters",
		Type:                          "listener_filter",
		CanonicalName:                 "envoy.filters.listener.http_inspector",
		Category:                      "envoy.filters.listener",
		URL:                           URLs["l_http_inspector"],
		Message:                       &l_http_inspector.HttpInspector{},
		DownstreamFiltersFunc:         downstreamfilters.DownstreamTypedFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(ListenerHTTPInspector.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	ListenerOriginalDst: {
		PrettyName:                    "Original Dst",
		Collection:                    "filters",
		Type:                          "listener_filter",
		CanonicalName:                 "envoy.filters.listener.original_dst",
		Category:                      "envoy.filters.listener",
		URL:                           URLs["l_original_dst"],
		Message:                       &l_original_dst.OriginalDst{},
		DownstreamFiltersFunc:         downstreamfilters.DownstreamTypedFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(ListenerOriginalDst.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	ListenerOriginalSrc: {
		PrettyName:                    "Original Src",
		Collection:                    "filters",
		Type:                          "listener_filter",
		CanonicalName:                 "envoy.filters.listener.original_src",
		Category:                      "envoy.filters.listener",
		URL:                           URLs["l_original_src"],
		Message:                       &l_original_src.OriginalSrc{},
		DownstreamFiltersFunc:         downstreamfilters.DownstreamTypedFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(ListenerOriginalSrc.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	ListenerTLSInspector: {
		PrettyName:                    "TLS Inspector",
		Collection:                    "filters",
		Type:                          "listener_filter",
		CanonicalName:                 "envoy.filters.listener.tls_inspector",
		Category:                      "envoy.filters.listener",
		URL:                           URLs["l_tls_inspector"],
		Message:                       &l_tls_inspector.TlsInspector{},
		DownstreamFiltersFunc:         downstreamfilters.DownstreamTypedFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(ListenerTLSInspector.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	ListenerDNSFilter: {
		PrettyName:                    "DNS Filter",
		Collection:                    "filters",
		Type:                          "udp_filter",
		CanonicalName:                 "envoy.filters.udp.dns_filter",
		Category:                      "envoy.filters.udp_listener",
		URL:                           URLs["l_dns_filter"],
		Message:                       &l_dns_filter.DnsFilterConfig{},
		DownstreamFiltersFunc:         downstreamfilters.DownstreamTypedFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(ListenerDNSFilter.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	ListeneProxyProtocol: {
		PrettyName:                    "Proxy Protocol",
		Collection:                    "filters",
		Type:                          "listener_filter",
		CanonicalName:                 "envoy.filters.listener.proxy_protocol",
		Category:                      "envoy.filters.listener",
		URL:                           URLs["l_proxy_protocol"],
		Message:                       &l_proxy_protocol.ProxyProtocol{},
		DownstreamFiltersFunc:         downstreamfilters.DownstreamTypedFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(ListeneProxyProtocol.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	ConnectionLimit: {
		PrettyName:                    "Connection Limit",
		Collection:                    "filters",
		Type:                          "network_filter",
		CanonicalName:                 "envoy.filters.network.connection_limit",
		Category:                      "envoy.filters.network",
		URL:                           URLs["connection_limit"],
		Message:                       &connection_limit.ConnectionLimit{},
		DownstreamFiltersFunc:         downstreamfilters.DownstreamTypedFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(ConnectionLimit.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	NetworkLocalRatelimit: {
		PrettyName:                    "Local Ratelimit",
		Collection:                    "filters",
		Type:                          "network_filter",
		CanonicalName:                 "envoy.filters.network.local_ratelimit",
		Category:                      "envoy.filters.network",
		URL:                           URLs["n_local_ratelimit"],
		Message:                       &n_local_ratelimit.LocalRateLimit{},
		DownstreamFiltersFunc:         downstreamfilters.DownstreamTypedFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(NetworkLocalRatelimit.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	HTTPLocalRatelimit: {
		PrettyName:                    "Local Ratelimit",
		Collection:                    "filters",
		Type:                          "http_filter",
		CanonicalName:                 "envoy.filters.http.local_ratelimit",
		Category:                      "envoy.filters.http",
		URL:                           URLs["h_local_ratelimit"],
		Message:                       &h_local_ratelimit.LocalRateLimit{},
		DownstreamFiltersFunc:         downstreamfilters.DiscoverAndTypedHTTPFilterDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(HTTPLocalRatelimit.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	GenericSecret: {
		PrettyName:                    "Generic Secret",
		Collection:                    "secrets",
		Type:                          "secret",
		CanonicalName:                 "envoy.transport_sockets.GenericSecret",
		Category:                      "envoy.transport_sockets.tls",
		URL:                           URLs["secrets"],
		Message:                       &tls.GenericSecret{},
		DownstreamFiltersFunc:         downstreamfilters.TLSCertificateDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(GenericSecret.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	TLSSessionTicketKeys: {
		PrettyName:                    "TLS Session Ticket Keys",
		Collection:                    "secrets",
		Type:                          "secret",
		CanonicalName:                 "envoy.transport_sockets.TlsSessionTicketKeys",
		Category:                      "envoy.transport_sockets.tls",
		URL:                           URLs["secrets"],
		Message:                       &tls.TlsSessionTicketKeys{},
		DownstreamFiltersFunc:         downstreamfilters.TLSCertificateDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(TLSSessionTicketKeys.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	OAuth2: {
		PrettyName:                    "OAuth2",
		Collection:                    "filters",
		Type:                          "http_filter",
		CanonicalName:                 "envoy.filters.http.oauth2",
		Category:                      "envoy.filters.http",
		URL:                           URLs["oauth2"],
		Message:                       &oauth2.OAuth2{},
		DownstreamFiltersFunc:         downstreamfilters.ConfigDiscoveryHTTPFilterDownstreamFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(OAuth2.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 nil,
	},
	OpenTelemetry: {
		PrettyName:                    "Open Telemetry",
		Collection:                    "extensions",
		Type:                          "stat_sinks",
		CanonicalName:                 "envoy.stat_sinks.open_telemetry",
		Category:                      "envoy.stats_sinks",
		URL:                           URLs["stat_sinks"],
		Message:                       &stat_sink_otel.SinkConfig{},
		DownstreamFiltersFunc:         downstreamfilters.TypedConfigDownstreamBootstrapFilters,
		TemplateDownstreamFiltersFunc: createTemplateFilterFunc(OpenTelemetry.String()),
		TypedConfigPaths:              nil,
		UpstreamPaths:                 GenericGRPCServiceUpstreams,
	},
}

func (gt GType) String() string {
	return string(gt)
}

func (gt GType) CollectionString() string {
	if mapping, exists := gTypeMappings[gt]; exists {
		return mapping.Collection
	}
	return unknown
}

func (gt GType) URL() string {
	if mapping, exists := gTypeMappings[gt]; exists {
		return mapping.URL
	}
	return unknown
}

func (gt GType) PrettyName() string {
	if mapping, exists := gTypeMappings[gt]; exists {
		return mapping.PrettyName
	}
	return unknown
}

func (gt GType) ProtoMessage() proto.Message {
	if mapping, exists := gTypeMappings[gt]; exists {
		return mapping.Message
	}
	return &anypb.Any{}
}

func (gt GType) DownstreamFilters(dfm downstreamfilters.DownstreamFilter) []downstreamfilters.MongoFilters {
	if mapping, exists := gTypeMappings[gt]; exists && mapping.DownstreamFiltersFunc != nil {
		return mapping.DownstreamFiltersFunc(dfm)
	}
	return nil
}

func (gt GType) TemplateDownstreamFilters(name, project, version string) []downstreamfilters.MongoFilters {
	if mapping, exists := gTypeMappings[gt]; exists && mapping.TemplateDownstreamFiltersFunc != nil {
		return mapping.TemplateDownstreamFiltersFunc(name, project, version)
	}
	return nil
}

func (gt GType) TypedConfigPaths() []TypedConfigPath {
	if mapping, exists := gTypeMappings[gt]; exists && mapping.TypedConfigPaths != nil {
		return mapping.TypedConfigPaths
	}
	return nil
}

func (gt GType) UpstreamPaths() map[string]GType {
	if mapping, exists := gTypeMappings[gt]; exists && mapping.UpstreamPaths != nil {
		return mapping.UpstreamPaths
	}
	return nil
}

func (gt GType) Validate() map[string]GType {
	if mapping, exists := gTypeMappings[gt]; exists && mapping.UpstreamPaths != nil {
		return mapping.UpstreamPaths
	}
	return nil
}

func (gt GType) Type() string {
	if mapping, exists := gTypeMappings[gt]; exists {
		return mapping.Type
	}
	return unknown
}

func (gt GType) CanonicalName() string {
	if mapping, exists := gTypeMappings[gt]; exists {
		return mapping.CanonicalName
	}
	return unknown
}

func (gt GType) Category() string {
	if mapping, exists := gTypeMappings[gt]; exists {
		return mapping.Category
	}
	return unknown
}
