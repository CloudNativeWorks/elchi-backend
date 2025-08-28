package models

type Settings struct {
	Project          string                `bson:"project"`
	Tokens           []Token               `bson:"tokens"`
	OpenRouterToken  string                `bson:"openrouter_token,omitempty"`
	AIDefaultModel   string                `bson:"ai_default_model,omitempty"`
	DiscoveryToken   string                `bson:"discovery_token,omitempty"`
	Clouds           map[string]CloudConfig `bson:"clouds,omitempty"`
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
	Interface          string    `json:"interface,omitempty" bson:"interface,omitempty"` // OpenStack specific
	IdentityAPIVersion int       `json:"identity_api_version,omitempty" bson:"identity_api_version,omitempty"` // OpenStack specific
	AuthType           string    `json:"auth_type,omitempty" bson:"auth_type,omitempty"` // OpenStack specific
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