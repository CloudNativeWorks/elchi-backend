package common

import (
	"fmt"

	cluster "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/cluster/v3"
	core "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/core/v3"
	endpoint "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/endpoint/v3"
	listener "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/listener/v3"
	route "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/config/route/v3"
	tls "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	"github.com/CloudNativeWorks/versioned-go-control-plane/pkg/cache/types"
)

type Resources struct {
	NodeID          string
	Version         string
	ResourceVersion string
	Project         string
	Listener        []types.Resource
	Cluster         []types.Resource
	Route           []types.Resource
	Endpoint        []types.Resource
	Secret          []types.Resource
	Extensions      []types.Resource
	VirtualHost     []types.Resource
	UniqueResources map[string]struct{}
}

type AllResources interface {
	SetNodeID(nodeID string)
	GetNodeID() string

	SetResourceVersion(version string)
	SetVersion(version string)
	GetVersion() string

	SetProject(project string)
	GetProject() string

	SetListener(listener []types.Resource)
	GetListener() []*listener.Listener
	GetListenerT() []types.Resource

	SetCluster(cluster []types.Resource)
	GetCluster() []*cluster.Cluster
	GetClusterT() []types.Resource

	SetRoute(route *route.RouteConfiguration)
	GetRoute() *route.RouteConfiguration
	AppendRoute(route *route.RouteConfiguration)

	SetVirtualHost(virtualHost *route.VirtualHost)
	GetVirtualHost() *route.VirtualHost
	GetVirtualHostT() []types.Resource
	AppendVirtualHost(route *route.VirtualHost)

	SetEndpoint(endpoint []types.Resource)
	GetEndpoint() []*endpoint.Endpoint
	GetEndpointT() []types.Resource

	SetSecret(secret *tls.Secret)
	GetSecret() *tls.Secret
	AppendSecret(secret *tls.Secret)

	SetExtensions(extensions []types.Resource)
	GetExtensions() []*core.TypedExtensionConfig
	GetExtensionsT() []types.Resource
}

func (ar *Resources) SetNodeID(nodeID string) {
	ar.NodeID = nodeID
}

func (ar *Resources) GetNodeID() string {
	return ar.NodeID
}

func (ar *Resources) SetResourceVersion(version string) {
	ar.ResourceVersion = version
}

func (ar *Resources) SetVersion(version string) {
	ar.Version = version
}

func (ar *Resources) GetVersion() string {
	return ar.Version
}

func (ar *Resources) SetProject(project string) {
	ar.Project = project
}

func (ar *Resources) GetProject() string {
	return ar.Project
}

func (ar *Resources) SetListener(listener []types.Resource) {
	ar.Listener = listener
}

func (ar *Resources) GetListener() []*listener.Listener {
	listeners := make([]*listener.Listener, 0, len(ar.Listener))
	for _, res := range ar.Listener {
		if listenerConfig, ok := res.(*listener.Listener); ok {
			listeners = append(listeners, listenerConfig)
		} else {
			fmt.Printf("Unexpected type in ar.Listener")
		}
	}
	return listeners
}

func (ar *Resources) GetListenerT() []types.Resource {
	return ar.Listener
}

func (ar *Resources) GetVirtualHostT() []types.Resource {
	return ar.VirtualHost
}

func (ar *Resources) SetCluster(cluster []types.Resource) {
	ar.Cluster = cluster
}

func (ar *Resources) GetCluster() []*cluster.Cluster {
	clusters := make([]*cluster.Cluster, 0, len(ar.Cluster))
	for _, res := range ar.Cluster {
		if clusterConfig, ok := res.(*cluster.Cluster); ok {
			clusters = append(clusters, clusterConfig)
		} else {
			fmt.Printf("Unexpected type in ar.Cluster")
		}
	}
	return clusters
}

func (ar *Resources) GetClusterT() []types.Resource {
	return ar.Cluster
}

func (ar *Resources) SetRoute(route []types.Resource) {
	ar.Route = route
}

func (ar *Resources) AppendRoute(route *route.RouteConfiguration) {
	ar.Route = append(ar.Route, route)
}

func (ar *Resources) GetRoute() []*route.RouteConfiguration {
	routes := make([]*route.RouteConfiguration, 0, len(ar.Route))
	for _, res := range ar.Route {
		if routeConfig, ok := res.(*route.RouteConfiguration); ok {
			routes = append(routes, routeConfig)
		} else {
			fmt.Print("Unexpected type in ar.Route")
		}
	}
	return routes
}

func (ar *Resources) GetRouteT() []types.Resource {
	return ar.Route
}

func (ar *Resources) SetEndpoint(endpoint []types.Resource) {
	ar.Endpoint = endpoint
}

func (ar *Resources) GetEndpoint() []*endpoint.Endpoint {
	endpoints := make([]*endpoint.Endpoint, len(ar.Endpoint))
	for i, res := range ar.Endpoint {
		if endpointVal, ok := res.(*endpoint.Endpoint); ok {
			endpoints[i] = endpointVal
		} else {
			fmt.Printf("Unexpected type in ar.Endpoint at index %d", i)
		}
	}
	return endpoints
}

func (ar *Resources) GetEndpointT() []types.Resource {
	return ar.Endpoint
}

func (ar *Resources) SetSecret(secret []types.Resource) {
	ar.Secret = secret
}

func (ar *Resources) AppendSecret(secret *tls.Secret) {
	ar.Secret = append(ar.Secret, secret)
}

func (ar *Resources) GetSecret() []*tls.Secret {
	secret := make([]*tls.Secret, len(ar.Secret))
	for i, res := range ar.Secret {
		if secretVal, ok := res.(*tls.Secret); ok {
			secret[i] = secretVal
		} else {
			fmt.Printf("Unexpected type in ar.Secret at index %d", i)
		}
	}
	return secret
}

func (ar *Resources) GetSecretT() []types.Resource {
	return ar.Secret
}

func (ar *Resources) SetExtensions(extensions []types.Resource) {
	ar.Extensions = extensions
}

func (ar *Resources) GetExtensions() []*core.TypedExtensionConfig {
	extensions := make([]*core.TypedExtensionConfig, 0, len(ar.Extensions))

	for _, res := range ar.Extensions {
		if ext, ok := res.(*core.TypedExtensionConfig); ok {
			extensions = append(extensions, ext)
		} else {
			fmt.Printf("Unexpected type in Extensions slice: %T\n", res)
		}
	}

	return extensions
}

func (ar *Resources) GetExtensionsT() []types.Resource {
	return ar.Extensions
}
