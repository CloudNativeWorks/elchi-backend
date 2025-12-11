package models

type RequestDetails struct {
	ResourceID     string
	Collection     string
	Type           KnownTYPES
	GType          GType
	CanonicalName  string
	Name           string
	Category       string
	Version        string
	User           UserDetails
	SaveOrPublish  string
	Project        string
	Metadata       map[string]string
	WithServiceIPs string
	ClientID       string
	ServiceID      string
	FromClient     string
	ForMetrics     string
	Search         string // Search query for filtering resources
	Validate       string // Enable resource validation (validate=true)
	// Forward support fields
	Token             string
	RefreshToken      string
	IsForwarded       bool
	OriginalBody      []byte
	DownstreamAddress string
}

type UserDetails struct {
	Groups    []string
	Projects  []string
	BaseGroup string
	Role      Role
	IsOwner   bool
	UserID    string
	UserName  string
}
