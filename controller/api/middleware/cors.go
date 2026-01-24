package middleware

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
)

var allowedOrigins string = "*" // Default value, will be updated from config

// SetCORSConfig sets the CORS configuration from app config
func SetCORSConfig(corsAllowedOrigins string) {
	if corsAllowedOrigins != "" {
		allowedOrigins = corsAllowedOrigins
	}
}

// CORS Allow with configurable origins
func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")

		// Set appropriate Access-Control-Allow-Origin header
		if allowedOrigins == "*" {
			// Allow all origins
			c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		} else if origin != "" && isOriginAllowed(origin, allowedOrigins) {
			// Allow specific origin
			c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
		}
		// Origin not allowed: no CORS headers set (continues processing for non-browser requests)

		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With, from-elchi, envoy-version, x-openrouter-token")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

// isOriginAllowed checks if the origin is allowed based on configuration
func isOriginAllowed(origin, allowedOrigins string) bool {
	if allowedOrigins == "*" {
		return true
	}

	// Split comma-separated origins
	origins := strings.Split(allowedOrigins, ",")
	for _, allowed := range origins {
		allowed = strings.TrimSpace(allowed)

		// Exact match
		if origin == allowed {
			return true
		}

		// Wildcard subdomain matching (e.g., "*.example.com")
		if strings.HasPrefix(allowed, "*.") {
			domain := allowed[2:] // Remove "*."
			parsedOrigin, err := url.Parse(origin)
			if err != nil {
				continue
			}
			if strings.HasSuffix(parsedOrigin.Host, "."+domain) || parsedOrigin.Host == domain {
				return true
			}
		}

		// Regex pattern matching (e.g., "^https://.*\\.company\\.com$")
		if strings.HasPrefix(allowed, "^") && strings.HasSuffix(allowed, "$") {
			if matched, _ := regexp.MatchString(allowed, origin); matched {
				return true
			}
		}
	}

	return false
}
