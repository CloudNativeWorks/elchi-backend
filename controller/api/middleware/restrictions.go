package middleware

var Collections = []string{
	"clusters",
	"listeners",
	"endpoints",
	"routes",
	"virtual_hosts",
	"extensions",
	"filters",
	"scenarios",
	"snippets",
}

var AllowedEndpoints = []string{
	"/logout",
	"/refresh",
	"/auth/login",
	// Admin + Owner endpoints (User/Group management)
	"/api/v3/setting/user_list",
	"/api/v3/setting/user/:user_id",
	"/api/v3/setting/users/:user_id",
	"/api/v3/setting/group_list",
	"/api/v3/setting/group/:group_id",

	// Owner-only endpoints (Project management)
	"/api/v3/setting/project_list",
	"/api/v3/setting/project/:project_id",
	"/api/v3/setting/permissions/:kind/:type/:id",
	"/api/v3/setting/tokens",
	"/api/v3/setting/tokens/:token_id",
	"/api/v3/setting/openrouter-token",
	"/api/v3/setting/discovery-token",
	"/api/v3/setting/discovery-token/generate",
	"/api/v3/setting/clouds",
	"/api/v3/setting/clouds/:cloud_name",
	"/api/v3/setting/ldap-config",
	"/api/v3/setting/ldap-config/test",
	"/api/v3/setting/ldap-config/test-auth",
	"/api/v3/setting/syslog-config",
	"/api/v3/setting/syslog-config/test",
	"/api/v3/setting/api_discovery",
	"/api/v3/setting/threat-intel",
	"/api/v3/setting/threat-intel/upload",
	"/api/v3/setting/threat-intel/:name",
	"/api/v3/setting/geoip",
	"/api/v3/setting/geoip/upload",
	"/api/v3/setting/geoip/download",
	"/api/v3/setting/geoip/:kind",

	// OTP Admin endpoints (Admin/Owner only)
	"/api/v3/setting/otp-config",
	"/api/v3/setting/otp/reset-user/:user_id",

	// License endpoints. GET is open to all authenticated users (UI banner);
	// POST endpoints are Admin/Owner only via InitSettingMiddleware.
	"/api/v3/setting/license",
	"/api/v3/setting/license/activate",
	"/api/v3/setting/license/check",

	// Profile endpoints (All authenticated users)
	"/api/v3/profile",
	"/api/v3/profile/email",
	"/api/v3/profile/password",
	"/api/v3/profile/otp/status",
	"/api/v3/profile/otp/enable",
	"/api/v3/profile/otp/verify",
	"/api/v3/profile/otp/disable",
	"/api/v3/profile/otp/regenerate-backup-codes",

	"/api/v3/xds/secrets",
	"/api/v3/xds/secrets/:name",
	"/api/v3/xds/bootstrap",
	"/api/v3/xds/bootstrap/:name",
	"/api/v3/xds/listeners",
	"/api/v3/xds/listeners/:name",
	"/api/v3/xds/routes",
	"/api/v3/xds/routes/:name",
	"/api/v3/xds/tls",
	"/api/v3/xds/tls/:name",
	"/api/v3/xds/virtual_hosts",
	"/api/v3/xds/virtual_hosts/:name",
	"/api/v3/xds/clusters",
	"/api/v3/xds/clusters/:name",
	"/api/v3/xds/hcm",
	"/api/v3/xds/hcm/:name",
	"/api/v3/xds/endpoints",
	"/api/v3/xds/endpoints/:name",
	"/api/v3/eo/:collection/:type",
	"/api/v3/eo/:collection/:type/:canonical_name",
	"/api/v3/eo/:collection/:type/:canonical_name/:name",
	"/api/v3/eo/:collection/extensions/:type",
	"/api/v3/eo/:collection/extensions/:type/:name",
	"/api/v3/custom/resource_list",
	"/api/v3/custom/resource_list_search",
	"/api/v3/custom/http_filter_list",
	"/api/v3/custom/count/filters",
	"/api/v3/custom/count/all",
	"/api/v3/custom/available_versions",
	"/api/v3/custom/error_summary",
	"/api/v3/custom/clear_errors",
	"/api/v3/dependency/:name",
	"/api/v3/scenario/components",
	"/api/v3/scenario/scenarios",
	"/api/v3/scenario/scenarios/:scenario_id",
	"/api/v3/scenario/execute",
	"/api/v3/scenario/validate",
	"/api/v3/scenario/import",
	"/api/v3/scenario/export",
	"/api/v3/registry/data",
	"/api/v3/registry/instances",
	"/api/v3/registry/controller/:id",
	"/api/v3/registry/control-plane/:id",
	"/api/op/clients",
	"/api/op/clients/:client_id",
	"/api/op/clients/:client_id/openstack/interfaces",
	"/api/op/clients/:client_id/openstack/subnets/:subnet_id/available_ips",
	"/api/op/services",
	"/api/op/services/:service_id",
	"/api/op/services/:service_id/recreate-gslb",
	"/api/op/services/envoys/:service_id",
	"/api/v3/ai/analyze",
	"/api/v3/ai/analyze-logs",
	"/api/v3/ai/status",
	"/api/v3/ai/usage/stats",
	"/api/v3/ai/usage/recent",
	"/api/v3/ai/usage/status",
	"/api/v3/ai/usage/cleanup",
	"/api/v3/ai/models",
	"/api/v3/ai/models/recommended",
	"/api/v3/ai/models/free",
	"/api/v3/ai/models/:model_id",
	"/api/v3/ai/models/test",
	"/api/discovery/k8s",
	"/api/discovery/clusters",
	"/api/discovery/clusters/:id",
	"/api/discovery/clusters/:id/usage",
	"/api/v3/jobs",
	"/api/v3/jobs/:id",
	"/api/v3/jobs/:id/retry",
	"/api/v3/jobs/stuck",
	"/api/v3/jobs/workers",
	"/api/v3/jobs/stats",
	// API Inventory (read-only over Mongo `api_inventory` + ClickHouse
	// `api_events_*`). Per-handler project-scope auth runs inside the
	// handlers; PathCheck only validates the URL shape here.
	"/api/v3/inventory",
	"/api/v3/inventory/operations",
	"/api/v3/inventory/attack-surface",
	"/api/v3/inventory/openapi",
	"/api/v3/inventory/listeners",
	"/api/v3/inventory/geo",
	"/api/v3/inventory/discoveries",
	"/api/v3/inventory/auth-coverage",
	"/api/v3/inventory/bot-scanner",
	"/api/v3/inventory/pii",
	"/api/v3/inventory/zombies",
	"/api/v3/inventory/risk-summary",
	"/api/v3/inventory/security-score",
	"/api/v3/inventory/transport",
	"/api/v3/inventory/errors",
	"/api/v3/inventory/normalize-gaps",
	"/api/v3/inventory/cleanup-stale",
	"/api/v3/inventory/:id",
	"/api/v3/inventory/:id/events",
	"/api/v3/inventory/:id/stats",
	"/api/v3/inventory/:id/reset",
	"/api/v3/bridge/nodes/:node_id/snapshot",
	"/api/v3/audit/logs",
	"/api/v3/audit/stats",
	"/api/v3/templates/check/:gtype",
	"/api/v3/templates/:gtype",
	"/api/v3/routemap/supported-types",
	"/api/v3/routemap/:name",
	// Snippet endpoints
	"/api/v3/snippets",
	"/api/v3/snippets/search",
	"/api/v3/snippets/stats",
	"/api/v3/snippets/batch",
	"/api/v3/snippets/:id",
	// Search endpoint
	"/api/v3/search",
	// WAF endpoints
	"/api/v3/waf/crs",
	"/api/v3/waf/crs/versions",
	"/api/v3/waf/crs/:crs_version/:rule_id",
	"/api/v3/waf/config",
	"/api/v3/waf/config/:config_id",
	"/api/v3/waf/config/:config_id/versions",
	"/api/v3/waf/config/:config_id/versions/:version",
	"/api/v3/waf/config/:config_id/versions/:version/restore",
	// Upgrade endpoint
	"/api/v3/resource/upgrade",
	// Maintenance endpoints
	"/api/v3/setting/maintenance/cleanup/versions/:version",
	"/api/v3/setting/maintenance/backup/export",
	"/api/v3/setting/maintenance/backup/import",
	"/api/v3/setting/maintenance/backup/validate",
	"/api/v3/setting/maintenance/backup/metadata",
	// Let's Encrypt certificate management endpoints
	"/api/v3/acme/certificates",
	"/api/v3/acme/certificates/:cert_id",
	"/api/v3/acme/certificates/:cert_id/duplicate",
	"/api/v3/acme/certificates/:cert_id/dns-challenges",
	"/api/v3/acme/certificates/:cert_id/verify",
	"/api/v3/acme/certificates/:cert_id/renew",
	"/api/v3/acme/certificates/:cert_id/retry-verification",
	"/api/v3/acme/certificates/:cert_id/dns-credential",
	"/api/v3/acme/dns-credentials",
	"/api/v3/acme/dns-credentials/:cred_id",
	"/api/v3/acme/dns-credentials/test",
	// Let's Encrypt ACME accounts management endpoints
	"/api/v3/acme/acme-accounts",
	"/api/v3/acme/acme-accounts/:account_id",
	"/api/v3/acme/acme-accounts/:account_id/validate",
	// CA Providers endpoints
	"/api/v3/acme/ca-providers",
	"/api/v3/acme/ca-providers/:provider/validate-eab",
	// GSLB (Global Server Load Balancing) endpoints
	"/api/v3/setting/gslb",             // GSLB configuration (GET, PUT, DELETE)
	"/api/v3/setting/gslb/options",     // GSLB options: failover zones + regions (GET)
	"/api/v3/gslb",                     // GSLB records list (GET) and create (POST)
	"/api/v3/gslb/batch",               // GSLB bulk update (PATCH) - enable/disable multiple records
	"/api/v3/gslb/:id",                 // GSLB record detail (GET, PUT, DELETE)
	"/api/v3/gslb/:id/ips",             // GSLB record IP management (POST, DELETE)
	"/api/v3/gslb/:id/ips/:ip",         // GSLB record IP detail (GET, DELETE)
	"/api/v3/gslb/:id/ips/:ip/regions", // GSLB IP region assignment (PUT) - Admin/Owner only
	"/api/v3/gslb/ip/:id/history",      // Clear IP status history (DELETE) - Admin/Owner only
	"/api/v3/gslb/nodes",               // GSLB node tracking list (GET)
	"/api/v3/gslb/nodes/:id",           // GSLB node delete (DELETE) - Admin/Owner only
	"/api/v3/gslb/nodes/notify-all",    // GSLB node proxy: broadcast notify to all nodes (POST)
	"/api/v3/gslb/nodes/:id/health",    // GSLB node proxy: health check (GET)
	"/api/v3/gslb/nodes/:id/records",   // GSLB node proxy: records query (GET)
	"/api/v3/gslb/nodes/:id/notify",    // GSLB node proxy: notify specific node (POST)
	// DNS API endpoints (CoreDNS plugin authentication via DNS secret)
	"/dns/snapshot", // DNS snapshot for CoreDNS plugin
	"/dns/changes",  // Incremental DNS changes (if implemented)

	// pprof profiling endpoints (runtime monitoring)
	"/debug/pprof",
	"/debug/pprof/cmdline",
	"/debug/pprof/profile",
	"/debug/pprof/symbol",
	"/debug/pprof/trace",
	"/debug/pprof/allocs",
	"/debug/pprof/block",
	"/debug/pprof/goroutine",
	"/debug/pprof/heap",
	"/debug/pprof/mutex",
	"/debug/pprof/threadcreate",
}
