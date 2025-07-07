package scenario

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/scenario/scenarios"
	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

func (sc *AppHandler) SetScenario(ctx context.Context, scenario models.ScenarioBody, reqDetails models.RequestDetails) (any, error) {
	templateMap, exists := scenarios.Scenarios[scenarios.Scenario(reqDetails.Metadata["scenario_id"])]
	if !exists {
		return nil, errors.New("scenario not found")
	}

	listenerUniqs := map[string]string{
		"UniqListenerNameID":    helper.GenerateUniqueID(6),
		"UniqFilterChainNameID": helper.GenerateUniqueID(6),
		"UniqFilterNameID":      helper.GenerateUniqueID(6),
	}

	successfulResources := []models.ResourceClass{}
	response := map[string]any{}

	for key, templateStr := range templateMap {
		if key != "managed" {
			if data, ok := scenario[key]; ok {
				templateData := map[string]any{
					"Data":     data,
					"Version":  reqDetails.Version,
					"Project":  reqDetails.Project,
					"Listener": listenerUniqs,
					"Managed":  scenario["managed"],
				}

				tmpl, err := template.New("template").Funcs(sprig.FuncMap()).Parse(templateStr)
				if err != nil {
					sc.rollback(ctx, successfulResources, reqDetails)
					return nil, fmt.Errorf("template parse error: %w", err)
				}

				var buf bytes.Buffer
				if err := tmpl.Execute(&buf, templateData); err != nil {
					sc.rollback(ctx, successfulResources, reqDetails)
					return nil, fmt.Errorf("template execute error: %w", err)
				}

				var jsonData any
				if err := json.Unmarshal(buf.Bytes(), &jsonData); err != nil {
					sc.rollback(ctx, successfulResources, reqDetails)
					return nil, fmt.Errorf("failed to parse template output as JSON: %w", err)
				}

				data, err := decodeXdsExtension(jsonData)
				if err != nil {
					sc.rollback(ctx, successfulResources, reqDetails)
					return nil, fmt.Errorf("failed to decode XDS extension: %w", err)
				}

				xdsResponse, err := sc.SetResource(ctx, data, reqDetails)
				if err != nil {
					sc.rollback(ctx, successfulResources, reqDetails)
					return nil, fmt.Errorf("failed to save resource: %w", err)
				}

				if key == "listener" {
					if managed, ok := scenario["managed"]; ok {
						data.SetManaged(managed.(bool))
					}
					response = xdsResponse
				}

				successfulResources = append(successfulResources, data)
			}
		}
	}

	return response, nil
}

func (sc *AppHandler) SetResource(ctx context.Context, data models.ResourceClass, reqDetails models.RequestDetails) (map[string]any, error) {
	Gtype := data.GetGeneral().GType
	var response any
	var err error

	if helper.Contains([]string{"filters", "extensions"}, Gtype.CollectionString()) {
		response, err = sc.Extension.SetExtension(ctx, data, reqDetails)
	} else {
		response, err = sc.XDS.SetResource(ctx, data, reqDetails)
	}

	if err != nil {
		return nil, err
	}

	result, ok := response.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected response type: %T", response)
	}

	return result, nil
}

func (sc *AppHandler) rollback(ctx context.Context, resources []models.ResourceClass, reqDetails models.RequestDetails) {
	retryList := make([]models.ResourceClass, len(resources))
	copy(retryList, resources)

	for len(retryList) > 0 {
		sc.Logger.Infof("Rollback attempt, resources left: %d", len(retryList))
		var failedResources []models.ResourceClass

		for _, resource := range retryList {
			err := sc.DeleteResource(ctx, resource, reqDetails)
			if err != nil {
				if isDependencyError(err) {
					sc.Logger.Infof("Resource %v has dependencies, will retry: %v", resource.GetGeneral().Name, err)
					failedResources = append(failedResources, resource)
				} else {
					sc.Logger.Infof("Failed to delete resource %v: %v", resource.GetGeneral().Name, err)
				}
			}
		}

		if len(failedResources) == len(retryList) {
			sc.Logger.Errorf("Rollback stuck, no progress made in removing resources. Exiting...")
			for _, resource := range failedResources {
				sc.Logger.Errorf("Could not delete resource: %v", resource.GetGeneral().Name)
			}
			break
		}

		retryList = failedResources
	}

	if len(retryList) > 0 {
		sc.Logger.Errorf("Rollback completed with unresolved resources: %d", len(retryList))
		for _, resource := range retryList {
			sc.Logger.Errorf("Unresolved resource: %v", resource.GetGeneral().Name)
		}
	} else {
		sc.Logger.Infof("Rollback successfully completed with no remaining resources.")
	}
}

func isDependencyError(err error) bool {
	return strings.Contains(err.Error(), "Resource has dependencies")
}

func (sc *AppHandler) DeleteResource(ctx context.Context, resource models.ResourceClass, reqDetails models.RequestDetails) error {
	general := resource.GetGeneral()
	reqDetails.Name = general.Name
	reqDetails.Collection = general.Collection
	reqDetails.GType = general.GType
	reqDetails.ResourceID = resource.GetID()

	if helper.Contains([]string{"filters", "extensions"}, general.GType.CollectionString()) {
		_, err := sc.Extension.DelExtension(ctx, resource, reqDetails)
		return err
	}

	_, err := sc.XDS.DelResource(ctx, resource, reqDetails)
	return err
}

func decodeXdsExtension(data any) (models.ResourceClass, error) {
	var resource models.DBResource

	resourceBytes, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal data to JSON: %w", err)
	}

	if err := json.Unmarshal(resourceBytes, &resource); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON to DBResource: %w", err)
	}

	return &resource, nil
}
