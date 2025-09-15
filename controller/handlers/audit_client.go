package handlers

import (
	"encoding/json"

	"github.com/CloudNativeWorks/elchi-backend/pkg/audit"
	"github.com/gin-gonic/gin"
)

// ================== CLIENT COMMAND AUDIT HANDLERS ==================
//
// This file contains audit functionality for Client Command operations:
// - Client command parsing and audit context setup
// - BGP, FRR, Network, Service command extraction
// - Client delete audit handling
//

// extractBGPOperationFromRequest extracts BGP operation name from request body for audit filtering
func (h *Handler) extractBGPOperationFromRequest(c *gin.Context) string {
	// Get cached body from middleware
	originalBody, exists := c.Get("_original_body")
	if !exists {
		return ""
	}

	bodyBytes, ok := originalBody.([]byte)
	if !ok {
		return ""
	}

	// Parse request body to get BGP operation
	var requestBody map[string]any
	if err := json.Unmarshal(bodyBytes, &requestBody); err != nil {
		return ""
	}

	// Navigate to BGP operation: command.bgp.operation
	if command, ok := requestBody["command"].(map[string]any); ok {
		if bgp, ok := command["bgp"].(map[string]any); ok {
			if operation, ok := bgp["operation"].(string); ok {
				return operation
			}
		}
	}

	return ""
}

// extractEnvoyVersionOperationFromRequest extracts Envoy Version operation name from request body for audit filtering
func (h *Handler) extractEnvoyVersionOperationFromRequest(c *gin.Context) string {
	// Get cached body from middleware
	originalBody, exists := c.Get("_original_body")
	if !exists {
		return ""
	}

	bodyBytes, ok := originalBody.([]byte)
	if !ok {
		return ""
	}

	// Parse request body to get Envoy Version operation
	var requestBody map[string]any
	if err := json.Unmarshal(bodyBytes, &requestBody); err != nil {
		return ""
	}

	// Navigate to Envoy Version operation: envoy_version.operation
	if envoyVersion, ok := requestBody["envoy_version"].(map[string]any); ok {
		if operation, ok := envoyVersion["operation"].(string); ok {
			return operation
		}
		// Also try numeric operation values
		if operationNum, ok := envoyVersion["operation"].(float64); ok {
			switch int(operationNum) {
			case 0:
				return "GET_VERSIONS"
			case 1:
				return "SET_VERSION"
			}
		}
	}

	return ""
}

// ================== CLIENT COMMAND AUDIT HANDLERS ==================

// setClientDeleteAuditContext handles audit context for client DELETE operations
func (h *Handler) setClientDeleteAuditContext(c *gin.Context) {
	// Extract client ID from URL path: /api/op/clients/{client_id}
	clientID := c.Param("client_id")
	if clientID == "" {
		return
	}

	// Get project from query parameter
	project := c.Query("project")

	// Set audit action and resource
	audit.SetAuditAction(c, "CLIENT_DELETE")
	audit.SetAuditResource(c, "client", clientID, clientID, project)
	// Don't set command details for DELETE - it's not a command operation
}

// hasProxyQueriesInRequest checks if PROXY command is a config change operation (has queries indicating modification)
func (h *Handler) hasProxyQueriesInRequest(c *gin.Context) bool {
	// Get cached body from middleware
	originalBody, exists := c.Get("_original_body")
	if !exists {
		return false
	}

	bodyBytes, ok := originalBody.([]byte)
	if !ok {
		return false
	}

	// Parse request body to check for queries field
	var requestBody map[string]any
	if err := json.Unmarshal(bodyBytes, &requestBody); err != nil {
		return false
	}

	// Check if command.queries exists and is not empty (indicates config modification)
	if command, ok := requestBody["command"].(map[string]any); ok {
		if queries, ok := command["queries"].(map[string]any); ok {
			// Only audit if queries exist and contain actual modification parameters
			// Skip read-only queries like {"format": "json"}
			for key := range queries {
				if key != "format" && key != "include_hidden" && key != "filter" {
					// This is a modification query (like "level" for logging)
					return true
				}
			}
		}
	}

	return false // Read-only operations without modification queries should not be audited
}
