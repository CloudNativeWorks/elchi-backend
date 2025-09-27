package catalog

import "github.com/CloudNativeWorks/elchi-backend/pkg/models"

// RouterFilterDefinition defines the router HTTP filter component
var RouterFilterDefinition = models.ComponentDefinition{
	Name:            "router_filter",
	Label:           "Router HTTP Filter",
	Description:     "HTTP router filter for routing requests to upstream clusters",
	Category:        "envoy.extensions.filters.http.router.v3.Router",
	Collection:      "filters",
	CanonicalName:   "envoy.filters.http.router",
	GType:           "envoy.extensions.filters.http.router.v3.Router",
	Priority:        300,
	AvailableFields: []models.AvailableField{
		// Router filter has no configuration fields - metadata is set automatically
	},
	Rules: models.ComponentRule{
		RequiredWith: []string{"http_connection_manager"},
		MinCount:     0,
		MaxCount:     20,
	},
}
