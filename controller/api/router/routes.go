package router

import (
	"github.com/CloudNativeWorks/elchi-backend/controller/api/middleware"
	"github.com/CloudNativeWorks/elchi-backend/controller/handlers"
	"github.com/gin-gonic/gin"
)

func initClientRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"GET", "", h.ListClients},
		{"POST", "", h.Commands},
		{"GET", "/:client_id", h.GetClient},
		{"DELETE", "/:client_id", h.DeleteClient},
	}

	initRoutes(rg, routes)
}

func initServiceRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"GET", "", h.ListServices},
		{"GET", "/from_client", h.GetService},
		{"GET", "/envoys/:service_id", h.GetEnvoyDetails},
		{"GET", "/:service_id", h.GetService},
		{"POST", "/:service_id/recreate-gslb", h.RecreateGSLB}, // Disaster recovery: recreate GSLB record from service
	}

	initRoutes(rg, routes)
}

func initAuthRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"POST", "/login", h.Settings.Login()},
	}

	initRoutes(rg, routes)
}

func initBridgeRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"GET", "/nodes/:nodeID/snapshot", h.GetNodeSnapshot},
		{"DELETE", "/nodes/:nodeID/snapshot", h.ClearNodeSnapshot},
	}

	initRoutes(rg, routes)
}

func initSettingRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	rg.Use(middleware.InitSettingMiddleware())

	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"GET", "/user_list", h.Settings.ListUsers},
		{"GET", "/user/:user_id", h.Settings.GetUser},
		{"GET", "/users/:user_id", h.Settings.GetUserByID},
		{"PUT", "/user/:user_id", h.SetUpdateUserWithAudit},
		{"DELETE", "/user/:user_id", h.DeleteUserWithAudit},

		{"GET", "/group_list", h.Settings.ListGroups},
		{"GET", "/group/:group_id", h.Settings.GetGroup},
		{"PUT", "/group/:group_id", h.SetUpdateGroupWithAudit},
		{"DELETE", "/group/:group_id", h.DeleteGroupWithAudit},

		// Project endpoints moved to separate Owner-only section below

		{"GET", "/permissions/:kind/:type/:id", h.Settings.GetPermissions},

		{"GET", "/tokens", h.Settings.GetTokens},
		{"POST", "/tokens", h.SetTokenWithAudit},
		{"DELETE", "/tokens/:token_id", h.DeleteTokenWithAudit},

		{"GET", "/openrouter-token", h.Settings.GetOpenRouterToken},
		{"POST", "/openrouter-token", h.SetOpenRouterTokenWithAudit},
		{"PUT", "/openrouter-token", h.UpdateOpenRouterTokenWithAudit},
		{"DELETE", "/openrouter-token", h.DeleteOpenRouterTokenWithAudit},

		{"GET", "/discovery-token", h.Settings.GetDiscoveryToken},
		{"DELETE", "/discovery-token", h.DeleteDiscoveryTokenWithAudit},
		{"POST", "/discovery-token/generate", h.GenerateDiscoveryTokenWithAudit},

		// Cloud configuration endpoints
		{"GET", "/clouds", h.Settings.GetClouds},
		{"GET", "/clouds/:cloud_name", h.Settings.GetCloud},
		{"POST", "/clouds/:cloud_name", h.SetCloudWithAudit},
		{"PUT", "/clouds/:cloud_name", h.UpdateCloudWithAudit},
		{"DELETE", "/clouds/:cloud_name", h.DeleteCloudWithAudit},

		// LDAP configuration endpoints
		{"GET", "/ldap-config", h.Settings.GetLDAPConfig},
		{"POST", "/ldap-config", h.SetLDAPConfigWithAudit},
		{"PUT", "/ldap-config", h.UpdateLDAPConfigWithAudit},
		{"DELETE", "/ldap-config", h.DeleteLDAPConfigWithAudit},
		{"POST", "/ldap-config/test", h.TestLDAPConfigWithAudit},
		{"POST", "/ldap-config/test-auth", h.TestLDAPAuthWithAudit},

		// GSLB configuration endpoints
		{"GET", "/gslb", h.Settings.GetGSLBConfig},
		{"PUT", "/gslb", h.SetGSLBConfigWithAudit},
		{"POST", "/gslb", h.UpdateGSLBConfigWithAudit},
		{"DELETE", "/gslb", h.DeleteGSLBConfigWithAudit},
		{"GET", "/gslb/options", h.Settings.GetGSLBOptions},

		// API Discovery — elchi-collector runtime config singleton
		// (api_collector_config). All routes are Admin/Owner-only via
		// InitSettingMiddleware (the whole /setting group); the write
		// handlers re-check as defence in depth.
		{"GET", "/api_discovery", h.Settings.GetAPIDiscoveryConfig},
		{"PUT", "/api_discovery", h.Settings.UpdateAPIDiscoveryConfig},

		// Threat-intel feeds — collector's api_collector_threatintel
		// singleton. All routes Admin/Owner-only (InitSettingMiddleware).
		{"GET", "/threat-intel", h.Settings.GetThreatIntel},
		{"PUT", "/threat-intel", h.Settings.UpdateThreatIntel},
		{"POST", "/threat-intel/upload", h.Settings.UploadThreatIntelFeed},
		{"GET", "/threat-intel/:name", h.Settings.GetThreatIntelFeed},
		{"DELETE", "/threat-intel/:name", h.Settings.DeleteThreatIntelFeed},

		// GeoIP MMDB databases — collector's `geoip` GridFS bucket.
		// All routes Admin/Owner-only (InitSettingMiddleware).
		{"GET", "/geoip", h.Settings.GetGeoIP},
		{"POST", "/geoip/upload", h.Settings.UploadGeoIP},
		{"POST", "/geoip/download", h.Settings.DownloadGeoIP},
		{"DELETE", "/geoip/:kind", h.Settings.DeleteGeoIP},

		// Audit-log syslog forwarding (global)
		{"GET", "/syslog-config", h.Settings.GetSyslogConfig},
		{"POST", "/syslog-config", h.SetSyslogConfigWithAudit},
		{"PUT", "/syslog-config", h.UpdateSyslogConfigWithAudit},
		{"DELETE", "/syslog-config", h.DeleteSyslogConfigWithAudit},
		{"POST", "/syslog-config/test", h.TestSyslogConnectionHandler},

		// OTP configuration endpoints (Admin/Owner only)
		{"GET", "/otp-config", h.Settings.GetOTPConfig()},
		{"PUT", "/otp-config", h.Settings.UpdateOTPConfig()},
		{"POST", "/otp/reset-user/:user_id", h.Settings.ResetUserOTP()},

		// License endpoints. GET is exempted in InitSettingMiddleware so any
		// authenticated user can read plan info for the UI banner; mutating
		// endpoints stay Admin/Owner. activate takes BOTH license_key and api_key
		// in one call — there is no separate api-key-only endpoint.
		{"GET", "/license", h.Settings.GetLicenseStatus},
		{"POST", "/license/activate", h.SetLicenseActivateWithAudit},
		{"POST", "/license/check", h.ForceLicenseCheckWithAudit},
		{"DELETE", "/license", h.DeleteLicenseWithAudit},
	}

	initRoutes(rg, routes)

	// Owner-only endpoints (Project Management)
	initProjectRoutes(rg, h)
}

func initProjectRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	// Apply Owner-only middleware for project management
	projectGroup := rg.Group("")
	projectGroup.Use(middleware.OwnerOnlyMiddleware())

	projectRoutes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"GET", "/project_list", h.Settings.ListProjects},
		{"GET", "/project/:project_id", h.Settings.GetProject},
		{"PUT", "/project/:project_id", h.SetUpdateProjectWithAudit},
		{"DELETE", "/project/:project_id", h.DeleteProjectWithAudit},
	}

	initRoutes(projectGroup, projectRoutes)
}

func initCustomRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"GET", "/resource_list", h.GetCustomResourceList},
		{"GET", "/resource_list_search", h.GetCustomResourceListWithSearch},
		{"GET", "/http_filter_list", h.GetCustomHTTPFilterList},
		{"GET", "/available_versions", h.GetAvailableVersions},

		{"GET", "/count/all", h.GetResourceCounts},
		{"GET", "/count/filters", h.GetFilterCounts},
		{"GET", "/error_summary", h.GetErrorSummary},
		{"GET", "/clear_errors", h.ClearErrorsByID},
	}

	initRoutes(rg, routes)
}

func initScenarioRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		// Scenario endpoints
		{"GET", "/components", h.GetComponentCatalogHandler},
		{"POST", "/scenarios", h.CreateScenarioHandler},
		{"GET", "/scenarios", h.GetScenariosHandler},
		{"GET", "/scenarios/:scenario_id", h.GetScenarioHandler},
		{"PUT", "/scenarios/:scenario_id", h.UpdateScenarioHandler},
		{"DELETE", "/scenarios/:scenario_id", h.DeleteScenarioHandler},
		{"POST", "/execute", h.ExecuteScenarioHandler},
		{"POST", "/validate", h.ValidateScenarioHandler},
		{"POST", "/export", h.ExportScenariosHandler},
		{"POST", "/import", h.ImportScenariosHandler},
	}

	initRoutes(rg, routes)
}

func initDependencyRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"GET", "/:name", h.GetResourceDependencies},
	}

	initRoutes(rg, routes)
}

func initExtensionRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"GET", "/:collection/extensions/:type", h.GetExtensions},
		{"POST", "/:collection/extensions/:type", h.SetExtension},
		{"GET", "/:collection/extensions/:type/:name", h.GetOtherExtension},
		{"PUT", "/:collection/extensions/:type/:name", h.UpdateOtherExtensions},
		{"DELETE", "/:collection/extensions/:type/:name", h.DelExtension},

		{"GET", "/:collection/:type/:canonical_name", h.ListExtensions},
		{"POST", "/:collection/:type/:canonical_name", h.SetExtension},
		{"GET", "/:collection/:type/:canonical_name/:name", h.GetExtension},
		{"PUT", "/:collection/:type/:canonical_name/:name", h.UpdateExtension},
		{"DELETE", "/:collection/:type/:canonical_name/:name", h.DelExtension},
	}

	initRoutes(rg, routes)
}

func initResourceRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"GET", "/:collection", h.ListResource},
		{"POST", "/:collection", h.SetResource},
		{"GET", "/:collection/:name", h.GetResource},
		{"PUT", "/:collection/:name", h.UpdateResource},
		{"DELETE", "/:collection/:name", h.DelResource},
	}

	initRoutes(rg, routes)
}

func initResourceOpsRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	// Check if upgrade handler is available
	if h.Upgrade == nil {
		return
	}

	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"POST", "/upgrade", h.Upgrade.UpgradeResource},
	}

	initRoutes(rg, routes)
}

func initRoutes(rg *gin.RouterGroup, routes []struct {
	method  string
	path    string
	handler gin.HandlerFunc
},
) {
	for _, route := range routes {
		switch route.method {
		case "GET":
			rg.GET(route.path, route.handler)
		case "POST":
			rg.POST(route.path, route.handler)
		case "PUT":
			rg.PUT(route.path, route.handler)
		case "DELETE":
			rg.DELETE(route.path, route.handler)
		}
	}
}

func initRegistryRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"GET", "/data", h.Registry.GetRegistryData},
		{"DELETE", "/controller/:id", h.Registry.DeleteController},
		{"DELETE", "/control-plane/:id", h.Registry.DeleteControlPlane},
	}

	initRoutes(rg, routes)
}

func initAIRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"POST", "/analyze", h.AnalyzeResourceConfigWithAI},
		{"POST", "/analyze-logs", h.AnalyzeLogsWithConfig},
		{"GET", "/status", h.GetAIStatus},
		{"GET", "/usage/stats", h.GetAIUsageStats},
		{"GET", "/usage/recent", h.GetRecentAIUsage},
		{"GET", "/usage/status", h.GetAIUsageStatus},
		{"DELETE", "/usage/cleanup", h.CleanupOldAIUsage},
		// Model management endpoints
		{"GET", "/models", h.GetAvailableModels},
		{"POST", "/models/test", h.TestModelConnection},
	}

	initRoutes(rg, routes)
}

func initDiscoveryRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"POST", "/k8s", h.HandleK8sDiscovery},
		{"GET", "/clusters", h.GetClusters},
		{"GET", "/clusters/:id/usage", h.GetClusterUsage},
		{"DELETE", "/clusters/:id", h.DeleteCluster},
	}

	initRoutes(rg, routes)
}

func initJobRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"GET", "", h.ListJobs},                // GET /api/v3/jobs
		{"GET", "/stats", h.GetJobStats},       // GET /api/v3/jobs/stats
		{"GET", "/:id", h.GetJob},              // GET /api/v3/jobs/:id
		{"POST", "/:id/retry", h.RetryJob},     // POST /api/v3/jobs/:id/retry
		{"GET", "/stuck", h.GetStuckJobs},      // GET /api/v3/jobs/stuck
		{"GET", "/workers", h.GetWorkerStatus}, // GET /api/v3/jobs/workers
	}

	initRoutes(rg, routes)
}

