package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// FieldType represents the type of a field
type FieldType string

const (
	FieldTypeString       FieldType = "string"
	FieldTypeInt          FieldType = "int"
	FieldTypeBool         FieldType = "bool"
	FieldTypeArray        FieldType = "array"
	FieldTypeObject       FieldType = "object"
	FieldTypeSelect       FieldType = "select"
	FieldTypeConditional  FieldType = "conditional"   // Field with sub-options
	FieldTypeNestedChoice FieldType = "nested_choice" // Choice between sub-configurations
)

// FieldOption represents an option for select type fields
type FieldOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// ConnectedField represents a field connected to another component
type ConnectedField struct {
	ComponentTypes []string `json:"component_types"` // Component types that can be connected
	FieldName      string   `json:"field_name"`      // e.g., "name" or ":componentname:"
}

// ConditionalChoice represents a choice in conditional field
type ConditionalChoice struct {
	Value       string           `json:"value"`                  // Choice value
	Label       string           `json:"label"`                  // Choice label
	SubFields   []AvailableField `json:"sub_fields"`             // Fields available when this choice is selected
	ApiEndpoint string           `json:"api_endpoint,omitempty"` // API endpoint for this choice
}

// NestedFieldConfig represents nested field configuration
type NestedFieldConfig struct {
	Choices           []ConditionalChoice `json:"choices"`            // Available choices
	DefaultChoice     string              `json:"default_choice"`     // Default selected choice
	MutuallyExclusive bool                `json:"mutually_exclusive"` // Only one choice can be selected
}

// AvailableField represents a field that can be selected for a component
type AvailableField struct {
	Name                 string             `json:"name"`                           // Field identifier
	Label                string             `json:"label"`                          // Human readable label
	Description          string             `json:"description"`                    // Field description
	Type                 FieldType          `json:"type"`                           // Field type
	RequiredForCreation  bool               `json:"required_for_creation"`          // Must be selected when creating scenario
	RequiredForExecution bool               `json:"required_for_execution"`         // Must have value when executing scenario (if selected)
	DefaultValue         interface{}        `json:"default_value"`                  // Default value (can be nil)
	UseComponentName     bool               `json:"use_component_name"`             // If true, field value syncs with component name automatically
	Options              []FieldOption      `json:"options,omitempty"`              // For select type fields
	Connected            *ConnectedField    `json:"connected,omitempty"`            // Connected to other scenario component
	ApiEndpoint          string             `json:"api_endpoint,omitempty"`         // API endpoint to fetch system resources
	ValidationRules      []string           `json:"validation_rules,omitempty"`     // Validation rules
	ArraySchema          *ArraySchema       `json:"array_schema,omitempty"`         // For array type fields
	NestedConfig         *NestedFieldConfig `json:"nested_config,omitempty"`        // For conditional/nested_choice fields
	HasMetadata          bool               `json:"has_metadata,omitempty"`         // If true, UI should preserve full object metadata from API
	UseApiAndScenario    bool               `json:"use_api_and_scenario,omitempty"` // If true, UI should allow selection from both API and scenario resources
}

// ArraySchema defines the structure of array items
type ArraySchema struct {
	ItemType   FieldType           `json:"item_type"`            // Type of array items (object, string, etc.)
	Properties map[string]FieldDef `json:"properties,omitempty"` // For object items
}

// FieldDef defines a field within array objects
type FieldDef struct {
	Type            FieldType           `json:"type"`
	Label           string              `json:"label"`
	Description     string              `json:"description,omitempty"`
	Required        bool                `json:"required"`
	Options         []FieldOption       `json:"options,omitempty"`
	DefaultValue    interface{}         `json:"default_value,omitempty"`
	Properties      map[string]FieldDef `json:"properties,omitempty"` // For nested objects
	Connected       *ConnectedField     `json:"connected,omitempty"`  // For connected fields
	ValidationRules []string            `json:"validation_rules,omitempty"` // Validation rules for the field
}

