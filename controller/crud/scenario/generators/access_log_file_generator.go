package generators

import (
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// FileAccessLogGenerator generates file access log configuration
type FileAccessLogGenerator struct {
	*BaseGenerator
}

// NewFileAccessLogGenerator creates a new file access log generator
func NewFileAccessLogGenerator(project, version string, user models.UserDetails) *FileAccessLogGenerator {
	return &FileAccessLogGenerator{
		BaseGenerator: NewBaseGenerator(project, version, user),
	}
}

// Generate creates the file access log configuration
func (fg *FileAccessLogGenerator) Generate(instance models.ComponentInstance) (any, error) {
	// Get GType information
	gtype := models.GType(instance.GType)

	// Build general section using GType methods
	general := fg.BuildGeneralSection(
		instance,
		gtype.Type(),
		gtype.CollectionString(),
		gtype.CanonicalName(),
		gtype.String(),
		gtype.Category(),
	)

	// Build resource
	resource := make(map[string]any)

	// Add path (required)
	if path := fg.GetFieldValueIfSelected(instance.SelectedFields, "path"); path != nil {
		resource["path"] = path
	} else {
		resource["path"] = "/dev/stdout" // default
	}

	// Handle log_format nested choice
	if logFormat := fg.GetFieldValueIfSelected(instance.SelectedFields, "log_format"); logFormat != nil {
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
func (fg *FileAccessLogGenerator) GetComponentType() string {
	return "access_log_file"
}

// GetCollection returns the MongoDB collection name
func (fg *FileAccessLogGenerator) GetCollection() string {
	return "extensions"
}