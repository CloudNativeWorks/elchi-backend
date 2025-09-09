package generators

import (
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// FluentdAccessLogGenerator generates fluentd access log configuration
type FluentdAccessLogGenerator struct {
	*BaseGenerator
	metadataProcessor *MetadataProcessor
}

// NewFluentdAccessLogGenerator creates a new fluentd access log generator
func NewFluentdAccessLogGenerator(project, version string, user models.UserDetails) *FluentdAccessLogGenerator {
	return &FluentdAccessLogGenerator{
		BaseGenerator:     NewBaseGenerator(project, version, user),
		metadataProcessor: NewMetadataProcessor(),
	}
}

// Generate creates the fluentd access log configuration
func (fg *FluentdAccessLogGenerator) Generate(instance models.ComponentInstance) (any, error) {
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

	// Add cluster (required) - this is a reference to a cluster, not metadata
	if cluster := fg.GetFieldValueIfSelected(instance.SelectedFields, "cluster"); cluster != nil {
		// Check if it's an object with metadata (HasMetadata field)
		if clusterObj, ok := cluster.(map[string]any); ok {
			// If it has a "name" field, use that (it's from the API with metadata)
			if name, exists := clusterObj["name"]; exists {
				resource["cluster"] = name
			}
		} else {
			// Otherwise, use the value directly (simple string)
			resource["cluster"] = cluster
		}
	}

	// Add tag (required)
	if tag := fg.GetFieldValueIfSelected(instance.SelectedFields, "tag"); tag != nil {
		resource["tag"] = tag
	}

	// Add stat_prefix (optional)
	if statPrefix := fg.GetFieldValueIfSelected(instance.SelectedFields, "stat_prefix"); statPrefix != nil {
		resource["stat_prefix"] = statPrefix
	}

	// Add buffer_flush_interval (optional)
	if interval := fg.GetFieldValueIfSelected(instance.SelectedFields, "buffer_flush_interval"); interval != nil {
		resource["buffer_flush_interval"] = interval
	}

	// Add buffer_size_bytes (optional)
	if size := fg.GetFieldValueIfSelected(instance.SelectedFields, "buffer_size_bytes"); size != nil {
		resource["buffer_size_bytes"] = size
	}

	// Add record (optional) - JSON object with log fields
	if record := fg.GetFieldValueIfSelected(instance.SelectedFields, "record"); record != nil {
		resource["record"] = record
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
func (fg *FluentdAccessLogGenerator) GetComponentType() string {
	return "access_log_fluentd"
}

// GetCollection returns the MongoDB collection name
func (fg *FluentdAccessLogGenerator) GetCollection() string {
	return "extensions"
}