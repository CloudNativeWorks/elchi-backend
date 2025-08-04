package scenario

import (
	"context"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/controller/crud/scenario/scenarios"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

func (t *AppHandler) GetScenarios(_ context.Context, _ models.ResourceClass, _ models.RequestDetails) (any, error) {
	return scenarios.Resources, nil
}

func (t *AppHandler) GetScenario(_ context.Context, _ models.ResourceClass, reqDetails models.RequestDetails) (any, error) {
	scenarioID := reqDetails.Metadata["scenario_id"]

	for _, resource := range scenarios.Resources {
		if resource.Scenario == scenarios.Scenario(scenarioID) {
			return resource, nil
		}
	}

	return nil, fmt.Errorf("scenario not found for scenario ID: %s", scenarioID)
}