// ComponentRule represents validation rules for a component
type ComponentRule struct {
	RequiredWith                []string        `json:"required_with,omitempty"`                  // Other components that must exist
	ConflictWith                []string        `json:"conflicts_with,omitempty"`                 // Components that cannot coexist
	MinCount                    int             `json:"min_count,omitempty"`                      // Minimum instances required
	MaxCount                    int             `json:"max_count,omitempty"`                      // Maximum instances allowed
	FieldConflicts              []FieldConflict `json:"field_conflicts,omitempty"`                // Fields that cannot be used together
	FieldRequires               []FieldRequires `json:"field_requires,omitempty"`                 // Fields that require other fields
	ValidationRulesForCreation  []string        `json:"validation_rules_for_creation,omitempty"`  // Rules that only apply during scenario creation
	ValidationRulesForExecution []string        `json:"validation_rules_for_execution,omitempty"` // Rules that only apply during scenario execution
}

// FieldConflict defines fields that cannot be selected together
type FieldConflict struct {
	Fields  []string `json:"fields"`  // Field names that conflict
	Message string   `json:"message"` // Custom error message
}

// FieldRequires defines fields that require other fields
type FieldRequires struct {
	Field          string   `json:"field"`           // Field that has requirements
	RequiredFields []string `json:"required_fields"` // Fields that must be present
	Message        string   `json:"message"`         // Custom error message
}

// ComponentDefinition represents the definition of a component type
type ComponentDefinition struct {
	Name            string           `json:"name"`             // Component type identifier
	Label           string           `json:"label"`            // Human readable label
	Description     string           `json:"description"`      // Component description
	Category        string           `json:"category"`         // Envoy category (cluster, listener, etc.)
	Collection      string           `json:"collection"`       // MongoDB collection name
	CanonicalName   string           `json:"canonical_name"`   // Envoy canonical name
	GType           string           `json:"gtype"`            // Envoy protobuf type (e.g., envoy.config.cluster.v3.Cluster)
	Priority        int              `json:"priority"`         // Priority for UI ordering (lower = shown first)
	AvailableFields []AvailableField `json:"available_fields"` // Fields available for this component
	Rules           ComponentRule    `json:"rules,omitempty"`  // Validation rules
}

// NestedFieldSelection represents nested field selections
type NestedFieldSelection struct {
	SelectedChoice string          `json:"selected_choice"` // Which choice was selected
	SubFields      []SelectedField `json:"sub_fields"`      // Sub-field selections
}

// SelectedField represents a field selected by user with value
type SelectedField struct {
	FieldName       string                `json:"field_name"`
	Required        bool                  `json:"required"`                   // User-defined requirement for execution
	Value           interface{}           `json:"value,omitempty"`            // Can be null during scenario creation, must be filled for execution
	NestedSelection *NestedFieldSelection `json:"nested_selection,omitempty"` // For nested/conditional fields
}

// ComponentInstance represents a component instance in user's scenario
type ComponentInstance struct {
	Type           string          `json:"type"`            // Component type from ComponentDefinition
	Name           string          `json:"name"`            // User-defined instance name
	Priority       int             `json:"priority"`        // User-defined priority for ordering in wizard
	GType          string          `json:"gtype,omitempty"` // Component's GType for metadata lookup (optional)
	SelectedFields []SelectedField `json:"selected_fields"` // Fields selected by user
}

// Scenario represents a user-created scenario
type Scenario struct {
	ID          primitive.ObjectID  `json:"id" bson:"_id,omitempty"`
	Name        string              `json:"name" bson:"name"`
	Description string              `json:"description" bson:"description"`
	ScenarioID  string              `json:"scenario_id" bson:"scenario_id"`
	Components  []ComponentInstance `json:"components" bson:"components"`
	IsDefault   bool                `json:"is_default" bson:"is_default"`
	CreatedBy   string              `json:"created_by" bson:"created_by"`
	Project     *string             `json:"project,omitempty" bson:"project,omitempty"` // nil means all projects
	CreatedAt   time.Time           `json:"created_at" bson:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at" bson:"updated_at"`
}

