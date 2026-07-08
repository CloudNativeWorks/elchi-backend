package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/controller/waf"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pkgwaf "github.com/CloudNativeWorks/elchi-backend/pkg/waf"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// crsFleetVersion is one OWASP CRS (coraza-coreruleset) version present across a
// project's shield fleet, with how many nodes run it (and how many are connected).
type crsFleetVersion struct {
	Version   string `json:"version"`
	Nodes     int    `json:"nodes"`
	Connected int    `json:"connected"`
}

// GetShieldCRSFleet reports the CRS versions the project's shield sidecars compiled
// in — with node counts and a `primary` (the version on the most nodes). shield's CRS
// is embedded at build time, so this is the ground truth of what each edge actually
// enforces; the UI auto-pins its CRS rule library to `primary` and warns when `mixed`.
// Versions come from each node's reported metadata (`shield_coreruleset_version`, set
// by elchi-client); nodes that don't report one are counted under `unreported`.
//
// GET /api/v3/shield/crs/fleet?project=<id>
func (h *ShieldHandler) GetShieldCRSFleet(c *gin.Context) {
	if !h.isAdmin(c) {
		return
	}
	project := c.Query("project")
	if project == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "project is required"})
		return
	}

	ctx := c.Request.Context()
	// Only the two fields we aggregate — avoid pulling full client docs into memory for
	// a large fleet.
	proj := options.Find().SetProjection(bson.M{"connected": 1, "metadata": 1})
	cur, err := h.dbContext.Client.Collection("clients").Find(ctx, bson.M{"project": project}, proj)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
		return
	}
	var docs []struct {
		Connected bool              `bson:"connected"`
		Metadata  map[string]string `bson:"metadata"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
		return
	}

	type agg struct{ nodes, connected int }
	byVer := map[string]*agg{}
	unreported := 0
	for _, d := range docs {
		v := ""
		if d.Metadata != nil {
			v = strings.TrimSpace(d.Metadata["shield_coreruleset_version"])
		}
		if v == "" {
			unreported++
			continue
		}
		a := byVer[v]
		if a == nil {
			a = &agg{}
			byVer[v] = a
		}
		a.nodes++
		if d.Connected {
			a.connected++
		}
	}

	versions := make([]crsFleetVersion, 0, len(byVer))
	for v, a := range byVer {
		versions = append(versions, crsFleetVersion{Version: v, Nodes: a.nodes, Connected: a.connected})
	}
	// Most nodes first; tiebreak by highest version so the newest wins as `primary`.
	sort.Slice(versions, func(i, j int) bool {
		if versions[i].Nodes != versions[j].Nodes {
			return versions[i].Nodes > versions[j].Nodes
		}
		return crsVersionLess(versions[j].Version, versions[i].Version)
	})

	primary := ""
	if len(versions) > 0 {
		primary = versions[0].Version
	}

	c.JSON(http.StatusOK, gin.H{
		"project":    project,
		"versions":   versions,
		"primary":    primary,
		"mixed":      len(versions) > 1,
		"unreported": unreported,
	})
}

// GetShieldCRSVersions lists the coreruleset versions the backend has a shield CRS
// library for (so the UI can offer a reference "browse another version" list).
//
// GET /api/v3/shield/crs/versions
func (h *ShieldHandler) GetShieldCRSVersions(c *gin.Context) {
	if !h.isAdmin(c) {
		return
	}
	resp := waf.CRSVersionsResponse{}
	for _, v := range pkgwaf.ShieldCRSVersions() {
		var m waf.CRSMetadata
		if _, meta, ok := pkgwaf.ShieldCRSData(v); ok && len(meta) > 0 {
			_ = json.Unmarshal(meta, &m)
		}
		if m.CRSVersion == "" {
			m.CRSVersion = v
		}
		resp.Versions = append(resp.Versions, m)
	}
	// Newest first.
	sort.Slice(resp.Versions, func(i, j int) bool { return crsVersionLess(resp.Versions[j].CRSVersion, resp.Versions[i].CRSVersion) })
	c.JSON(http.StatusOK, resp)
}

// GetShieldCRSRules serves the shield CRS rule library for a coreruleset version —
// the EXACT ruleset the matching shield binary embeds (generated from that
// coraza-coreruleset version), so tuning matches enforcement. Same filter set and
// response shape as the WASM path, but a separate, shield-keyed data store.
//
// GET /api/v3/shield/crs?crs_version=v4.25.0&severity=CRITICAL&paranoia_level=1&rule_type=blocking&tags=sqli&search=xss
func (h *ShieldHandler) GetShieldCRSRules(c *gin.Context) {
	if !h.isAdmin(c) {
		return
	}
	version := c.Query("crs_version")
	if version == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "crs_version is required"})
		return
	}
	rulesJSON, metaJSON, ok := pkgwaf.ShieldCRSData(version)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"message": fmt.Sprintf("shield CRS library for %q is not available in this backend", version)})
		return
	}
	var rules []models.CRSRule
	if err := json.Unmarshal(rulesJSON, &rules); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to parse shield CRS rules: " + err.Error()})
		return
	}

	filtered := waf.NewWAFService().FilterRules(rules, shieldCRSFilters(c))

	var meta waf.CRSMetadata
	if len(metaJSON) > 0 {
		_ = json.Unmarshal(metaJSON, &meta)
	}
	c.JSON(http.StatusOK, waf.CRSRulesResponse{
		CorazaVersion: meta.CorazaVersion,
		CRSVersion:    version,
		TotalRules:    len(rules),
		FilteredRules: len(filtered),
		Rules:         filtered,
	})
}

// GetShieldCRSRuleIDs returns ONLY the rule ids present in a coreruleset version — a
// tiny payload the UI uses to flag exclude_rule_ids that don't exist in the version the
// fleet runs (SecRuleRemoveById of a missing id is a silent no-op otherwise). Cheaper
// than shipping the full rule bodies just to validate a handful of ids.
//
// GET /api/v3/shield/crs/ids?crs_version=v4.25.0
func (h *ShieldHandler) GetShieldCRSRuleIDs(c *gin.Context) {
	if !h.isAdmin(c) {
		return
	}
	version := c.Query("crs_version")
	if version == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "crs_version is required"})
		return
	}
	rulesJSON, _, ok := pkgwaf.ShieldCRSData(version)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"message": fmt.Sprintf("shield CRS library for %q is not available in this backend", version)})
		return
	}
	var rules []models.CRSRule
	if err := json.Unmarshal(rulesJSON, &rules); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to parse shield CRS rules: " + err.Error()})
		return
	}
	ids := make([]int, 0, len(rules))
	for i := range rules {
		if id := rules[i].Characteristics.ID; id != 0 {
			ids = append(ids, id)
		}
	}
	c.JSON(http.StatusOK, gin.H{"crs_version": version, "ids": ids})
}

// shieldCRSFilters parses the rule-list query filters (mirrors the WASM handler's
// parseFilters so the shared CRS-library UI works unchanged against the shield store).
func shieldCRSFilters(c *gin.Context) waf.RuleFilters {
	f := waf.RuleFilters{
		Severity:   c.Query("severity"),
		RuleType:   c.Query("rule_type"),
		SearchTerm: c.Query("search"),
	}
	if plStr := c.Query("paranoia_level"); plStr != "" {
		if pl, err := strconv.Atoi(plStr); err == nil {
			f.ParanoiaLevel = &pl
		}
	}
	if tags := c.Query("tags"); tags != "" {
		for _, t := range strings.Split(tags, ",") {
			if t = strings.TrimSpace(t); t != "" {
				f.Tags = append(f.Tags, t)
			}
		}
	} else {
		f.Tags = c.QueryArray("tags")
	}
	return f
}

// crsVersionLess reports a < b for coreruleset version strings like "v4.25.0",
// comparing dotted numeric components so v4.9.0 < v4.25.0. Non-numeric parts fall
// back to a string compare.
func crsVersionLess(a, b string) bool {
	as := strings.Split(strings.TrimPrefix(a, "v"), ".")
	bs := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		ai, aerr := strconv.Atoi(as[i])
		bi, berr := strconv.Atoi(bs[i])
		if aerr == nil && berr == nil {
			if ai != bi {
				return ai < bi
			}
			continue
		}
		if as[i] != bs[i] {
			return as[i] < bs[i]
		}
	}
	return len(as) < len(bs)
}
