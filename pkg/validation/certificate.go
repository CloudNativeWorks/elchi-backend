package validation

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// CertificateValidationError represents a certificate validation error
type CertificateValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
	Type    string `json:"type"`
}

// CertificateValidationResult contains validation results for certificates
type CertificateValidationResult struct {
	Valid  bool                         `json:"valid"`
	Errors []CertificateValidationError `json:"errors,omitempty"`
}

// ValidateTLSCertificatesInResource validates TLS certificates in a resource
func ValidateTLSCertificatesInResource(resource models.ResourceClass, requestDetails models.RequestDetails, logger *logger.Logger) *CertificateValidationResult {
	result := &CertificateValidationResult{Valid: true, Errors: []CertificateValidationError{}}

	logger.Debugf("Validating TLS certificates in resource: %s, collection: %s, gtype: %s", requestDetails.Name, requestDetails.Collection, requestDetails.GType)

	// Convert resource to map for easier processing
	resourceData, err := convertResourceToMap(resource)
	if err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, CertificateValidationError{
			Field:   "resource",
			Message: fmt.Sprintf("Failed to process resource data: %v", err),
			Type:    "PROCESSING_ERROR",
		})
		return result
	}

	// Search for TLS certificates in the resource
	findTLSCertificates(resourceData, "", result, logger)

	if !result.Valid {
		logger.Warnf("TLS certificate validation failed for resource %s: %d errors found", requestDetails.Name, len(result.Errors))
	} else {
		logger.Debugf("TLS certificate validation passed for resource %s", requestDetails.Name)
	}

	return result
}

// convertResourceToMap converts any resource to map[string]any for processing
func convertResourceToMap(resource models.ResourceClass) (map[string]any, error) {
	jsonData, err := json.Marshal(resource)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal resource: %v", err)
	}

	var resourceMap map[string]any
	if err := json.Unmarshal(jsonData, &resourceMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal resource: %v", err)
	}

	return resourceMap, nil
}

// findTLSCertificates recursively searches for TLS certificates in the data
func findTLSCertificates(data any, path string, result *CertificateValidationResult, logger *logger.Logger) {
	switch v := data.(type) {
	case map[string]any:
		// Check if this is a TlsCertificate object
		if isTLSCertificate(v) {
			validateTLSCertificate(v, path, result, logger)
		} else {
			// Recursively search in nested objects
			for key, value := range v {
				newPath := path
				if newPath != "" {
					newPath += "."
				}
				newPath += key
				findTLSCertificates(value, newPath, result, logger)
			}
		}
	case []any:
		// Search in arrays
		for i, item := range v {
			newPath := fmt.Sprintf("%s[%d]", path, i)
			findTLSCertificates(item, newPath, result, logger)
		}
	}
}

// isTLSCertificate checks if the object represents an Envoy TlsCertificate with inline data
func isTLSCertificate(obj map[string]any) bool {
	// Check for certificate_chain with inline_string or inline_bytes
	if certChain, exists := obj["certificate_chain"]; exists {
		if certChainMap, ok := certChain.(map[string]any); ok {
			if _, hasInlineString := certChainMap["inline_string"]; hasInlineString {
				return true
			}
			if _, hasInlineBytes := certChainMap["inline_bytes"]; hasInlineBytes {
				return true
			}
		}
	}

	// Check for direct inline certificate fields
	if inlineString, exists := obj["inline_string"]; exists {
		if inlineStr, ok := inlineString.(string); ok {
			if strings.Contains(inlineStr, "BEGIN CERTIFICATE") {
				return true
			}
		}
	}

	if inlineBytes, exists := obj["inline_bytes"]; exists {
		if bytesStr, ok := inlineBytes.(string); ok {
			if decoded, err := base64.StdEncoding.DecodeString(bytesStr); err == nil {
				if strings.Contains(string(decoded), "BEGIN CERTIFICATE") {
					return true
				}
			}
		}
	}

	return false
}

