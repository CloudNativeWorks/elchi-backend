package config

import "fmt"

type AppConfig struct {
	ElchiAddress               string   `mapstructure:"ELCHI_ADDRESS" yaml:"ELCHI_ADDRESS"`
	ElchiPort                  string   `mapstructure:"ELCHI_PORT" yaml:"ELCHI_PORT"`
	ElchiTLSEnabled            string   `mapstructure:"ELCHI_TLS_ENABLED" yaml:"ELCHI_TLS_ENABLED"`
	ElchiVersions              []string `mapstructure:"ELCHI_VERSIONS" yaml:"ELCHI_VERSIONS"`
	ElchiInternalCommunication string   `mapstructure:"ELCHI_INTERNAL_COMMUNICATION" yaml:"ELCHI_INTERNAL_COMMUNICATION"`
	ElchiInternalAddressPort   string   `mapstructure:"ELCHI_INTERNAL_ADDRESS_PORT" yaml:"ELCHI_INTERNAL_ADDRESS_PORT"`
	ElchiNamespace             string   `mapstructure:"ELCHI_NAMESPACE" yaml:"ELCHI_NAMESPACE"`

	MongodbHosts      string `mapstructure:"MONGODB_HOSTS" yaml:"MONGODB_HOSTS"`
	MongodbUsername   string `mapstructure:"MONGODB_USERNAME" yaml:"MONGODB_USERNAME"`
	MongodbPassword   string `mapstructure:"MONGODB_PASSWORD" yaml:"MONGODB_PASSWORD"`
	MongodbPort       string `mapstructure:"MONGODB_PORT" yaml:"MONGODB_PORT"`
	MongodbDatabase   string `mapstructure:"MONGODB_DATABASE" yaml:"MONGODB_DATABASE"`
	MongodbScheme     string `mapstructure:"MONGODB_SCHEME" yaml:"MONGODB_SCHEME"`
	MongodbReplicaSet string `mapstructure:"MONGODB_REPLICASET" yaml:"MONGODB_REPLICASET"`
	MongodbTimeoutMs  string `mapstructure:"MONGODB_TIMEOUTMS" yaml:"MONGODB_TIMEOUTMS"`
	MongodbTLSEnabled string `mapstructure:"MONGODB_TLS_ENABLED" yaml:"MONGODB_TLS_ENABLED"`

	MongodbAuthSource    string `mapstructure:"MONGODB_AUTH_SOURCE" yaml:"MONGODB_AUTH_SOURCE"`
	MongodbAuthMechanism string `mapstructure:"MONGODB_AUTH_MECHANISM" yaml:"MONGODB_AUTH_MECHANISM"`

	// Keep nested for YAML compatibility
	Logging LoggingConfig `mapstructure:"LOGGING" yaml:"LOGGING"`

	// JWT Security configuration
	ElchiJWTSecret               string `mapstructure:"ELCHI_JWT_SECRET" yaml:"ELCHI_JWT_SECRET"`
	ElchiJWTAccessTokenDuration  string `mapstructure:"ELCHI_JWT_ACCESS_TOKEN_DURATION" yaml:"ELCHI_JWT_ACCESS_TOKEN_DURATION"`
	ElchiJWTRefreshTokenDuration string `mapstructure:"ELCHI_JWT_REFRESH_TOKEN_DURATION" yaml:"ELCHI_JWT_REFRESH_TOKEN_DURATION"`

	// CORS configuration
	ElchiCORSAllowedOrigins string `mapstructure:"ELCHI_CORS_ALLOWED_ORIGINS" yaml:"ELCHI_CORS_ALLOWED_ORIGINS"`

	// Listen-port configuration.
	// CONTROLLER_PORT     : HTTP/REST port the controller listens on (default 8099).
	// CONTROLLER_GRPC_PORT: gRPC port the controller's client server binds on
	//                       (default 50051). Override per-instance to run multiple
	//                       controllers on the same host.
	// REGISTRY_PORT       : gRPC port the registry listens on AND the port controllers/
	//                       control-planes use to dial it (default 9090).
	// CONTROL_PLANE_PORT  : gRPC xDS port the control-plane listens on (default 18000).
	// Registry and control-plane also accept a `--port` CLI flag override.
	// If unset (or 0) the binaries fall back to their built-in defaults — startup
	// never fails on a missing port.
	RegistryAddress    string `mapstructure:"REGISTRY_ADDRESS" yaml:"REGISTRY_ADDRESS"`
	RegistryPort       uint   `mapstructure:"REGISTRY_PORT" yaml:"REGISTRY_PORT"`
	ControlPlanePort   uint   `mapstructure:"CONTROL_PLANE_PORT" yaml:"CONTROL_PLANE_PORT"`
	ControllerPort     uint   `mapstructure:"CONTROLLER_PORT" yaml:"CONTROLLER_PORT"`
	ControllerGRPCPort uint   `mapstructure:"CONTROLLER_GRPC_PORT" yaml:"CONTROLLER_GRPC_PORT"`

	// REGISTRY_TLS_ENABLED — opt-in TLS dialing for the registry gRPC
	// channel. Default false (legacy plaintext, unchanged behavior).
	// When true, controller and control-plane dial the registry with TLS
	// using the system trust pool + standard hostname verification. The
	// server itself is expected to be fronted by a TLS terminator
	// (sidecar / mesh / load balancer) — this flag does NOT bind the
	// registry's listener to TLS, only the client side of the connection.
	RegistryTLSEnabled bool `mapstructure:"REGISTRY_TLS_ENABLED" yaml:"REGISTRY_TLS_ENABLED"`

	// CONTROLLER_ID overrides the auto-derived controller identity used for
	// x-target-cluster routing.
	// Default (K8s):     hostname (StatefulSet pod name).
	// Default (non-K8s): "<hostname>-controller"
	// Controller is version-agnostic so the suffix omits the envoy version.
	ControllerID string `mapstructure:"CONTROLLER_ID" yaml:"CONTROLLER_ID"`

	// CONTROL_PLANE_ID overrides the auto-derived control-plane identity used
	// for x-target-cluster routing.
	// Default (K8s):     hostname (StatefulSet pod name).
	// Default (non-K8s): "<hostname>-controlplane-<version>"
	//                    (e.g. "m2host-controlplane-1.38.0").
	// Set this when running multiple control-plane binaries on the same host
	// and you want to publish a custom name in /etc/hosts and Envoy clusters.
	ControlPlaneID string `mapstructure:"CONTROL_PLANE_ID" yaml:"CONTROL_PLANE_ID"`

	// Registry HA — leader election + state snapshot (active-passive).
	// All four are optional; built-in defaults apply when blank/zero.
	// Defaults: lock TTL 30s, renewal 10s, snapshot write 5m (leader),
	// snapshot poll 30s (standby). See registry/leader and registry/snapshot.
	RegistryLeaderLockTTL         string `mapstructure:"REGISTRY_LEADER_LOCK_TTL"          yaml:"REGISTRY_LEADER_LOCK_TTL"`
	RegistryLeaderRenewalInterval string `mapstructure:"REGISTRY_LEADER_RENEWAL_INTERVAL" yaml:"REGISTRY_LEADER_RENEWAL_INTERVAL"`
	RegistrySnapshotInterval      string `mapstructure:"REGISTRY_SNAPSHOT_INTERVAL"       yaml:"REGISTRY_SNAPSHOT_INTERVAL"`
	RegistrySnapshotPollInterval  string `mapstructure:"REGISTRY_SNAPSHOT_POLL_INTERVAL"  yaml:"REGISTRY_SNAPSHOT_POLL_INTERVAL"`

	// ACME certificate management configuration
	ACME ACMEConfig `mapstructure:"ACME" yaml:"ACME"`

	// CA Providers configuration (embedded in config instead of separate file)
	CAProviders map[string]CAProviderConfig `mapstructure:"CA_PROVIDERS" yaml:"CA_PROVIDERS"`
}