// API Request/Response Models

// CreateScenarioRequest represents request to create a new scenario
type CreateScenarioRequest struct {
	Name        string              `json:"name" binding:"required"`
	Description string              `json:"description" binding:"required"`
	ScenarioID  string              `json:"scenario_id" binding:"required"`
	Components  []ComponentInstance `json:"components" binding:"required"`
	Project     string              `json:"project,omitempty"` // Project ID for the scenario
	AllProjects bool                `json:"all_projects"`      // true: available to all projects, false: only current project
}

// UpdateScenarioRequest represents request to update a scenario
type UpdateScenarioRequest struct {
	Name        string              `json:"name,omitempty"`
	Description string              `json:"description,omitempty"`
	Components  []ComponentInstance `json:"components,omitempty"`
	AllProjects *bool               `json:"all_projects,omitempty"` // Update project availability
}

// ExecuteScenarioRequest represents request to execute a scenario with filled values
type ExecuteScenarioRequest struct {
	ScenarioID string              `json:"scenario_id" binding:"required"`
	Components []ComponentInstance `json:"components" binding:"required"`
	Project    string              `json:"project" binding:"required"` // Project for all resources
	Version    string              `json:"version" binding:"required"` // Version for all resources
	Managed    bool                `json:"managed"`                    // Whether to save to database (applies to all resources)
}

// ScenarioListResponse represents response for scenarios
type ScenarioListResponse struct {
	Scenarios []Scenario `json:"scenarios"`
	Total     int64      `json:"total"`
}

// ComponentCatalogResponse represents the response containing available components
type ComponentCatalogResponse struct {
	Components []ComponentDefinition `json:"components"`
}

// ExecuteScenarioResponse represents the response from scenario execution
type ExecuteScenarioResponse struct {
	Resources []map[string]interface{} `json:"resources"`
	Success   bool                     `json:"success"`
	Message   string                   `json:"message,omitempty"`
}

// ExportScenarioRequest represents request to export scenarios
type ExportScenarioRequest struct {
	ScenarioIDs []string `json:"scenario_ids" binding:"required"` // List of scenario IDs to export
}

// ExportScenarioResponse represents response with exported scenario data
type ExportScenarioResponse struct {
	Scenarios   []Scenario `json:"scenarios"`
	ExportedBy  string     `json:"exported_by"`
	ExportedAt  time.Time  `json:"exported_at"`
	Version     string     `json:"version"`
	Count       int        `json:"count"`
}

// ImportScenarioRequest represents request to import scenarios
type ImportScenarioRequest struct {
	Scenarios      []Scenario `json:"scenarios" binding:"required"`
	Project        string     `json:"project,omitempty"`        // Target project for imported scenarios
	ConflictAction string     `json:"conflict_action,omitempty"` // "skip", "overwrite", "rename"
	Version        string     `json:"version,omitempty"`         // Version for compatibility check
}

// ScenarioConflict represents a scenario ID conflict during import
type ScenarioConflict struct {
	ScenarioID   string `json:"scenario_id"`
	ExistingName string `json:"existing_name"`
	ImportName   string `json:"import_name"`
	Action       string `json:"action"` // "skipped", "overwritten", "renamed"
	NewName      string `json:"new_name,omitempty"` // For renamed scenarios
}

// ImportScenarioResponse represents response from scenario import
type ImportScenarioResponse struct {
	Success    bool               `json:"success"`
	Message    string             `json:"message,omitempty"`
	Imported   int                `json:"imported"`   // Number of successfully imported scenarios
	Skipped    int                `json:"skipped"`    // Number of skipped scenarios
	Conflicts  []ScenarioConflict `json:"conflicts"`  // List of conflicts and resolutions
	ImportedBy string             `json:"imported_by"`
	ImportedAt time.Time          `json:"imported_at"`
}