// validateTLSCertificate validates a single TLS certificate
func validateTLSCertificate(tlsCert map[string]any, path string, result *CertificateValidationResult, logger *logger.Logger) {
	logger.Debugf("Validating TLS certificate at path: %s", path)

	// Validate certificate chain
	if certChain, exists := tlsCert["certificate_chain"]; exists {
		if certChainMap, ok := certChain.(map[string]any); ok {
			validateCertificateChain(certChainMap, path+".certificate_chain", result, logger)
		}
	}

	// Check for direct inline certificate fields
	if inlineString, exists := tlsCert["inline_string"]; exists {
		if certStr, ok := inlineString.(string); ok && strings.Contains(certStr, "BEGIN CERTIFICATE") {
			parseCertificateChain(certStr, path+".inline_string", result, logger)
		}
	}

	if inlineBytes, exists := tlsCert["inline_bytes"]; exists {
		if bytesStr, ok := inlineBytes.(string); ok {
			if decoded, err := base64.StdEncoding.DecodeString(bytesStr); err == nil {
				if strings.Contains(string(decoded), "BEGIN CERTIFICATE") {
					parseCertificateChain(string(decoded), path+".inline_bytes", result, logger)
				}
			}
		}
	}

	// Validate private key if present
	if privateKey, exists := tlsCert["private_key"]; exists {
		if privateKeyMap, ok := privateKey.(map[string]any); ok {
			validatePrivateKey(privateKeyMap, path+".private_key", result, logger)
		}
	}
}

// validateCertificateChain validates the certificate chain
func validateCertificateChain(certChain map[string]any, path string, result *CertificateValidationResult, logger *logger.Logger) {
	var certData string
	var found bool

	// Check for inline_string
	if inlineString, exists := certChain["inline_string"]; exists {
		if certStr, ok := inlineString.(string); ok {
			certData = certStr
			found = true
		}
	}

	// Check for inline_bytes (base64 encoded)
	if !found {
		if inlineBytes, exists := certChain["inline_bytes"]; exists {
			if bytesStr, ok := inlineBytes.(string); ok {
				decoded, err := base64.StdEncoding.DecodeString(bytesStr)
				if err != nil {
					result.Valid = false
					result.Errors = append(result.Errors, CertificateValidationError{
						Field:   path + ".inline_bytes",
						Message: fmt.Sprintf("Invalid base64 encoding: %v", err),
						Type:    "ENCODING_ERROR",
					})
					return
				}
				certData = string(decoded)
				found = true
			}
		}
	}

	if !found {
		// No inline certificate data found, skip validation
		logger.Debugf("No inline certificate data found at %s, skipping validation", path)
		return
	}

	// Parse and validate the certificate
	parseCertificateChain(certData, path, result, logger)
}

// parseCertificateChain parses and validates certificate chain
func parseCertificateChain(certData, path string, result *CertificateValidationResult, logger *logger.Logger) {
	// Parse all PEM blocks first
	rest := []byte(certData)
	var certificates []*x509.Certificate
	certCount := 0

	for {
		block, remaining := pem.Decode(rest)
		if block == nil {
			break
		}

		if block.Type != "CERTIFICATE" {
			rest = remaining
			continue
		}

		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, CertificateValidationError{
				Field:   fmt.Sprintf("%s.certificate[%d]", path, certCount),
				Message: fmt.Sprintf("Invalid certificate: %v", err),
				Type:    "CERTIFICATE_PARSE_ERROR",
			})
			rest = remaining
			certCount++
			continue
		}

		certificates = append(certificates, cert)
		rest = remaining
		certCount++
	}

	if len(certificates) == 0 {
		result.Valid = false
		result.Errors = append(result.Errors, CertificateValidationError{
			Field:   path,
			Message: "No valid certificates found in certificate chain",
			Type:    "NO_CERTIFICATES_FOUND",
		})
		return
	}

	// Validate each certificate individually
	now := time.Now()
	for i, cert := range certificates {
		// Check if certificate is expired
		if cert.NotAfter.Before(now) {
			result.Valid = false
			result.Errors = append(result.Errors, CertificateValidationError{
				Field:   fmt.Sprintf("%s.certificate[%d]", path, i),
				Message: fmt.Sprintf("Certificate expired on %s (Subject: %s)", cert.NotAfter.Format("2006-01-02 15:04:05"), cert.Subject.CommonName),
				Type:    "CERTIFICATE_EXPIRED",
			})
		}

		// Check if certificate is not yet valid
		if cert.NotBefore.After(now) {
			result.Valid = false
			result.Errors = append(result.Errors, CertificateValidationError{
				Field:   fmt.Sprintf("%s.certificate[%d]", path, i),
				Message: fmt.Sprintf("Certificate not valid until %s (Subject: %s)", cert.NotBefore.Format("2006-01-02 15:04:05"), cert.Subject.CommonName),
				Type:    "CERTIFICATE_NOT_YET_VALID",
			})
		}

		// Warn if certificate expires soon (within 30 days)
		if cert.NotAfter.Before(now.AddDate(0, 0, 30)) && cert.NotAfter.After(now) {
			logger.Warnf("Certificate at %s.certificate[%d] expires soon on %s (Subject: %s)", path, i, cert.NotAfter.Format("2006-01-02 15:04:05"), cert.Subject.CommonName)
		}

		logger.Debugf("Certificate at %s.certificate[%d] is valid (Subject: %s, Expires: %s)", path, i, cert.Subject.CommonName, cert.NotAfter.Format("2006-01-02 15:04:05"))
	}

	// Validate certificate chain if more than one certificate
	if len(certificates) > 1 {
		validateCertificateChainOrder(certificates, path, result, logger)
	}
}

