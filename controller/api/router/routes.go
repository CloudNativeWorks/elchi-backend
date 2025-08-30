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
	}

	initRoutes(rg, routes)
}

func initAuthRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	enableDemo := h.Settings.Context.Config.ElchiEnableDemo == "true"
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"POST", "/login", h.Settings.Login()},
	}

	if enableDemo {
		routes = append(routes, struct {
			method  string
			path    string
			handler gin.HandlerFunc
		}{
			"POST", "/demo/:email", h.Settings.DemoAccount,
		})
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
		{"PUT", "/user/:user_id", h.Settings.SetUpdateUser},
		{"DELETE", "/user/:user_id", h.Settings.DeleteUser},

		{"GET", "/group_list", h.Settings.ListGroups},
		{"GET", "/group/:group_id", h.Settings.GetGroup},
		{"PUT", "/group/:group_id", h.Settings.SetUpdateGroup},
		{"DELETE", "/group/:group_id", h.Settings.DeleteGroup},

		{"GET", "/project_list", h.Settings.ListProjects},
		{"GET", "/project/:project_id", h.Settings.GetProject},
		{"PUT", "/project/:project_id", h.Settings.SetUpdateProject},
		{"DELETE", "/project/:project_id", h.Settings.DeleteProject},

		{"GET", "/permissions/:kind/:type/:id", h.Settings.GetPermissions},

		{"GET", "/tokens", h.Settings.GetTokens},
		{"POST", "/tokens", h.Settings.SetToken},
		{"DELETE", "/tokens/:token_id", h.Settings.DeleteToken},

		{"GET", "/openrouter-token", h.Settings.GetOpenRouterToken},
		{"POST", "/openrouter-token", h.Settings.SetOpenRouterToken},
		{"PUT", "/openrouter-token", h.Settings.UpdateOpenRouterToken},
		{"DELETE", "/openrouter-token", h.Settings.DeleteOpenRouterToken},

		{"GET", "/discovery-token", h.Settings.GetDiscoveryToken},
		{"DELETE", "/discovery-token", h.Settings.DeleteDiscoveryToken},
		{"POST", "/discovery-token/generate", h.Settings.GenerateDiscoveryToken},

		// Cloud configuration endpoints
		{"GET", "/clouds", h.Settings.GetClouds},
		{"GET", "/clouds/:cloud_name", h.Settings.GetCloud},
		{"POST", "/clouds/:cloud_name", h.Settings.SetCloud},
		{"PUT", "/clouds/:cloud_name", h.Settings.UpdateCloud},
		{"DELETE", "/clouds/:cloud_name", h.Settings.DeleteCloud},
	}

	initRoutes(rg, routes)
}

func initCustomRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"GET", "/resource_list", h.GetCustomResourceList},
		{"GET", "/http_filter_list", h.GetCustomHTTPFilterList},
		{"GET", "/available_versions", h.GetAvailableVersions},

		{"GET", "/count/all", h.GetResourceCounts},
		{"GET", "/count/filters", h.GetFilterCounts},
		{"GET", "/error_summary", h.GetErrorSummary},
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
	}

	initRoutes(rg, routes)
}

func initJobRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"GET", "", h.ListJobs},                    // GET /api/v3/jobs
		{"GET", "/stats", h.GetJobStats},           // GET /api/v3/jobs/stats
		{"GET", "/:id", h.GetJob},                  // GET /api/v3/jobs/:id
		{"POST", "/:id/retry", h.RetryJob},         // POST /api/v3/jobs/:id/retry
		{"GET", "/stuck", h.GetStuckJobs},          // GET /api/v3/jobs/stuck
		{"GET", "/workers", h.GetWorkerStatus},     // GET /api/v3/jobs/workers
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
		{"GET", "/openstack/networks/:network_id", h.GetNetworkDetails},
		{"GET", "/openstack/subnets/:subnet_id", h.GetSubnetDetails},
		{"GET", "/openstack/networks/:network_id/subnets", h.GetNetworkSubnets},
	}

	initRoutes(rg, routes)
}