// initInventoryRoutes mounts the read-only API inventory endpoints.
// Project-scope authorization runs inside each handler (the
// `api_inventory` documents have no `general.permissions` field, so
// the standard AddSecureUserFilter pattern doesn't apply — handlers
// call authorization.ValidateRequestProject directly).
func initInventoryRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"GET", "", h.Inventory.ListInventory},                    // GET /api/v3/inventory  (flat per-endpoint list)
		{"GET", "/listeners", h.Inventory.ListInventoryListeners}, // GET /api/v3/inventory/listeners (project listeners summary — UI default landing)
		{"GET", "/geo", h.Inventory.GeoSummary},                   // GET /api/v3/inventory/geo (country/asn/UA/TI aggregates + optional time series)
		// API discovery + security surfaces. All five share the
		// inventory schema + project-scope auth pattern. Static
		// segments are registered BEFORE the `:id` catch-all so
		// gin's radix tree binds them correctly.
		{"GET", "/discoveries", h.Inventory.ListDiscoveries},   // GET /api/v3/inventory/discoveries (first_seen within window)
		{"GET", "/auth-coverage", h.Inventory.AuthCoverage},    // GET /api/v3/inventory/auth-coverage (unauth / inconsistent endpoints)
		{"GET", "/bot-scanner", h.Inventory.BotScannerHeatmap}, // GET /api/v3/inventory/bot-scanner (CH heatmap of bot/scanner traffic)
		{"GET", "/pii", h.Inventory.PIIInventory},              // GET /api/v3/inventory/pii (pii_categories breakdown)
		{"GET", "/zombies", h.Inventory.ListZombies},           // GET /api/v3/inventory/zombies (old + previously popular endpoints)
		{"GET", "/risk-summary", h.Inventory.RiskSummary},      // GET /api/v3/inventory/risk-summary (risk_flags by flag/class/severity)
		{"GET", "/security-score", h.Inventory.SecurityScore},  // GET /api/v3/inventory/security-score (A–F posture grade)
		{"GET", "/transport", h.Inventory.TransportPosture},    // GET /api/v3/inventory/transport (TLS/protocol posture)
		{"GET", "/errors", h.Inventory.ErrorAnalysis},          // GET /api/v3/inventory/errors (4xx/5xx hotspots + series)
		// Collector runtime config moved to GET/PUT /api/v3/setting/api_discovery
		{"GET", "/:id", h.Inventory.GetInventoryItem},          // GET /api/v3/inventory/:id
		{"GET", "/:id/events", h.Inventory.GetInventoryEvents}, // GET /api/v3/inventory/:id/events
		{"GET", "/:id/stats", h.Inventory.GetInventoryStats},   // GET /api/v3/inventory/:id/stats
	}
	initRoutes(rg, routes)
}

func initOpenStackRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"GET", "/:client_id/openstack/interfaces", h.GetClientOpenStackInterfaces},
		{"GET", "/:client_id/openstack/subnets/:subnet_id/available_ips", h.GetSubnetAvailableIPs},
	}

	initRoutes(rg, routes)
}

func initAuditRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"GET", "/logs", h.GetAuditLogs},   // GET /api/v3/audit/logs
		{"GET", "/stats", h.GetAuditStats}, // GET /api/v3/audit/stats
	}

	initRoutes(rg, routes)
}

func initWAFRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		// CRS Rules Endpoints
		{"GET", "/crs", h.WAF.GetCRSRules},                          // GET /api/v3/waf/crs?crs_version=4.14.0&severity=CRITICAL
		{"GET", "/crs/versions", h.WAF.GetCRSVersions},              // GET /api/v3/waf/crs/versions
		{"GET", "/crs/:crs_version/:rule_id", h.WAF.GetCRSRuleByID}, // GET /api/v3/waf/crs/4.14.0/941100

		// WAF Config CRUD Endpoints
		{"POST", "/config", h.WAF.CreateWAFConfigWithAudit},              // POST /api/v3/waf/config
		{"PUT", "/config/:config_id", h.WAF.UpdateWAFConfigWithAudit},    // PUT /api/v3/waf/config/:config_id
		{"DELETE", "/config/:config_id", h.WAF.DeleteWAFConfigWithAudit}, // DELETE /api/v3/waf/config/:config_id
		{"GET", "/config/:config_id", h.WAF.GetWAFConfig},                // GET /api/v3/waf/config/:config_id
		{"GET", "/config", h.WAF.ListWAFConfigs},                         // GET /api/v3/waf/config?project=xxx&version=1.32

		// WAF Config Versioning Endpoints
		{"GET", "/config/:config_id/versions", h.WAF.ListWAFVersions},                     // GET /api/v3/waf/config/:config_id/versions[?limit=N]
		{"GET", "/config/:config_id/versions/:version", h.WAF.GetWAFVersion},              // GET /api/v3/waf/config/:config_id/versions/:version
		{"POST", "/config/:config_id/versions/:version/restore", h.WAF.RestoreWAFVersion}, // POST /api/v3/waf/config/:config_id/versions/:version/restore
	}

	initRoutes(rg, routes)
}

func initRouteMapRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"GET", "/supported-types", h.RouteMap.GetSupportedTypes}, // GET /api/v3/routemap/supported-types
		{"GET", "/:name", h.RouteMap.GetRouteMap},                 // GET /api/v3/routemap/:name
	}

	initRoutes(rg, routes)
}

func initTemplateRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"GET", "/check/:gtype", h.Template.CheckTemplateExists}, // GET /api/v3/templates/check/:gtype
		{"GET", "/:gtype", h.Template.GetTemplate},               // GET /api/v3/templates/:gtype
		{"POST", "/:gtype", h.Template.CreateOrUpdateTemplate},   // POST /api/v3/templates/:gtype (create/update)
		{"DELETE", "/:gtype", h.Template.DeleteTemplate},         // DELETE /api/v3/templates/:gtype
	}

	initRoutes(rg, routes)
}

func initSnippetRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"POST", "", h.Snippet.CreateSnippet},               // POST /api/v3/snippets
		{"GET", "", h.Snippet.ListSnippets},                 // GET /api/v3/snippets
		{"GET", "/search", h.Snippet.SearchSnippets},        // GET /api/v3/snippets/search
		{"GET", "/stats", h.Snippet.GetSnippetStats},        // GET /api/v3/snippets/stats
		{"POST", "/batch", h.Snippet.BatchCreateSnippets},   // POST /api/v3/snippets/batch
		{"DELETE", "/batch", h.Snippet.BatchDeleteSnippets}, // DELETE /api/v3/snippets/batch
		{"GET", "/:id", h.Snippet.GetSnippet},               // GET /api/v3/snippets/:id
		{"PUT", "/:id", h.Snippet.UpdateSnippet},            // PUT /api/v3/snippets/:id
		{"DELETE", "/:id", h.Snippet.DeleteSnippet},         // DELETE /api/v3/snippets/:id
	}

	initRoutes(rg, routes)
}

func initSearchRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"GET", "", h.GlobalSearch}, // GET /api/v3/search?query=example.com&project=xxx
	}

	initRoutes(rg, routes)
}

func initMaintenanceRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	// Order matters: body limit MUST run before InitSettingMiddleware,
	// because InitSettingMiddleware's checkBackupAuthorization body-parses
	// the request to enforce per-endpoint authorization — without the
	// MaxBytesReader wrap, a multi-GB POST would be fully read here
	// before any size check.
	// Cleanup endpoints don't carry oversized bodies but applying the cap
	// globally is harmless (DELETE bodies are tiny) and keeps the chain
	// uniform.
	rg.Use(middleware.BodyLimitMiddleware(middleware.MaxBackupBodyBytes))
	rg.Use(middleware.InitSettingMiddleware())

	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		// Cleanup endpoints
		{"DELETE", "/cleanup/versions/:version", h.Maintenance.Cleanup.DeleteVersionResources},

		// Backup/Restore endpoints
		{"POST", "/backup/export", h.Maintenance.Backup.ExportBackup},
		{"POST", "/backup/import", h.Maintenance.Backup.ImportBackup},
		{"POST", "/backup/validate", h.Maintenance.Backup.ValidateBackup},
		{"POST", "/backup/metadata", h.Maintenance.Backup.GetBackupMetadata},
	}

	initRoutes(rg, routes)
}

func initProfileRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		// Profile endpoints
		{"GET", "", h.Profile.GetProfile()},
		{"PUT", "/email", h.Profile.UpdateEmail()},
		{"PUT", "/password", h.Profile.UpdatePassword()},

		// OTP management endpoints
		{"GET", "/otp/status", h.Profile.GetOTPStatus()},
		{"POST", "/otp/enable", h.Profile.EnableOTP()},
		{"POST", "/otp/verify", h.Profile.VerifyOTP()},
		{"POST", "/otp/disable", h.Profile.DisableOTP()},
		{"POST", "/otp/regenerate-backup-codes", h.Profile.RegenerateBackupCodes()},
	}

	initRoutes(rg, routes)
}

func initACMERoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	// Apply InitSettingMiddleware for admin/owner-only access to DNS credentials
	// Note: Handler methods will perform additional role checks as needed
	rg.Use(middleware.InitSettingMiddleware())
	rg.Use(middleware.AuditMiddleware(h.AuditService))

	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		// Certificate Management
		{"POST", "/certificates", h.ACME.CreateCertificate},                                     // POST /api/v3/acme/certificates?project=xxx
		{"GET", "/certificates", h.ACME.ListCertificates},                                       // GET /api/v3/acme/certificates?project=xxx
		{"GET", "/certificates/:cert_id", h.ACME.GetCertificate},                                // GET /api/v3/acme/certificates/:cert_id?project=xxx
		{"DELETE", "/certificates/:cert_id", h.ACME.DeleteCertificate},                          // DELETE /api/v3/acme/certificates/:cert_id?project=xxx
		{"POST", "/certificates/:cert_id/duplicate", h.ACME.DuplicateCertificate},               // POST /api/v3/acme/certificates/:cert_id/duplicate?project=xxx
		{"GET", "/certificates/:cert_id/dns-challenges", h.ACME.GetDNSChallenges},               // GET /api/v3/acme/certificates/:cert_id/dns-challenges?project=xxx
		{"POST", "/certificates/:cert_id/verify", h.ACME.VerifyDNS},                             // POST /api/v3/acme/certificates/:cert_id/verify?project=xxx
		{"POST", "/certificates/:cert_id/retry-verification", h.ACME.RetryVerification},         // POST /api/v3/acme/certificates/:cert_id/retry-verification?project=xxx
		{"POST", "/certificates/:cert_id/renew", h.ACME.RenewCertificate},                       // POST /api/v3/acme/certificates/:cert_id/renew?project=xxx
		{"PUT", "/certificates/:cert_id/dns-credential", h.ACME.ChangeCertificateDNSCredential}, // PUT /api/v3/acme/certificates/:cert_id/dns-credential?project=xxx

		// DNS Credentials Management
		{"POST", "/dns-credentials", h.ACME.CreateDNSCredential},            // POST /api/v3/acme/dns-credentials?project=xxx
		{"GET", "/dns-credentials", h.ACME.ListDNSCredentials},              // GET /api/v3/acme/dns-credentials?project=xxx
		{"GET", "/dns-credentials/:cred_id", h.ACME.GetDNSCredential},       // GET /api/v3/acme/dns-credentials/:cred_id?project=xxx
		{"PUT", "/dns-credentials/:cred_id", h.ACME.UpdateDNSCredential},    // PUT /api/v3/acme/dns-credentials/:cred_id?project=xxx
		{"DELETE", "/dns-credentials/:cred_id", h.ACME.DeleteDNSCredential}, // DELETE /api/v3/acme/dns-credentials/:cred_id?project=xxx
		{"POST", "/dns-credentials/test", h.ACME.TestDNSCredential},         // POST /api/v3/acme/dns-credentials/test?project=xxx

		// ACME Accounts Management
		{"POST", "/acme-accounts", h.ACME.CreateACMEAccount},                        // POST /api/v3/acme/acme-accounts?project=xxx
		{"GET", "/acme-accounts", h.ACME.ListACMEAccounts},                          // GET /api/v3/acme/acme-accounts?project=xxx
		{"GET", "/acme-accounts/:account_id", h.ACME.GetACMEAccount},                // GET /api/v3/acme/acme-accounts/:account_id?project=xxx
		{"DELETE", "/acme-accounts/:account_id", h.ACME.DeleteACMEAccount},          // DELETE /api/v3/acme/acme-accounts/:account_id?project=xxx
		{"POST", "/acme-accounts/:account_id/validate", h.ACME.ValidateACMEAccount}, // POST /api/v3/acme/acme-accounts/:account_id/validate?project=xxx

		// CA Providers
		{"GET", "/ca-providers", h.CAProviders.ListSupportedProviders},                         // GET /api/v3/acme/ca-providers
		{"POST", "/ca-providers/:provider/validate-eab", h.CAProviders.ValidateEABCredentials}, // POST /api/v3/acme/ca-providers/:provider/validate-eab
	}

	initRoutes(rg, routes)
}

