package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// validateCreateScenarioRequest validates the create scenario request
func validateCreateScenarioRequest(request models.CreateScenarioRequest) error {
	// Check for unsupported component types
	supportedTypes := map[string]bool{
		"cluster":                 true,
		"listener":                true,
		"http_connection_manager": true,
		"route":                   true,
		"virtual_host":            true,
		"endpoint":                true,
		"tcp_proxy":               true,
		"router_filter":           true,
		"access_log_file":         true,
		"access_log_stdout":       true,
		"access_log_fluentd":      true,
	}

	for _, component := range request.Components {
		// Check component type
		if !supportedTypes[component.Type] {
			if component.Type == "hcm" {
				return fmt.Errorf("component type 'hcm' is no longer supported. Use 'http_connection_manager' instead")
			}
			return fmt.Errorf("unsupported component type: %s", component.Type)
		}

		// For scenario creation, we only validate that required fields are included in the selection
		// We don't validate if their values are filled (that's for execution time)
		// This allows users to create scenarios with templates/placeholders
	}

	return nil
}

// GetComponentCatalogHandler handles GET /api/v3/scenario/components
func (h *Handler) GetComponentCatalogHandler(c *gin.Context) {
	h.handleRequest(c, func(ctx context.Context, _ models.ResourceClass, reqDetails models.RequestDetails) (any, error) {
		return h.Scenario.GetComponentCatalog()
	})
}

// CreateScenarioHandler handles POST /api/v3/scenario/scenarios
func (h *Handler) CreateScenarioHandler(c *gin.Context) {
	var request models.CreateScenarioRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "JSON binding failed: " + err.Error(), "data": nil})
		return
	}

	// Validate payload before processing
	if err := validateCreateScenarioRequest(request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error(), "data": nil})
		return
	}

	// Store request in context for audit
	c.Set("_scenario_request", request)

	h.handleRequest(c, func(ctx context.Context, _ models.ResourceClass, reqDetails models.RequestDetails) (any, error) {
		// Override project from query param
		if projectParam := c.Query("project"); projectParam != "" {
			reqDetails.Project = projectParam
		}

		return h.Scenario.CreateScenario(request, reqDetails)
	})
}

// GetScenariosHandler handles GET /api/v3/scenario/scenarios
func (h *Handler) GetScenariosHandler(c *gin.Context) {
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Project parameter is required", "data": nil})
		return
	}

	h.handleRequest(c, func(ctx context.Context, _ models.ResourceClass, reqDetails models.RequestDetails) (any, error) {
		return h.Scenario.GetScenarios(project, reqDetails)
	})
}

// GetScenarioHandler handles GET /api/v3/scenario/scenarios/:scenario_id
func (h *Handler) GetScenarioHandler(c *gin.Context) {
	scenarioID := c.Param("scenario_id")

	h.handleRequest(c, func(ctx context.Context, _ models.ResourceClass, reqDetails models.RequestDetails) (any, error) {
		return h.Scenario.GetScenarioByID(scenarioID, reqDetails)
	})
}

// UpdateScenarioHandler handles PUT /api/v3/scenario/scenarios/:scenario_id
func (h *Handler) UpdateScenarioHandler(c *gin.Context) {
	scenarioID := c.Param("scenario_id")

	var request models.UpdateScenarioRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request format: " + err.Error(), "data": nil})
		return
	}

	// Store request in context for audit
	c.Set("_scenario_request", request)
	c.Set("_scenario_id", scenarioID)

	h.handleRequest(c, func(ctx context.Context, _ models.ResourceClass, reqDetails models.RequestDetails) (any, error) {
		// Override project from query param
		if projectParam := c.Query("project"); projectParam != "" {
			reqDetails.Project = projectParam
		}

		return h.Scenario.UpdateScenario(scenarioID, request, reqDetails)
	})
}

// DeleteScenarioHandler handles DELETE /api/v3/scenario/scenarios/:scenario_id
func (h *Handler) DeleteScenarioHandler(c *gin.Context) {
	scenarioID := c.Param("scenario_id")

	// Store scenario ID in context for audit
	c.Set("_scenario_id", scenarioID)

	h.handleRequest(c, func(ctx context.Context, _ models.ResourceClass, reqDetails models.RequestDetails) (any, error) {
		err := h.Scenario.DeleteScenario(scenarioID, reqDetails)
		if err != nil {
			return nil, err
		}
		return gin.H{"message": "Scenario deleted successfully"}, nil
	})
}

// ExecuteScenarioHandler handles POST /api/v3/scenario/execute
func (h *Handler) ExecuteScenarioHandler(c *gin.Context) {
	var request models.ExecuteScenarioRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request format: " + err.Error(), "data": nil})
		return
	}

	// Store request in context for audit
	c.Set("_scenario_request", request)

	h.handleRequest(c, func(ctx context.Context, _ models.ResourceClass, reqDetails models.RequestDetails) (any, error) {
		// Use project and version from request body
		reqDetails.Project = request.Project
		reqDetails.Version = request.Version

		resources, err := h.Scenario.ExecuteScenario(request, reqDetails)
		if err != nil {
			return nil, err
		}

		return models.ExecuteScenarioResponse{
			Resources: resources,
			Success:   true,
			Message:   "Scenario executed successfully",
		}, nil
	})
}

