package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
	"gopkg.in/yaml.v3"

	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
	"github.com/CloudNativeWorks/elchi-backend/pkg/clickhouse"
	"github.com/CloudNativeWorks/elchi-backend/pkg/security"
)

// openapiExportMaxOps caps how many inventory operations a single export
// pulls. The collector cardinality cap is 100K endpoints per replica
// (schema.md); a project is normally far smaller, but the cap keeps a
// pathological project from building a multi-hundred-MB document in memory.
const openapiExportMaxOps = 20000

// pathParamRe matches OpenAPI-style placeholders in a normalized_path
// ("/api/v1/users/{id}" → "id"). The collector emits exactly this form, so
// each match becomes a required path parameter in the generated spec.
var pathParamRe = regexp.MustCompile(`\{([^}/]+)\}`)

// openapiInvDoc is the projection of api_inventory needed to build the spec.
type openapiInvDoc struct {
	Host           string         `bson:"host"`
	Method         string         `bson:"method"`
	NormalizedPath string         `bson:"normalized_path"`
	Protocol       string         `bson:"protocol"`
	GRPCService    string         `bson:"grpc_service"`
	GRPCMethod     string         `bson:"grpc_method"`
	StatusDist     map[string]any `bson:"status_dist"`
	AuthSchemes    []string       `bson:"auth_schemes"`
	RiskFlags      []string       `bson:"risk_flags"`
	PIICategories  []string       `bson:"pii_categories"`
	EndpointCats   []string       `bson:"endpoint_categories"`
	SeenCount      int64          `bson:"seen_count"`
	MaxRiskScore   int            `bson:"max_risk_score"`
	LastSeen       time.Time      `bson:"last_seen"`
}

// httpMethods is the set of OpenAPI Path Item operation keys. Anything else
// (gRPC, malformed) is skipped — OpenAPI 3.0 only models HTTP operations.
var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

// ExportOpenAPI handles GET /api/v3/inventory/openapi.
//
// Generates a download-able OpenAPI 3.0.3 skeleton from the discovered
// api_inventory (confirmed endpoints only). The inventory is a passive
// observation set — there is no request/response body schema — so this is a
// "skeleton": paths, methods, path parameters, observed responses, and the
// security posture, enriched with x-elchi-* extensions (risk/pii/seen).
//
//	?project=<id>            required
//	?format=yaml|json        default yaml
//	?listener_name=<prefix>  optional filter
//	?host=<host>             optional filter (normalized like the catalog)
//
// Auth: project-scoped (same as the rest of the inventory read API).
func (h *InventoryHandler) ExportOpenAPI(c *gin.Context) {
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

	format := strings.ToLower(strings.TrimSpace(c.Query("format")))
	if format == "" {
		format = "yaml"
	}
	if format != "yaml" && format != "json" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "format must be 'yaml' or 'json'"})
		return
	}

	// confirmed:true — export the clean catalog, never scanner noise.
	filter := bson.M{"project_id": projectID, "confirmed": true}
	if v := strings.TrimSpace(c.Query("listener_name")); v != "" {
		if san, ok := security.SanitizeSearchInput(v); ok && san != "" {
			filter["listener_name"] = bson.M{"$regex": "^" + san}
		}
	}
	if v := clickhouse.NormalizeHost(c.Query("host")); v != "" {
		if san, ok := security.SanitizeSearchInput(v); ok && san != "" {
			filter["host"] = bson.M{"$regex": "^" + san}
		}
	}

	coll := h.Context.Client.Collection(inventoryCollection)
	// Stable ordering so the generated spec is deterministic across exports
	// (diff-friendly): host, then path, then method.
	findOpts := options.Find().
		SetLimit(openapiExportMaxOps).
		SetSort(bson.D{
			{Key: "host", Value: 1},
			{Key: "normalized_path", Value: 1},
			{Key: "method", Value: 1},
		})
	cur, err := coll.Find(ctx, filter, findOpts)
	if err != nil {
		h.Logger.Errorf("openapi export find failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query inventory"})
		return
	}
	defer cur.Close(ctx)

	var docs []openapiInvDoc
	if err := cur.All(ctx, &docs); err != nil {
		h.Logger.Errorf("openapi export decode failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to decode inventory"})
		return
	}

	spec := buildOpenAPISpec(projectID, docs)

	var (
		body        []byte
		contentType string
		ext         string
	)
	switch format {
	case "json":
		body, err = json.MarshalIndent(spec, "", "  ")
		contentType, ext = "application/json; charset=utf-8", "json"
	default:
		body, err = yaml.Marshal(spec)
		contentType, ext = "application/yaml; charset=utf-8", "yaml"
	}
	if err != nil {
		h.Logger.Errorf("openapi export marshal failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to encode openapi spec"})
		return
	}

	filename := fmt.Sprintf("openapi-%s.%s", projectID, ext)
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Data(http.StatusOK, contentType, body)
}

