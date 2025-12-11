package waf

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// WAFWasmInjector handles WAF configuration injection into WASM extensions
type WAFWasmInjector struct {
	db     *mongo.Database
	logger *logger.Logger
}

// NewWAFWasmInjector creates a new WAF WASM injector
func NewWAFWasmInjector(db *mongo.Database, logger *logger.Logger) *WAFWasmInjector {
	return &WAFWasmInjector{
		db:     db,
		logger: logger,
	}
}

// InjectWAFConfig loads WAF config from MongoDB and injects it into WASM resource configuration
// Returns modified resource or error
func (w *WAFWasmInjector) InjectWAFConfig(ctx context.Context, resource models.ResourceClass, wafConfigName string) (models.ResourceClass, error) {
	// Load WAF config from MongoDB
	wafConfig, err := w.loadWAFConfig(ctx, wafConfigName)
	if err != nil {
		return nil, fmt.Errorf("failed to load WAF config '%s': %w", wafConfigName, err)
	}

	// Build Coraza configuration JSON string (entire Data structure as JSON string)
	corazaConfig, err := w.buildCorazaConfigJSON(wafConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build Coraza config: %w", err)
	}

	// Base64 encode configuration
	encodedConfig := base64.StdEncoding.EncodeToString([]byte(corazaConfig))

	// Inject into resource.resource.config.configuration
	if err := w.injectConfiguration(resource, encodedConfig); err != nil {
		return nil, fmt.Errorf("failed to inject configuration: %w", err)
	}

	w.logger.Infof("Successfully injected WAF config '%s' into WASM extension", wafConfigName)
	return resource, nil
}

// loadWAFConfig loads WAF configuration from MongoDB
func (w *WAFWasmInjector) loadWAFConfig(ctx context.Context, configName string) (*WAFConfig, error) {
	collection := w.db.Collection("waf")

	var config WAFConfig
	err := collection.FindOne(ctx, bson.M{"name": configName}).Decode(&config)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, fmt.Errorf("WAF config '%s' not found", configName)
		}
		return nil, err
	}

	return &config, nil
}

// buildCorazaConfigJSON builds Coraza WAF configuration as JSON string
// Returns the entire Data structure as a JSON string (not base64 yet)
func (w *WAFWasmInjector) buildCorazaConfigJSON(config *WAFConfig) (string, error) {
	// Convert Data struct to JSON
	jsonBytes, err := json.Marshal(config.Data)
	if err != nil {
		return "", fmt.Errorf("failed to marshal WAF data to JSON: %w", err)
	}

	return string(jsonBytes), nil
}

// injectConfiguration injects base64-encoded configuration into WASM resource
// Expected structure: resource.resource.config.configuration
func (w *WAFWasmInjector) injectConfiguration(resource models.ResourceClass, encodedConfig string) error {
	// Get the underlying resource data (Envoy proto structure)
	resourceData := resource.GetResource()
	if resourceData == nil {
		return fmt.Errorf("resource data is nil")
	}

	// Handle both map[string]any and bson.M (primitive.M)
	var resourceMap map[string]any
	switch v := resourceData.(type) {
	case map[string]any:
		resourceMap = v
	case bson.M:
		// Convert bson.M to map[string]any
		resourceMap = map[string]any(v)
	default:
		return fmt.Errorf("resource is not a map, type: %T", resourceData)
	}

	// Navigate to config
	var config map[string]any
	if configVal, exists := resourceMap["config"]; exists {
		switch cv := configVal.(type) {
		case map[string]any:
			config = cv
		case bson.M:
			config = map[string]any(cv)
		default:
			// Create new config if type is unexpected
			config = make(map[string]any)
			resourceMap["config"] = config
		}
	} else {
		// Create config if it doesn't exist
		config = make(map[string]any)
		resourceMap["config"] = config
	}

	// Replace configuration with WAF config
	config["configuration"] = map[string]any{
		"type_url": "type.googleapis.com/google.protobuf.StringValue",
		"value":    encodedConfig,
	}

	w.logger.Debugf("Injected %d bytes of base64-encoded WAF configuration", len(encodedConfig))
	return nil
}
