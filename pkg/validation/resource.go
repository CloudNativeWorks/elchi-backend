package validation

import (
	"fmt"
	"regexp"
	"strings"

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

// GetValidationErrorMessage formats validation error for user-friendly response
func GetValidationErrorMessage(err error) string {
	if resourceErr, ok := err.(*ResourceNameError); ok {
		return resourceErr.Error()
	}

	return err.Error()
}
