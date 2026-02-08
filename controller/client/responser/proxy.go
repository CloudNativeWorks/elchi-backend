package responser

import (
	"encoding/json"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
)

var jsonPool = sync.Pool{
	New: func() any {
		return make(map[string]any)
	},
}

type ProxyResponser struct{}

func tryParseJSON(str string) (any, bool) {
	if len(str) < 2 || (str[0] != '{' && str[0] != '[') {
		return str, false
	}

	var parsed any
	if err := json.Unmarshal([]byte(str), &parsed); err != nil {
		return str, false
	}
	return parsed, true
}

func tryParseYAML(str string) (any, bool) {
	if len(str) == 0 {
		return str, false
	}

	var parsed any
	if err := yaml.Unmarshal([]byte(str), &parsed); err != nil {
		return str, false
	}
	return parsed, true
}

func parseBody(body map[string]any, isYAML bool) map[string]any {
	result := jsonPool.Get().(map[string]any)

	for key, value := range body {
		switch v := value.(type) {
		case string:
			var parsed any
			var ok bool
			if isYAML {
				parsed, ok = tryParseYAML(v)
			} else {
				parsed, ok = tryParseJSON(v)
			}
			if ok {
				result[key] = parsed
			} else {
				result[key] = v
			}
		case map[string]any:
			result[key] = parseBody(v, isYAML)
		case []any:
			newArray := make([]any, len(v))
			for i, item := range v {
				if subMap, ok := item.(map[string]any); ok {
					newArray[i] = parseBody(subMap, isYAML)
				} else {
					newArray[i] = item
				}
			}
			result[key] = newArray
		default:
			result[key] = v
		}
	}

	return result
}

func (p *ProxyResponser) ValidateAndTransform(op models.OperationClass, response *pb.CommandResponse) any {
	if response == nil || response.GetEnvoyAdmin() == nil {
		return response
	}

	path, _ := op.GetCommandPath()
	isYAML := path == "/logging" || path == "/envoy"

	// Don't return pooled map directly - it gets reused and causes duplicate responses!
	// Use pool for temporary processing, then create a new map for return value
	tempMap := jsonPool.Get().(map[string]any)
	defer func() {
		// Clear map before returning to pool
		for k := range tempMap {
			delete(tempMap, k)
		}
		jsonPool.Put(tempMap)
	}()

	b, err := json.Marshal(response)
	if err != nil {
		return response
	}

	if err := json.Unmarshal(b, &tempMap); err != nil {
		return response
	}

	if result, ok := tempMap["Result"].(map[string]any); ok {
		if envoyAdmin, ok := result["EnvoyAdmin"].(map[string]any); ok {
			if bodyStr, ok := envoyAdmin["body"].(string); ok {
				var parsedBody map[string]any

				if isYAML {
					if parsed, ok := tryParseYAML(bodyStr); ok {
						if mapBody, ok := parsed.(map[string]any); ok {
							parsedBody = mapBody
						}
					}
				} else {
					if parsed, ok := tryParseJSON(bodyStr); ok {
						if mapBody, ok := parsed.(map[string]any); ok {
							parsedBody = mapBody
						}
					}
				}

				if parsedBody != nil {
					processedBody := parseBody(parsedBody, isYAML)
					// Mask sensitive secret data in config_dump
					maskSecretsInEnvoyConfig(processedBody)
					envoyAdmin["body"] = processedBody
				}
			}
		}
	}

	// Create a NEW map to return (not the pooled one!)
	// This prevents the pool from reusing the same map instance for multiple responses
	resultMap := make(map[string]any, len(tempMap))
	for k, v := range tempMap {
		resultMap[k] = v
	}

	return resultMap
}

// maskSecretsInEnvoyConfig masks sensitive data in Envoy admin config dump
func maskSecretsInEnvoyConfig(data map[string]any) {
	// Check if this is a config_dump response
	if configDump, ok := data["config_dump"].(map[string]any); ok {
		if configs, ok := configDump["configs"].([]any); ok {
			for _, config := range configs {
				if configMap, ok := config.(map[string]any); ok {
					maskSecretsRecursive(configMap)
				}
			}
		}
	}

	// Also check top-level certs section
	if certs, ok := data["certs"].(map[string]any); ok {
		maskSecretsRecursive(certs)
	}
}

// maskSecretsRecursive recursively masks sensitive fields in nested structures
func maskSecretsRecursive(data map[string]any) {
	for key, value := range data {
		// Mask sensitive field values
		if isSensitiveField(key) {
			data[key] = "***REDACTED***"
			continue
		}

		// Recursively process nested structures
		switch v := value.(type) {
		case map[string]any:
			maskSecretsRecursive(v)
		case []any:
			for _, item := range v {
				if itemMap, ok := item.(map[string]any); ok {
					maskSecretsRecursive(itemMap)
				}
			}
		}
	}
}

// isSensitiveField checks if a field contains sensitive data
func isSensitiveField(fieldName string) bool {
	// Exact match fields (case-insensitive)
	exactMatchFields := []string{
		"private_key",
		"password",
		"token",
		"api_key",
		"credential",
		"inline_bytes",
		"inline_string",
	}

	// Case-insensitive comparison
	fieldLower := ""
	for _, c := range fieldName {
		if c >= 'A' && c <= 'Z' {
			fieldLower += string(c + 32)
		} else {
			fieldLower += string(c)
		}
	}

	// Check exact matches
	for _, sensitive := range exactMatchFields {
		if fieldLower == sensitive {
			return true
		}
	}

	// Special case: "secret" only if it's the exact field name or ends with "_secret"
	// This avoids masking "sds_secret_configs" which is just configuration
	if fieldLower == "secret" || strings.HasSuffix(fieldLower, "_secret") {
		// But exclude configuration fields that just reference secrets
		if !strings.Contains(fieldLower, "secret_config") && !strings.Contains(fieldLower, "secret_provider") {
			return true
		}
	}

	return false
}
