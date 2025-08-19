package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/CloudNativeWorks/elchi-backend/controller/handlers"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// ClientOperationExceptions defines which type+subtype combinations are allowed for editors
var ClientOperationExceptions = map[string][]string{
	"SERVICE": {"SUB_LOGS"}, // SERVICE type with SUB_LOGS subtype - editors can access
}

// isClientOperationAllowedForEditor checks if a client operation is allowed for editors
func isClientOperationAllowedForEditor(c *gin.Context) bool {
	// Try to get the body from middleware cache first
	if originalBody, exists := c.Get("_original_body"); exists {
		if bodyBytes, ok := originalBody.([]byte); ok {
			return checkOperationFromBody(bodyBytes)
		}
	}

	// Fallback: read body directly
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return false
	}

	// Restore body for further processing
	c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	return checkOperationFromBody(bodyBytes)
}

// checkOperationFromBody parses operation body and checks against exceptions
func checkOperationFromBody(bodyBytes []byte) bool {
	var operation struct {
		Type    string `json:"type"`
		SubType string `json:"sub_type"`
		Command struct {
			Path string `json:"path"`
		} `json:"command"`
	}

	if err := json.Unmarshal(bodyBytes, &operation); err != nil {
		return false
	}

	// Special case for PROXY type - check path in command
	if operation.Type == "PROXY" {
		// For PROXY type, allow if path is "/logging"
		return operation.Command.Path == "/logging"
	}

	// Check if this type+subtype combination is allowed for editors
	if allowedSubTypes, exists := ClientOperationExceptions[operation.Type]; exists {
		// If allowedSubTypes is empty, allow all subtypes for this type
		if len(allowedSubTypes) == 0 {
			return true
		}
		// Otherwise check specific subtypes
		for _, allowedSubType := range allowedSubTypes {
			if operation.SubType == allowedSubType {
				return true
			}
		}
	}

	return false
}

func InitSettingMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		userDetails, _ := handlers.GetUserDetails(c)

		// Special case for OpenRouter token GET - allow editors to read
		if c.Request.URL.Path == "/api/v3/setting/openrouter-token" && c.Request.Method == "GET" {
			// Editors, Admins, and Owners can GET OpenRouter token
			if userDetails.Role == models.RoleEditor || userDetails.Role == models.RoleAdmin || userDetails.IsOwner {
				c.Next()
				return
			}
		}

		// Special case for client operations - check for editor exceptions
		if strings.HasPrefix(c.Request.URL.Path, "/api/op/clients") && c.Request.Method == "POST" {
			if userDetails.Role == models.RoleEditor {
				// Check if this operation is allowed for editors
				if isClientOperationAllowedForEditor(c) {
					c.Next()
					return
				}
			}
		}

		// For all other operations, only owners and admins
		if !userDetails.IsOwner && userDetails.Role != models.RoleAdmin {
			c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
			c.Abort()
			return
		}
		c.Next()
	}
}
