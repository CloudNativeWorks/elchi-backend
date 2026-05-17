package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
	"golang.org/x/sync/errgroup"

	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/clickhouse"
)

// scoreComponent is one weighted pillar of the API Security Score.
type scoreComponent struct {
	Key     string  `json:"key"`
	Label   string  `json:"label"`
	Score   int     `json:"score"` // 0..100
	Grade   string  `json:"grade"` // A..F
	Weight  float64 `json:"weight"`
	Summary string  `json:"summary"`
}

// securityEndpointCounts is the MongoDB half of the score input —
// endpoint-level cardinalities the event stream can't give cheaply.
type securityEndpointCounts struct {
	Total       int64
	Write       int64
	UnauthWrite int64
	External    int64
	Sensitive   int64
}

// SecurityScore handles GET /api/v3/inventory/security-score.
//
// Computes a single A–F security grade for a project from five
// weighted pillars. The ClickHouse event scan and the MongoDB
// endpoint-count aggregate run in parallel; the pillar formulas are
// pure arithmetic on those two inputs.
//
//	?project=<id>   required
//	?from=&to=      optional, RFC3339 (default 24h, max 7d)
//
// A project with zero events in the window grades "N/A" — scoring an
// idle project as either perfect or failing would both mislead.
func (h *InventoryHandler) SecurityScore(c *gin.Context) {
	if h.Context.Clickhouse == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "clickhouse not configured; security score unavailable"})
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

	var (
		metrics clickhouse.SecurityMetrics
		counts  securityEndpointCounts
	)
	g, gctx := errgroup.WithContext(parentCtx)
	g.Go(func() error {
		m, err := h.Context.Clickhouse.QuerySecurityMetrics(gctx, clickhouse.SecurityScoreFilter{
			ProjectID: projectID, From: from, To: to,
		})
		if err != nil {
			return fmt.Errorf("security metrics: %w", err)
		}
		metrics = m
		return nil
	})
	g.Go(func() error {
		mctx, cancel := context.WithTimeout(gctx, mongoQueryTimeout)
		defer cancel()
		ec, err := h.querySecurityEndpointCounts(mctx, projectID)
		if err != nil {
			return fmt.Errorf("endpoint counts: %w", err)
		}
		counts = ec
		return nil
	})
	if err := g.Wait(); err != nil {
		h.Logger.Errorf("security score failed: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to compute security score"})
		return
	}

	// Idle project — no events to grade against.
	if metrics.TotalEvents == 0 {
		c.JSON(http.StatusOK, gin.H{
			"score":       nil,
			"grade":       "N/A",
			"computed_at": time.Now().UTC(),
			"time_window": gin.H{"from": from, "to": to},
			"components":  []scoreComponent{},
			"totals":      gin.H{"endpoints": counts.Total, "events_analyzed": 0},
		})
		return
	}

	components := computeScoreComponents(metrics, counts)

	// Weighted total. Weights sum to 1.0 so no renormalisation needed
	// while all five pillars are always present.
	var weighted float64
	for _, comp := range components {
		weighted += float64(comp.Score) * comp.Weight
	}
	total := int(weighted + 0.5)

	c.JSON(http.StatusOK, gin.H{
		"score":       total,
		"grade":       letterGrade(total),
		"computed_at": time.Now().UTC(),
		"time_window": gin.H{"from": from, "to": to},
		"components":  components,
		"totals": gin.H{
			"endpoints":       counts.Total,
			"events_analyzed": metrics.TotalEvents,
		},
	})
}

