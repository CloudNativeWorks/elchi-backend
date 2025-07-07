package resources

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"

	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

func DecodeBase64Config(encodedConfig string) (*models.TypedConfig, error) {
	decodedString, err := base64.StdEncoding.DecodeString(encodedConfig)
	if err != nil {
		return nil, err
	}

	var configData models.TypedConfig
	err = json.Unmarshal(decodedString, &configData)
	if err != nil {
		return nil, err
	}

	return &configData, nil
}

func GetTypedConfigValue(jsonStringStr, path string, logger *logrus.Logger) *models.TypedConfig {
	value := gjson.Get(jsonStringStr, path).String()

	if value == "" {
		return nil
	}

	typedConfig, err := DecodeBase64Config(value)
	if err != nil {
		logger.Debugf("Error decoding base64 config: %v", err)
		return nil
	}

	return typedConfig
}

func ProcessTypedConfigs(jsonStringStr string, typedConfigPath models.TypedConfigPath, logger *logrus.Logger) ([]*models.TypedConfig, map[string]*models.TypedConfig) {
	var typedConfigs []*models.TypedConfig
	typedConfigsMap := make(map[string]*models.TypedConfig)
	seenConfigs := make(map[string]struct{})

	if typedConfigPath.IsPerTypedConfig {
		handlePerTypedConfig(jsonStringStr, typedConfigPath, &typedConfigs, typedConfigsMap, seenConfigs, logger)
	} else {
		handleNonPerTypedConfig(jsonStringStr, typedConfigPath, &typedConfigs, typedConfigsMap, seenConfigs, logger)
	}

	return typedConfigs, typedConfigsMap
}

func handlePerTypedConfig(jsonStringStr string, typedConfigPath models.TypedConfigPath, typedConfigs *[]*models.TypedConfig, typedConfigsMap map[string]*models.TypedConfig, seenConfigs map[string]struct{}, logger *logrus.Logger) {
	if len(typedConfigPath.ArrayPaths) == 0 {
		result := gjson.Get(jsonStringStr, typedConfigPath.PathTemplate)
		if result.Exists() {
			result.ForEach(func(key, _ gjson.Result) bool {
				dynamicKey := key.String()
				dynamicPath := fmt.Sprintf("%s.%s", typedConfigPath.PathTemplate, helper.EscapePointKey(dynamicKey))
				processPath(jsonStringStr, dynamicPath, typedConfigs, typedConfigsMap, seenConfigs, logger)
				return true
			})
		}
	} else {
		result := gjson.Get(jsonStringStr, typedConfigPath.ArrayPaths[0].ParentPath)
		if result.IsArray() {
			processPerTypedConfigArray(result.Array(), jsonStringStr, typedConfigPath.PathTemplate, typedConfigPath.ArrayPaths, typedConfigs, typedConfigsMap, seenConfigs, logger)
		} else if result.Exists() {
			processDynamicKey(result, typedConfigPath.ArrayPaths[0].ParentPath, typedConfigs, typedConfigsMap, seenConfigs, logger)
		}
	}
}

func handleNonPerTypedConfig(jsonStringStr string, typedConfigPath models.TypedConfigPath, typedConfigs *[]*models.TypedConfig, typedConfigsMap map[string]*models.TypedConfig, seenConfigs map[string]struct{}, logger *logrus.Logger) {
	if len(typedConfigPath.ArrayPaths) == 0 {
		processPath(jsonStringStr, typedConfigPath.PathTemplate, typedConfigs, typedConfigsMap, seenConfigs, logger)
	} else {
		result := gjson.Get(jsonStringStr, typedConfigPath.ArrayPaths[0].ParentPath)
		if result.IsArray() {
			processArray(result.Array(), jsonStringStr, typedConfigPath.PathTemplate, typedConfigPath.ArrayPaths, typedConfigs, typedConfigsMap, seenConfigs, logger)
		} else if result.Exists() {
			processPath(result.String(), typedConfigPath.ArrayPaths[0].ParentPath, typedConfigs, typedConfigsMap, seenConfigs, logger)
		}
	}
}

func processDynamicKey(result gjson.Result, basePath string, typedConfigs *[]*models.TypedConfig, typedConfigsMap map[string]*models.TypedConfig, seenConfigs map[string]struct{}, logger *logrus.Logger) {
	result.ForEach(func(key, _ gjson.Result) bool {
		dynamicKey := key.String()
		dynamicPath := fmt.Sprintf("%s.%s", basePath, dynamicKey)
		processPath(result.String(), dynamicPath, typedConfigs, typedConfigsMap, seenConfigs, logger)
		return true
	})
}