type LoggingConfig struct {
	Level      string `mapstructure:"level"`
	Format     string `mapstructure:"format"`
	OutputPath string `mapstructure:"output_path"`
}

// ACMEConfig contains ACME certificate management configuration
type ACMEConfig struct {
	Enabled            bool   `mapstructure:"enabled" yaml:"enabled"`
	DefaultEnvironment string `mapstructure:"default_environment" yaml:"default_environment"` // "staging" | "production"
	DefaultCAProvider  string `mapstructure:"default_ca_provider" yaml:"default_ca_provider"` // "letsencrypt" | "google"
}

// CAProviderEnvironment represents a specific environment (staging/production) for a CA provider
type CAProviderEnvironment struct {
	DirectoryURL string         `yaml:"directory_url" mapstructure:"directory_url"`
	RateLimits   map[string]int `yaml:"rate_limits" mapstructure:"rate_limits"`
}

// CAProviderConfig represents a Certificate Authority provider configuration
type CAProviderConfig struct {
	Name               string                           `yaml:"name" mapstructure:"name"`
	Description        string                           `yaml:"description" mapstructure:"description"`
	Supported          bool                             `yaml:"supported" mapstructure:"supported"`
	RequiresEAB        bool                             `yaml:"requires_eab" mapstructure:"requires_eab"`
	EABInstructionsURL string                           `yaml:"eab_instructions_url,omitempty" mapstructure:"eab_instructions_url"`
	Environments       map[string]CAProviderEnvironment `yaml:"environments" mapstructure:"environments"`
}

// GetDirectoryURL returns the ACME directory URL for a given provider and environment
func (a *AppConfig) GetDirectoryURL(provider, environment string) (string, error) {
	providerConfig, exists := a.CAProviders[provider]
	if !exists {
		return "", fmt.Errorf("unknown CA provider: %s", provider)
	}

	if !providerConfig.Supported {
		return "", fmt.Errorf("CA provider %s is not yet supported", provider)
	}

	envConfig, exists := providerConfig.Environments[environment]
	if !exists {
		return "", fmt.Errorf("environment %s not supported for %s", environment, provider)
	}

	return envConfig.DirectoryURL, nil
}

// RequiresEAB returns true if the given CA provider requires External Account Binding
func (a *AppConfig) RequiresEAB(provider string) bool {
	if providerConfig, exists := a.CAProviders[provider]; exists {
		return providerConfig.RequiresEAB
	}
	return false
}

// GetSupportedProviders returns a list of all supported CA providers
func (a *AppConfig) GetSupportedProviders() []string {
	var providers []string
	for name, config := range a.CAProviders {
		if config.Supported {
			providers = append(providers, name)
		}
	}
	return providers
}
