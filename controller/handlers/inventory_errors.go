package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/clickhouse"
)

// ErrorAnalysis handles GET /api/v3/inventory/errors.
//
// Returns the project's 4xx/5xx hotspot list (per endpoint, ranked by
// error volume) plus a project-wide error roll-up, and optionally a
// bucketed error time series from the rollup tables. Backed entirely
// by ClickHouse — 503 when the CH client is not configured.
//
//	?project=<id>            required
//	?listener_name=<name>    optional exact-match
//	?from=&to=               optional, RFC3339 (default 24h, max 7d)
//	?min_error_rate=N        optional 0..100 — drop endpoints below N%
//	?limit=&offset=          hotspot pagination (default 20, max 200)
//	?include_series=true     append time_series block
//	?series_granularity=     1m|1h|1d (auto-picked when omitted)
func (h *InventoryHandler) ErrorAnalysis(c *gin.Context) {
	if h.Context.Clickhouse == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "clickhouse not configured; error analysis unavailable"})
		return
	}

	userDetails, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return
	}
	projectID := authorization.ExtractProjectFromRequest(c)
	if projectID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project query parameter is required"})
		return
	}
	parentCtx := c.Request.Context()
	if err := authorization.ValidateRequestProject(parentCtx, h.Context.Client, userDetails, projectID); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	from, to, perr := parseTimeWindow(c, detailDefaultRangeMs, detailMaxRangeDays)
	if perr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": perr.Error()})
		return
	}

	limit, offset := parseLimitOffset(c, listDefaultLimit, 200)
	listenerName := strings.TrimSpace(c.Query("listener_name"))

	var minRate float64
	if v := c.Query("min_error_rate"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			minRate = n
		}
	}

	filter := clickhouse.ErrorFilter{
		ProjectID:    projectID,
		ListenerName: listenerName,
		From:         from,
		To:           to,
		MinErrorRate: minRate,
		Limit:        int(limit),
		Offset:       int(offset),
	}

	hotspots, summary, qerr := h.Context.Clickhouse.QueryErrorHotspots(parentCtx, filter)
	if qerr != nil {
		h.Logger.Errorf("error analysis hotspots failed: %v", qerr)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to query error hotspots"})
		return
	}
	if hotspots == nil {
		hotspots = []clickhouse.ErrorHotspot{}
	}

	resp := gin.H{
		"summary":  summary,
		"hotspots": hotspots,
		"count":    len(hotspots),
		"limit":    limit,
		"offset":   offset,
		"from":     from,
		"to":       to,
	}

	// Optional time series. Same DOS-guarded windowing the geo
	// dashboard uses — bucket count is capped per granularity.
	if c.Query("include_series") == "true" {
		granularity := strings.TrimSpace(c.Query("series_granularity"))
		if granularity == "" {
			granularity = pickAutoGranularity(from, to)
		}
		if err := validateSeriesWindow(granularity, from, to); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		points, serr := h.Context.Clickhouse.QueryErrorSeries(parentCtx, filter, granularity)
		if serr != nil {
			// Series failure shouldn't sink the hotspot response — surface
			// it on the side and return the snapshot.
			h.Logger.Warnf("error analysis time series failed (omitting): %v", serr)
		} else {
			if points == nil {
				points = []clickhouse.ErrorSeriesPoint{}
			}
			resp["time_series"] = gin.H{
				"granularity": granularity,
				"points":      points,
			}
		}
	}

	c.JSON(http.StatusOK, resp)
}
