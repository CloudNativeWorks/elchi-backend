package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/clickhouse"
)

// TransportPosture handles GET /api/v3/inventory/transport.
//
// Surfaces the project's TLS-version + protocol distribution plus the
// list of endpoints observed on a non-ideal transport (below TLSv1.3,
// plain text, or a legacy protocol). Backed entirely by ClickHouse —
// returns 503 when the CH client is not configured.
//
//	?project=<id>          required
//	?listener_name=<name>  optional exact-match
//	?from=&to=             optional, RFC3339 (default 24h, max 7d)
//	?top=N                 weak-transport list cap (default 100, max 500)
//
// `summary` carries the derived percentages the UI banners need;
// `weak_transport` is empty when every endpoint is on TLSv1.3 + HTTP/2+.
func (h *InventoryHandler) TransportPosture(c *gin.Context) {
	if h.Context.Clickhouse == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "clickhouse not configured; transport posture unavailable"})
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

	top := 0
	if v := c.Query("top"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			top = n
		}
	}

	res, qerr := h.Context.Clickhouse.QueryTransportPosture(parentCtx, clickhouse.TransportFilter{
		ProjectID:    projectID,
		ListenerName: strings.TrimSpace(c.Query("listener_name")),
		From:         from,
		To:           to,
		Top:          top,
	})
	if qerr != nil {
		h.Logger.Errorf("transport posture failed: %v", qerr)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to query transport posture"})
		return
	}

	// Derived percentages. total==0 → all zero (no divide-by-zero).
	summary := gin.H{
		"total_events":        res.TotalEvents,
		"weak_tls_pct":        0.0,
		"plaintext_pct":       0.0,
		"legacy_protocol_pct": 0.0,
		"http2_adoption_pct":  0.0,
	}
	if res.TotalEvents > 0 {
		total := float64(res.TotalEvents)
		weakTLS := res.TLSVersions["TLSv1_2"] + res.TLSVersions["TLSv1_0/1_1"]
		http2plus := res.Protocols["http/2"] + res.Protocols["http/3"]
		summary["weak_tls_pct"] = pct1(float64(weakTLS) / total)
		summary["plaintext_pct"] = pct1(float64(res.TLSVersions["plaintext"]) / total)
		summary["http2_adoption_pct"] = pct1(float64(http2plus) / total)
		// legacy_protocol share — derived from the http/1.x count, which
		// is what the collector's legacy_protocol flag tracks.
		summary["legacy_protocol_pct"] = pct1(float64(res.Protocols["http/1.x"]) / total)
	}

	weak := res.WeakTransport
	if weak == nil {
		weak = []clickhouse.WeakTransportRow{}
	}

	c.JSON(http.StatusOK, gin.H{
		"summary":        summary,
		"tls_versions":   res.TLSVersions,
		"protocols":      res.Protocols,
		"weak_transport": weak,
		"count":          len(weak),
		"from":           from,
		"to":             to,
	})
}

// pct1 converts a 0..1 ratio to a percentage rounded to one decimal.
func pct1(ratio float64) float64 {
	return float64(int64(ratio*1000+0.5)) / 10.0
}
