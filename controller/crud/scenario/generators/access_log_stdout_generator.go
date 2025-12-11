package generators

import (
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// StdoutAccessLogGenerator generates stdout access log configuration
type StdoutAccessLogGenerator struct {
	*BaseGenerator
}

// NewStdoutAccessLogGenerator creates a new stdout access log generator
func NewStdoutAccessLogGenerator(project, version string, user models.UserDetails) *StdoutAccessLogGenerator {
	return &StdoutAccessLogGenerator{
		BaseGenerator: NewBaseGenerator(project, version, user),
	}
}

// Generate creates the stdout access log configuration
func (sg *StdoutAccessLogGenerator) Generate(instance models.ComponentInstance) (any, error) {
	// Get GType information
	gtype := models.GType(instance.GType)

	// Build general section using GType methods
	general := sg.BuildGeneralSection(
		instance,
		gtype.Type(),
		gtype.CollectionString(),
		gtype.CanonicalName(),
		gtype.String(),
		gtype.Category(),
	)

	// Build resource
	resource := make(map[string]any)

	// Handle log_format nested choice
	if logFormat := sg.GetFieldValueIfSelected(instance.SelectedFields, "log_format"); logFormat != nil {
		if nestedChoice, ok := logFormat.(map[string]any); ok {
			// Check which format was selected
			if textValue, exists := nestedChoice["text_format"]; exists && textValue != nil {
				// Text format selected - build text_format_source structure
				resource["log_format"] = map[string]any{
					"text_format_source": map[string]any{
						"inline_string": textValue,
					},
				}
			} else if jsonValue, exists := nestedChoice["json_format"]; exists && jsonValue != nil {
				// JSON format selected - build json_format structure
				resource["log_format"] = map[string]any{
					"json_format": jsonValue,
				}
			}
		}
	}

	// Return the complete document
	return map[string]any{
		"general": general,
		"resource": map[string]any{
			"version":  "3",
			"resource": resource,
		},
	}, nil
}

// GetComponentType returns the component type
func (sg *StdoutAccessLogGenerator) GetComponentType() string {
	return "access_log_stdout"
}

// GetCollection returns the MongoDB collection name
func (sg *StdoutAccessLogGenerator) GetCollection() string {
	return "extensions"
}
