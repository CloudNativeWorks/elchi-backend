package resource

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tidwall/sjson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/anypb"

	// hcm "github.com/CloudNativeWorks/versioned-go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3" // Commented out - VHDS inline route config not supported
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
)

func (ar *AllResources) GetTypedConfigs(ctx context.Context, paths []models.TypedConfigPath, jsonData any, context *db.AppContext) (any, error) {
	jsonStringStr, err := helper.MarshalJSON(jsonData, context.Logger)
	if err != nil {
		return jsonData, err
	}

	for _, pathd := range paths {
		if err := ar.processTypedConfigPath(ctx, pathd, &jsonStringStr, context); err != nil {
			context.Logger.Debugf("Error processing typed config path: %v", err)
		}
	}

	var updatedJSONData any
	if err := json.Unmarshal([]byte(jsonStringStr), &updatedJSONData); err != nil {
		context.Logger.Errorf("Error unmarshalling updated JSON: %v", err)
		return nil, err
	}

	return updatedJSONData, nil
}

func (ar *AllResources) processTypedConfigPath(ctx context.Context, pathd models.TypedConfigPath, jsonStringStr *string, context *db.AppContext) error {
	_, typedConfigsMap := resources.ProcessTypedConfigs(*jsonStringStr, pathd, context.Logger)

	for path, tempTypedConfig := range typedConfigsMap {
		conf, err := resources.GetResourceNGeneral(ctx, context, tempTypedConfig.Collection, tempTypedConfig.Name, ar.Project, ar.ResourceVersion)
		if err != nil {
			context.Logger.Warnf("Error getting resource from DB: %v", err)
			return err
		}

		resource := conf.GetResource()

		// FileAccessLog path override for managed listeners
		if tempTypedConfig.Gtype == models.FileAccessLog && ar.IsManaged {
			ar.overrideFileAccessLogPath(resource, context)
		}

		typedConfigJSON, err := json.Marshal(resource)
		if err != nil {
			context.Logger.Warnf("Error marshaling typed config: %v", err)
			return err
		}
		typedConfigStr := string(typedConfigJSON)

		ar.processUpstreamPaths(ctx, tempTypedConfig.Gtype.UpstreamPaths(), &typedConfigStr, tempTypedConfig.ParentName, context, context.Logger)

		ar.processConfigDiscoveries(ctx, conf.General.ConfigDiscovery, context, context.Logger)

		ar.processTypedConfigPaths(ctx, tempTypedConfig.Gtype.TypedConfigPaths(), &typedConfigStr, context, context.Logger)

		typedConfig, err := decodeTypedConfig([]byte(typedConfigStr), tempTypedConfig.Gtype)
		if err != nil {
			context.Logger.Warnf("Error decoding typed config: %v", err)
			return err
		}

		// COMMENTED OUT: VHDS doesn't work with inline route config in Envoy currently
		// Keeping this code for future if Envoy adds support
		// See: https://github.com/envoyproxy/envoy/issues/23263
		/*
		// Update VHDS metadata for HCM after decoding to protobuf
		if tempTypedConfig.Gtype == models.HTTPConnectionManager {
			if err := ar.updateHCMVhdsMetadata(typedConfig, context); err != nil {
				context.Logger.Warnf("Error updating HCM VHDS metadata: %v", err)
			}
		}
		*/

		if err := ar.updateJSONConfig(jsonStringStr, path, typedConfig, pathd.IsPerTypedConfig, tempTypedConfig); err != nil {
			context.Logger.Warnf("Error updating JSON config: %v", err)
			return err
		}
	}

	return nil
}

// COMMENTED OUT: VHDS doesn't work with inline route config in Envoy currently
// Keeping this function for future if Envoy adds support
// See: https://github.com/envoyproxy/envoy/issues/23263
/*
// updateHCMVhdsMetadata updates VHDS metadata in HCM if it exists
func (ar *AllResources) updateHCMVhdsMetadata(typedConfig *anypb.Any, context *db.AppContext) error {
	// Extract HCM from anypb.Any
	hcmConfig := &hcm.HttpConnectionManager{}
	if err := typedConfig.UnmarshalTo(hcmConfig); err != nil {
		return fmt.Errorf("failed to unmarshal HCM from Any: %w", err)
	}

	updated := false
	
	// Check for inline route config with VHDS
	if routeConfig := hcmConfig.GetRouteConfig(); routeConfig != nil {
		if vhds := routeConfig.GetVhds(); vhds != nil {
			context.Logger.Debugf("🔍 VHDS: Found VHDS in HCM typed_config, updating metadata")
			ar.UpdateVhdsMetadataNodeID(vhds)
			updated = true
		}
	}

	// If we updated the VHDS, we need to re-pack the HCM into the Any
	if updated {
		newAny, err := anypb.New(hcmConfig)
		if err != nil {
			return fmt.Errorf("failed to pack updated HCM into Any: %w", err)
		}
		
		// Replace the content of the original Any
		typedConfig.TypeUrl = newAny.TypeUrl
		typedConfig.Value = newAny.Value
		
		context.Logger.Debugf("🔧 VHDS: Updated HCM typed_config VHDS metadata")
	}

	return nil
}
*/

func (ar *AllResources) updateJSONConfig(jsonStringStr *string, path string, typedConfig *anypb.Any, isPerTypedConfig bool, tempTypedConfig *models.TypedConfig) error {
	var config any
	var err error

	if isPerTypedConfig && tempTypedConfig.Disabled {
		config = map[string]any{
			"@type":    "type.googleapis.com/envoy.config.route.v3.FilterConfig",
			"disabled": true,
		}
	} else {
		anyJSON, err := protojson.Marshal(typedConfig)
		if err != nil {
			return fmt.Errorf("error marshaling any typed config: %w", err)
		}

		if err := json.Unmarshal(anyJSON, &config); err != nil {
			return fmt.Errorf("error marshaling any typed config: %w", err)
		}
	}

	if *jsonStringStr, err = sjson.Set(*jsonStringStr, path, config); err != nil {
		return fmt.Errorf("error setting new config value with sjson.Set: %w", err)
	}

	return nil
}

func decodeTypedConfig(typedConfigJSON []byte, gtype models.GType) (*anypb.Any, error) {
	msg := gtype.ProtoMessage()
	if err := helper.Unmarshaler.Unmarshal(typedConfigJSON, msg); err != nil {
		return nil, fmt.Errorf("typed_config not resolved: %w", err)
	}

	return anypb.New(msg)
}

// overrideFileAccessLogPath overrides the path field in FileAccessLog resources for managed listeners
// Format: /var/log/elchi/<listener_name>-<admin_port>_access.log
func (ar *AllResources) overrideFileAccessLogPath(resource any, context *db.AppContext) {
	context.Logger.Debugf("📝 Found FileAccessLog in typed_config, IsManaged=%v, ListenerName=%s, AdminPort=%d, ResourceType=%T",
		ar.IsManaged, ar.ListenerName, ar.AdminPort, resource)

	var oldPath any
	var pathUpdated bool
	newPath := fmt.Sprintf("/var/log/elchi/%s-%d_access.log", ar.ListenerName, ar.AdminPort)

	if primitiveMap, ok := resource.(primitive.M); ok {
		oldPath = primitiveMap["path"]
		primitiveMap["path"] = newPath
		pathUpdated = true
	}

	if pathUpdated {
		context.Logger.Infof("🔄 [PATH_OVERRIDE] FileAccessLog for managed listener '%s': %s → %s",
			ar.ListenerName, oldPath, newPath)
	} else {
		context.Logger.Warnf("Failed to override FileAccessLog path: unknown resource type %T", resource)
	}
}
