package handlers

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/ai"
	"github.com/gin-gonic/gin"
)

// GetAIUsageStats returns AI usage statistics for a project
func (h *Handler) GetAIUsageStats(c *gin.Context) {
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "project parameter is required",
		})
		return
	}

	// Create usage tracker
	usageTracker := ai.NewUsageTracker(h.Settings.Context)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get usage statistics
	stats, err := usageTracker.GetUsageStats(ctx, project)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to get usage stats",
			"details": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"stats":   stats,
	})
}

// GetRecentAIUsage returns recent AI usage records for a project
func (h *Handler) GetRecentAIUsage(c *gin.Context) {
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "project parameter is required",
		})
		return
	}

	// Parse limit parameter (default: 50, max: 200)
	limitStr := c.DefaultQuery("limit", "50")
	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	// Create usage tracker
	usageTracker := ai.NewUsageTracker(h.Settings.Context)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get recent usage records
	records, err := usageTracker.GetRecentUsage(ctx, project, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to get usage records",
			"details": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"records": records,
		"count":   len(records),
	})
}

// GetAIUsageStatus returns current AI service status with usage statistics
func (h *Handler) GetAIUsageStatus(c *gin.Context) {
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "project parameter is required",
		})
		return
	}

	// Check if OpenRouter token is configured
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	openRouterToken, err := h.getOpenRouterTokenFromSettings(project)
	tokenConfigured := err == nil && openRouterToken != ""

	// Get basic usage stats
	var stats *ai.AIUsageStats
	if tokenConfigured {
		usageTracker := ai.NewUsageTracker(h.Settings.Context)
		stats, _ = usageTracker.GetUsageStats(ctx, project)
	}

	status := gin.H{
		"success": true,
		"status": gin.H{
			"token_configured":    tokenConfigured,
			"service_available":   tokenConfigured,
			"supported_features": []string{"analyze", "analyze-logs"},
			"max_tokens_per_request": 4000,
		},
	}

	if stats != nil {
		status["usage_summary"] = gin.H{
			"total_requests":     stats.TotalRequests,
			"tokens_used_today":  stats.TokensToday,
			"tokens_used_month":  stats.TokensThisMonth,
			"last_used":         stats.LastUsed,
			"success_rate":      float64(stats.SuccessfulRequests) / float64(max(stats.TotalRequests, 1)) * 100,
		}
	}

	c.JSON(http.StatusOK, status)
}

// CleanupOldAIUsage cleans up old AI usage records (admin only)
func (h *Handler) CleanupOldAIUsage(c *gin.Context) {
	// Parse days parameter (default: 90 days)
	daysStr := c.DefaultQuery("days", "90")
	days, err := strconv.Atoi(daysStr)
	if err != nil || days <= 0 {
		days = 90
	}

	// Ensure minimum retention period (7 days)
	if days < 7 {
		days = 7
	}

	// Create usage tracker
	usageTracker := ai.NewUsageTracker(h.Settings.Context)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Cleanup old records
	err = usageTracker.CleanupOldRecords(ctx, days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to cleanup old records",
			"details": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Old usage records cleaned up successfully",
		"retention_days": days,
	})
}

// Helper function for max
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

