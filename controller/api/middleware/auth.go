package middleware

import (
	"net/http"
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/controller/api/settings"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"

	"github.com/gin-gonic/gin"
)

// RespondWithAuthError provides consistent authentication/authorization error responses
func RespondWithAuthError(c *gin.Context, errorType string, message string) {
	var statusCode int
	var action string

	switch errorType {
	case "TOKEN_EXPIRED":
		statusCode = http.StatusUnauthorized // 401 - Frontend should refresh
		action = "REFRESH_TOKEN"
	case "TOKEN_INVALID", "TOKEN_ERROR":
		statusCode = http.StatusUnprocessableEntity // 422 - Frontend should redirect to login
		action = "REDIRECT_TO_LOGIN"
	case "INSUFFICIENT_PERMISSION", "ACCESS_DENIED":
		statusCode = http.StatusForbidden // 403 - Frontend should show access denied
		action = "SHOW_ACCESS_DENIED"
	default:
		statusCode = http.StatusUnprocessableEntity // 422
		action = "REDIRECT_TO_LOGIN"
	}

	c.JSON(statusCode, gin.H{
		"error":      message,
		"error_type": errorType,
		"action":     action,
	})
	c.Abort()
}

func Authentication() gin.HandlerFunc {
	return func(c *gin.Context) {
		isOwner := false

		// Require standard Authorization: Bearer <token> header
		authHeader := c.Request.Header.Get("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header required"})
			c.Abort()
			return
		}

		// Extract Bearer token
		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid Authorization format. Use 'Bearer <token>'"})
			c.Abort()
			return
		}

		clientToken := parts[1]
		if clientToken == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Token is required"})
			c.Abort()
			return
		}

		claims, err := settings.ValidateToken(clientToken)
		if err != "" {
			// Determine appropriate error type for proper frontend handling
			var errorType string

			switch {
			case strings.Contains(err, "token is expired"):
				errorType = "TOKEN_EXPIRED"
			case strings.Contains(err, "invalid") || strings.Contains(err, "malformed"):
				errorType = "TOKEN_INVALID"
			default:
				errorType = "TOKEN_ERROR"
			}

			RespondWithAuthError(c, errorType, err)
			return
		}

		c.Set("email", claims.Email)
		c.Set("username", claims.Username)
		c.Set("user_id", claims.UserID)
		c.Set("groups", claims.Groups)
		c.Set("projects", claims.Projects)
		c.Set("role", claims.Role)
		c.Set("user_name", claims.Username)
		c.Set("base_group", func() string {
			if claims.BaseGroup != nil {
				return *claims.BaseGroup
			}
			return ""
		}())

		if claims.Role != nil && *claims.Role == models.RoleOwner {
			isOwner = true
		}
		c.Set("isOwner", isOwner)
		c.Next()
	}
}

func Refresh() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Require standard Authorization: Bearer <refresh-token> header
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header required for refresh"})
			c.Abort()
			return
		}

		// Extract Bearer token
		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid Authorization format. Use 'Bearer <refresh-token>'"})
			c.Abort()
			return
		}

		tokenString := parts[1]
		if tokenString == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Refresh token is required"})
			c.Abort()
			return
		}

		claims, err := settings.ValidateRefreshToken(tokenString)
		if err != nil {
			c.JSON(http.StatusUnauthorized, err.Error())
			c.Abort()
			return
		}

		c.Set("user_id", claims.UserID)
		c.Set("username", claims.Username)
		c.Set("email", claims.Email)
		c.Set("groups", claims.Groups)
		c.Set("projects", claims.Projects)
		c.Set("role", claims.Role)
		c.Set("user_name", claims.Username)

		c.Next()
	}
}

// PathCheck Path Allow.
func PathCheck() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		pathParts := strings.Split(path, "/")
		for _, allowedPath := range AllowedEndpoints {
			allowedParts := strings.Split(allowedPath, "/")
			if len(pathParts) != len(allowedParts) {
				continue
			}

			matched := true
			for i := range pathParts {
				if allowedParts[i] != pathParts[i] && !strings.HasPrefix(allowedParts[i], ":") {
					matched = false
					break
				}
			}

			if matched {
				c.Next()
				return
			}
		}
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"message": "Invalid path"})
	}
}