// validateCertificateChainOrder validates the certificate chain order and trust relationships
func validateCertificateChainOrder(certificates []*x509.Certificate, path string, result *CertificateValidationResult, logger *logger.Logger) {
	logger.Debugf("Validating certificate chain order for %d certificates at %s", len(certificates), path)

	// First certificate should be the end-entity (leaf) certificate
	leafCert := certificates[0]

	// Check if the first certificate is actually a leaf certificate (not a CA)
	if leafCert.IsCA {
		logger.Warnf("First certificate in chain appears to be a CA certificate, expected leaf certificate (Subject: %s)", leafCert.Subject.CommonName)
	}

	// Validate chain: each certificate should be signed by the next one
	for i := 0; i < len(certificates)-1; i++ {
		cert := certificates[i]
		issuer := certificates[i+1]

		// Check if the issuer's subject matches the certificate's issuer
		if !namesEqual(cert.Issuer, issuer.Subject) {
			result.Valid = false
			result.Errors = append(result.Errors, CertificateValidationError{
				Field:   fmt.Sprintf("%s.certificate[%d]", path, i),
				Message: fmt.Sprintf("Certificate chain broken: certificate issued by '%s' but next certificate is '%s'", cert.Issuer.CommonName, issuer.Subject.CommonName),
				Type:    "CERTIFICATE_CHAIN_BROKEN",
			})
			continue
		}

		// Verify the signature (basic check)
		err := cert.CheckSignatureFrom(issuer)
		if err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, CertificateValidationError{
				Field:   fmt.Sprintf("%s.certificate[%d]", path, i),
				Message: fmt.Sprintf("Certificate signature verification failed: %v (Subject: %s, Issuer: %s)", err, cert.Subject.CommonName, issuer.Subject.CommonName),
				Type:    "CERTIFICATE_SIGNATURE_INVALID",
			})
		} else {
			logger.Debugf("Certificate chain link verified: %s -> %s", cert.Subject.CommonName, issuer.Subject.CommonName)
		}
	}

	// Check if the last certificate is self-signed (root CA)
	lastCert := certificates[len(certificates)-1]
	if namesEqual(lastCert.Subject, lastCert.Issuer) {
		// This is a self-signed root CA
		err := lastCert.CheckSignatureFrom(lastCert)
		if err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, CertificateValidationError{
				Field:   fmt.Sprintf("%s.certificate[%d]", path, len(certificates)-1),
				Message: fmt.Sprintf("Root CA self-signature verification failed: %v (Subject: %s)", err, lastCert.Subject.CommonName),
				Type:    "ROOT_CA_SIGNATURE_INVALID",
			})
		} else {
			logger.Debugf("Root CA self-signature verified: %s", lastCert.Subject.CommonName)
		}
	} else {
		// The last certificate is not self-signed, might be an intermediate
		logger.Warnf("Last certificate in chain is not self-signed, chain might be incomplete (Subject: %s, Issuer: %s)", lastCert.Subject.CommonName, lastCert.Issuer.CommonName)
	}
}

