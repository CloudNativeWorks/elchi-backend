package extension

import (
	"context"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/controller/waf"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/mongo"
)

// ProcessWAFInjection handles WAF configuration injection for HTTP WASM extensions
// This function is used by both create and update handlers
// If general.waf is empty, it clears the WAF configuration (removes it)
func ProcessWAFInjection(ctx context.Context, resource models.ResourceClass, db *mongo.Database, logger *logger.Logger) error {
	general := resource.GetGeneral()

	// Only apply to HTTP WASM extensions
	if general.GType != models.HTTPWasm {
		// Not a WASM extension, skip WAF processing entirely
		return nil
	}

	// Case 1: WAF is specified -> Inject WAF config
	if general.WAF != "" {
		injector := waf.NewWAFWasmInjector(db, logger)
		updatedResource, err := injector.InjectWAFConfig(ctx, resource, general.WAF)
		if err != nil {
			return fmt.Errorf("failed to inject WAF configuration: %w", err)
		}

		if updatedResource != nil {
			logger.Infof("Successfully injected WAF configuration '%s' into HTTP WASM extension '%s'", general.WAF, general.Name)
		}
		return nil
	}

	// Case 2: WAF is empty -> Clear/remove WAF configuration
	logger.Infof("WAF field is empty for HTTP WASM extension '%s', clearing WAF configuration", general.Name)
	return clearWAFConfiguration(resource, logger)
}

// clearWAFConfiguration removes WAF configuration from the WASM extension
func clearWAFConfiguration(resource models.ResourceClass, logger *logger.Logger) error {
	// Get the underlying resource (Envoy proto structure)
	resourceData := resource.GetResource()
	if resourceData == nil {
		logger.Warnf("Unable to clear WAF configuration: resource data is nil")
		return nil // Don't fail the entire operation
	}

	// Cast to map for manipulation
	resourceMap, ok := resourceData.(map[string]any)
	if !ok {
		logger.Warnf("Unable to clear WAF configuration: resource is not a map")
		return nil // Don't fail the entire operation
	}

	// Navigate to config.configuration and remove it
	// Structure: resource.config.configuration
	if config, ok := resourceMap["config"].(map[string]any); ok {
		// Remove the configuration field
		delete(config, "configuration")
		logger.Debugf("Cleared WAF configuration from WASM extension")
	}

	return nil
}
