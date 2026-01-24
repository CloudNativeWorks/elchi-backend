// Package ldap provides LDAP authentication client for integrating
// with directory services for user authentication.
package ldap

import (
	"crypto/tls"
	"fmt"
	"log"
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/go-ldap/ldap/v3"
)

// Client represents LDAP client for password validation
type Client struct {
	conn   *ldap.Conn
	config *models.LDAPConfig
}

// NewClient creates a new LDAP client
func NewClient(config *models.LDAPConfig) (*Client, error) {
	if config == nil {
		return nil, fmt.Errorf("LDAP config cannot be nil")
	}
	return &Client{config: config}, nil
}

// UserInfo holds LDAP user information
type UserInfo struct {
	Username string
	Email    string
	DN       string
}

// ValidatePassword validates username/password against LDAP directory
func (c *Client) ValidatePassword(username, password string) error {
	if username == "" || password == "" {
		return fmt.Errorf("username and password are required")
	}

	// Connect to LDAP server
	if err := c.connect(); err != nil {
		return fmt.Errorf("LDAP connection failed: %w", err)
	}
	defer c.close()

	// Bind with service account if provided
	if c.config.BindUser != "" {
		if err := c.conn.Bind(c.config.BindUser, c.config.BindPassword); err != nil {
			return fmt.Errorf("service account bind failed for user '%s': %w", c.config.BindUser, err)
		}
	}

	// Search for user DN (escape username to prevent LDAP injection)
	escapedUsername := ldap.EscapeFilter(username)
	userFilter := strings.ReplaceAll(c.config.UserFilter, "{username}", escapedUsername)
	searchRequest := ldap.NewSearchRequest(
		c.config.BaseDN,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		0, 0, false,
		userFilter,
		[]string{"dn"}, // Only need DN for authentication
		nil,
	)

	sr, err := c.conn.Search(searchRequest)
	if err != nil {
		return fmt.Errorf("user search failed: %w", err)
	}

	if len(sr.Entries) == 0 {
		return fmt.Errorf("user not found in LDAP directory")
	}

	// Try to bind with user credentials (this validates the password)
	userDN := sr.Entries[0].DN
	if err := c.conn.Bind(userDN, password); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	return nil // Authentication successful
}

// GetUserInfo retrieves user information from LDAP directory
func (c *Client) GetUserInfo(username string) (*UserInfo, error) {
	if username == "" {
		return nil, fmt.Errorf("username is required")
	}

	// Connect to LDAP server
	if err := c.connect(); err != nil {
		return nil, fmt.Errorf("LDAP connection failed: %w", err)
	}
	defer c.close()

	// Bind with service account if provided
	if c.config.BindUser != "" {
		if err := c.conn.Bind(c.config.BindUser, c.config.BindPassword); err != nil {
			return nil, fmt.Errorf("service account bind failed for user '%s': %w", c.config.BindUser, err)
		}
	}

	// Search for user with email attribute
	escapedUsername := ldap.EscapeFilter(username)
	userFilter := strings.ReplaceAll(c.config.UserFilter, "{username}", escapedUsername)
	searchRequest := ldap.NewSearchRequest(
		c.config.BaseDN,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		0, 0, false,
		userFilter,
		[]string{"dn", "mail", "email", "emailAddress"}, // Common email attributes
		nil,
	)

	sr, err := c.conn.Search(searchRequest)
	if err != nil {
		return nil, fmt.Errorf("user search failed: %w", err)
	}

	if len(sr.Entries) == 0 {
		return nil, fmt.Errorf("user not found in LDAP directory")
	}

	entry := sr.Entries[0]
	userInfo := &UserInfo{
		Username: username,
		DN:       entry.DN,
	}

	// Try to get email from different common attributes
	if email := entry.GetAttributeValue("mail"); email != "" {
		userInfo.Email = email
	} else if email := entry.GetAttributeValue("email"); email != "" {
		userInfo.Email = email
	} else if email := entry.GetAttributeValue("emailAddress"); email != "" {
		userInfo.Email = email
	} else {
		// If no email found, use fallback format
		userInfo.Email = fmt.Sprintf("%s@local.ldap", username)
	}

	return userInfo, nil
}

// connect establishes connection to LDAP server
func (c *Client) connect() error {
	var conn *ldap.Conn
	var err error

	if c.config.TLSEnabled {
		url := fmt.Sprintf("ldaps://%s:%d", c.config.Server, c.config.Port)
		// G402: TLSSkipVerify is user-configurable via database (settings.ldap_config.tls_skip_verify)
		// Administrators set this when configuring LDAP integration via API (/api/v3/setting/ldap)
		// Allows connection to corporate LDAP servers with self-signed or internal CA certificates
		// Security responsibility is delegated to the administrator managing LDAP configuration
		conn, err = ldap.DialURL(url, ldap.DialWithTLSConfig(&tls.Config{
			InsecureSkipVerify: c.config.TLSSkipVerify, // #nosec G402
		}))
	} else {
		url := fmt.Sprintf("ldap://%s:%d", c.config.Server, c.config.Port)
		conn, err = ldap.DialURL(url)
	}

	if err != nil {
		return err
	}

	c.conn = conn
	return nil
}

// close closes the LDAP connection
func (c *Client) close() {
	if c.conn != nil {
		if err := c.conn.Close(); err != nil {
			log.Printf("failed to close LDAP connection: %v", err)
		}
	}
}

// Close closes the LDAP connection (public method)
func (c *Client) Close() {
	c.close()
}

// TestConnection tests LDAP connection without authentication
func (c *Client) TestConnection() error {
	if err := c.connect(); err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer c.close()

	// Test bind with service account if configured
	if c.config.BindUser != "" {
		if err := c.conn.Bind(c.config.BindUser, c.config.BindPassword); err != nil {
			return fmt.Errorf("service account bind failed for user '%s': %w", c.config.BindUser, err)
		}
	}

	return nil
}
