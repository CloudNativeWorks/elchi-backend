package responser

import (
	"encoding/json"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
)

var (
	jsonPool = sync.Pool{
		New: func() any {
			return make(map[string]any)
		},
	}
)

type ProxyResponser struct {
}

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

	respMap := jsonPool.Get().(map[string]any)
	defer jsonPool.Put(respMap)

	b, err := json.Marshal(response)
	if err != nil {
		return response
	}

	if err := json.Unmarshal(b, &respMap); err != nil {
		return response
	}

	if result, ok := respMap["Result"].(map[string]any); ok {
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
					envoyAdmin["body"] = parseBody(parsedBody, isYAML)
				}
			}
		}
	}

	return respMap
}
