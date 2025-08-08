package handlers

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/ai"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"log"
)

// AnalyzeResourceConfigWithAI herhangi bir resource'u AI ile analiz eder
func (h *Handler) AnalyzeResourceConfigWithAI(c *gin.Context) {
	var req ai.ConfigAnalyzerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "Invalid request format",
			"details": err.Error(),
		})
		return
	}

	// Request validasyonu
	if req.ResourceName == "" || req.Collection == "" || req.Project == "" || req.Version == "" || req.Question == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "resource_name, collection, project, version and question are required",
		})
		return
	}

	// Collection validasyonu
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

	// Default values
	if req.Depth == 0 {
		req.Depth = 3
	}
	// Dependencies varsayılan olarak dahil et
	if !req.IncludeDependencies {
		req.IncludeDependencies = true
	}

	// AI API key kontrolü - önce header'dan, sonra settings'den al
	aiAPIKey := c.GetHeader("x-claude-token")
	if aiAPIKey == "" {
		// Settings'den Claude token'ını al
		settingsAPIKey, err := h.getClaudeTokenFromSettings(req.Project)
		if err == nil && settingsAPIKey != "" {
			aiAPIKey = settingsAPIKey
		} else {
			// Son çare olarak environment variable'dan al
			aiAPIKey = os.Getenv("CLAUDE_API_KEY")
		}
	}
	
	if aiAPIKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "AI API key required. Set via x-claude-token header, project settings, or CLAUDE_API_KEY environment variable",
		})
		return
	}

	// AI client oluştur
	claudeClient := ai.NewClaudeClient(aiAPIKey)

	// Config analyzer oluştur
	configAnalyzer := ai.NewConfigAnalyzer(h.Settings.Context, claudeClient, h.Settings.Logger.Logger)

	// Analizi başlat
	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()

	log.Printf("Analyzing %s config: name=%s, project=%s, version=%s", req.Collection, req.ResourceName, req.Project, req.Version)

	analysisResult, err := configAnalyzer.AnalyzeResourceConfig(ctx, req)
	if err != nil {
		log.Printf("ERROR: Failed to analyze %s config: %v", req.Collection, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Config analysis failed",
			"details": err.Error(),
		})
		return
	}

	// Analiz sonuçlarını yanıtla
	c.JSON(http.StatusOK, gin.H{
		"success":           true,
		"analysis_result":   analysisResult,
		"processed_at":      analysisResult.ProcessedAt.Format(time.RFC3339),
		"message":          fmt.Sprintf("%s configuration analyzed successfully", strings.ToUpper(req.Collection)),
		"dependency_count":  len(analysisResult.Dependencies.Nodes),
		"resource_count":    len(analysisResult.RelatedResources),
	})
}


// GetAIStatus AI sistemi durumunu döndürür
func (h *Handler) GetAIStatus(c *gin.Context) {
	// AI API key kontrolü
	claudeKey := c.GetHeader("x-claude-token")
	if claudeKey == "" {
		claudeKey = os.Getenv("CLAUDE_API_KEY")
	}
	
	openaiKey := c.GetHeader("x-openai-token")
	if openaiKey == "" {
		openaiKey = os.Getenv("OPENAI_API_KEY")
	}
	
	status := map[string]any{
		"available": claudeKey != "" || openaiKey != "",
		"providers": map[string]bool{
			"claude": claudeKey != "",
			"openai": openaiKey != "",
		},
		"default_model": "claude-3-5-sonnet-20241022",
		"supported_features": []string{
			"config_generation",
			"resource_validation",
			"backend_api_integration",
			"concurrent_resource_creation",
		},
		"status": "online",
	}
	
	if !status["available"].(bool) {
		status["status"] = "no_api_key"
		status["message"] = "No AI API key configured. Set x-claude-token or x-openai-token header."
	}
	
	c.JSON(http.StatusOK, status)
}

// getClaudeTokenFromSettings project settings'den Claude token'ını alır
func (h *Handler) getClaudeTokenFromSettings(project string) (string, error) {
	ctx := context.Background()
	settingsCollection := h.Settings.Context.Client.Collection("settings")

	filter := bson.M{"project": project}
	
	var settings bson.M
	err := settingsCollection.FindOne(ctx, filter).Decode(&settings)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return "", fmt.Errorf("no settings found for project: %s", project)
		}
		return "", fmt.Errorf("could not get settings: %w", err)
	}

	if token, ok := settings["claude_token"].(string); ok && token != "" {
		return token, nil
	}

	return "", fmt.Errorf("no Claude token found in settings for project: %s", project)
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
	if req.ResourceName == "" || req.Collection == "" || req.Project == "" || req.Question == "" || req.Logs == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "resource_name, collection, project, question and logs are required",
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

	// Log line count validation (max 500 lines)
	logLines := strings.Split(strings.TrimSpace(req.Logs), "\n")
	if len(logLines) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "Log too large",
			"message": fmt.Sprintf("Provided log has %d lines. Maximum 500 lines allowed.", len(logLines)),
			"max_lines": 500,
			"current_lines": len(logLines),
		})
		return
	}

	// Default values
	if req.Depth == 0 {
		req.Depth = 3
	}
	// Include dependencies by default
	if !req.IncludeDependencies {
		req.IncludeDependencies = true
	}

	// AI API key check - first from header, then from settings
	aiAPIKey := c.GetHeader("x-claude-token")
	if aiAPIKey == "" {
		// Get Claude token from settings
		settingsAPIKey, err := h.getClaudeTokenFromSettings(req.Project)
		if err == nil && settingsAPIKey != "" {
			aiAPIKey = settingsAPIKey
		} else {
			// Last resort: environment variable
			aiAPIKey = os.Getenv("CLAUDE_API_KEY")
		}
	}
	
	if aiAPIKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "AI API key required. Set via x-claude-token header, project settings, or CLAUDE_API_KEY environment variable",
		})
		return
	}

	// Create AI client
	claudeClient := ai.NewClaudeClient(aiAPIKey)

	// Create config analyzer
	configAnalyzer := ai.NewConfigAnalyzer(h.Settings.Context, claudeClient, h.Settings.Logger.Logger)

	// Start log analysis
	ctx, cancel := context.WithTimeout(c.Request.Context(), 120*time.Second) // Longer timeout for log analysis
	defer cancel()

	log.Printf("Analyzing logs for %s config: name=%s, project=%s, log_lines=%d", req.Collection, req.ResourceName, req.Project, len(logLines))

	analysisResult, err := configAnalyzer.AnalyzeLogsWithConfig(ctx, req)
	if err != nil {
		log.Printf("ERROR: Failed to analyze logs with %s config: %v", req.Collection, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Log analysis failed",
			"details": err.Error(),
		})
		return
	}

	// Return analysis results
	c.JSON(http.StatusOK, gin.H{
		"success":           true,
		"analysis_result":   analysisResult,
		"processed_at":      analysisResult.ProcessedAt.Format(time.RFC3339),
		"message":          fmt.Sprintf("Logs analyzed with %s configuration successfully", strings.ToUpper(req.Collection)),
		"log_line_count":    analysisResult.LogLineCount,
		"errors_detected":   len(analysisResult.ErrorsDetected),
		"dependency_count":  len(analysisResult.Dependencies.Nodes),
		"resource_count":    len(analysisResult.RelatedResources),
	})
}


