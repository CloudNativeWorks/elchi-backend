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
