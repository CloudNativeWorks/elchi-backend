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
	Collection            string
	URL                   string
	PrettyName            string
	Message               proto.Message
	DownstreamFiltersFunc func(downstreamfilters.DownstreamFilter) []downstreamfilters.MongoFilters
	TypedConfigPaths      []TypedConfigPath
	UpstreamPaths         map[string]GTypes
}

const unknown = "unknown"

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
	"tls":                   "/resource/tls/",
	"stat_sinks":            "/extensions/stat_sinks/",
}

var gTypeMappings = map[GTypes]GTypeMapping{
	BootStrap: {
		PrettyName:            "Bootstrap",
		Collection:            "bootstrap",
		URL:                   URLs["bootstrap"],
		Message:               &bootstrap.Bootstrap{},
		DownstreamFiltersFunc: nil,
		TypedConfigPaths:      BootstrapTypedConfigPaths,
		UpstreamPaths:         BootstrapUpstreams,
	},
	HTTPConnectionManager: {
		PrettyName:            "Http Connection Manager",
		Collection:            "filters",
		URL:                   URLs["hcm"],
		Message:               &hcm.HttpConnectionManager{},
		DownstreamFiltersFunc: downstreamfilters.DownstreamTypedFilters,
		TypedConfigPaths:      HTTPConnectionManagerTypedConfigPaths,
		UpstreamPaths:         HTTPConnectionManagerUpstreams,
	},
	RBAC: {
		PrettyName:            "RBAC",
		Collection:            "filters",
		URL:                   URLs["n_rbac"],
		Message:               &n_rbac.RBAC{},
		DownstreamFiltersFunc: downstreamfilters.DownstreamTypedFilters,
		TypedConfigPaths:      RBACTypedConfigPaths,
		UpstreamPaths:         nil,
	},
	HTTPRBAC: {
		PrettyName:            "Http RBAC",
		Collection:            "filters",
		URL:                   URLs["h_rbac"],
		Message:               &h_rbac.RBAC{},
		DownstreamFiltersFunc: downstreamfilters.ConfigDiscoveryHTTPFilterDownstreamFilters,
		TypedConfigPaths:      RBACTypedConfigPaths,
		UpstreamPaths:         nil,
	},
	HTTPRBACPerRoute: {
		PrettyName:            "Http RBAC Per Route",
		Collection:            "filters",
		URL:                   URLs["h_rbac"],
		Message:               &h_rbac.RBACPerRoute{},
		DownstreamFiltersFunc: downstreamfilters.TypedHTTPFilterDownstreamFilters,
		TypedConfigPaths:      RBACPerRouteTypedConfigPaths,
		UpstreamPaths:         nil,
	},
	Router: {
		PrettyName:            "Router",
		Collection:            "filters",
		URL:                   URLs["http_router"],
		Message:               &router.Router{},
		DownstreamFiltersFunc: downstreamfilters.ConfigDiscoveryHTTPFilterDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	Cluster: {
		PrettyName:            "Cluster",
		Collection:            "clusters",
		URL:                   URLs["clusters"],
		Message:               &cluster.Cluster{},
		DownstreamFiltersFunc: downstreamfilters.ClusterDownstreamFilters,
		TypedConfigPaths:      ClusterTypedConfigPaths,
		UpstreamPaths:         ClusterUpstreams,
	},
	Listener: {
		PrettyName:            "Listener",
		Collection:            "listeners",
		URL:                   URLs["listeners"],
		Message:               &listener.Listener{},
		DownstreamFiltersFunc: nil,
		TypedConfigPaths:      ListenerTypedConfigPaths,
		UpstreamPaths:         nil,
	},
	Endpoint: {
		PrettyName:            "Endpoint",
		Collection:            "endpoints",
		URL:                   URLs["endpoints"],
		Message:               &endpoint.ClusterLoadAssignment{},
		DownstreamFiltersFunc: downstreamfilters.EdsDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	Route: {
		PrettyName:            "Route",
		Collection:            "routes",
		URL:                   URLs["routes"],
		Message:               &route.RouteConfiguration{},
		DownstreamFiltersFunc: downstreamfilters.RouteDownstreamFilters,
		TypedConfigPaths:      RouteTypedConfigPaths,
		UpstreamPaths:         RouteUpstreams,
	},
	VirtualHost: {
		PrettyName:            "Virtual Host",
		Collection:            "virtual_hosts",
		URL:                   URLs["virtual_hosts"],
		Message:               &route.VirtualHost{},
		DownstreamFiltersFunc: downstreamfilters.VirtualHostDownstreamFilters,
		TypedConfigPaths:      VirtualHostTypedConfigPaths,
		UpstreamPaths:         VirtualHostUpstreams,
	},
	TCPProxy: {
		PrettyName:            "Tcp Proxy",
		Collection:            "filters",
		URL:                   URLs["tcp_proxy"],
		Message:               &tcp.TcpProxy{},
		DownstreamFiltersFunc: downstreamfilters.DownstreamTypedFilters,
		TypedConfigPaths:      GeneralAccessLogTypedConfigPaths,
		UpstreamPaths:         TCPProxyUpstreams,
	},
	FluentdAccessLog: {
		PrettyName:            "Access Log(Fluentd)",
		Collection:            "extensions",
		URL:                   URLs["access_log"],
		Message:               &al_fluentd.FluentdAccessLogConfig{},
		DownstreamFiltersFunc: downstreamfilters.ALSDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         FluentdAccessLogUpstreams,
	},
	FileAccessLog: {
		PrettyName:            "Access Log(File)",
		Collection:            "extensions",
		URL:                   URLs["access_log"],
		Message:               &al_file.FileAccessLog{},
		DownstreamFiltersFunc: downstreamfilters.ALSDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	StdoutAccessLog: {
		PrettyName:            "Access Log(StdOut)",
		Collection:            "extensions",
		URL:                   URLs["access_log"],
		Message:               &al_stream.StdoutAccessLog{},
		DownstreamFiltersFunc: downstreamfilters.ALSDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	StdErrAccessLog: {
		PrettyName:            "Access Log(StdErr)",
		Collection:            "extensions",
		URL:                   URLs["access_log"],
		Message:               &al_stream.StderrAccessLog{},
		DownstreamFiltersFunc: downstreamfilters.ALSDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	DownstreamTLSContext: {
		PrettyName:            "Downstream TLS",
		Collection:            "tls",
		URL:                   URLs["tls"],
		Message:               &tls.DownstreamTlsContext{},
		DownstreamFiltersFunc: downstreamfilters.DownstreamTypedFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         DownstreamTLSContextUpstreams,
	},
	UpstreamTLSContext: {
		PrettyName:            "Upstream TLS",
		Collection:            "tls",
		URL:                   URLs["tls"],
		Message:               &tls.UpstreamTlsContext{},
		DownstreamFiltersFunc: downstreamfilters.UpstreamTLSDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         UpstreamTLSContextUpstreams,
	},
	TLSCertificate: {
		PrettyName:            "TLS Certificate",
		Collection:            "secrets",
		URL:                   URLs["secrets"],
		Message:               &tls.TlsCertificate{},
		DownstreamFiltersFunc: downstreamfilters.TLSCertificateDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	CertificateValidationContext: {
		PrettyName:            "Certificate Validation",
		Collection:            "secrets",
		URL:                   URLs["secrets"],
		Message:               &tls.CertificateValidationContext{},
		DownstreamFiltersFunc: downstreamfilters.ContextValidateDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	HealthCheckEventFileSink: {
		PrettyName:            "Health Check Event File Sink",
		Collection:            "extensions",
		URL:                   URLs["hcefs"],
		Message:               &hcefs.HealthCheckEventFileSink{},
		DownstreamFiltersFunc: downstreamfilters.HCEFSDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	URITemplateMatch: {
		PrettyName:            "Uri Template Match",
		Collection:            "extensions",
		URL:                   URLs["utm"],
		Message:               &utm.UriTemplateMatchConfig{},
		DownstreamFiltersFunc: downstreamfilters.UTMDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	BasicAuth: {
		PrettyName:            "Basic Auth",
		Collection:            "filters",
		URL:                   URLs["basic_auth"],
		Message:               &basic_auth.BasicAuth{},
		DownstreamFiltersFunc: downstreamfilters.ConfigDiscoveryHTTPFilterDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	BasicAuthPerRoute: {
		PrettyName:            "Basic Auth Per Route",
		Collection:            "filters",
		URL:                   URLs["basic_auth"],
		Message:               &basic_auth.BasicAuthPerRoute{},
		DownstreamFiltersFunc: downstreamfilters.TypedHTTPFilterDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	Cors: {
		PrettyName:            "Cors",
		Collection:            "filters",
		URL:                   URLs["cors"],
		Message:               &cors.Cors{},
		DownstreamFiltersFunc: downstreamfilters.ConfigDiscoveryHTTPFilterDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	CorsPolicy: {
		PrettyName:            "Cors Policy",
		Collection:            "filters",
		URL:                   URLs["cors"],
		Message:               &cors.CorsPolicy{},
		DownstreamFiltersFunc: downstreamfilters.TypedHTTPFilterDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	BandwidthLimit: {
		PrettyName:            "Bandwidth Limit",
		Collection:            "filters",
		URL:                   URLs["bandwidth_limit"],
		Message:               &bandwidth_limit.BandwidthLimit{},
		DownstreamFiltersFunc: downstreamfilters.DiscoverAndTypedHTTPFilterDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	Compressor: {
		PrettyName:            "Compressor",
		Collection:            "filters",
		URL:                   URLs["compressor"],
		Message:               &compressor.Compressor{},
		DownstreamFiltersFunc: downstreamfilters.ConfigDiscoveryHTTPFilterDownstreamFilters,
		TypedConfigPaths:      CompressorTypedConfigPaths,
		UpstreamPaths:         nil,
	},
	CompressorPerRoute: {
		PrettyName:            "Compressor Per Route",
		Collection:            "filters",
		URL:                   URLs["compressor"],
		Message:               &compressor.CompressorPerRoute{},
		DownstreamFiltersFunc: downstreamfilters.TypedHTTPFilterDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	GzipCompressor: {
		PrettyName:            "Gzip Compressor",
		Collection:            "extensions",
		URL:                   URLs["compressor_library"],
		Message:               &gzip_compressor.Gzip{},
		DownstreamFiltersFunc: downstreamfilters.TypedConfigDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	BrotliCompressor: {
		PrettyName:            "Brotli Compressor",
		Collection:            "extensions",
		URL:                   URLs["compressor_library"],
		Message:               &brotli_compressor.Brotli{},
		DownstreamFiltersFunc: downstreamfilters.TypedConfigDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	ZstdCompressor: {
		PrettyName:            "Zstd Compressor",
		Collection:            "extensions",
		URL:                   URLs["compressor_library"],
		Message:               &zstd_compressor.Zstd{},
		DownstreamFiltersFunc: downstreamfilters.TypedConfigDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	HTTPProtocolOptions: {
		PrettyName:            "Http Protocol Options",
		Collection:            "extensions",
		URL:                   URLs["http_protocol_options"],
		Message:               &http_protocol_options.HttpProtocolOptions{},
		DownstreamFiltersFunc: downstreamfilters.TypedHTTPProtocolDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	Lua: {
		PrettyName:            "Lua",
		Collection:            "filters",
		URL:                   URLs["lua"],
		Message:               &lua.Lua{},
		DownstreamFiltersFunc: downstreamfilters.ConfigDiscoveryHTTPFilterDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	LuaPerRoute: {
		PrettyName:            "Lua Per Route",
		Collection:            "filters",
		URL:                   URLs["lua"],
		Message:               &lua.LuaPerRoute{},
		DownstreamFiltersFunc: downstreamfilters.TypedHTTPFilterDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	Buffer: {
		PrettyName:            "Buffer",
		Collection:            "filters",
		URL:                   URLs["buffer"],
		Message:               &buffer.Buffer{},
		DownstreamFiltersFunc: downstreamfilters.ConfigDiscoveryHTTPFilterDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	BufferPerRoute: {
		PrettyName:            "Buffer Per Route",
		Collection:            "filters",
		URL:                   URLs["buffer"],
		Message:               &buffer.BufferPerRoute{},
		DownstreamFiltersFunc: downstreamfilters.TypedHTTPFilterDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	AdaptiveConcurrency: {
		PrettyName:            "Adaptive Concurrency",
		Collection:            "filters",
		URL:                   URLs["adaptive_concurrency"],
		Message:               &adaptive_concurrency.AdaptiveConcurrency{},
		DownstreamFiltersFunc: downstreamfilters.DiscoverAndTypedHTTPFilterDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	AdmissionControl: {
		PrettyName:            "Admission Control",
		Collection:            "filters",
		URL:                   URLs["admission_control"],
		Message:               &admission_control.AdmissionControl{},
		DownstreamFiltersFunc: downstreamfilters.ConfigDiscoveryHTTPFilterDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	CookieBasedSessionState: {
		PrettyName:            "Cookie Based Session State",
		Collection:            "extensions",
		URL:                   URLs["session_state"],
		Message:               &stateful_session_cookie.CookieBasedSessionState{},
		DownstreamFiltersFunc: downstreamfilters.TypedConfigDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	HeaderBasedSessionState: {
		PrettyName:            "Header Based Session State",
		Collection:            "extensions",
		URL:                   URLs["session_state"],
		Message:               &stateful_session_header.HeaderBasedSessionState{},
		DownstreamFiltersFunc: downstreamfilters.TypedConfigDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	StatefulSession: {
		PrettyName:            "Stateful Session",
		Collection:            "filters",
		URL:                   URLs["stateful_session"],
		Message:               &stateful_session.StatefulSession{},
		DownstreamFiltersFunc: downstreamfilters.ConfigDiscoveryHTTPFilterDownstreamFilters,
		TypedConfigPaths:      StatefulSessionTypedConfigPaths,
		UpstreamPaths:         nil,
	},
	StatefulSessionPerRoute: {
		PrettyName:            "Stateful Session Per Route",
		Collection:            "filters",
		URL:                   URLs["stateful_session"],
		Message:               &stateful_session.StatefulSessionPerRoute{},
		DownstreamFiltersFunc: downstreamfilters.TypedHTTPFilterDownstreamFilters,
		TypedConfigPaths:      StatefulSessionPerRouteTypedConfigPaths,
		UpstreamPaths:         nil,
	},
	CsrfPolicy: {
		PrettyName:            "Csrf Policy",
		Collection:            "filters",
		URL:                   URLs["csrf_policy"],
		Message:               &csrf_policy.CsrfPolicy{},
		DownstreamFiltersFunc: downstreamfilters.ConfigDiscoveryHTTPFilterDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	ListenerLocalRatelimit: {
		PrettyName:            "Local Ratelimit",
		Collection:            "filters",
		URL:                   URLs["l_local_ratelimit"],
		Message:               &l_local_ratelimit.LocalRateLimit{},
		DownstreamFiltersFunc: downstreamfilters.DownstreamTypedFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	ListenerHTTPInspector: {
		PrettyName:            "Http Inspector",
		Collection:            "filters",
		URL:                   URLs["l_http_inspector"],
		Message:               &l_http_inspector.HttpInspector{},
		DownstreamFiltersFunc: downstreamfilters.DownstreamTypedFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	ListenerOriginalDst: {
		PrettyName:            "Original Dst",
		Collection:            "filters",
		URL:                   URLs["l_original_dst"],
		Message:               &l_original_dst.OriginalDst{},
		DownstreamFiltersFunc: downstreamfilters.DownstreamTypedFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	ListenerOriginalSrc: {
		PrettyName:            "Original Src",
		Collection:            "filters",
		URL:                   URLs["l_original_src"],
		Message:               &l_original_src.OriginalSrc{},
		DownstreamFiltersFunc: downstreamfilters.DownstreamTypedFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	ListenerTLSInspector: {
		PrettyName:            "Original Src",
		Collection:            "filters",
		URL:                   URLs["l_original_src"],
		Message:               &l_tls_inspector.TlsInspector{},
		DownstreamFiltersFunc: downstreamfilters.DownstreamTypedFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	ListenerDNSFilter: {
		PrettyName:            "DNS Filter",
		Collection:            "filters",
		URL:                   URLs["l_dns_filter"],
		Message:               &l_dns_filter.DnsFilterConfig{},
		DownstreamFiltersFunc: downstreamfilters.DownstreamTypedFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	ListeneProxyProtocol: {
		PrettyName:            "Proxy Protocol",
		Collection:            "filters",
		URL:                   URLs["l_proxy_protocol"],
		Message:               &l_proxy_protocol.ProxyProtocol{},
		DownstreamFiltersFunc: downstreamfilters.DownstreamTypedFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	ConnectionLimit: {
		PrettyName:            "Connection Limit",
		Collection:            "filters",
		URL:                   URLs["connection_limit"],
		Message:               &connection_limit.ConnectionLimit{},
		DownstreamFiltersFunc: downstreamfilters.DownstreamTypedFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	NetworkLocalRatelimit: {
		PrettyName:            "Local Ratelimit",
		Collection:            "filters",
		URL:                   URLs["n_local_ratelimit"],
		Message:               &n_local_ratelimit.LocalRateLimit{},
		DownstreamFiltersFunc: downstreamfilters.DownstreamTypedFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	HTTPLocalRatelimit: {
		PrettyName:            "Local Ratelimit",
		Collection:            "filters",
		URL:                   URLs["h_local_ratelimit"],
		Message:               &h_local_ratelimit.LocalRateLimit{},
		DownstreamFiltersFunc: downstreamfilters.DiscoverAndTypedHTTPFilterDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	GenericSecret: {
		PrettyName:            "Generic Secret",
		Collection:            "secrets",
		URL:                   URLs["secrets"],
		Message:               &tls.GenericSecret{},
		DownstreamFiltersFunc: downstreamfilters.TLSCertificateDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	TLSSessionTicketKeys: {
		PrettyName:            "TLS Session Ticket Keys",
		Collection:            "secrets",
		URL:                   URLs["secrets"],
		Message:               &tls.TlsSessionTicketKeys{},
		DownstreamFiltersFunc: downstreamfilters.TLSCertificateDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	OAuth2: {
		PrettyName:            "OAuth2",
		Collection:            "filters",
		URL:                   URLs["oauth2"],
		Message:               &oauth2.OAuth2{},
		DownstreamFiltersFunc: downstreamfilters.ConfigDiscoveryHTTPFilterDownstreamFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         nil,
	},
	OpenTelemetry: {
		PrettyName:            "Open Telemetry",
		Collection:            "extensions",
		URL:                   URLs["stat_sinks"],
		Message:               &stat_sink_otel.SinkConfig{},
		DownstreamFiltersFunc: downstreamfilters.TypedConfigDownstreamBootstrapFilters,
		TypedConfigPaths:      nil,
		UpstreamPaths:         GenericGRPCServiceUpstreams,
	},
}

func (gt GTypes) String() string {
	return string(gt)
}

func (gt GTypes) CollectionString() string {
	if mapping, exists := gTypeMappings[gt]; exists {
		return mapping.Collection
	}
	return unknown
}

func (gt GTypes) URL() string {
	if mapping, exists := gTypeMappings[gt]; exists {
		return mapping.URL
	}
	return unknown
}

func (gt GTypes) PrettyName() string {
	if mapping, exists := gTypeMappings[gt]; exists {
		return mapping.PrettyName
	}
	return unknown
}

func (gt GTypes) ProtoMessage() proto.Message {
	if mapping, exists := gTypeMappings[gt]; exists {
		return mapping.Message
	}
	return &anypb.Any{}
}

func (gt GTypes) DownstreamFilters(dfm downstreamfilters.DownstreamFilter) []downstreamfilters.MongoFilters {
	if mapping, exists := gTypeMappings[gt]; exists && mapping.DownstreamFiltersFunc != nil {
		return mapping.DownstreamFiltersFunc(dfm)
	}
	return nil
}

func (gt GTypes) TypedConfigPaths() []TypedConfigPath {
	if mapping, exists := gTypeMappings[gt]; exists && mapping.TypedConfigPaths != nil {
		return mapping.TypedConfigPaths
	}
	return nil
}

func (gt GTypes) UpstreamPaths() map[string]GTypes {
	if mapping, exists := gTypeMappings[gt]; exists && mapping.UpstreamPaths != nil {
		return mapping.UpstreamPaths
	}
	return nil
}

func (gt GTypes) Validate() map[string]GTypes {
	if mapping, exists := gTypeMappings[gt]; exists && mapping.UpstreamPaths != nil {
		return mapping.UpstreamPaths
	}
	return nil
}
