package middleware

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/CloudNativeWorks/elchi-backend/pkg/security"
)

// ValidateSearchInput validates search query parameters to prevent ReDoS attacks
func ValidateSearchInput() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Check search query parameter - use raw query to avoid Gin's auto-decoding
		search := c.Query("search")

		if search != "" {
			// Extract search param from raw query to preserve + characters
			rawQuery := c.Request.URL.RawQuery
			if strings.Contains(rawQuery, "search=") {
				parts := strings.Split(rawQuery, "search=")
				if len(parts) > 1 {
					searchPart := strings.Split(parts[1], "&")[0]
					if unescaped, err := url.QueryUnescape(searchPart); err == nil {
						search = unescaped
					}
				}
			}

			// Check for ReDoS patterns using comprehensive validation
			if !security.IsValidSearchInput(search) {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": "Invalid search query. Contains patterns that could cause performance issues.",
					"type":  "REDOS_PATTERN_DETECTED",
				})
				c.Abort()
				return
			}
		}

		// Check http_filter parameter in custom endpoints
		httpFilter := c.Query("http_filter")
		if httpFilter != "" {
			if !security.IsValidSearchInput(httpFilter) {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": "Invalid filter query. Contains suspicious patterns that could cause performance issues.",
					"type":  "REDOS_PATTERN_DETECTED",
				})
				c.Abort()
				return
			}
		}

		// Check name parameter in pagination (XDS list operations)
		name := c.Query("name")
		if name != "" {
			if !security.IsValidSearchInput(name) {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": "Invalid name filter. Contains patterns that could cause performance issues.",
					"type":  "REDOS_PATTERN_DETECTED",
				})
				c.Abort()
				return
			}
		}

		// Check version parameter in pagination
		version := c.Query("version")
		if version != "" && version != "latest" {
			if !security.IsValidSearchInput(version) {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": "Invalid version filter. Contains patterns that could cause performance issues.",
					"type":  "REDOS_PATTERN_DETECTED",
				})
				c.Abort()
				return
			}
		}

		c.Next()
	}
}

// ValidateResourceParams validates resource-related parameters
func ValidateResourceParams() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Validate collection parameter
		collection := c.Param("collection")
		if collection != "" && !isValidCollectionName(collection) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Invalid collection name",
			})
			c.Abort()
			return
		}

		// Validate name parameter
		name := c.Param("name")
		if name != "" && len(name) > 100 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Resource name too long (max 100 characters)",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

// isValidCollectionName validates MongoDB collection names
func isValidCollectionName(name string) bool {
	// MongoDB collection name restrictions
	if len(name) > 64 {
		return false
	}

	// Allowed collections in Elchi
	allowedCollections := map[string]bool{
		"clusters":      true,
		"listeners":     true,
		"endpoints":     true,
		"routes":        true,
		"virtual_hosts": true,
		"extensions":    true,
		"filters":       true,
		"scenarios":     true,
		"secrets":       true,
		"bootstrap":     true,
		"tls":           true,
		"envoys":        true,
	}

	return allowedCollections[name]
}
