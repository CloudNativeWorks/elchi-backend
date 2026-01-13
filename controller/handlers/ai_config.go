package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/ai"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

func (h *Handler) AnalyzeResourceConfigWithAI(c *gin.Context) {
	var req ai.ConfigAnalyzerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "Invalid request format",
			"details": err.Error(),
		})
		return
	}

	// Request validation
	if req.ResourceName == "" || req.Collection == "" || req.Project == "" || req.Version == "" || req.Question == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "resource_name, collection, project, version and question are required",
		})
		return
	}

	// Collection validation
	validCollections := []string{"listeners", "clusters", "routes", "endpoints", "virtual_hosts", "filters", "extensions", "secrets", "tls"}
	isValid := false
	for _, valid := range validCollections {
		if req.Collection == valid {
			isValid = true
			break
		}
	}
	if !isValid {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "invalid collection. Valid collections: " + strings.Join(validCollections, ", "),
		})
		return
	}

	// Default value for IncludeDependencies
	if !req.IncludeDependencies {
		req.IncludeDependencies = true
	}

	// Setup AI analyzer using helper function
	configAnalyzer, ctx, cancel, err := h.setupAIAnalyzer(c, req.Project, req.Depth, req.IncludeDependencies)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defer cancel()

	// Get user ID from context (from JWT middleware)
	userID, exists := c.Get("user_id")
	if !exists {
		userID = "unknown" // fallback
	}

	analysisResult, err := configAnalyzer.AnalyzeResourceConfig(ctx, req, userID.(string))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Config analysis failed",
			"details": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success":          true,
		"analysis_result":  analysisResult,
		"processed_at":     analysisResult.ProcessedAt.Format(time.RFC3339),
		"message":          fmt.Sprintf("%s configuration analyzed successfully", strings.ToUpper(req.Collection)),
		"dependency_count": len(analysisResult.Dependencies.Nodes),
		"resource_count":   len(analysisResult.RelatedResources),
		"token_usage": gin.H{
			"input_tokens":  analysisResult.TokenUsage.InputTokens,
			"output_tokens": analysisResult.TokenUsage.OutputTokens,
			"total_tokens":  analysisResult.TokenUsage.TotalTokens,
			"cost_usd":      analysisResult.TokenUsage.CostUSD,
		},
	})
}

func (h *Handler) GetAIStatus(c *gin.Context) {
	project := c.Query("project")

	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "project parameter is required",
		})
		return
	}

	openRouterKey := c.GetHeader("x-openrouter-token")
	if openRouterKey == "" {
		settingsKey, err := h.getOpenRouterTokenFromSettings(project)
		if err == nil && settingsKey != "" {
			openRouterKey = settingsKey
		} else {
			openRouterKey = os.Getenv("OPENROUTER_API_KEY")
		}
	}

	// Default model'ı al
	defaultModel, _ := h.getAIModelFromSettings(project)

	status := map[string]any{
		"available": openRouterKey != "",
		"providers": map[string]bool{
			"openrouter": openRouterKey != "",
		},
		"default_model": defaultModel,
		"supported_features": []string{
			"analyze",
			"analyze-logs",
		},
		"status":  "online",
		"project": project,
	}

	if !status["available"].(bool) {
		status["status"] = "no_api_key"
		status["message"] = "No OpenRouter API key configured. Set via x-openrouter-token header, project settings, or OPENROUTER_API_KEY environment variable"
	}

	c.JSON(http.StatusOK, status)
}

// getOpenRouterTokenFromSettings project settings'den OpenRouter token'ını alır
func (h *Handler) getOpenRouterTokenFromSettings(project string) (string, error) {
	ctx := context.Background()
	settingsCollection := h.Settings.Context.Client.Collection("settings")

	filter := bson.M{"project": project}

	var settings bson.M
	err := settingsCollection.FindOne(ctx, filter).Decode(&settings)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return "", fmt.Errorf("no settings found for project: %s", project)
		}
		return "", fmt.Errorf("could not get settings: %w", err)
	}

	if token, ok := settings["openrouter_token"].(string); ok && token != "" {
		return token, nil
	}

	return "", fmt.Errorf("no OpenRouter token found in settings for project: %s", project)
}

// getAIModelFromSettings project settings'den AI model'ını alır
func (h *Handler) getAIModelFromSettings(project string) (string, error) {
	ctx := context.Background()
	settingsCollection := h.Settings.Context.Client.Collection("settings")

	filter := bson.M{"project": project}

	var settings bson.M
	err := settingsCollection.FindOne(ctx, filter).Decode(&settings)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return ai.DefaultModel, nil // Return default model if no settings
		}
		return ai.DefaultModel, nil
	}

	if model, ok := settings["ai_default_model"].(string); ok && model != "" {
		return model, nil
	}

	return ai.DefaultModel, nil // Return default model if not set
}

