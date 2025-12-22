package acme

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-acme/lego/v4/acme"
	"github.com/go-acme/lego/v4/acme/api"
	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
)

// ACMEOrderClient wraps lego.Client and adds order-based operations
type ACMEOrderClient struct {
	client     *lego.Client
	core       *api.Core
	accountKey *ecdsa.PrivateKey
}

// NewACMEOrderClient creates a new order-capable ACME client
func NewACMEOrderClient(email, environment string, accountKey *ecdsa.PrivateKey) (*ACMEOrderClient, error) {
	// Determine ACME directory URL
	var caURL string
	if environment == "production" {
		caURL = lego.LEDirectoryProduction
	} else {
		caURL = lego.LEDirectoryStaging
	}

	// Create lego config
	config := lego.NewConfig(&ACMEUser{
		Email: email,
		key:   accountKey,
	})
	config.CADirURL = caURL
	config.Certificate.KeyType = certcrypto.EC256

	// Create lego client
	client, err := lego.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create lego client: %w", err)
	}

	// Register account if needed
	reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
	if err != nil {
		// Check if account already exists
		if !strings.Contains(err.Error(), "already exists") {
			return nil, fmt.Errorf("failed to register ACME account: %w", err)
		}
		// If account already exists, we need to get the registration URL
		// For existing accounts, we'll use QueryRegistration to get the reg resource
		reg, err = client.Registration.QueryRegistration()
		if err != nil {
			return nil, fmt.Errorf("failed to query existing ACME account: %w", err)
		}
	}

	// Create api.Core using same configuration
	core, err := api.New(config.HTTPClient, config.UserAgent, config.CADirURL, reg.URI, accountKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create ACME core API: %w", err)
	}

	return &ACMEOrderClient{
		client:     client,
		core:       core,
		accountKey: accountKey,
	}, nil
}

// CreateOrderAndCaptureChallenge creates an ACME order and returns order URL + DNS challenges
func (c *ACMEOrderClient) CreateOrderAndCaptureChallenge(domains []string) (string, []DNSChallenge, error) {
	// Create ACME order
	order, err := c.core.Orders.New(domains)
	if err != nil {
		return "", nil, fmt.Errorf("failed to create ACME order: %w", err)
	}

	// Extract order URL from Location header
	orderURL := order.Location
	if orderURL == "" {
		return "", nil, fmt.Errorf("ACME order created but no Location URL returned")
	}

	// Process authorizations to get DNS challenges
	var challenges []DNSChallenge
	now := time.Now()

	for _, authzURL := range order.Authorizations {
		// Get authorization details
		authz, err := c.core.Authorizations.Get(authzURL)
		if err != nil {
			return "", nil, fmt.Errorf("failed to get authorization for %s: %w", authzURL, err)
		}

		// Find DNS-01 challenge
		var dnsChallenge *acme.Challenge
		for _, chal := range authz.Challenges {
			if chal.Type == "dns-01" {
				dnsChallenge = &chal
				break
			}
		}

		if dnsChallenge == nil {
			return "", nil, fmt.Errorf("no DNS-01 challenge found for domain %s", authz.Identifier.Value)
		}

		// Compute key authorization if not provided by ACME server
		// Key authorization = token + "." + JWK thumbprint
		keyAuth := dnsChallenge.KeyAuthorization
		if keyAuth == "" {
			keyAuth, err = c.computeKeyAuthorization(dnsChallenge.Token, c.accountKey)
			if err != nil {
				return "", nil, fmt.Errorf("failed to compute key authorization: %w", err)
			}
		}

		// Generate DNS record FQDN and value using lego's dns01 package
		challengeInfo := dns01.GetChallengeInfo(authz.Identifier.Value, keyAuth)
		fqdn := challengeInfo.FQDN
		value := challengeInfo.Value

		challenges = append(challenges, DNSChallenge{
			Domain:    authz.Identifier.Value,
			FQDN:      fqdn,
			Type:      "TXT",
			Value:     value,
			Token:     dnsChallenge.Token,
			KeyAuth:   dnsChallenge.KeyAuthorization,
			CreatedAt: now,
			ExpiresAt: now.Add(24 * time.Hour),
		})
	}

	return orderURL, challenges, nil
}

