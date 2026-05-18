package handlers

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
)

// riskFlagMeta pairs a risk flag with its severity weight and
// security class. Mirrors the collector's `internal/policy/risk.go`
// catalog as pinned in elchi-collector/docs/schema.md → "Risk flag
// taxonomy". Severity weights feed the collector's per-event
// risk_score; we keep them here only to bucket the dashboard.
type riskFlagMeta struct {
	Severity int    `json:"severity"`
	Class    string `json:"class"`
}

// riskFlagTaxonomy is the read-side copy of the collector's risk-flag
// catalog. It is intentionally a plain lookup table, NOT a source of
// truth: the collector owns the catalog and ships changes as schema
// migrations. An unknown flag (collector added one we don't know yet)
// degrades to class "unknown", severity 0 — the endpoint still shows
// up, it just lands in the "unknown" class until this map is synced.
var riskFlagTaxonomy = map[string]riskFlagMeta{
	"unauthenticated":                {4, "auth"},
	"weak_token_ttl":                 {7, "auth"},
	"internal_host":                  {1, "discovery"},
	"external_host":                  {1, "discovery"},
	"sensitive_path_keyword":         {7, "discovery"},
	"version_disclosure":             {1, "discovery"},
	"error_status":                   {4, "behavior"},
	"client_error_status":            {1, "behavior"},
	"latency_anomaly":                {4, "behavior"},
	"error_rate_spike":               {4, "behavior"},
	"plain_text_transport":           {7, "transport"},
	"legacy_protocol":                {4, "transport"},
	"weak_tls_version":               {10, "transport"},
	"missing_hsts":                   {7, "transport"},
	"permissive_cors":                {4, "transport"},
	"missing_x_content_type_options": {1, "transport"},
	"missing_x_frame_options":        {1, "transport"},
	"missing_csp":                    {1, "transport"},
	"pii_observed":                   {7, "data_leak"},
	"oversized_response":             {7, "data_leak"},
	"bola_suspect":                   {7, "attack_pattern"},
	"bfla_suspect":                   {10, "attack_pattern"},
	"brute_force_suspect":            {10, "attack_pattern"},
	"rate_anomaly":                   {7, "attack_pattern"},
	"ip_rate_anomaly":                {7, "attack_pattern"},
	"payment_abuse_suspect":          {10, "attack_pattern"},
	"threat_intel_hit":               {10, "attack_pattern"},
	"replay_suspect":                 {7, "attack_pattern"},
	"impossible_travel":              {7, "attack_pattern"},
	"unsafe_method_on_readonly":      {4, "attack_pattern"},
	"scanner_user_agent":             {7, "attack_pattern"},
	"vuln_probe_path":                {7, "attack_pattern"},
	"path_scan_suspect":              {7, "attack_pattern"},
	"auth_inconsistent":              {7, "consistency"},
	// NOTE: `ua_mismatch` was retired from the collector's catalog (it
	// no longer fires on any new event). It is intentionally absent
	// here: historical `api_inventory` rows may still carry it in their
	// accumulated `risk_flags`, and per the lookup contract above an
	// unknown flag degrades to class "unknown" / severity 0 — still
	// visible, just unclassified — which is the desired behaviour for a
	// retired flag.
}

// riskSeverityBucket maps a numeric severity to the catalog's named
// band (schema.md → "Severity catalog"). 0 (unknown flag) lands in
// "low" so it stays visible without inflating the critical count.
func riskSeverityBucket(sev int) string {
	switch {
	case sev >= 10:
		return "critical"
	case sev >= 7:
		return "high"
	case sev >= 4:
		return "medium"
	default:
		return "low"
	}
}

// riskFlagRow is one entry in the risk-summary by_flag list.
type riskFlagRow struct {
	Flag          string `json:"flag"`
	Severity      int    `json:"severity"`
	Class         string `json:"class"`
	SeverityBand  string `json:"severity_band"`
	EndpointCount int64  `json:"endpoint_count"`
}

// riskClassRow aggregates flags into one security class.
type riskClassRow struct {
	Class         string   `json:"class"`
	EndpointCount int64    `json:"endpoint_count"` // sum of flag occurrences in this class
	Flags         []string `json:"flags"`
}

