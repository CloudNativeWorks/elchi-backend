package downstreamfilters

import "go.mongodb.org/mongo-driver/bson"

// TemplateDownstreamFiltersForGType finds templates that reference a specific resource by GType
// This is the correct function to use for template downstream filtering
func TemplateDownstreamFiltersForGType(name, project, version, gtype string) []MongoFilters {
	// Get GType-specific template reference conditions
	var orConditions bson.A

	switch gtype {
	case "envoy.config.cluster.v3.Cluster":
		orConditions = getTemplateClusterConditions(name)
	case "envoy.config.route.v3.RouteConfiguration":
		orConditions = getTemplateRouteConditions(name)
	case "envoy.config.route.v3.VirtualHost":
		orConditions = getTemplateVirtualHostConditions(name)
	case "envoy.config.listener.v3.Listener":
		orConditions = getTemplateListenerConditions(name)
	case "envoy.config.endpoint.v3.ClusterLoadAssignment":
		orConditions = getTemplateEndpointConditions(name)
	// TLS/Secret types
	case "envoy.extensions.transport_sockets.tls.v3.TlsCertificate":
		orConditions = getTemplateTLSCertificateConditions(name)
	case "envoy.extensions.transport_sockets.tls.v3.CertificateValidationContext":
		orConditions = getTemplateCertValidationConditions(name)
	case "envoy.extensions.transport_sockets.tls.v3.GenericSecret":
		orConditions = getTemplateGenericSecretConditions(name)
	case "envoy.extensions.transport_sockets.tls.v3.TlsSessionTicketKeys":
		orConditions = getTemplateTLSSessionTicketConditions(name)
	default:
		// For HTTP filters and extensions
		switch {
		case isHTTPFilter(gtype):
			orConditions = getTemplateHTTPFilterConditions(name)
		case isExtension(gtype):
			orConditions = getTemplateExtensionConditions(name)
		default:
			// For unknown types, check general fields
			orConditions = bson.A{
				bson.D{{Key: "general.config_discovery.name", Value: name}},
				bson.D{{Key: "general.typed_config.name", Value: name}},
			}
		}
	}

	// If no conditions, return empty
	if len(orConditions) == 0 {
		return []MongoFilters{}
	}

	return []MongoFilters{
		{
			Collection: "resource_templates",
			Filter: bson.D{
				{Key: "$and", Value: bson.A{
					bson.D{{Key: "project", Value: project}},
					bson.D{{Key: "version", Value: version}},
					bson.D{{Key: "$or", Value: orConditions}},
				}},
			},
		},
	}
}

// getTemplateClusterConditions returns cluster template reference conditions
// Based on ClusterDownstreamFilters - template structure uses "resource." instead of "resource.resource."
func getTemplateClusterConditions(clusterName string) bson.A {
	return bson.A{
		// Routes: cluster references (from routes collection check)
		bson.D{{Key: "resource.virtual_hosts.routes.route.cluster", Value: clusterName}},
		bson.D{{Key: "resource.virtual_hosts.routes.route.weighted_clusters.clusters.name", Value: clusterName}},
		bson.D{{Key: "resource.virtual_hosts.request_mirror_policies.cluster", Value: clusterName}},
		bson.D{{Key: "resource.request_mirror_policies.cluster", Value: clusterName}},
		// TCP Proxy filters
		bson.D{{Key: "resource.cluster", Value: clusterName}},
		bson.D{{Key: "resource.weighted_clusters.clusters.name", Value: clusterName}},
		// HCM filters
		bson.D{{Key: "resource.route_config.virtual_hosts.routes.route.cluster", Value: clusterName}},
		bson.D{{Key: "resource.route_config.virtual_hosts.routes.route.weighted_clusters.clusters.name", Value: clusterName}},
		bson.D{{Key: "resource.route_config.virtual_hosts.request_mirror_policies.cluster", Value: clusterName}},
		// Fluentd access logger extensions
		bson.D{{Key: "resource.cluster", Value: clusterName}},
		// OpenTelemetry stat sinks extensions
		bson.D{{Key: "resource.grpc_service.envoy_grpc.cluster_name", Value: clusterName}},
		// Virtual hosts: cluster references
		bson.D{{Key: "resource.routes.route.cluster", Value: clusterName}},
		bson.D{{Key: "resource.routes.route.weighted_clusters.clusters.name", Value: clusterName}},
		// Bootstrap: static_resources.clusters
		bson.D{{Key: "resource.static_resources.clusters.name", Value: clusterName}},
	}
}

// getTemplateRouteConditions returns route template reference conditions
// Based on RouteDownstreamFilters - only HCM filters reference routes
func getTemplateRouteConditions(routeName string) bson.A {
	return bson.A{
		// HCM filters: route_config_name reference (template structure)
		bson.D{{Key: "resource.rds.route_config_name", Value: routeName}},
	}
}

// getTemplateVirtualHostConditions returns virtual host template reference conditions
// Based on VirtualHostDownstreamFilters - routes and filters reference virtual hosts via config_discovery
func getTemplateVirtualHostConditions(vhostName string) bson.A {
	return bson.A{
		// Routes and filters reference virtual hosts via config_discovery
		bson.D{{Key: "general.config_discovery.name", Value: vhostName}},
	}
}

// getTemplateListenerConditions returns listener template reference conditions
// Listeners are not referenced by other resources - they are the top level
func getTemplateListenerConditions(listenerName string) bson.A {
	return bson.A{
		// Bootstrap: static_resources.listeners (only place listeners are referenced)
		bson.D{{Key: "resource.static_resources.listeners.name", Value: listenerName}},
	}
}