// groupValidationErrors groups validation errors by component for better readability
func groupValidationErrors(errors []string) map[string][]string {
	grouped := make(map[string][]string)

	for _, err := range errors {
		// Try to extract component name from error message
		// Most errors follow pattern "Component XXX: message" or start with component-agnostic message
		if strings.Contains(err, "Component ") {
			parts := strings.SplitN(err, ":", 2)
			if len(parts) == 2 {
				componentPart := strings.TrimPrefix(parts[0], "Component ")
				message := strings.TrimSpace(parts[1])
				grouped[componentPart] = append(grouped[componentPart], message)
			} else {
				grouped["General"] = append(grouped["General"], err)
			}
		} else {
			// General errors not specific to a component
			grouped["General"] = append(grouped["General"], err)
		}
	}

	return grouped
}

// ValidateScenarioHandler handles POST /api/v3/scenario/validate
func (h *Handler) ValidateScenarioHandler(c *gin.Context) {
	var components []models.ComponentInstance
	if err := c.ShouldBindJSON(&components); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request format: " + err.Error(), "data": nil})
		return
	}

	// Store components in context for audit
	c.Set("_scenario_components", components)

	h.handleRequest(c, func(ctx context.Context, _ models.ResourceClass, reqDetails models.RequestDetails) (any, error) {
		// Call validate function
		errors := h.Scenario.ValidateComponentRules(components)

		// Group errors for better readability
		groupedErrors := groupValidationErrors(errors)

		return gin.H{
			"valid":          len(errors) == 0,
			"errors":         errors,        // Original flat list
			"grouped_errors": groupedErrors, // Grouped by component
			"error_count":    len(errors),
		}, nil
	})
}

// ExportScenariosHandler handles POST /api/v3/scenario/export
func (h *Handler) ExportScenariosHandler(c *gin.Context) {
	var request models.ExportScenarioRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request format: " + err.Error(), "data": nil})
		return
	}

	// Store request in context for audit
	c.Set("_scenario_request", request)

	h.handleRequest(c, func(ctx context.Context, _ models.ResourceClass, reqDetails models.RequestDetails) (any, error) {
		// Override project from query param
		if projectParam := c.Query("project"); projectParam != "" {
			reqDetails.Project = projectParam
		}

		return h.Scenario.ExportScenarios(request, reqDetails)
	})
}

// ImportScenariosHandler handles POST /api/v3/scenario/import
func (h *Handler) ImportScenariosHandler(c *gin.Context) {
	// Try different parsing strategies for different input formats
	var request models.ImportScenarioRequest
	var rawData map[string]any

	// First parse as raw JSON to determine structure
	if err := c.ShouldBindJSON(&rawData); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid JSON format: " + err.Error(), "data": nil})
		return
	}

	// Check if it's wrapped in a "data" field (export response format)
	if dataField, hasData := rawData["data"]; hasData {
		if dataMap, ok := dataField.(map[string]any); ok {
			// Parse the nested data as ExportScenarioResponse
			dataBytes, _ := json.Marshal(dataMap)
			var exportData models.ExportScenarioResponse
			if err := json.Unmarshal(dataBytes, &exportData); err == nil {
				request = models.ImportScenarioRequest{
					Scenarios: exportData.Scenarios,
					Version:   exportData.Version,
				}
			} else {
				c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid export data format: " + err.Error(), "data": nil})
				return
			}
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid data field format", "data": nil})
			return
		}
	} else {
		// Try to parse as direct formats
		rawBytes, _ := json.Marshal(rawData)

		// Try as ExportScenarioResponse first
		var exportData models.ExportScenarioResponse
		if err := json.Unmarshal(rawBytes, &exportData); err == nil && len(exportData.Scenarios) > 0 {
			request = models.ImportScenarioRequest{
				Scenarios: exportData.Scenarios,
				Version:   exportData.Version,
			}
		} else {
			// Try as direct ImportScenarioRequest
			if err := json.Unmarshal(rawBytes, &request); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid import file format. Expected scenarios array.", "data": nil})
				return
			}
		}
	}

	// Set project and conflict_action from query params if not already set
	if request.Project == "" {
		if projectParam := c.Query("project"); projectParam != "" {
			request.Project = projectParam
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"message": "Project parameter is required", "data": nil})
			return
		}
	}

	if request.ConflictAction == "" {
		if actionParam := c.Query("conflict_action"); actionParam != "" {
			request.ConflictAction = actionParam
		} else {
			request.ConflictAction = "skip" // Default to skip
		}
	}

	// Validate conflict action
	validActions := map[string]bool{
		"skip":      true,
		"overwrite": true,
		"rename":    true,
	}
	if !validActions[request.ConflictAction] {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid conflict_action. Must be 'skip', 'overwrite', or 'rename'", "data": nil})
		return
	}

	// Store request in context for audit
	c.Set("_scenario_request", request)
	c.Set("_raw_data", rawData)

	h.handleRequest(c, func(ctx context.Context, _ models.ResourceClass, reqDetails models.RequestDetails) (any, error) {
		// Use project from request body
		reqDetails.Project = request.Project

		return h.Scenario.ImportScenarios(request, reqDetails)
	})
}
