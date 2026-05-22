package handlers

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/clickhouse"
)

// consumerHashPattern is the accepted shape of a consumer_hash: the
// collector emits a lowercase hex digest (64-bit → 16 chars today; the
// range tolerates 128/256-bit variants up to 64 chars). Validating
// hex-only BEFORE the value reaches ClickHouse turns the path parameter
// into a closed set — even though the query also binds it as a
// parameter, a malformed hash is rejected with 400 rather than running
// a guaranteed-empty scan.
var consumerHashPattern = regexp.MustCompile(`^[a-f0-9]{16,64}$`)

// ListConsumers backs GET /api/v3/inventory/consumers — the consumer
// leaderboard. Answers "who is generating this traffic" by aggregating
// ClickHouse api_events_raw on consumer_hash.
//
// Query params:
//   - project=<id>      required
//   - from=&to=         optional, ISO-8601 (7d cap, raw retention)
//   - top=N             leaderboard size (1..50, default 10)
//
// Response: clickhouse.ConsumerSummary (top_consumers[] + the separate
// anonymous_events bucket).
func (h *InventoryHandler) ListConsumers(c *gin.Context) {
	if h.Context.Clickhouse == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "clickhouse not configured; consumer analytics unavailable"})
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

	filter := clickhouse.ConsumerFilter{
		ProjectID: projectID,
		From:      from,
		To:        to,
	}
	if v := strings.TrimSpace(c.Query("top")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Top = n
		}
	}

	summary, err := h.Context.Clickhouse.QueryConsumerTop(parentCtx, filter)
	if err != nil {
		h.Logger.Errorf("consumer top query failed: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to query consumers"})
		return
	}

	c.JSON(http.StatusOK, summary)
}

// GetConsumerDetail backs GET /api/v3/inventory/consumers/:hash — the
// investigation pivot. Given one consumer identity, returns every
// operation it touched, its method/status mix, geo/ASN attribution and
// threat signal (max risk score + critical events + TI hits) over the
// window.
//
// The :hash path parameter is regex-validated to a 64-char hex digest
// (400 on mismatch) and bound as a query parameter inside the
// ClickHouse call — defence in depth against injection.
func (h *InventoryHandler) GetConsumerDetail(c *gin.Context) {
	if h.Context.Clickhouse == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "clickhouse not configured; consumer analytics unavailable"})
		return
	}

	userDetails, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return
	}

	hash := strings.ToLower(strings.TrimSpace(c.Param("hash")))
	if !consumerHashPattern.MatchString(hash) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid consumer hash; expected 64-char hex digest"})
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

	filter := clickhouse.ConsumerFilter{
		ProjectID: projectID,
		From:      from,
		To:        to,
	}
	if v := strings.TrimSpace(c.Query("top")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Top = n
		}
	}

	detail, err := h.Context.Clickhouse.QueryConsumerDetail(parentCtx, hash, filter)
	if err != nil {
		h.Logger.Errorf("consumer detail query failed: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to query consumer detail"})
		return
	}

	c.JSON(http.StatusOK, detail)
}
