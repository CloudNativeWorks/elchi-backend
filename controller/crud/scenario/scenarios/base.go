package scenarios

type Scenario string

const (
	Scenario1 Scenario = "1"
	Scenario2 Scenario = "2"
	Scenario3 Scenario = "3"
	Scenario4 Scenario = "4"
	Scenario5 Scenario = "5"
	Scenario6 Scenario = "6"
)

type ResourceTemplate struct {
	Name        string              `json:"name"`
	Scenario    Scenario            `json:"scenario"`
	Description string              `json:"description"`
	Components  []ComponentTemplate `json:"components"`
}

type ComponentTemplate struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

var Resources = []ResourceTemplate{
	{
		Name:        "Basic HTTP Service",
		Scenario:    Scenario1,
		Description: "Defines an HTTP service with a Non-EDS Cluster, HttpConnectionManager, and a Listener.",
		Components: []ComponentTemplate{
			{
				Name:        "non_eds_cluster",
				Title:       "Non-Eds Cluster",
				Description: "cluster configuration.",
			},
			{
				Name:        "basic_hcm",
				Title:       "Http Connection Manager",
				Description: "http connection manager configuration.",
			},
			{
				Name:        "single_listener_http",
				Title:       "Listener",
				Description: "listener configuration.",
			},
		},
	},
	{
		Name:        "HTTP Service with Endpoints (EDS)",
		Scenario:    Scenario2,
		Description: "Configures an HTTP service that uses Endpoint Discovery Service (EDS), with a Cluster, HttpConnectionManager, and a Listener.",
		Components: []ComponentTemplate{
			{
				Name:        "endpoint",
				Title:       "Endpoint",
				Description: "endpoint configuration.",
			},
			{
				Name:        "eds_cluster",
				Title:       "EDS Cluster",
				Description: "cluster configuration.",
			},
			{
				Name:        "basic_hcm",
				Title:       "Http Connection Manager",
				Description: "http connection manager configuration.",
			},
			{
				Name:        "single_listener_http",
				Title:       "Listener",
				Description: "listener configuration.",
			},
		},
	},
	{
		Name:        "HTTP Service with Endpoints and Routing",
		Scenario:    Scenario3,
		Description: "Extends the HTTP service to include Route configuration, enabling request routing. Includes Endpoints, a Cluster, Route, HttpConnectionManager, and a Listener.",
		Components: []ComponentTemplate{
			{
				Name:        "endpoint",
				Title:       "Endpoint",
				Description: "endpoint configuration.",
			},
			{
				Name:        "eds_cluster",
				Title:       "EDS Cluster",
				Description: "cluster configuration.",
			},
			{
				Name:        "route_with_direct_virtualhost",
				Title:       "Route",
				Description: "route configuration.",
			},
			{
				Name:        "rds_hcm",
				Title:       "Http Connection Manager",
				Description: "http connection manager configuration.",
			},
			{
				Name:        "single_listener_http",
				Title:       "Listener",
				Description: "listener configuration.",
			},
		},
	},
	{
		Name:        "HTTP Service with Virtual Host, Routing, and Endpoints",
		Scenario:    Scenario4,
		Description: "A fully configured HTTP service that supports Virtual Hosts for advanced routing. Includes Endpoints, a Cluster, Virtual Hosts, Routes, HttpConnectionManager, and a Listener.",
		Components: []ComponentTemplate{
			{
				Name:        "endpoint",
				Title:       "Endpoint",
				Description: "endpoint configuration.",
			},
			{
				Name:        "eds_cluster",
				Title:       "EDS Cluster",
				Description: "cluster configuration.",
			},
			{
				Name:        "virtual_host",
				Title:       "Virtual Host",
				Description: "virtual host configuration.",
			},
			{
				Name:        "route_with_vhds",
				Title:       "Route",
				Description: "route configuration.",
			},
			{
				Name:        "rds_hcm",
				Title:       "Http Connection Manager",
				Description: "http connection manager configuration.",
			},
			{
				Name:        "single_listener_http",
				Title:       "Listener",
				Description: "listener configuration.",
			},
		},
	},
	{
		Name:        "Basic TCP Service",
		Scenario:    Scenario5,
		Description: "Defines a TCP service with a Cluster, TcpProxy, and a Listener.",
		Components: []ComponentTemplate{
			{
				Name:        "non_eds_cluster",
				Title:       "Non-Eds Cluster",
				Description: "cluster configuration.",
			},
			{
				Name:        "tcp_proxy",
				Title:       "TCP Proxy",
				Description: "tcp proxy configuration.",
			},
			{
				Name:        "single_listener_tcp",
				Title:       "Listener",
				Description: "listener configuration.",
			},
		},
	},
	/* {
		Name:        "Multiple Listener Service (HTTP & HTTPS)",
		Scenario:    Scenario6,
		Description: "Configures a service with multiple listeners, including one for HTTP and another with SSL (HTTPS). Includes Endpoints, a Cluster, HttpConnectionManager, and multiple Listeners.",
		Components: []ComponentTemplate{
			{
				Name:        "cluster",
				Title:       "Cluster",
				Description: "cluster configuration.",
			},
			{
				Name:        "http_connection_manager",
				Title:       "Http Connection Manager",
				Description: "http connection manager configuration.",
			},
			{
				Name:        "listener",
				Title:       "Listener",
				Description: "listener configuration.",
			},
		},
	}, */
}

var Scenarios = map[Scenario]map[string]string{
	"1": {
		"cluster":  NonEdsCluster,
		"listener": SingleListenerHTTP,
		"hcm":      BasicHcm,
	},
	"2": {
		"endpoint": Endpoint,
		"cluster":  EdsCluster,
		"listener": SingleListenerHTTP,
		"hcm":      BasicHcm,
	},
	"3": {
		"endpoint": Endpoint,
		"cluster":  EdsCluster,
		"listener": SingleListenerHTTP,
		"hcm":      RDSHcm,
		"route":    RouteWithDirectVirtualHost,
	},
	"4": {
		"endpoint":     Endpoint,
		"cluster":      EdsCluster,
		"virtual_host": virtualHost,
		"listener":     SingleListenerHTTP,
		"hcm":          RDSHcm,
		"route":        RouteWithVHDS,
	},
	"5": {
		"cluster":   NonEdsCluster,
		"tcp_proxy": TCPProxy,
		"listener":  SingleListenerTCP,
	},
}