func processPerTypedConfigArray(array []gjson.Result, jsonStringStr, pathTemplate string, arrayPaths []models.ArrayPath, typedConfigs *[]*models.TypedConfig, typedConfigsMap map[string]*models.TypedConfig, seenConfigs map[string]struct{}, logger *logrus.Logger) {
	placeholderCount := strings.Count(pathTemplate, "%d")

	for i := range array {
		combinations := generateIndexCombinations(jsonStringStr, arrayPaths)

		for _, indices := range combinations {
			if len(indices) == placeholderCount {
				indices[0] = i
				finalPath := fmt.Sprintf(pathTemplate, indices...)
				dynamicResult := gjson.Get(jsonStringStr, finalPath)

				if dynamicResult.Exists() {
					dynamicResult.ForEach(func(key, _ gjson.Result) bool {
						dynamicKey := key.String()
						dynamicPath := fmt.Sprintf("%s.%s", finalPath, dynamicKey)
						processPath(jsonStringStr, dynamicPath, typedConfigs, typedConfigsMap, seenConfigs, logger)
						return true
					})
				}
			}
		}
	}
}

func processArray(array []gjson.Result, jsonStringStr, pathTemplate string, arrayPaths []models.ArrayPath, typedConfigs *[]*models.TypedConfig, typedConfigsMap map[string]*models.TypedConfig, seenConfigs map[string]struct{}, logger *logrus.Logger) {
	placeholderCount := strings.Count(pathTemplate, "%d")

	for i := range array {
		combinations := generateIndexCombinations(jsonStringStr, arrayPaths)

		for _, indices := range combinations {
			if len(indices) == placeholderCount {
				indices[0] = i
				processPath(jsonStringStr, fmt.Sprintf(pathTemplate, indices...), typedConfigs, typedConfigsMap, seenConfigs, logger)
			}
		}
	}
}

func processPath(jsonStringStr, path string, typedConfigs *[]*models.TypedConfig, typedConfigsMap map[string]*models.TypedConfig, seenConfigs map[string]struct{}, logger *logrus.Logger) {
	singleTypedConfig := GetTypedConfigValue(jsonStringStr, path+".value", logger)

	if singleTypedConfig != nil {
		uniqueKey := fmt.Sprintf("%s|%s|%s|%s", singleTypedConfig.Gtype, singleTypedConfig.Name, path, singleTypedConfig.ParentName)

		if _, exists := seenConfigs[uniqueKey]; !exists {
			*typedConfigs = append(*typedConfigs, singleTypedConfig)
			typedConfigsMap[path] = singleTypedConfig
			seenConfigs[uniqueKey] = struct{}{}
		} else {
			logger.Debugf("Duplicate typed_config detected for key: %s", uniqueKey)
		}
	}
}

func generateIndexCombinations(jsonStringStr string, arrayPaths []models.ArrayPath) [][]any {
	var combinations [][]any
	if len(arrayPaths) == 0 {
		return combinations
	}

	indices := make([]any, len(arrayPaths))
	generateCombinations(arrayPaths, indices, 0, &combinations, jsonStringStr)

	return combinations
}

func generateCombinations(arrayPaths []models.ArrayPath, indices []any, level int, combinations *[][]any, jsonStringStr string) {
	if level == len(indices) {
		*combinations = append(*combinations, append([]any(nil), indices...))
		return
	}

	parentPath := fillIndices(arrayPaths[level].ParentPath, indices[:level])
	currentArray := gjson.Get(jsonStringStr, parentPath)
	if !currentArray.IsArray() {
		return
	}

	for i := 0; i < len(currentArray.Array()); i++ {
		indices[level] = i
		generateCombinations(arrayPaths, indices, level+1, combinations, jsonStringStr)
	}
}

func fillIndices(pathTemplate string, indices []any) string {
	return fmt.Sprintf(pathTemplate, indices...)
}

func DecodeSetTypedConfigs(resource models.ResourceClass, logger *logrus.Logger) []*models.TypedConfig {
	var typedConfigs []*models.TypedConfig

	if paths := resource.GetGtype().TypedConfigPaths(); paths != nil {
		typedConfigs = getTypedConfigs(resource, logger, paths)
	} else {
		logger.Debugf("Unsupported general type: %v", resource.GetGeneral().GType)
	}

	return typedConfigs
}

func getTypedConfigs(resource models.ResourceClass, logger *logrus.Logger, paths []models.TypedConfigPath) []*models.TypedConfig {
	var typedConfigs []*models.TypedConfig
	resourceValue := resource.GetResource()

	processResource := func(value any) {
		jsonStringStr, err := helper.MarshalJSON(value, logger)
		if err != nil {
			logger.Errorf("Error marshaling JSON: %v", err)
			return
		}

		for _, path := range paths {
			typedConfigsPart, _ := ProcessTypedConfigs(jsonStringStr, path, logger)
			typedConfigs = append(typedConfigs, typedConfigsPart...)
		}
	}

	switch v := resourceValue.(type) {
	case []any:
		for _, r := range v {
			processResource(r)
		}
	case any:
		processResource(v)
	default:
		logger.Errorf("Unsupported resource type")
	}

	return typedConfigs
}