// initDNSRoutes initializes GSLB DNS API routes
// These endpoints are used by CoreDNS plugin for DNS record retrieval
// Authentication: DNSAuthMiddleware validates zone-based X-Elchi-Secret header
func initDNSRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		// DNS Snapshot API (used by CoreDNS plugin)
		{"GET", "/snapshot", h.DNS.GetDNSSnapshot}, // GET /dns/snapshot?zone=X
		{"GET", "/changes", h.DNS.GetDNSChanges},   // GET /dns/changes?zone=X&since=VERSION
	}

	initRoutes(rg, routes)
}

// initGSLBRoutes initializes GSLB CRUD API routes
// These endpoints are for manual GSLB record management (Admin/Owner only for write operations)
func initGSLBRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		// GSLB Record CRUD
		{"GET", "", h.GSLB.ListGSLBRecords},         // GET /api/v3/gslb?project=X
		{"GET", "/:id", h.GSLB.GetGSLBRecord},       // GET /api/v3/gslb/:id?project=X
		{"POST", "", h.GSLB.CreateGSLBRecord},       // POST /api/v3/gslb (Admin/Owner only)
		{"PUT", "/:id", h.GSLB.UpdateGSLBRecord},    // PUT /api/v3/gslb/:id (Admin/Owner only)
		{"DELETE", "/:id", h.GSLB.DeleteGSLBRecord}, // DELETE /api/v3/gslb/:id?project=X (Admin/Owner only)

		// Bulk Operations
		{"PUT", "/batch", h.GSLB.BulkUpdateGSLBRecords}, // PUT /api/v3/gslb/batch (Admin/Owner only) - Bulk enable/disable

		// IP Management (NEW)
		{"GET", "/:id/ips", h.GSLB.ListIPsForRecord},            // GET /api/v3/gslb/:id/ips - List all IPs for a GSLB record
		{"POST", "/:id/ips", h.GSLB.AddIPToRecord},              // POST /api/v3/gslb/:id/ips (Admin/Owner only)
		{"PUT", "/:id/ips/:ip", h.GSLB.UpdateIPHealthState},     // PUT /api/v3/gslb/:id/ips/:ip (Admin/Owner only) - Manual health state control
		{"PUT", "/:id/ips/:ip/regions", h.GSLB.UpdateIPRegions}, // PUT /api/v3/gslb/:id/ips/:ip/regions (Admin/Owner only) - Assign regions to IP
		{"DELETE", "/:id/ips/:ip", h.GSLB.RemoveIPFromRecord},   // DELETE /api/v3/gslb/:id/ips/:ip (Admin/Owner only)
		{"DELETE", "/ip/:id/history", h.GSLB.ClearIPHistory},    // DELETE /api/v3/gslb/ip/:id/history (Admin/Owner only) - Clear status history for specific IP health document

		// GSLB Node Tracking (elchi-gslb instances)
		{"GET", "/nodes", h.GSLB.ListGSLBNodes},         // GET /api/v3/gslb/nodes?zone=X - List tracked GSLB nodes
		{"DELETE", "/nodes/:id", h.GSLB.DeleteGSLBNode}, // DELETE /api/v3/gslb/nodes/:id - Remove stale node entry

		// GSLB Node Proxy (forward requests to elchi-gslb instances)
		{"POST", "/nodes/notify-all", h.GSLB.ProxyNotifyAll},   // POST /api/v3/gslb/nodes/notify-all - Broadcast notify to all nodes in zone
		{"GET", "/nodes/:id/health", h.GSLB.ProxyNodeHealth},   // GET /api/v3/gslb/nodes/:id/health - Proxy health check
		{"GET", "/nodes/:id/records", h.GSLB.ProxyNodeRecords}, // GET /api/v3/gslb/nodes/:id/records - Proxy records query
		{"POST", "/nodes/:id/notify", h.GSLB.ProxyNodeNotify},  // POST /api/v3/gslb/nodes/:id/notify - Notify specific node
	}

	initRoutes(rg, routes)
}