// getTemplateEndpointConditions returns endpoint template reference conditions
// Based on EdsDownstreamFilters - clusters reference endpoints via eds_cluster_config.service_name
func getTemplateEndpointConditions(endpointName string) bson.A {
	return bson.A{
		// Clusters reference endpoints via eds_cluster_config.service_name
		bson.D{{Key: "resource.eds_cluster_config.service_name", Value: endpointName}},
	}
}

// getTemplateHTTPFilterConditions returns HTTP filter template reference conditions
// HTTP filters are referenced via config_discovery and typed_config in templates
func getTemplateHTTPFilterConditions(filterName string) bson.A {
	return bson.A{
		// General: HTTP filters referenced via config_discovery and typed_config
		bson.D{{Key: "general.config_discovery.name", Value: filterName}},
		bson.D{{Key: "general.typed_config.name", Value: filterName}},
	}
}

// getTemplateExtensionConditions returns extension template reference conditions
// Extensions are referenced via typed_config and in bootstrap admin/stats_sinks
func getTemplateExtensionConditions(extensionName string) bson.A {
	return bson.A{
		// General: extensions referenced via typed_config
		bson.D{{Key: "general.typed_config.name", Value: extensionName}},
		// Bootstrap: admin.access_log and stats_sinks
		bson.D{{Key: "resource.admin.access_log.name", Value: extensionName}},
		bson.D{{Key: "resource.stats_sinks.name", Value: extensionName}},
	}
}

// isHTTPFilter checks if the given gtype is an HTTP filter
func isHTTPFilter(gtype string) bool {
	httpFilters := []string{
		"envoy.extensions.filters.http.router.v3.Router",
		"envoy.extensions.filters.http.cors.v3.Cors",
		"envoy.extensions.filters.http.rbac.v3.RBAC",
		"envoy.extensions.filters.http.local_ratelimit.v3.LocalRateLimit",
		"envoy.extensions.filters.http.lua.v3.Lua",
		"envoy.extensions.filters.http.buffer.v3.Buffer",
		"envoy.extensions.filters.http.compressor.v3.Compressor",
		"envoy.extensions.filters.http.bandwidth_limit.v3.BandwidthLimit",
		"envoy.extensions.filters.http.basic_auth.v3.BasicAuth",
		"envoy.extensions.filters.http.adaptive_concurrency.v3.AdaptiveConcurrency",
		"envoy.extensions.filters.http.admission_control.v3.AdmissionControl",
		"envoy.extensions.filters.http.stateful_session.v3.StatefulSession",
		"envoy.extensions.filters.http.csrf.v3.CsrfPolicy",
		"envoy.extensions.filters.http.oauth2.v3.OAuth2",
	}

	for _, filter := range httpFilters {
		if gtype == filter {
			return true
		}
	}
	return false
}

// isExtension checks if the given gtype is an extension (access_logger, stat_sink, etc.)
func isExtension(gtype string) bool {
	extensions := []string{
		"envoy.extensions.access_loggers.fluentd.v3.FluentdAccessLogConfig",
		"envoy.extensions.access_loggers.file.v3.FileAccessLog",
		"envoy.extensions.access_loggers.stream.v3.StdoutAccessLog",
		"envoy.extensions.access_loggers.stream.v3.StderrAccessLog",
		"envoy.extensions.stat_sinks.open_telemetry.v3.SinkConfig",
		"envoy.extensions.transport_sockets.tls.v3.TlsCertificate",
		"envoy.extensions.transport_sockets.tls.v3.CertificateValidationContext",
		"envoy.extensions.transport_sockets.tls.v3.TlsSessionTicketKeys",
		"envoy.extensions.transport_sockets.tls.v3.GenericSecret",
		"envoy.extensions.health_check.event_sinks.file.v3.HealthCheckEventFileSink",
	}

	for _, ext := range extensions {
		if gtype == ext {
			return true
		}
	}
	return false
}

// getTemplateTLSCertificateConditions returns TLS certificate template reference conditions
// Based on TLSCertificateDownstreamFilters - TLS contexts reference certificates
func getTemplateTLSCertificateConditions(certName string) bson.A {
	return bson.A{
		// TLS contexts reference certificates via tls_certificate_sds_secret_configs
		bson.D{{Key: "resource.common_tls_context.tls_certificate_sds_secret_configs.name", Value: certName}},
	}
}

// getTemplateCertValidationConditions returns certificate validation template reference conditions
// Based on ContextValidateDownstreamFilters - TLS contexts reference validation contexts
func getTemplateCertValidationConditions(validationName string) bson.A {
	return bson.A{
		// TLS contexts reference validation contexts via validation_context_sds_secret_config
		bson.D{{Key: "resource.common_tls_context.validation_context_sds_secret_config.name", Value: validationName}},
	}
}

// getTemplateGenericSecretConditions returns generic secret template reference conditions
// Generic secrets use same paths as TLS certificates
func getTemplateGenericSecretConditions(secretName string) bson.A {
	return getTemplateTLSCertificateConditions(secretName)
}

// getTemplateTLSSessionTicketConditions returns TLS session ticket template reference conditions
// TLS session tickets use same paths as TLS certificates
func getTemplateTLSSessionTicketConditions(ticketName string) bson.A {
	return getTemplateTLSCertificateConditions(ticketName)
}
