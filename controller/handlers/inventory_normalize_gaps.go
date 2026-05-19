package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/pkg/authorization"
)

const (
	// normalizeGapsCollection is the elchi-collector-owned collection
	// holding suspected path-normalization gaps — path prefixes under
	// which the collector saw many distinct literal last-segments, i.e.
	// an un-normalized id format the operator should add a
	// policy.path_normalize_patterns rule for. The collector keeps it
	// TTL-indexed (entries age out ~7d after a prefix stops bloating),
	// so the controller only ever reads it.
	normalizeGapsCollection = "api_collector_normalize_gaps"

	// normalizeGapsMaxResults bounds the response. The collection only
	// holds prefixes that crossed the detector threshold and ages them
	// out, so realistic cardinality is small; this is a defensive cap.
	normalizeGapsMaxResults = 500
)

// normalizeGapRow is one suspected normalization gap. bson tags decode
// the collector's document (`{_id, project, prefix, updated_at}`); json
// tags shape the API response. `_id` and `project` are intentionally
// omitted from the response — the caller already supplied the project,
// and the prefix is the actionable field.
type normalizeGapRow struct {
	Prefix    string    `json:"prefix" bson:"prefix"`
	UpdatedAt time.Time `json:"updated_at" bson:"updated_at"`
}

// ListNormalizeGaps handles GET /api/v3/inventory/normalize-gaps.
//
// Returns the project's suspected path-normalization gaps so the UI can
// prompt the operator to add a policy.path_normalize_patterns rule for
// each bloating prefix. Read-only over the collector-owned
// `api_collector_normalize_gaps` collection; project-scope auth via
// authorization.ValidateRequestProject (open to every project role —
// acting on a gap goes through the Admin/Owner-gated config PUT).
func (h *InventoryHandler) ListNormalizeGaps(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), mongoQueryTimeout)
	defer cancel()

	userDetails, err := GetUserDetails(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
		return
	}

	projectID := authorization.ExtractProjectFromRequest(c)
	if projectID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project is required"})
		return
	}
	if err := authorization.ValidateRequestProject(ctx, h.Context.Client, userDetails, projectID); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	cur, err := h.Context.Client.Collection(normalizeGapsCollection).Find(
		ctx,
		bson.M{"project": projectID},
		options.Find().
			SetSort(bson.D{{Key: "updated_at", Value: -1}}).
			SetLimit(normalizeGapsMaxResults),
	)
	if err != nil {
		h.Logger.Errorf("normalize-gaps find failed for project %s: %v", projectID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch normalize gaps"})
		return
	}
	defer cur.Close(ctx)

	rows := make([]normalizeGapRow, 0)
	if err := cur.All(ctx, &rows); err != nil {
		h.Logger.Errorf("normalize-gaps decode failed for project %s: %v", projectID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read normalize gaps"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": rows, "count": len(rows)})
}
