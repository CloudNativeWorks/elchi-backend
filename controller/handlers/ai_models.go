package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/ai"
	"github.com/gin-gonic/gin"
)

// GetAvailableModels returns available AI models from OpenRouter
func (h *Handler) GetAvailableModels(c *gin.Context) {
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "project parameter is required",
		})
		return
	}

	// Get OpenRouter API key
	apiKey := c.GetHeader("x-openrouter-token")
	if apiKey == "" {
		settingsKey, err := h.getOpenRouterTokenFromSettings(project)
		if err == nil && settingsKey != "" {
			apiKey = settingsKey
		}
	}

	if apiKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "OpenRouter API key required",
		})
		return
	}

	// Create OpenRouter client
	client := ai.NewOpenRouterClient(apiKey)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get models from OpenRouter API
	models, err := client.GetModels(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to get models",
			"details": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"models":  models,
		"count":   len(models),
	})
}

// TestModelConnection tests connection to a specific model
func (h *Handler) TestModelConnection(c *gin.Context) {
	project := c.Query("project")
	modelID := c.Query("model_id")

	if project == "" || modelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "project and model_id parameters are required",
		})
		return
	}

	// Get OpenRouter API key
	apiKey := c.GetHeader("x-openrouter-token")
	if apiKey == "" {
		settingsKey, err := h.getOpenRouterTokenFromSettings(project)
		if err == nil && settingsKey != "" {
			apiKey = settingsKey
		}
	}

	if apiKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "OpenRouter API key required",
		})
		return
	}

	// Create OpenRouter client
	client := ai.NewOpenRouterClient(apiKey)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Test with specific model
	testReq := ai.OpenRouterRequest{
		Model:       modelID,
		MaxTokens:   10,
		Temperature: 0.1,
		Messages: []ai.OpenRouterMessage{
			{
				Role:    "user",
				Content: "Hello, this is a connection test. Please respond with 'OK'.",
			},
		},
	}

	start := time.Now()
	response, err := client.GetCompletion(ctx, testReq)
	duration := time.Since(start)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success":     false,
			"error":       "Connection test failed",
			"details":     err.Error(),
			"model_id":    modelID,
			"duration_ms": duration.Milliseconds(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success":     true,
		"message":     "Model connection successful",
		"model_id":    modelID,
		"duration_ms": duration.Milliseconds(),
		"response":    response.Choices[0].Message.Content,
		"token_usage": gin.H{
			"prompt_tokens":     response.Usage.PromptTokens,
			"completion_tokens": response.Usage.CompletionTokens,
			"total_tokens":      response.Usage.TotalTokens,
		},
	})
}
