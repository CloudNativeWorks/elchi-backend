// Package router provides HTTP route definitions and middleware configuration
// for the controller REST API using Gin framework.
package router

import (
	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"

	"github.com/CloudNativeWorks/elchi-backend/controller/api/middleware"
	"github.com/CloudNativeWorks/elchi-backend/controller/handlers"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

func InitRouter(h *handlers.Handler) *gin.Engine {
	logger := logger.NewLogger("controller/router")
	gin.SetMode(gin.ReleaseMode)
	e := gin.New()

	e.HandleMethodNotAllowed = true
	e.ForwardedByClientIP = true

	e.Use(middleware.CORS())
	e.Use(middleware.ValidateSearchInput()) // Add early search input validation BEFORE PathCheck
	e.Use(middleware.PathCheck())
	e.Use(middleware.GinLog(logger.Logger), gin.Recovery())

	// Register pprof endpoints at /debug/pprof/*
	// Access: http://localhost:8099/debug/pprof/
	pprof.Register(e)
	logger.Logger.Info("pprof profiling endpoints enabled at /debug/pprof/")

	e.POST("/logout", middleware.Authentication(h.XDS.Context), h.Settings.Logout())
	e.POST("/refresh", middleware.Refresh(), h.Settings.Refresh())

	api := e.Group("/api")
	v3 := api.Group("/v3")
	op := api.Group("/op")

	// Add body capture middleware before authentication for groups
	v3.Use(middleware.BodyCaptureMiddleware())
	op.Use(middleware.BodyCaptureMiddleware())

	v3.Use(middleware.Authentication(h.XDS.Context))
	op.Use(middleware.Authentication(h.XDS.Context))

	// Add audit middleware after authentication for authenticated groups
	if h.AuditService != nil {
		v3.Use(middleware.AuditMiddleware(h.AuditService))
		op.Use(middleware.AuditMiddleware(h.AuditService))
	}

	apiAuth := e.Group("/auth")
	apiSettings := v3.Group("/setting")
	apiProfile := v3.Group("/profile")
	apiCustom := v3.Group("/custom")
	apiExtension := v3.Group("/eo")
	apiExtension.Use(middleware.ValidateResourceParams()) // Add resource parameter validation
	apiResource := v3.Group("/xds")
	apiResource.Use(middleware.ValidateResourceParams()) // Add resource parameter validation
	apiResourceOps := v3.Group("/resource")
	apiDependency := v3.Group("/dependency")
	apiScenario := v3.Group("/scenario")
	apiBridge := v3.Group("/bridge")
	apiClient := op.Group("/clients")
	apiClient.Use(middleware.InitSettingMiddleware()) // Only owners and admins can manage clients
	apiService := op.Group("/services")
	apiService.Use(middleware.InitSettingMiddleware()) // Only owners and admins can manage services
	apiRegistry := v3.Group("/registry")
	apiAI := v3.Group("/ai")
	apiDiscovery := api.Group("/discovery")
	// Add audit middleware for discovery endpoints (token-based auth)
	if h.AuditService != nil {
		apiDiscovery.Use(middleware.AuditMiddleware(h.AuditService))
	}
	apiJobs := v3.Group("/jobs")
	apiAudit := v3.Group("/audit")
	apiAudit.Use(middleware.InitSettingMiddleware()) // Only owners and admins can access audit logs
	apiTemplates := v3.Group("/templates")
	apiRouteMap := v3.Group("/routemap")
	apiSnippets := v3.Group("/snippets")
	apiSearch := v3.Group("/search")
	apiWAF := v3.Group("/waf")
	apiMaintenance := apiSettings.Group("/maintenance") // Maintenance routes under settings
	apiACME := v3.Group("/acme")
	apiDNS := e.Group("/dns")                               // GSLB DNS API routes (zone-based authentication via middleware)
	apiDNS.Use(middleware.DNSAuthMiddleware(h.XDS.Context)) // Zone-based DNS authentication
	apiGSLB := v3.Group("/gslb")                            // GSLB CRUD API routes (handlers perform own Admin/Owner checks)

	initAuthRoutes(apiAuth, h)
	initSettingRoutes(apiSettings, h)
	initProfileRoutes(apiProfile, h) // Profile routes under /api/v3/profile
	initCustomRoutes(apiCustom, h)
	initExtensionRoutes(apiExtension, h)
	initScenarioRoutes(apiScenario, h)
	initResourceRoutes(apiResource, h)
	initResourceOpsRoutes(apiResourceOps, h)
	initDependencyRoutes(apiDependency, h)
	initBridgeRoutes(apiBridge, h)
	initClientRoutes(apiClient, h)
	initServiceRoutes(apiService, h)
	initRegistryRoutes(apiRegistry, h)
	initAIRoutes(apiAI, h)
	initDiscoveryRoutes(apiDiscovery, h)
	initJobRoutes(apiJobs, h)
	initAuditRoutes(apiAudit, h)
	initTemplateRoutes(apiTemplates, h)
	initRouteMapRoutes(apiRouteMap, h)
	initSnippetRoutes(apiSnippets, h)
	initSearchRoutes(apiSearch, h)
	initWAFRoutes(apiWAF, h)
	initMaintenanceRoutes(apiMaintenance, h) // Maintenance/cleanup routes
	initOpenStackRoutes(apiClient, h)        // OpenStack routes under /api/op/clients
	initACMERoutes(apiACME, h)               // ACME routes under /api/v3/acme
	initDNSRoutes(apiDNS, h)                 // GSLB DNS routes under /api/v3/dns
	initGSLBRoutes(apiGSLB, h)               // GSLB CRUD routes under /api/v3/gslb

	// logRoutes(e)
	return e
}

/* func logRoutes(r *gin.Engine) {
	log.Println("Registered Routes:")
	for _, route := range r.Routes() {
		log.Printf("Method: %s, Path: %s\n", route.Method, route.Path)
	}
}
*/
