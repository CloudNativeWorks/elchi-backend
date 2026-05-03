package settings

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// GetLicenseStatus returns the current license snapshot for UI rendering.
// Available to any authenticated user; does NOT expose the raw key or API key.
// Adds current_clients (cluster-wide connected count) when a ClientCounter is
// wired so the UI can render "x/y clients" usage. A counting failure is logged
// but does not fail the response — the license card still renders without the
// usage figure.
func (handler *AppHandler) GetLicenseStatus(c *gin.Context) {
	if handler.License == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "license service not initialized"})
		return
	}
	status := handler.License.GetCachedStatus()
	if handler.ClientCounter != nil {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()
		if count, err := handler.ClientCounter.CountActiveClients(ctx); err != nil {
			handler.Logger.Warnf("license status: failed to count active clients: %v", err)
		} else {
			status.CurrentClients = &count
		}
	}
	c.JSON(http.StatusOK, gin.H{"license": status})
}

// ActivateLicense activates a license key against the CNW License Server.
// BOTH license_key and api_key are required in the request body — there is no
// separate "save api key only" endpoint. Admin/Owner only (enforced upstream).
func (handler *AppHandler) ActivateLicense(c *gin.Context) {
	if handler.License == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "license service not initialized"})
		return
	}
	var body struct {
		LicenseKey string `json:"license_key" binding:"required"`
		APIKey     string `json:"api_key" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "invalid request body: license_key and api_key are both required", "error": err.Error()})
		return
	}
	if err := handler.License.ActivateLicense(c.Request.Context(), body.LicenseKey, body.APIKey); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "license activation failed", "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "license activated",
		"license": handler.License.GetCachedStatus(),
	})
}

// DeleteLicense removes the activated license entirely (hard delete) and
// reverts the installation to the free plan. Admin/Owner only.
func (handler *AppHandler) DeleteLicense(c *gin.Context) {
	if handler.License == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "license service not initialized"})
		return
	}
	if err := handler.License.DeleteLicense(c.Request.Context()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "license delete failed", "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "license deleted; reverted to free plan",
		"license": handler.License.GetCachedStatus(),
	})
}

// ForceLicenseCheck triggers an immediate online re-validation, bypassing the
// per-cluster 24h dedup. The "Re-check now" button in the UI calls this.
// Admin/Owner only.
func (handler *AppHandler) ForceLicenseCheck(c *gin.Context) {
	if handler.License == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "license service not initialized"})
		return
	}
	if err := handler.License.ForceCheckLicense(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"message": "license re-check failed (cached state preserved)",
			"error":   err.Error(),
			"license": handler.License.GetCachedStatus(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "license re-checked", "license": handler.License.GetCachedStatus()})
}