// VerifyExistingOrder verifies DNS challenges for an existing ACME order and retrieves certificate
func (c *ACMEOrderClient) VerifyExistingOrder(orderURL string, domains []string, privateKey crypto.PrivateKey) ([]byte, error) {
	// Get current order status
	order, err := c.core.Orders.Get(orderURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get order status: %w", err)
	}

	// Process each authorization
	for _, authzURL := range order.Authorizations {
		authz, err := c.core.Authorizations.Get(authzURL)
		if err != nil {
			return nil, fmt.Errorf("failed to get authorization: %w", err)
		}

		// Skip if already valid
		if authz.Status == "valid" {
			continue
		}

		// Find DNS-01 challenge
		var dnsChallenge *acme.Challenge
		for _, chal := range authz.Challenges {
			if chal.Type == "dns-01" {
				dnsChallenge = &chal
				break
			}
		}

		if dnsChallenge == nil {
			return nil, fmt.Errorf("no DNS-01 challenge found for domain %s", authz.Identifier.Value)
		}

		// Trigger challenge validation
		_, err = c.core.Challenges.New(dnsChallenge.URL)
		if err != nil {
			return nil, fmt.Errorf("failed to accept challenge for %s: %w", authz.Identifier.Value, err)
		}

		// Poll for challenge completion (max 60 seconds)
		for i := 0; i < 12; i++ {
			time.Sleep(5 * time.Second)

			updatedAuthz, err := c.core.Authorizations.Get(authzURL)
			if err != nil {
				return nil, fmt.Errorf("failed to poll authorization: %w", err)
			}

			if updatedAuthz.Status == "valid" {
				break
			}

			if updatedAuthz.Status == "invalid" {
				// Find the challenge error
				for _, chal := range updatedAuthz.Challenges {
					if chal.Type == "dns-01" && chal.Error != nil {
						return nil, fmt.Errorf("challenge failed for %s: %s", authz.Identifier.Value, chal.Error.Detail)
					}
				}
				return nil, fmt.Errorf("challenge failed for %s: status invalid", authz.Identifier.Value)
			}
		}

		// Final check
		finalAuthz, err := c.core.Authorizations.Get(authzURL)
		if err != nil {
			return nil, fmt.Errorf("failed to get final authorization status: %w", err)
		}

		if finalAuthz.Status != "valid" {
			return nil, fmt.Errorf("authorization not valid after 60 seconds for domain %s", authz.Identifier.Value)
		}
	}

	// Generate CSR (mustStaple=false since OCSP must-staple is deprecated by Let's Encrypt)
	csr, err := certcrypto.CreateCSR(privateKey, certcrypto.CSROptions{
		Domain:     domains[0],
		SAN:        domains[1:],
		MustStaple: false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to generate CSR: %w", err)
	}

	// Finalize order
	finalizedOrder, err := c.core.Orders.UpdateForCSR(order.Finalize, csr)
	if err != nil {
		return nil, fmt.Errorf("failed to finalize order: %w", err)
	}

	// Poll for certificate (max 30 seconds)
	var updatedOrder acme.ExtendedOrder
	for i := 0; i < 6; i++ {
		time.Sleep(5 * time.Second)

		updatedOrder, err = c.core.Orders.Get(orderURL)
		if err != nil {
			return nil, fmt.Errorf("failed to get order status: %w", err)
		}

		if updatedOrder.Status == "valid" {
			break
		}

		if updatedOrder.Status == "invalid" {
			return nil, fmt.Errorf("order became invalid")
		}
	}

	if updatedOrder.Status != "valid" {
		return nil, fmt.Errorf("order not ready after 30 seconds")
	}

	// Use finalized order certificate URL if available
	certURL := updatedOrder.Certificate
	if certURL == "" && finalizedOrder.Certificate != "" {
		certURL = finalizedOrder.Certificate
	}

	if certURL == "" {
		return nil, fmt.Errorf("no certificate URL in order")
	}

	// Download certificate with bundle=true (includes full chain up to root)
	// certBytes contains: end-entity cert + all intermediate certs
	// issuerCert is redundant when bundle=true
	certBytes, _, err := c.core.Certificates.Get(certURL, true)
	if err != nil {
		return nil, fmt.Errorf("failed to download certificate: %w", err)
	}

	// Return full certificate chain (end-entity + intermediates)
	// Do NOT append issuerCert again - it's already in certBytes when bundle=true
	return certBytes, nil
}

// computeKeyAuthorization computes ACME key authorization for a token
func (c *ACMEOrderClient) computeKeyAuthorization(token string, accountKey *ecdsa.PrivateKey) (string, error) {
	// Compute JWK thumbprint according to RFC 7638
	thumbprint, err := computeJWKThumbprint(accountKey)
	if err != nil {
		return "", err
	}

	// Key authorization = token + "." + thumbprint
	return token + "." + thumbprint, nil
}

// computeJWKThumbprint computes JWK thumbprint per RFC 7638
func computeJWKThumbprint(key *ecdsa.PrivateKey) (string, error) {
	// For P-256, coordinates must be exactly 32 bytes
	xBytes := key.PublicKey.X.Bytes()
	yBytes := key.PublicKey.Y.Bytes()

	// Pad to 32 bytes if needed
	x := make([]byte, 32)
	y := make([]byte, 32)
	copy(x[32-len(xBytes):], xBytes)
	copy(y[32-len(yBytes):], yBytes)

	// Create JWK representation with lexicographically ordered fields
	// For ECDSA P-256, we need: crv, kty, x, y (alphabetically ordered)
	jwk := struct {
		Crv string `json:"crv"`
		Kty string `json:"kty"`
		X   string `json:"x"`
		Y   string `json:"y"`
	}{
		Crv: "P-256",
		Kty: "EC",
		X:   base64.RawURLEncoding.EncodeToString(x),
		Y:   base64.RawURLEncoding.EncodeToString(y),
	}

	// Serialize to JSON (fields are already in lexicographic order)
	jwkJSON, err := json.Marshal(jwk)
	if err != nil {
		return "", fmt.Errorf("failed to marshal JWK: %w", err)
	}

	// Compute SHA-256 hash
	hash := sha256.Sum256(jwkJSON)

	// Base64url encode (without padding)
	return base64.RawURLEncoding.EncodeToString(hash[:]), nil
}
