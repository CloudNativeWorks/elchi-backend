package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
)

func (s *ResourceServiceServer) ValidateResource(ctx context.Context, req *bridge.ValidateResourceRequest) (*bridge.ValidateResourceResponse, error) {
	gtype := models.GTypes(req.Gtype)
	var genericData any
	var isArray bool

	if req.Resource == nil {
		return &bridge.ValidateResourceResponse{Error: "Resource cannot be nil"}, nil
	}

	jsonBytes, err := protojson.Marshal(req.Resource)
	if err != nil {
		return &bridge.ValidateResourceResponse{Error: fmt.Sprintf("Failed to marshal resource: %v", err)}, nil
	}

	var typedData map[string]any
	if err := json.Unmarshal(jsonBytes, &typedData); err != nil {
		return &bridge.ValidateResourceResponse{Error: fmt.Sprintf("Failed to unmarshal for type check: %v", err)}, nil
	}

	typeField, hasType := typedData["@type"].(string)
	isListValue := hasType && strings.Contains(typeField, "google.protobuf.ListValue")
	isStructValue := hasType && strings.Contains(typeField, "google.protobuf.Struct")

	if isListValue || isStructValue {
		if valueField, hasValue := typedData["value"]; hasValue {
			valueJSON, err := json.Marshal(valueField)
			if err != nil {
				return &bridge.ValidateResourceResponse{Error: fmt.Sprintf("Failed to marshal value field: %v", err)}, nil
			}
			jsonBytes = valueJSON

			if isListValue {
				isArray = true
			}
		}
	}

	if isArray {
		var arrayData []any
		if err := json.Unmarshal(jsonBytes, &arrayData); err != nil {
			return &bridge.ValidateResourceResponse{Error: fmt.Sprintf("Failed to unmarshal to array: %v", err)}, nil
		}
		genericData = arrayData
	} else {
		var mapData map[string]any
		if err := json.Unmarshal(jsonBytes, &mapData); err != nil {
			return &bridge.ValidateResourceResponse{Error: fmt.Sprintf("Failed to unmarshal to map: %v", err)}, nil
		}
		genericData = mapData
	}

	validationErrors, isError, _ := resources.Validate(gtype, genericData)

	if isError {
		var formattedError string

		formattedError = ":"
		for _, errMsg := range validationErrors {
			formattedError += fmt.Sprintf("    %s\n", errMsg)
		}

		return &bridge.ValidateResourceResponse{Error: formattedError}, nil
	}

	return &bridge.ValidateResourceResponse{Error: ""}, nil
}
