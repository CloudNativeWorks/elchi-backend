package middleware

import (
	"github.com/CloudNativeWorks/elchi-backend/controller/handlers"
	"github.com/gin-gonic/gin"
)

// OwnerOnlyMiddleware restricts access to Owner-only operations (Project Management)
func OwnerOnlyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		userDetails, _ := handlers.GetUserDetails(c)

		// Only owners can perform project management operations
		if !userDetails.IsOwner {
			RespondWithAuthError(c, "INSUFFICIENT_PERMISSION", "Only system owners can manage projects")
			return
		}
		
		c.Next()
	}
}