// AnalyzeLogsWithConfig analyzes Envoy logs with configuration context using AI
func (h *Handler) AnalyzeLogsWithConfig(c *gin.Context) {
	var req ai.LogAnalyzerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "Invalid request format",
			"details": err.Error(),
		})
		return
	}

	// Request validation
	if req.ResourceName == "" || req.Collection == "" || req.Project == "" || req.Logs == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "resource_name, collection, project and logs are required",
		})
		return
	}

	// If question is empty, set default question for general log analysis
	if req.Question == "" {
		req.Question = "Analyze these logs and identify any issues, errors, or important information."
	}

	// Collection validation
	validCollections := []string{"listeners", "clusters", "routes", "endpoints", "virtual_hosts", "filters", "extensions", "secrets", "tls"}
	isValid := false
	for _, valid := range validCollections {
		if req.Collection == valid {
			isValid = true
			break
		}
	}
	if !isValid {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "invalid collection. Valid collections: " + strings.Join(validCollections, ", "),
		})
		return
	}

	// Log line count validation (max 500 lines)
	logLines := strings.Split(strings.TrimSpace(req.Logs), "\n")
	if len(logLines) > 999 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":         "Log too large",
			"message":       fmt.Sprintf("Provided log has %d lines. Maximum 999 lines allowed.", len(logLines)),
			"max_lines":     999,
			"current_lines": len(logLines),
		})
		return
	}

	// Default value for IncludeDependencies
	if !req.IncludeDependencies {
		req.IncludeDependencies = true
	}

	// Setup AI analyzer using helper function
	configAnalyzer, ctx, cancel, err := h.setupAIAnalyzer(c, req.Project, req.Depth, req.IncludeDependencies)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defer cancel()

	// Get user ID from context (from JWT middleware)
	userID, exists := c.Get("user_id")
	if !exists {
		userID = "unknown" // fallback
	}

	analysisResult, err := configAnalyzer.AnalyzeLogsWithConfig(ctx, req, userID.(string))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Log analysis failed",
			"details": err.Error(),
		})
		return
	}

	// Return analysis results
	c.JSON(http.StatusOK, gin.H{
		"success":          true,
		"analysis_result":  analysisResult,
		"processed_at":     analysisResult.ProcessedAt.Format(time.RFC3339),
		"message":          fmt.Sprintf("Logs analyzed with %s configuration successfully", strings.ToUpper(req.Collection)),
		"log_line_count":   analysisResult.LogLineCount,
		"errors_detected":  len(analysisResult.ErrorsDetected),
		"dependency_count": len(analysisResult.Dependencies.Nodes),
		"resource_count":   len(analysisResult.RelatedResources),
		"token_usage": gin.H{
			"input_tokens":  analysisResult.TokenUsage.InputTokens,
			"output_tokens": analysisResult.TokenUsage.OutputTokens,
			"total_tokens":  analysisResult.TokenUsage.TotalTokens,
			"cost_usd":      analysisResult.TokenUsage.CostUSD,
		},
	})
}

// setupAIAnalyzer is a helper function to set up AI analyzer with common logic
// Returns analyzer, context, cancel function, and error
func (h *Handler) setupAIAnalyzer(c *gin.Context, project string, depth int, _ bool) (*ai.ConfigAnalyzer, context.Context, context.CancelFunc, error) {
	// Set default values
	if depth == 0 {
		depth = 3
	}

	// AI API key check - first from header, then from settings, finally from env
	aiAPIKey := c.GetHeader("x-openrouter-token")
	if aiAPIKey == "" {
		settingsAPIKey, err := h.getOpenRouterTokenFromSettings(project)
		if err == nil && settingsAPIKey != "" {
			aiAPIKey = settingsAPIKey
		} else {
			aiAPIKey = os.Getenv("OPENROUTER_API_KEY")
		}
	}

	if aiAPIKey == "" {
		return nil, nil, nil, fmt.Errorf("AI API key required. Set via x-openrouter-token header, project settings, or OPENROUTER_API_KEY environment variable")
	}

	// Get default model
	defaultModel, _ := h.getAIModelFromSettings(project)

	// Create AI client and analyzer
	openRouterClient := ai.NewOpenRouterClient(aiAPIKey)
	configAnalyzer := ai.NewConfigAnalyzer(h.Settings.Context, openRouterClient, defaultModel, h.Settings.Logger)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)

	return configAnalyzer, ctx, cancel, nil
}
