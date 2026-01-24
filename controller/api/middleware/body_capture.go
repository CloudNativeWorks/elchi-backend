package middleware

import (
	"bytes"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// BodyCaptureMiddleware captures request body for later use
// This solves the issue where request body can only be read once in Gin
func BodyCaptureMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Only capture body for POST/PUT methods
		if c.Request.Method == http.MethodPost || c.Request.Method == http.MethodPut {
			// Read body
			bodyBytes, err := io.ReadAll(c.Request.Body)
			if err != nil {
				c.AbortWithStatus(400)
				return
			}

			// Save original body to context for later use
			c.Set("_original_body", bodyBytes)

			// Restore body for Gin's normal processing
			c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		}

		c.Next()
	}
}
