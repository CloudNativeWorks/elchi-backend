package validation

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

var (
	// ValidResourceNameRegex allows letters, numbers, underscore, hyphen only
	// No spaces, no special characters except _ and -
	ValidResourceNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
)

// ResourceNameError represents a resource name validation error
type ResourceNameError struct {
	Field string
	Value string
	Rule  string
}

func (e *ResourceNameError) Error() string {
	return fmt.Sprintf("invalid %s '%s': %s", e.Field, e.Value, e.Rule)
}

// ValidateResourceName validates resource name according to XDS/Extension naming rules
func ValidateResourceName(name string) error {
	if name == "" {
		return &ResourceNameError{
			Field: "name",
			Value: name,
			Rule:  "name cannot be empty",
		}
	}

	// Check for spaces
	if strings.Contains(name, " ") {
		return &ResourceNameError{
			Field: "name",
			Value: name,
			Rule:  "spaces are not allowed",
		}
	}

	// Check allowed characters using regex
	if !ValidResourceNameRegex.MatchString(name) {
		return &ResourceNameError{
			Field: "name",
			Value: name,
			Rule:  "only letters, numbers, underscore (_) and hyphen (-) are allowed",
		}
	}

	// Additional length check (reasonable limit)
	if len(name) > 100 {
		return &ResourceNameError{
			Field: "name",
			Value: name,
			Rule:  "name cannot exceed 100 characters",
		}
	}

	return nil
}

// ValidateGeneral validates General struct fields
// Currently validates name field, can be extended for other fields in the future
func ValidateGeneral(general models.General) error {
	// Validate name field
	if err := ValidateResourceName(general.Name); err != nil {
		return err
	}

	// TODO: Add other general field validations here as needed
	// - version format validation
	// - canonical_name validation
	// - category validation
	// - metadata validation

	return nil
}

// ValidateXDSResource validates XDS resource using General validation
func ValidateXDSResource(resource models.ResourceClass) error {
	general := resource.GetGeneral()

	if err := ValidateGeneral(general); err != nil {
		return fmt.Errorf("xds resource validation failed: %w", err)
	}

	return nil
}

// ValidateExtensionResource validates Extension resource using General validation
func ValidateExtensionResource(resource models.ResourceClass) error {
	general := resource.GetGeneral()

	if err := ValidateGeneral(general); err != nil {
		return fmt.Errorf("extension resource validation failed: %w", err)
	}

	return nil
}

// ValidationFunction represents a validation function for a specific GType
type ValidationFunction func(resource models.ResourceClass, requestDetails models.RequestDetails, logger *logger.Logger) (any, error)

// GTypeValidatorRegistry holds validation functions for each GType
type GTypeValidatorRegistry struct {
	validators map[models.GType]ValidationFunction
}

// NewGTypeValidatorRegistry creates a new validator registry
func NewGTypeValidatorRegistry() *GTypeValidatorRegistry {
	registry := &GTypeValidatorRegistry{
		validators: make(map[models.GType]ValidationFunction),
	}

	// Register TLS-related validators
	registry.RegisterTLSValidators()

	return registry
}

// RegisterTLSValidators registers all TLS certificate related validators
func (r *GTypeValidatorRegistry) RegisterTLSValidators() {
	tlsGTypes := []models.GType{
		models.TLSCertificate,
	}

	// Register the same certificate validation function for all TLS-related GTypes
	for _, gtype := range tlsGTypes {
		r.validators[gtype] = r.validateTLSCertificates
	}
}

// validateTLSCertificates validates TLS certificates for any TLS-related GType
func (r *GTypeValidatorRegistry) validateTLSCertificates(resource models.ResourceClass, requestDetails models.RequestDetails, logger *logger.Logger) (any, error) {
	// Import the certificate validation from certificate.go
	result := ValidateTLSCertificatesInResource(resource, requestDetails, logger)

	if !result.Valid {
		return result, fmt.Errorf("certificate validation failed: %d errors found", len(result.Errors))
	}

	return nil, nil
}

// ValidateByGType automatically validates a resource based on its GType
func (r *GTypeValidatorRegistry) ValidateByGType(resource models.ResourceClass, requestDetails models.RequestDetails, logger *logger.Logger) (any, error) {
	// Only validate if requested
	if requestDetails.Validate != "true" {
		return nil, nil
	}

	general := resource.GetGeneral()
	gtype := general.GType

	logger.Debugf("Auto-validating resource %s with GType: %s", general.Name, gtype)

	// Check if we have a validator for this GType
	if validator, exists := r.validators[gtype]; exists {
		logger.Debugf("Found validator for GType: %s", gtype)
		return validator(resource, requestDetails, logger)
	}

	// No specific validator found, skip validation
	logger.Debugf("No validator found for GType: %s, skipping validation", gtype)
	return nil, nil
}

// Global registry instance
var GlobalValidatorRegistry = NewGTypeValidatorRegistry()