// buildOpenAPISpec assembles an OpenAPI 3.0.3 document from the inventory
// docs. The whole spec is map[string]any so YAML and JSON marshalling share
// one structure; the small loss of key ordering is cosmetic (every tool
// reads it fine).
func buildOpenAPISpec(projectID string, docs []openapiInvDoc) map[string]any {
	hostSet := map[string]struct{}{}
	authSet := map[string]struct{}{}
	// paths[normalizedPath] -> map[httpMethodLower]operationObject
	paths := map[string]map[string]any{}

	for i := range docs {
		d := &docs[i]
		if d.Host != "" {
			hostSet[d.Host] = struct{}{}
		}

		method := strings.ToLower(strings.TrimSpace(d.Method))
		if !httpMethods[method] {
			// gRPC / non-HTTP operation — OpenAPI 3.0 can't model it. Skip;
			// the host still contributes a server entry above.
			continue
		}
		p := d.NormalizedPath
		if p == "" {
			continue
		}

		op := buildOperation(d, authSet)
		if paths[p] == nil {
			paths[p] = map[string]any{}
			// parameters live at the Path Item level (shared by all methods
			// of the path) — derived once from the placeholders in p.
			if params := pathParameters(p); len(params) > 0 {
				paths[p]["parameters"] = params
			}
		}
		paths[p][method] = op
	}

	servers := make([]map[string]any, 0, len(hostSet))
	for _, host := range sortedKeys(hostSet) {
		servers = append(servers, map[string]any{"url": "https://" + host})
	}

	spec := map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       "Elchi API Inventory — " + projectID,
			"version":     time.Now().UTC().Format("2006-01-02T15:04:05Z"),
			"description": "Auto-generated OpenAPI skeleton from observed traffic (elchi api_inventory). Confirmed endpoints only. Bodies are not modeled — this is a discovery skeleton, not a hand-authored contract.",
		},
		"paths": paths,
	}
	if len(servers) > 0 {
		spec["servers"] = servers
	}
	if schemes := securitySchemes(authSet); len(schemes) > 0 {
		spec["components"] = map[string]any{"securitySchemes": schemes}
	}
	return spec
}

// buildOperation builds one OpenAPI Operation Object from an inventory doc,
// recording any recognized auth scheme into authSet for the components block.
func buildOperation(d *openapiInvDoc, authSet map[string]struct{}) map[string]any {
	op := map[string]any{
		"operationId": operationID(d),
		"responses":   responsesFromStatusDist(d.StatusDist),
	}
	if summary := strings.TrimSpace(d.NormalizedPath); summary != "" {
		op["summary"] = strings.ToUpper(d.Method) + " " + summary
	}

	// Security: map recognized schemes to OpenAPI security requirements.
	// "none" means anonymous / unfingerprinted — no requirement added.
	var reqs []map[string][]string
	for _, s := range d.AuthSchemes {
		switch s {
		case "jwt", "apikey", "mtls":
			authSet[s] = struct{}{}
			reqs = append(reqs, map[string][]string{s: {}})
		}
	}
	if len(reqs) > 0 {
		op["security"] = reqs
	}

	// x-elchi-* extensions — observability metadata that has no native
	// OpenAPI home but is useful in the exported skeleton.
	if len(d.RiskFlags) > 0 {
		op["x-elchi-risk-flags"] = d.RiskFlags
	}
	if len(d.PIICategories) > 0 {
		op["x-elchi-pii-categories"] = d.PIICategories
	}
	if len(d.EndpointCats) > 0 {
		op["x-elchi-endpoint-categories"] = d.EndpointCats
	}
	if len(d.AuthSchemes) > 0 {
		op["x-elchi-auth-schemes"] = d.AuthSchemes
	}
	if d.MaxRiskScore > 0 {
		op["x-elchi-max-risk-score"] = d.MaxRiskScore
	}
	op["x-elchi-seen-count"] = d.SeenCount
	if !d.LastSeen.IsZero() {
		op["x-elchi-last-seen"] = d.LastSeen.UTC().Format(time.RFC3339)
	}
	return op
}