// validatePrivateKey validates the private key
func validatePrivateKey(privateKey map[string]any, path string, result *CertificateValidationResult, logger *logger.Logger) {
	var keyData string
	var found bool

	// Check for inline_string
	if inlineString, exists := privateKey["inline_string"]; exists {
		if keyStr, ok := inlineString.(string); ok {
			keyData = keyStr
			found = true
		}
	}

	// Check for inline_bytes (base64 encoded)
	if !found {
		if inlineBytes, exists := privateKey["inline_bytes"]; exists {
			if bytesStr, ok := inlineBytes.(string); ok {
				decoded, err := base64.StdEncoding.DecodeString(bytesStr)
				if err != nil {
					result.Valid = false
					result.Errors = append(result.Errors, CertificateValidationError{
						Field:   path + ".inline_bytes",
						Message: fmt.Sprintf("Invalid base64 encoding for private key: %v", err),
						Type:    "ENCODING_ERROR",
					})
					return
				}
				keyData = string(decoded)
				found = true
			}
		}
	}

	if !found {
		// No inline private key data found, skip validation
		logger.Debugf("No inline private key data found at %s, skipping validation", path)
		return
	}

	// Parse and validate the private key
	parsePrivateKey(keyData, path, result, logger)
}

// parsePrivateKey parses and validates private key
func parsePrivateKey(keyData, path string, result *CertificateValidationResult, logger *logger.Logger) {
	// Parse PEM block
	block, _ := pem.Decode([]byte(keyData))
	if block == nil {
		result.Valid = false
		result.Errors = append(result.Errors, CertificateValidationError{
			Field:   path,
			Message: "Invalid PEM format for private key",
			Type:    "PRIVATE_KEY_PARSE_ERROR",
		})
		return
	}

	// Check if it's a known private key type
	validKeyTypes := []string{
		"PRIVATE KEY",
		"RSA PRIVATE KEY",
		"EC PRIVATE KEY",
		"DSA PRIVATE KEY",
		"ECDSA PRIVATE KEY",
	}

	isValidType := false
	for _, validType := range validKeyTypes {
		if strings.Contains(block.Type, validType) {
			isValidType = true
			break
		}
	}

	if !isValidType {
		result.Valid = false
		result.Errors = append(result.Errors, CertificateValidationError{
			Field:   path,
			Message: fmt.Sprintf("Unsupported private key type: %s", block.Type),
			Type:    "PRIVATE_KEY_TYPE_ERROR",
		})
		return
	}

	// Try to parse the private key
	_, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		// Try PKCS1 for RSA keys
		_, err = x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			// Try EC private key
			_, err = x509.ParseECPrivateKey(block.Bytes)
			if err != nil {
				result.Valid = false
				result.Errors = append(result.Errors, CertificateValidationError{
					Field:   path,
					Message: fmt.Sprintf("Invalid private key format: %v", err),
					Type:    "PRIVATE_KEY_PARSE_ERROR",
				})
				return
			}
		}
	}

	logger.Debugf("Private key at %s is valid", path)
}

// namesEqual compares two pkix.Name structures for equality
func namesEqual(name1, name2 pkix.Name) bool {
	return name1.CommonName == name2.CommonName &&
		strings.Join(name1.Country, ",") == strings.Join(name2.Country, ",") &&
		strings.Join(name1.Organization, ",") == strings.Join(name2.Organization, ",") &&
		strings.Join(name1.OrganizationalUnit, ",") == strings.Join(name2.OrganizationalUnit, ",") &&
		strings.Join(name1.Locality, ",") == strings.Join(name2.Locality, ",") &&
		strings.Join(name1.Province, ",") == strings.Join(name2.Province, ",")
}