// computeScoreComponents turns the raw metrics into the five graded
// pillars. Each pillar is 0..100; the penalty multipliers are tuned
// so a clean posture lands near 100 and a systemic problem (e.g. all
// traffic unauthenticated) drives the pillar toward 0.
func computeScoreComponents(m clickhouse.SecurityMetrics, ec securityEndpointCounts) []scoreComponent {
	total := float64(m.TotalEvents)

	// 1. Auth coverage — share of write endpoints with no observed auth.
	authScore := 100
	authSummary := "no write endpoints discovered"
	if ec.Write > 0 {
		unauthRatio := float64(ec.UnauthWrite) / float64(ec.Write)
		authScore = clampScore(100 - int(unauthRatio*100))
		authSummary = fmt.Sprintf("%d of %d write endpoints have no observed authentication",
			ec.UnauthWrite, ec.Write)
	}

	// 2. Transport security — weak TLS / plain text fully penalised,
	//    legacy protocol penalised lightly (it's a posture gap, not an
	//    active exploit path).
	weakPct := float64(m.WeakTransport) / total
	legacyPct := float64(m.LegacyProtocol) / total
	transportScore := clampScore(100 - int(weakPct*100) - int(legacyPct*30))
	transportSummary := fmt.Sprintf("%.1f%% weak/plain-text transport, %.1f%% legacy protocol",
		weakPct*100, legacyPct*100)

	// 3. Risk exposure — p95 risk_score plus the share of critical
	//    (risk_score >= 10) events.
	criticalPct := float64(m.CriticalEvents) / total
	riskScore := clampScore(100 - int(m.P95RiskScore*1.5) - int(criticalPct*50))
	riskSummary := fmt.Sprintf("p95 risk score %.0f, %.1f%% critical events",
		m.P95RiskScore, criticalPct*100)

	// 4. Attack surface — external-host + sensitive-path endpoints.
	externalScore := 100
	attackSummary := "no endpoints discovered"
	if ec.Total > 0 {
		externalRatio := float64(ec.External) / float64(ec.Total)
		sensitiveRatio := float64(ec.Sensitive) / float64(ec.Total)
		externalScore = clampScore(100 - int(externalRatio*50) - int(sensitiveRatio*100))
		attackSummary = fmt.Sprintf("%d external-host, %d sensitive-path endpoints (of %d)",
			ec.External, ec.Sensitive, ec.Total)
	}

	// 5. Threat activity — threat-intel hits (heavily weighted, rare)
	//    plus scanner/bot traffic share.
	tiPct := float64(m.TIHits) / total
	sbPct := float64(m.ScannerBot) / total
	threatScore := clampScore(100 - int(tiPct*500) - int(sbPct*100))
	threatSummary := fmt.Sprintf("%.2f%% threat-intel hits, %.1f%% scanner/bot traffic",
		tiPct*100, sbPct*100)

	return []scoreComponent{
		{"auth_coverage", "Authentication Coverage", authScore, letterGrade(authScore), 0.30, authSummary},
		{"transport_security", "Transport Security", transportScore, letterGrade(transportScore), 0.25, transportSummary},
		{"risk_exposure", "Risk Exposure", riskScore, letterGrade(riskScore), 0.20, riskSummary},
		{"attack_surface", "Attack Surface", externalScore, letterGrade(externalScore), 0.15, attackSummary},
		{"threat_activity", "Threat Activity", threatScore, letterGrade(threatScore), 0.10, threatSummary},
	}
}

// querySecurityEndpointCounts runs a single $facet aggregation that
// returns all five endpoint cardinalities in one Mongo round-trip.
func (h *InventoryHandler) querySecurityEndpointCounts(ctx context.Context, projectID string) (securityEndpointCounts, error) {
	coll := h.Context.Client.Collection(inventoryCollection)
	writeMatch := bson.M{"method": bson.M{"$in": writeMethods}}
	pipeline := []bson.M{
		{"$match": bson.M{"project_id": projectID}},
		{"$facet": bson.M{
			"total": []bson.M{{"$count": "n"}},
			"write": []bson.M{{"$match": writeMatch}, {"$count": "n"}},
			"unauth_write": []bson.M{
				{"$match": bson.M{
					"method":        bson.M{"$in": writeMethods},
					"auth_observed": bson.M{"$in": bson.A{false, 0}},
				}},
				{"$count": "n"},
			},
			"external":  []bson.M{{"$match": bson.M{"risk_flags": "external_host"}}, {"$count": "n"}},
			"sensitive": []bson.M{{"$match": bson.M{"risk_flags": "sensitive_path_keyword"}}, {"$count": "n"}},
		}},
	}
	cur, err := coll.Aggregate(ctx, pipeline, options.Aggregate().SetAllowDiskUse(true))
	if err != nil {
		return securityEndpointCounts{}, err
	}
	defer cur.Close(ctx)

	var facets []struct {
		Total []struct {
			N int64 `bson:"n"`
		} `bson:"total"`
		Write []struct {
			N int64 `bson:"n"`
		} `bson:"write"`
		UnauthWrite []struct {
			N int64 `bson:"n"`
		} `bson:"unauth_write"`
		External []struct {
			N int64 `bson:"n"`
		} `bson:"external"`
		Sensitive []struct {
			N int64 `bson:"n"`
		} `bson:"sensitive"`
	}
	if err := cur.All(ctx, &facets); err != nil {
		return securityEndpointCounts{}, err
	}
	if len(facets) == 0 {
		return securityEndpointCounts{}, nil
	}
	f := facets[0]
	return securityEndpointCounts{
		Total:       facetCount(f.Total),
		Write:       facetCount(f.Write),
		UnauthWrite: facetCount(f.UnauthWrite),
		External:    facetCount(f.External),
		Sensitive:   facetCount(f.Sensitive),
	}, nil
}

// facetCount unwraps a `[{n: N}]` $count facet — empty slice means 0.
func facetCount(arr []struct {
	N int64 `bson:"n"`
}) int64 {
	if len(arr) == 0 {
		return 0
	}
	return arr[0].N
}

// clampScore bounds a pillar score to the 0..100 range.
func clampScore(s int) int {
	if s < 0 {
		return 0
	}
	if s > 100 {
		return 100
	}
	return s
}

// letterGrade maps a 0..100 score to the A–F band.
func letterGrade(score int) string {
	switch {
	case score >= 90:
		return "A"
	case score >= 80:
		return "B"
	case score >= 70:
		return "C"
	case score >= 60:
		return "D"
	default:
		return "F"
	}
}
