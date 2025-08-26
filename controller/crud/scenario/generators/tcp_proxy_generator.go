package generators

import (
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// TCPProxyGenerator generates TCP proxy filter resources
type TCPProxyGenerator struct {
	*BaseGenerator
}

// NewTCPProxyGenerator creates a new TCP proxy generator
func NewTCPProxyGenerator(project, version string, user models.UserDetails) *TCPProxyGenerator {
	return &TCPProxyGenerator{
		BaseGenerator: NewBaseGenerator(project, version, user),
	}
}

// Generate generates a TCP proxy filter resource document
func (tg *TCPProxyGenerator) Generate(instance models.ComponentInstance) (any, error) {
	// Get gtype from instance and use it to build general section dynamically
	gtype := models.GType(instance.GType)
	
	// Build general section using GType methods
	general := tg.BuildGeneralSection(
		instance,
		gtype.Type(),
		gtype.CollectionString(),
		gtype.CanonicalName(),
		gtype.String(),
		gtype.Category(),
	)
	
	// Build TCP proxy configuration - only include selected fields
	tcpProxyConfig := map[string]any{}
	
	// Add stat_prefix only if selected
	if statPrefix := tg.GetFieldValueIfSelected(instance.SelectedFields, "stat_prefix"); statPrefix != nil {
		tcpProxyConfig["stat_prefix"] = statPrefix
	}
	
	// Handle cluster routing only if selected
	if cluster := tg.GetFieldValueIfSelected(instance.SelectedFields, "cluster"); cluster != nil {
		// Single cluster
		tcpProxyConfig["cluster"] = cluster
	}
	
	// Add new optional fields only if selected
	if idleTimeout := tg.GetFieldValueIfSelected(instance.SelectedFields, "idle_timeout"); idleTimeout != nil {
		tcpProxyConfig["idle_timeout"] = idleTimeout
	}
	
	if maxConnectAttempts := tg.GetFieldValueIfSelected(instance.SelectedFields, "max_connect_attempts"); maxConnectAttempts != nil {
		tcpProxyConfig["max_connect_attempts"] = maxConnectAttempts
	}
	
	if maxDownstreamConnectionDuration := tg.GetFieldValueIfSelected(instance.SelectedFields, "max_downstream_connection_duration"); maxDownstreamConnectionDuration != nil {
		tcpProxyConfig["max_downstream_connection_duration"] = maxDownstreamConnectionDuration
	}
	
	// Return tcpProxyConfig directly, BuildCompleteDocument will wrap it properly
	return tg.BuildCompleteDocument(general, tcpProxyConfig), nil
}

// GetComponentType returns the component type
func (tg *TCPProxyGenerator) GetComponentType() string {
	return "tcp_proxy"
}

// GetCollection returns the MongoDB collection name
func (tg *TCPProxyGenerator) GetCollection() string {
	return "filters"
}