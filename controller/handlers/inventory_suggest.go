package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
)

// SuggestPolicy handles POST /api/v3/inventory/suggest-policy.
//
// It turns API-Discovery findings into a DRAFT elchi-shield SecurityPolicy: it
// reads the selected inventory endpoints (or every confirmed endpoint of a host),
// maps each one's risk_flags / auth_schemes / pii_categories / endpoint_categories
// into the matching shield engines, and returns the policy YAML plus a per-route
// rationale ("which finding → which engine, and why"). The draft is NOT persisted
// — the UI opens it in the policy Builder for the user to review/edit/save through
// the normal (admin-gated) shield CRUD. Project-scoped, like the rest of the
// inventory read API.
//
// Request body (JSON):
//
//	{ "endpoint_ids": ["<oid>", ...] }   // single or multi-select (preferred)
//	{ "host": "api.example.com" }        // OR every confirmed endpoint of one host
//	{ "policy_name": "..." }             // optional; derived from host otherwise
//
// The generated YAML uses ONLY the field subset the UI Builder models, so it
// hydrates the structured Builder instead of falling back to raw-YAML mode.
func (h *InventoryHandler) SuggestPolicy(c *gin.Context) {
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

	var req struct {
		EndpointIDs []string `json:"endpoint_ids"`
		Host        string   `json:"host"`
		PolicyName  string   `json:"policy_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	filter := bson.M{"project_id": projectID}
	switch {
	case len(req.EndpointIDs) > 0:
		oids := make([]primitive.ObjectID, 0, len(req.EndpointIDs))
		for _, hex := range req.EndpointIDs {
			oid, err := primitive.ObjectIDFromHex(strings.TrimSpace(hex))
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid endpoint id %q", hex)})
				return
			}
			oids = append(oids, oid)
		}
		filter["_id"] = bson.M{"$in": oids}
	case strings.TrimSpace(req.Host) != "":
		filter["host"] = strings.TrimSpace(req.Host)
		filter["confirmed"] = true
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "provide endpoint_ids or host"})
		return
	}

	coll := h.Context.Client.Collection(inventoryCollection)
	cur, err := coll.Find(ctx, filter, options.Find().SetLimit(suggestMaxEndpoints).SetSort(bson.D{
		{Key: "host", Value: 1}, {Key: "normalized_path", Value: 1}, {Key: "method", Value: 1},
	}))
	if err != nil {
		h.Logger.Errorf("suggest-policy find failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query inventory"})
		return
	}
	defer cur.Close(ctx)

	var docs []suggestInvDoc
	if err := cur.All(ctx, &docs); err != nil {
		h.Logger.Errorf("suggest-policy decode failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to decode inventory"})
		return
	}
	if len(docs) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "no matching endpoints found"})
		return
	}

	name := suggestPolicyName(req.PolicyName, docs)
	yamlStr, rationale, err := buildSuggestedPolicy(name, docs)
	if err != nil {
		h.Logger.Errorf("suggest-policy build failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build suggested policy"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"policy_name": name,
		"yaml":        yamlStr,
		"rationale":   rationale,
	}})
}

// suggestMaxEndpoints bounds a single suggestion to a sane number of endpoints.
const suggestMaxEndpoints = 500

// suggestBlockScoreThreshold is the max_risk_score at which a route is suggested
// in `block` (vs `detect`) mode — the catalog's "high" band starts at 25.
const suggestBlockScoreThreshold = 25

// suggestInvDoc is the inventory projection the suggestion engine reads.
type suggestInvDoc struct {
	Host           string   `bson:"host"`
	Method         string   `bson:"method"`
	NormalizedPath string   `bson:"normalized_path"`
	AuthSchemes    []string `bson:"auth_schemes"`
	RiskFlags      []string `bson:"risk_flags"`
	PIICategories  []string `bson:"pii_categories"`
	EndpointCats   []string `bson:"endpoint_categories"`
	NoauthObserved bool     `bson:"noauth_observed"`
	AuthObserved   bool     `bson:"auth_observed"`
	MaxRiskScore   int      `bson:"max_risk_score"`
}

// suggestPolicyName derives a policy/file-friendly name.
func suggestPolicyName(explicit string, docs []suggestInvDoc) string {
	if n := strings.TrimSpace(explicit); n != "" {
		return n
	}
	host := ""
	for _, d := range docs {
		if d.Host != "" {
			host = d.Host
			break
		}
	}
	if host == "" {
		return "discovery-policy"
	}
	return "discovery-" + host
}
