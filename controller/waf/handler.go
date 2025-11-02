package waf

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// WAFHandler handles WAF-related HTTP endpoints
type WAFHandler struct {
	service *WAFService
}

// NewWAFHandler creates a new WAF handler
func NewWAFHandler() *WAFHandler {
	return &WAFHandler{
		service: NewWAFService(),
	}
}

// GetCRSRules returns CRS rules for a specific version
// GET /api/v3/waf/crs?crs_version=4.14.0&severity=CRITICAL&paranoia_level=1&rule_type=blocking&tags=sqli,xss&search=xss
func (h *WAFHandler) GetCRSRules(c *gin.Context) {
	// Get crs_version parameter (required)
	crsVersion := c.Query("crs_version")
	if crsVersion == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "crs_version parameter is required"})
		return
	}

	// Parse optional filters
	filters := h.parseFilters(c)

	// Load rules from JSON file
	rules, metadata, err := h.service.LoadCRSRules(crsVersion)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": fmt.Sprintf("CRS rules for version %s not found", crsVersion),
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Apply filters
	filteredRules := h.service.FilterRules(rules, filters)

	response := CRSRulesResponse{
		CorazaVersion: metadata.CorazaVersion,
		CRSVersion:    metadata.CRSVersion,
		TotalRules:    metadata.TotalRules,
		FilteredRules: len(filteredRules),
		Rules:         filteredRules,
	}

	c.JSON(http.StatusOK, response)
}

// GetCRSVersions returns available CRS versions
// GET /api/v3/waf/crs/versions
func (h *WAFHandler) GetCRSVersions(c *gin.Context) {
	versions, err := h.service.GetAvailableVersions()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, CRSVersionsResponse{
		Versions: versions,
	})
}

// GetCRSRuleByID returns a specific rule by ID
// GET /api/v3/waf/crs/:crs_version/:rule_id
func (h *WAFHandler) GetCRSRuleByID(c *gin.Context) {
	crsVersion := c.Param("crs_version")
	ruleIDStr := c.Param("rule_id")

	ruleID, err := strconv.Atoi(ruleIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid rule_id"})
		return
	}

	rules, _, err := h.service.LoadCRSRules(crsVersion)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": fmt.Sprintf("CRS rules for version %s not found", crsVersion),
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Find rule by ID
	rule := h.service.FindRuleByID(rules, ruleID)
	if rule == nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": fmt.Sprintf("Rule %d not found in version %s", ruleID, crsVersion),
		})
		return
	}

	c.JSON(http.StatusOK, rule)
}

// parseFilters extracts and validates filter parameters from query string
func (h *WAFHandler) parseFilters(c *gin.Context) RuleFilters {
	filters := RuleFilters{
		Severity:   c.Query("severity"),
		RuleType:   c.Query("rule_type"),
		SearchTerm: c.Query("search"),
	}

	// Parse paranoia level
	if plStr := c.Query("paranoia_level"); plStr != "" {
		if pl, err := strconv.Atoi(plStr); err == nil {
			filters.ParanoiaLevel = &pl
		}
	}

	// Parse tags (comma-separated or multiple query params)
	if tagsParam := c.Query("tags"); tagsParam != "" {
		// Support comma-separated tags: ?tags=sqli,xss
		filters.Tags = splitAndTrim(tagsParam, ",")
	} else {
		// Support multiple query params: ?tags=sqli&tags=xss
		filters.Tags = c.QueryArray("tags")
	}

	return filters
}

// splitAndTrim splits a string by delimiter and trims whitespace
func splitAndTrim(s, delimiter string) []string {
	parts := strings.Split(s, delimiter)
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
