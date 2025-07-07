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
		{"GET", "/envoys/:service_id", h.GetEnvoyDetails},
		{"GET", "/:service_id", h.GetService},
	}

	initRoutes(rg, routes)
}

func initAuthRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	enableDemo := h.Auth.Context.Config.ElchiEnableDemo == "true"
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"POST", "/login", h.Auth.Login()},
	}

	if enableDemo {
		routes = append(routes, struct {
			method  string
			path    string
			handler gin.HandlerFunc
		}{
			"POST", "/demo/:email", h.Auth.DemoAccount,
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
		{"GET", "/stats/snapshot-keys", h.GetSnapshotKeys},
		{"GET", "/stats/:name", h.GetSnapshotResources},
		{"POST", "/poke/:name", h.GetSnapshotResources},
		{"GET", "/snapshot_details", h.GetSnapshotDetails},
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
		{"GET", "/user_list", h.Auth.ListUsers},
		{"GET", "/user/:user_id", h.Auth.GetUser},
		{"PUT", "/user/:user_id", h.Auth.SetUpdateUser},
		{"DELETE", "/user/:user_id", h.Auth.DeleteUser},

		{"GET", "/group_list", h.Auth.ListGroups},
		{"GET", "/group/:group_id", h.Auth.GetGroup},
		{"PUT", "/group/:group_id", h.Auth.SetUpdateGroup},
		{"DELETE", "/group/:group_id", h.Auth.DeleteGroup},

		{"GET", "/project_list", h.Auth.ListProjects},
		{"GET", "/project/:project_id", h.Auth.GetProject},
		{"PUT", "/project/:project_id", h.Auth.SetUpdateProject},
		{"DELETE", "/project/:project_id", h.Auth.DeleteProject},

		{"GET", "/permissions/:kind/:type/:id", h.Auth.GetPermissions},
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

		{"GET", "/count/all", h.GetResourceCounts},
		{"GET", "/count/filters", h.GetFilterCounts},
	}

	initRoutes(rg, routes)
}

func initScenarioRoutes(rg *gin.RouterGroup, h *handlers.Handler) {
	routes := []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"GET", "/scenario_list", h.GetScenarios},
		{"GET", "/scenario", h.GetScenario},
		{"POST", "/scenario", h.SetScenario},
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
