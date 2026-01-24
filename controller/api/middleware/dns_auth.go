package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// DNSAuthMiddleware validates DNS secret from X-Elchi-Secret header
// Uses zone parameter from query string for zone-based authentication
func DNSAuthMiddleware(appContext *db.AppContext) gin.HandlerFunc {
	log := logger.NewLogger("middleware/dns-auth")

	return func(c *gin.Context) {
		// Get zone from query parameter
		zone := c.Query("zone")
		if zone == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "zone parameter is required",
			})
			c.Abort()
			return
		}

		// Get DNS secret from header
		secret := c.GetHeader("X-Elchi-Secret")
		if secret == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "missing X-Elchi-Secret header",
			})
			c.Abort()
			return
		}

		// Validate DNS secret against GSLBConfig for this zone
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var settings models.Settings
		collection := appContext.Client.Collection("settings")

		// Find ANY settings document where GSLBConfig.Zone matches
		if err := collection.FindOne(ctx, bson.M{"gslb_config.zone": zone}).Decode(&settings); err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				log.Warnf("DNS auth failed: zone '%s' not found or GSLB not configured", zone)
				c.JSON(http.StatusUnauthorized, gin.H{
					"error": "zone not found or GSLB not configured",
				})
				c.Abort()
				return
			}
			log.Errorf("DNS auth error: failed to get zone settings: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": fmt.Sprintf("failed to get zone settings: %v", err),
			})
			c.Abort()
			return
		}

		// Check if GSLB is configured
		if settings.GSLBConfig == nil {
			log.Warnf("DNS auth failed: GSLB not configured for zone '%s'", zone)
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "GSLB not configured for this zone",
			})
			c.Abort()
			return
		}

		// Validate secret
		if secret != settings.GSLBConfig.DNSSecret {
			log.Warnf("DNS auth failed: invalid DNS secret for zone '%s'", zone)
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "invalid DNS secret",
			})
			c.Abort()
			return
		}

		// Authentication successful - continue to handler
		c.Next()
	}
}
