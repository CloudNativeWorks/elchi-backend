package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/audit"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// AuditMiddleware creates audit entries for all non-GET requests
func AuditMiddleware(auditService *audit.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Skip GET requests and health checks
		if c.Request.Method == "GET" ||
			strings.Contains(c.Request.UserAgent(), "kube-probe") ||
			strings.Contains(c.Request.UserAgent(), "Envoy/HC") ||
			strings.Contains(c.Request.UserAgent(), "Consul Health Check") {
			c.Next()
			return
		}

		// Skip if this is a forwarded request (to prevent duplicate audits)
		if c.GetHeader("X-Forwarded-Request") == "true" {
			c.Next()
			return
		}

		// Create audit entry
		entry := &models.AuditEntry{
			RequestID: uuid.New().String(),
			Timestamp: time.Now(),
		}

		// Populate Request Context
		entry.ClientIP = c.ClientIP()
		entry.UserAgent = c.Request.UserAgent()
		entry.Method = c.Request.Method
		entry.Path = c.Request.URL.Path

		// Determine API Type
		if strings.HasPrefix(c.Request.URL.Path, "/api/op/clients") {
			entry.APIType = models.AuditAPITypeClientCommand
		} else {
			entry.APIType = models.AuditAPITypeREST
		}

		// Populate User Context from auth middleware
		if userID, exists := c.Get("user_id"); exists {
			if userIDStr, ok := userID.(string); ok {
				entry.UserID = userIDStr
			}
		}
		
		// Username - auth middleware sets this as *string from claims.Username
		if username, exists := c.Get("username"); exists {
			if usernamePtr, ok := username.(*string); ok && usernamePtr != nil {
				entry.Username = *usernamePtr
			} else if usernameStr, ok := username.(string); ok {
				entry.Username = usernameStr
			}
		}
		
		// Role can be either *models.Role or models.Role  
		if role, exists := c.Get("role"); exists {
			if rolePtr, ok := role.(*models.Role); ok && rolePtr != nil {
				entry.UserRole = string(*rolePtr)
			} else if roleVal, ok := role.(models.Role); ok {
				entry.UserRole = string(roleVal)
			}
		}
		
		// Groups can be either *[]string or []string
		if groups, exists := c.Get("groups"); exists {
			if groupsPtr, ok := groups.(*[]string); ok && groupsPtr != nil {
				entry.Groups = *groupsPtr
			} else if groupsSlice, ok := groups.([]string); ok {
				entry.Groups = groupsSlice
			}
		}


		// Capture command body for client commands
		if entry.APIType == models.AuditAPITypeClientCommand && (c.Request.Method == "POST" || c.Request.Method == "PUT") {
			if bodyBytes := captureBody(c); bodyBytes != nil {
				var command map[string]any
				if err := json.Unmarshal(bodyBytes, &command); err == nil {
					entry.Command = command
				}
			}
		}

		// Store in context
		audit.SetAuditEntry(c, entry)
		startTime := time.Now()

		// Process request
		c.Next()

		// After request completion
		entry.Duration = time.Since(startTime).Milliseconds()
		entry.ResponseStatus = c.Writer.Status()
		entry.Success = c.Writer.Status() < 400

		// Convert project ObjectID to project name if possible
		if entry.Project != "" && len(entry.Project) == 24 {
			// This looks like an ObjectID, try to get project name from context
			if projectName, exists := c.Get("project_name"); exists {
				if projNameStr, ok := projectName.(string); ok {
					entry.Project = projNameStr
				}
			}
		}

		// Get error if set
		if errorMsg, exists := c.Get("audit_error"); exists {
			entry.ErrorMessage = errorMsg.(string)
			entry.Success = false
		}

		// Store audit entry (only if action was set by handler)
		if entry.Action != "" {
			if err := auditService.Store(entry); err != nil {
				// Log error but don't fail the request
				c.Set("audit_store_error", err.Error())
			}
		}
	}
}

// captureBody captures and restores request body
func captureBody(c *gin.Context) []byte {
	// First check if body was already captured by BodyCaptureMiddleware
	if originalBody, exists := c.Get("_original_body"); exists {
		if bodyBytes, ok := originalBody.([]byte); ok {
			return bodyBytes
		}
	}

	// If not, capture it now
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil
	}

	// Restore body for further processing
	c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	return bodyBytes
}