// RiskSummary handles GET /api/v3/inventory/risk-summary.
//
// Aggregates the `risk_flags` arrays across a project's
// `api_inventory` documents and projects them onto the collector's
// flag taxonomy — by flag, by security class, by severity band. The
// security dashboard's "risk posture" panel binds to this.
//
//	?project=<id>           required
//	?listener_name=<name>   optional prefix filter
//
// Counts are flag-occurrence counts: an endpoint carrying three
// flags contributes to three by_flag rows. `flagged_endpoints` is the
// distinct count of endpoints with at least one flag, so the UI can
// show both "N findings" and "M endpoints affected".
func (h *InventoryHandler) RiskSummary(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), mongoQueryTimeout)
	defer cancel()

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
	if err := authorization.ValidateRequestProject(ctx, h.Context.Client, userDetails, projectID); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	baseFilter := bson.M{"project_id": projectID}
	if v := strings.TrimSpace(c.Query("listener_name")); v != "" {
		baseFilter["listener_name"] = bson.M{"$regex": "^" + escapeRegex(v)}
	}

	coll := h.Context.Client.Collection(inventoryCollection)

	// $unwind risk_flags → $sortByCount: one row per distinct flag
	// with its endpoint-occurrence count. $unwind silently drops
	// documents with an empty/absent risk_flags array, which is the
	// behaviour we want — only flagged endpoints contribute.
	pipeline := []bson.M{
		{"$match": baseFilter},
		{"$unwind": "$risk_flags"},
		{"$sortByCount": "$risk_flags"},
	}
	cur, err := coll.Aggregate(ctx, pipeline, options.Aggregate().SetAllowDiskUse(true))
	if err != nil {
		h.Logger.Errorf("risk summary aggregation failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to aggregate risk flags"})
		return
	}
	defer cur.Close(ctx)
	var rawFlags []struct {
		Flag  string `bson:"_id"`
		Count int64  `bson:"count"`
	}
	if err := cur.All(ctx, &rawFlags); err != nil {
		h.Logger.Errorf("risk summary decode failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to decode risk flags"})
		return
	}

	// Total + flagged endpoint counts. `risk_flags` non-empty is the
	// `$exists + $not $size:0` idiom (matches the PII handler) so a
	// missing-field legacy document doesn't count as flagged.
	totalEndpoints, err := coll.CountDocuments(ctx, baseFilter)
	if err != nil {
		h.Logger.Errorf("risk summary total count failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to count endpoints"})
		return
	}
	flaggedFilter := bson.M{}
	for k, v := range baseFilter {
		flaggedFilter[k] = v
	}
	flaggedFilter["risk_flags"] = bson.M{"$exists": true, "$not": bson.M{"$size": 0}}
	flaggedEndpoints, err := coll.CountDocuments(ctx, flaggedFilter)
	if err != nil {
		h.Logger.Errorf("risk summary flagged count failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to count flagged endpoints"})
		return
	}

	// Project each flag onto the taxonomy + roll the class / severity
	// aggregates up in a single pass.
	byFlag := make([]riskFlagRow, 0, len(rawFlags))
	classCounts := map[string]int64{}
	classFlags := map[string]map[string]struct{}{}
	severityCounts := map[string]int64{"critical": 0, "high": 0, "medium": 0, "low": 0}

	for _, rf := range rawFlags {
		meta, known := riskFlagTaxonomy[rf.Flag]
		if !known {
			// Unknown flag — collector shipped one ahead of this map.
			meta = riskFlagMeta{Severity: 0, Class: "unknown"}
		}
		band := riskSeverityBucket(meta.Severity)
		byFlag = append(byFlag, riskFlagRow{
			Flag:          rf.Flag,
			Severity:      meta.Severity,
			Class:         meta.Class,
			SeverityBand:  band,
			EndpointCount: rf.Count,
		})
		classCounts[meta.Class] += rf.Count
		if classFlags[meta.Class] == nil {
			classFlags[meta.Class] = map[string]struct{}{}
		}
		classFlags[meta.Class][rf.Flag] = struct{}{}
		severityCounts[band] += rf.Count
	}

	// by_class: stable ordering — highest occurrence count first.
	byClass := make([]riskClassRow, 0, len(classCounts))
	for class, count := range classCounts {
		flags := make([]string, 0, len(classFlags[class]))
		for f := range classFlags[class] {
			flags = append(flags, f)
		}
		sort.Strings(flags)
		byClass = append(byClass, riskClassRow{
			Class:         class,
			EndpointCount: count,
			Flags:         flags,
		})
	}
	sort.Slice(byClass, func(i, j int) bool {
		return byClass[i].EndpointCount > byClass[j].EndpointCount
	})

	c.JSON(http.StatusOK, gin.H{
		"total_endpoints":   totalEndpoints,
		"flagged_endpoints": flaggedEndpoints,
		"by_flag":           byFlag, // already sorted desc by $sortByCount
		"by_class":          byClass,
		"by_severity":       severityCounts,
	})
}
