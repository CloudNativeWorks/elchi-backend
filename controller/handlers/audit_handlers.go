package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/audit"
	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/gin-gonic/gin"
)

// ================== AUDIT API HANDLERS ==================

// AuditErrorResponse represents a standardized error response for audit operations
type AuditErrorResponse struct {
	Message   string `json:"message"`
	Operation string `json:"operation,omitempty"`
	Context   string `json:"context,omitempty"`
}

// respondWithAuditError sends a standardized audit error response
func respondWithAuditError(c *gin.Context, statusCode int, operation, message string, err error) {
	response := AuditErrorResponse{
		Message: message,
	}

	// Add operation context if this is an AuditError
	var auditErr *audit.AuditError
	if errors.As(err, &auditErr) {
		response.Operation = auditErr.Operation
		response.Context = auditErr.Context
	} else if operation != "" {
		response.Operation = operation
	}

	c.JSON(statusCode, response)
}

// handleAuditRequest validates project access for audit operations
func (h *Handler) handleAuditRequest(c *gin.Context) (models.RequestDetails, models.UserDetails, bool) {
	requestDetails, userDetails := h.getRequestDetails(c)

	// Check basic role permissions
	if err := checkRole(c, userDetails); err != nil {
		respondWithAuditError(c, http.StatusBadRequest, "authorization", "Insufficient permissions for audit access", err)
		return requestDetails, userDetails, false
	}

	// Validate project access if project is specified
	if requestDetails.Project != "" {
		if db := h.getDatabaseConnection(); db != nil {
			if err := authorization.ValidateRequestProject(c.Request.Context(), db, userDetails, requestDetails.Project); err != nil {
				respondWithAuditError(c, http.StatusForbidden, "project_validation", "Access denied to this project", err)
				return requestDetails, userDetails, false
			}
		}
	}

	return requestDetails, userDetails, true
}

// GetAuditLogs retrieves audit logs with pagination and filtering
func (h *Handler) GetAuditLogs(c *gin.Context) {
	ctx := c.Request.Context()

	// Validate project access and get request details
	requestDetails, _, ok := h.handleAuditRequest(c)
	if !ok {
		return // Error already handled
	}

	// Parse query parameters
	query := audit.AuditQuery{
		Project: requestDetails.Project, // Always filter by authorized project
	}

	// Basic filters
	query.UserID = c.Query("user_id")
	query.Username = c.Query("username")
	query.Action = c.Query("action")
	query.ResourceType = c.Query("resource_type")

	// Success filter
	if successStr := c.Query("success"); successStr != "" {
		if success, err := strconv.ParseBool(successStr); err == nil {
			query.Success = &success
		}
	}

	// Pagination
	if limitStr := c.Query("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 {
			query.Limit = limit
		}
	}
	if skipStr := c.Query("skip"); skipStr != "" {
		if skip, err := strconv.Atoi(skipStr); err == nil && skip >= 0 {
			query.Skip = skip
		}
	}

	// Time range filters
	if startTimeStr := c.Query("start_time"); startTimeStr != "" {
		if startTime, err := time.Parse(time.RFC3339, startTimeStr); err == nil {
			query.StartTime = startTime
		}
	}
	if endTimeStr := c.Query("end_time"); endTimeStr != "" {
		if endTime, err := time.Parse(time.RFC3339, endTimeStr); err == nil {
			query.EndTime = endTime
		}
	}

	// Default pagination
	if query.Limit == 0 {
		query.Limit = 50 // Default page size
	}
	if query.Limit > 1000 {
		query.Limit = 1000 // Max page size
	}

	// Query audit logs
	entries, err := h.AuditService.Query(ctx, query)
	if err != nil {
		respondWithAuditError(c, http.StatusInternalServerError, "query_logs", "Failed to retrieve audit logs", err)
		return
	}

	// Get total count for pagination metadata
	totalCount, _ := h.AuditService.Count(ctx, query)

	// Response with pagination metadata
	c.JSON(http.StatusOK, gin.H{
		"message": "Audit logs retrieved successfully",
		"data":    entries,
		"pagination": gin.H{
			"total":    totalCount,
			"limit":    query.Limit,
			"skip":     query.Skip,
			"count":    len(entries),
			"has_more": int64(query.Skip+len(entries)) < totalCount,
		},
	})
}

// GetAuditStats retrieves audit statistics for dashboard
func (h *Handler) GetAuditStats(c *gin.Context) {
	ctx := c.Request.Context()

	// Validate project access and get request details
	requestDetails, _, ok := h.handleAuditRequest(c)
	if !ok {
		return // Error already handled
	}

	// Parse query parameters (same as GetAuditLogs for consistency)
	query := audit.AuditQuery{
		Project: requestDetails.Project, // Always filter by authorized project
	}

	// Basic filters (same as audit logs list)
	query.UserID = c.Query("user_id")
	query.Username = c.Query("username")
	query.Action = c.Query("action")
	query.ResourceType = c.Query("resource_type")

	// Success filter
	if successStr := c.Query("success"); successStr != "" {
		if success, err := strconv.ParseBool(successStr); err == nil {
			query.Success = &success
		}
	}

	// Time range filters
	if startTimeStr := c.Query("start_time"); startTimeStr != "" {
		if startTime, err := time.Parse(time.RFC3339, startTimeStr); err == nil {
			query.StartTime = startTime
		}
	}

	if endTimeStr := c.Query("end_time"); endTimeStr != "" {
		if endTime, err := time.Parse(time.RFC3339, endTimeStr); err == nil {
			query.EndTime = endTime
		}
	}

	// Get statistics
	stats, err := h.AuditService.GetStats(ctx, query)
	if err != nil {
		respondWithAuditError(c, http.StatusInternalServerError, "query_stats", "Failed to retrieve audit statistics", err)
		return
	}

	// Build response filters - show all applied filters
	filters := gin.H{}
	if query.UserID != "" {
		filters["user_id"] = query.UserID
	}
	if query.Username != "" {
		filters["username"] = query.Username
	}
	if query.Action != "" {
		filters["action"] = query.Action
	}
	if query.ResourceType != "" {
		filters["resource_type"] = query.ResourceType
	}
	if query.Success != nil {
		filters["success"] = *query.Success
	}
	if !query.StartTime.IsZero() {
		filters["start_time"] = query.StartTime.Format(time.RFC3339)
	}
	if !query.EndTime.IsZero() {
		filters["end_time"] = query.EndTime.Format(time.RFC3339)
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Audit statistics retrieved successfully",
		"data":    stats,
		"filters": filters,
	})
}
