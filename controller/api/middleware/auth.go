package middleware

import (
	"net/http"
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/controller/api/auth"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"

	"github.com/gin-gonic/gin"
)

func Authentication() gin.HandlerFunc {
	return func(c *gin.Context) {
		isOwner := false
		clientToken := c.Request.Header.Get("token")

		if clientToken == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "No Authorization header provided"})
			c.Abort()
			return
		}

		claims, err := auth.ValidateToken(clientToken)
		if err != "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": err})
			c.Abort()
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
		tokenString := c.GetHeader("refresh-token")
		if tokenString == "" {
			c.JSON(http.StatusUnauthorized, "Refresh token required")
			c.Abort()
			return
		}

		claims, err := auth.ValidateRefreshToken(tokenString)
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
