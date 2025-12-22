package handlers

import (
	"net/http"

	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/gin-gonic/gin"
)

// CAProvidersHandler handles CA provider related endpoints
type CAProvidersHandler struct {
	config *config.AppConfig
}

// NewCAProvidersHandler creates a new CA providers handler
func NewCAProvidersHandler(cfg *config.AppConfig) *CAProvidersHandler {
	return &CAProvidersHandler{
		config: cfg,
	}
}

// ListSupportedProviders lists all supported CA providers
// GET /api/v3/acme/ca-providers
func (h *CAProvidersHandler) ListSupportedProviders(c *gin.Context) {
	var providers []map[string]any

	for providerKey, providerConfig := range h.config.CAProviders {
		// Extract environment names
		environments := make([]string, 0, len(providerConfig.Environments))
		for envName := range providerConfig.Environments {
			environments = append(environments, envName)
		}

		provider := map[string]any{
			"provider":    providerKey,
			"name":        providerConfig.Name,
			"description": providerConfig.Description,
			"supported":   providerConfig.Supported,
			"requires_eab": providerConfig.RequiresEAB,
			"environments": environments,
		}

		// Add EAB instructions URL if available
		if providerConfig.EABInstructionsURL != "" {
			provider["eab_instructions_url"] = providerConfig.EABInstructionsURL
		}

		providers = append(providers, provider)
	}

	c.JSON(http.StatusOK, gin.H{
		"data": providers,
	})
}

// ValidateEABCredentials validates EAB credentials for a specific CA provider
// POST /api/v3/acme/ca-providers/:provider/validate-eab
func (h *CAProvidersHandler) ValidateEABCredentials(c *gin.Context) {
	provider := c.Param("provider")

	var req struct {
		Email       string `json:"email" binding:"required"`
		Environment string `json:"environment" binding:"required"`
		EAB         struct {
			KeyID   string `json:"key_id" binding:"required"`
			HMACKey string `json:"hmac_key" binding:"required"`
		} `json:"eab" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid request body",
			"details": err.Error(),
		})
		return
	}

	// Validate provider exists
	providerConfig, exists := h.config.CAProviders[provider]
	if !exists {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Unknown CA provider: " + provider,
		})
		return
	}

	// Check if provider is supported
	if !providerConfig.Supported {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "CA provider " + provider + " is not yet supported",
		})
		return
	}

	// Check if EAB is required
	if !providerConfig.RequiresEAB {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "CA provider " + provider + " does not require EAB credentials",
		})
		return
	}

	// Validate environment
	_, envExists := providerConfig.Environments[req.Environment]
	if !envExists {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Environment " + req.Environment + " not supported for " + provider,
		})
		return
	}

	// TODO: Actual EAB validation would require creating a test ACME client
	// For now, we just validate the format and return success
	// In production, this would attempt registration with the CA

	c.JSON(http.StatusOK, gin.H{
		"message": "EAB credentials format validated successfully",
		"data": gin.H{
			"provider":    provider,
			"environment": req.Environment,
			"validation_note": "Full validation requires test registration with CA",
		},
	})
}