// pathParameters turns each "{name}" placeholder in a normalized path into a
// required string path parameter object.
func pathParameters(normalizedPath string) []map[string]any {
	matches := pathParamRe.FindAllStringSubmatch(normalizedPath, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]map[string]any, 0, len(matches))
	for _, m := range matches {
		name := m[1]
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, map[string]any{
			"name":     name,
			"in":       "path",
			"required": true,
			"schema":   map[string]any{"type": "string"},
		})
	}
	return out
}

// responsesFromStatusDist maps observed status codes to OpenAPI Response
// Objects. OpenAPI requires at least one response; when nothing was observed
// we emit a "default" with an explanatory description.
func responsesFromStatusDist(dist map[string]any) map[string]any {
	out := map[string]any{}
	for code := range dist {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		out[code] = map[string]any{"description": httpStatusText(code)}
	}
	if len(out) == 0 {
		out["default"] = map[string]any{"description": "No response status observed"}
	}
	return out
}

// operationId builds a stable, unique-ish operationId from method + path
// (+ gRPC coordinates when present). Not guaranteed globally unique across
// hosts, but stable per export and human-readable.
func operationID(d *openapiInvDoc) string {
	base := strings.ToLower(d.Method) + nonAlnum.ReplaceAllString(d.NormalizedPath, "_")
	if d.GRPCService != "" || d.GRPCMethod != "" {
		base += "_" + nonAlnum.ReplaceAllString(d.GRPCService+"_"+d.GRPCMethod, "_")
	}
	return strings.Trim(base, "_")
}

var nonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// securitySchemes builds components.securitySchemes for the auth schemes
// actually referenced by operations. mTLS has no dedicated 3.0.3 type
// (added in 3.1) — represented here as a documented apiKey-less note via a
// vendor-neutral http scheme placeholder is misleading, so we emit it as an
// x-elchi marker scheme that tools ignore but humans can read.
func securitySchemes(authSet map[string]struct{}) map[string]any {
	out := map[string]any{}
	for s := range authSet {
		switch s {
		case "jwt":
			out["jwt"] = map[string]any{
				"type":         "http",
				"scheme":       "bearer",
				"bearerFormat": "JWT",
			}
		case "apikey":
			out["apikey"] = map[string]any{
				"type": "apiKey",
				"in":   "header",
				"name": "X-Api-Key",
			}
		case "mtls":
			// OpenAPI 3.0.3 lacks a mutualTLS scheme type; describe it so the
			// document stays valid and the posture is still communicated.
			out["mtls"] = map[string]any{
				"type":           "apiKey",
				"in":             "header",
				"name":           "X-Forwarded-Client-Cert",
				"description":    "Mutual TLS — client certificate auth (observed). OpenAPI 3.0 has no native mutualTLS scheme; modeled as the mTLS-forwarded cert header for documentation.",
				"x-elchi-scheme": "mtls",
			}
		}
	}
	return out
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// httpStatusText returns a short description for a status code string.
func httpStatusText(code string) string {
	if n := atoiSafe(code); n > 0 {
		if txt := http.StatusText(n); txt != "" {
			return txt
		}
	}
	return "Observed status " + code
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
