package models

type Settings struct {
	Project         string                 `bson:"project"`
	Tokens          []Token                `bson:"tokens"`
	OpenRouterToken string                 `bson:"openrouter_token,omitempty"`
	AIDefaultModel  string                 `bson:"ai_default_model,omitempty"`
	DiscoveryToken  string                 `bson:"discovery_token,omitempty"`
	Clouds          map[string]CloudConfig `bson:"clouds,omitempty"`
	LDAPConfig      *LDAPConfig            `bson:"ldap_config,omitempty"`

	// OTP/2FA enforcement
	OTPEnforced bool `json:"otp_enforced" bson:"otp_enforced"` // Project-level OTP requirement

	// GSLB Configuration
	GSLBConfig *GSLBConfig `bson:"gslb_config,omitempty" json:"gslb_config,omitempty"`

	// Audit-log syslog forwarding (global; lives only on the system-settings document).
	SyslogConfig *SyslogConfig `bson:"syslog_config,omitempty" json:"syslog_config,omitempty"`
}

type Token struct {
	Token string `bson:"token"`
	Name  string `bson:"name"`
	ID    string `bson:"id"`
}

type CloudConfig struct {
	Provider           string    `json:"provider" bson:"provider"` // "openstack", "aws", "azure", "gcp" etc.
	Auth               CloudAuth `json:"auth" bson:"auth"`
	RegionName         string    `json:"region_name" bson:"region_name"`
	Interface          string    `json:"interface,omitempty" bson:"interface,omitempty"`                       // OpenStack specific
	IdentityAPIVersion int       `json:"identity_api_version,omitempty" bson:"identity_api_version,omitempty"` // OpenStack specific
	AuthType           string    `json:"auth_type,omitempty" bson:"auth_type,omitempty"`                       // OpenStack specific
}

type CloudAuth struct {
	// OpenStack fields
	AuthURL                     string `json:"auth_url,omitempty" bson:"auth_url,omitempty"`
	ApplicationCredentialID     string `json:"application_credential_id,omitempty" bson:"application_credential_id,omitempty"`
	ApplicationCredentialSecret string `json:"application_credential_secret,omitempty" bson:"application_credential_secret,omitempty"`

	// Future: AWS fields
	// AccessKeyID     string `json:"access_key_id,omitempty" bson:"access_key_id,omitempty"`
	// SecretAccessKey string `json:"secret_access_key,omitempty" bson:"secret_access_key,omitempty"`

	// Future: Azure fields
	// TenantID       string `json:"tenant_id,omitempty" bson:"tenant_id,omitempty"`
	// ClientID       string `json:"client_id,omitempty" bson:"client_id,omitempty"`
	// ClientSecret   string `json:"client_secret,omitempty" bson:"client_secret,omitempty"`
}

// LDAPConfig represents LDAP configuration for authentication
type LDAPConfig struct {
	Enabled       bool   `json:"enabled" bson:"enabled"`
	Server        string `json:"server" bson:"server"`
	Port          int    `json:"port" bson:"port"`
	BaseDN        string `json:"base_dn" bson:"base_dn"`
	UserFilter    string `json:"user_filter" bson:"user_filter"` // (uid={username})
	BindUser      string `json:"bind_user" bson:"bind_user"`
	BindPassword  string `json:"bind_password" bson:"bind_password"`
	TLSEnabled    bool   `json:"tls_enabled" bson:"tls_enabled"`
	TLSSkipVerify bool   `json:"tls_skip_verify" bson:"tls_skip_verify"`
}

// SyslogConfig stores the global audit-log syslog/SIEM forwarding settings.
// Stored as a sub-document on the system-settings record (project = sentinel).
type SyslogConfig struct {
	Enabled  bool   `bson:"enabled" json:"enabled"`
	Protocol string `bson:"protocol" json:"protocol"` // "udp" | "tcp" | "tcp+tls"
	Host     string `bson:"host" json:"host"`
	Port     int    `bson:"port" json:"port"`
	Facility string `bson:"facility,omitempty" json:"facility,omitempty"` // "local0".."local7" (default local0)
	Tag      string `bson:"tag,omitempty" json:"tag,omitempty"`           // RFC5424 APP-NAME (default "elchi-audit")

	// TLS material — populated only when Protocol == "tcp+tls".
	CACert     string `bson:"ca_cert,omitempty" json:"ca_cert,omitempty"`
	ClientCert string `bson:"client_cert,omitempty" json:"client_cert,omitempty"`
	ClientKey  string `bson:"client_key,omitempty" json:"client_key,omitempty"`

	// Tuning knobs (zeros fall back to defaults inside pkg/syslog).
	QueueSize        int `bson:"queue_size,omitempty" json:"queue_size,omitempty"`
	ConnectTimeoutMs int `bson:"connect_timeout_ms,omitempty" json:"connect_timeout_ms,omitempty"`
	WriteTimeoutMs   int `bson:"write_timeout_ms,omitempty" json:"write_timeout_ms,omitempty"`

	// Read-only response indicators — never persisted.
	HasCACert     bool `bson:"-" json:"has_ca_cert,omitempty"`
	HasClientCert bool `bson:"-" json:"has_client_cert,omitempty"`
	HasClientKey  bool `bson:"-" json:"has_client_key,omitempty"`
}

// GSLBConfig represents GSLB (Global Server Load Balancing) configuration
type GSLBConfig struct {
	Enabled       bool     `bson:"enabled" json:"enabled"`
	Zone          string   `bson:"zone" json:"zone"`                                         // DNS zone (e.g., "avrupa-gslb.elchi")
	FailoverZones []string `bson:"failover_zones,omitempty" json:"failover_zones,omitempty"` // Optional failover zones array (e.g., ["asya-gslb.elchi", "us-gslb.elchi"]) - first one is default
	DNSSecret     string   `bson:"dns_secret" json:"dns_secret"`                             // CoreDNS plugin authentication secret
	DefaultTTL    uint32   `bson:"default_ttl" json:"default_ttl"`                           // Default TTL for auto-created records (e.g., 60 seconds)
	Regions       []string `bson:"regions,omitempty" json:"regions,omitempty"`                // Predefined region names for geographic filtering (e.g., ["asya", "avrupa", "us-east"])
}
