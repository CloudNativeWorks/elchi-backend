package settings

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// apiCollectorConfigCollection / ID name the elchi-collector runtime
// config singleton. The collector owns this document (it polls the
// doc and applies policy/detection at runtime); the controller is the
// operator-facing editor — read + bump version on every change. See
// elchi-collector/docs/schema.md → "api_collector_config".
const (
	apiCollectorConfigCollection = "api_collector_config"
	apiCollectorConfigID         = "default"
)

// apiDiscoveryConfigUpdate is the PUT body. Only `policy` and
// `detection` are operator-editable; `_id`, `version`, `updated_at`,
// `updated_by` are server-managed and ignored if a caller tries to
// set them. Both fields are optional — a partial update touches only
// the sub-tree that's present.
type apiDiscoveryConfigUpdate struct {
	Policy    bson.M `json:"policy"`
	Detection bson.M `json:"detection"`
}

// GetAPIDiscoveryConfig handles GET /api/v3/setting/api_discovery.
//
// Returns the elchi-collector runtime config singleton plus the
// applied-migration generation on both backends so the settings UI
// can show what the collector is currently running. Admin/Owner only
// — InitSettingMiddleware gates the whole /setting group.
//
// `config: null` is a legitimate response — a fresh collector may not
// have persisted a config document yet.
func (handler *AppHandler) GetAPIDiscoveryConfig(c *gin.Context) {
	ctx := c.Request.Context()

	var config bson.M
	err := handler.Context.Client.Collection(apiCollectorConfigCollection).
		FindOne(ctx, bson.M{"_id": apiCollectorConfigID}).Decode(&config)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		handler.Logger.Errorf("api_discovery config read failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to read collector config"})
		return
	}
	if errors.Is(err, mongo.ErrNoDocuments) {
		config = nil
	}

	// Mongo schema generation — highest-_id row in the tracker.
	var mongoSchema bson.M
	if mErr := handler.Context.Client.Collection("_collector_migrations").
		FindOne(ctx, bson.M{}, options.FindOne().SetSort(bson.D{{Key: "_id", Value: -1}})).
		Decode(&mongoSchema); mErr != nil {
		mongoSchema = nil
	}

	// ClickHouse schema generation — only when the CH client is up.
	var chSchema any
	if handler.Context.Clickhouse != nil {
		if m, sErr := handler.Context.Clickhouse.SchemaGeneration(ctx); sErr == nil {
			chSchema = m
		} else {
			handler.Logger.Warnf("api_discovery: ch schema generation unavailable: %v", sErr)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"config": config,
		"schema": gin.H{
			"mongo":      mongoSchema,
			"clickhouse": chSchema,
		},
	})
}

// UpdateAPIDiscoveryConfig handles PUT /api/v3/setting/api_discovery.
//
// Operator-driven edit of the collector runtime config. Restricted to
// Admin / Owner. Applies a partial `$set` over `policy` / `detection`,
// bumps the monotonic `version`, and stamps `updated_at` / `updated_by`
// — the collector picks the change up on its next config poll.
//
// Upsert: if the collector has never written a config, the first PUT
// creates the `default` document with version 1.
func (handler *AppHandler) UpdateAPIDiscoveryConfig(c *gin.Context) {
	if !handler.canWriteCollectorConfig(c) {
		c.JSON(http.StatusForbidden, gin.H{"message": "only Admin or Owner can change collector config"})
		return
	}

	var body apiDiscoveryConfigUpdate
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "invalid request body: " + err.Error()})
		return
	}
	if body.Policy == nil && body.Detection == nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "at least one of 'policy' or 'detection' must be provided"})
		return
	}
	// Validate before writing. The collector runs its own Doc.Validate()
	// on every config poll and silently REJECTS the whole document on
	// failure (the old runtime config stays, only a log line records
	// it). Mirroring the checks here turns a silent no-op into an
	// immediate 400 the operator can act on.
	if err := validateCollectorConfigUpdate(body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	updatedBy := currentUsername(c)
	if updatedBy == "" {
		updatedBy = "unknown"
	}

	// Whitelist $set — only the two operator-editable sub-trees plus
	// the server-managed audit stamps. _id and version never come
	// from the request body.
	set := bson.M{
		"updated_at": time.Now().UTC(),
		"updated_by": updatedBy,
	}
	if body.Policy != nil {
		set["policy"] = body.Policy
	}
	if body.Detection != nil {
		set["detection"] = body.Detection
	}

	ctx := c.Request.Context()
	coll := handler.Context.Client.Collection(apiCollectorConfigCollection)
	// $inc on a missing field starts at 0 → first write lands version 1.
	// int64 keeps the field a BSON long, matching what the collector writes.
	update := bson.M{
		"$set": set,
		"$inc": bson.M{"version": int64(1)},
	}
	if _, err := coll.UpdateOne(ctx, bson.M{"_id": apiCollectorConfigID}, update,
		options.Update().SetUpsert(true)); err != nil {
		handler.Logger.Errorf("api_discovery config update failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to update collector config"})
		return
	}

	// Re-read so the response echoes the persisted doc (new version,
	// stamps) — the UI shows exactly what the collector will pick up.
	var updated bson.M
	if err := coll.FindOne(ctx, bson.M{"_id": apiCollectorConfigID}).Decode(&updated); err != nil {
		// Update succeeded; only the echo failed. Report success.
		handler.Logger.Warnf("api_discovery config re-read failed: %v", err)
		c.JSON(http.StatusOK, gin.H{"message": "collector config updated"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "collector config updated",
		"config":  updated,
	})
}

// detectorWindowKeys are the `detection` sub-objects that carry the
// {enabled, threshold, window_seconds} triple (collector's
// DetectorWindow type). response_size / missing_hsts / weak_tls have
// different shapes and aren't in this list.
var detectorWindowKeys = []string{
	"bola", "brute_force", "rate_anomaly", "payment_abuse",
	"replay", "path_scan", "geo_spread", "ip_rate",
}

// validateCollectorConfigUpdate mirrors the collector's Doc.Validate()
// well enough to catch the common operator mistakes BEFORE the write,
// so a bad config returns 400 instead of being silently rejected by
// the collector on its next poll. It is intentionally a subset — the
// collector remains the authority — but covers every documented rule
// whose shape is stable: detector-window bounds, the weak-token TTL
// floor, and ingest-deny-pattern regex compilability.
func validateCollectorConfigUpdate(body apiDiscoveryConfigUpdate) error {
	if body.Policy != nil {
		if err := validateCollectorPolicy(body.Policy); err != nil {
			return err
		}
	}
	if body.Detection != nil {
		if err := validateCollectorDetection(body.Detection); err != nil {
			return err
		}
	}
	return nil
}

// validateCollectorPolicy checks the policy sub-tree — currently only
// ingest_deny_patterns, every entry of which must be a Go-compilable
// regular expression (the collector compiles them at config load).
func validateCollectorPolicy(policy bson.M) error {
	raw, ok := policy["ingest_deny_patterns"]
	if !ok || raw == nil {
		return nil
	}
	patterns, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("policy.ingest_deny_patterns must be an array")
	}
	for i, p := range patterns {
		s, ok := p.(string)
		if !ok {
			return fmt.Errorf("policy.ingest_deny_patterns[%d] must be a string", i)
		}
		if _, err := regexp.Compile(s); err != nil {
			return fmt.Errorf("policy.ingest_deny_patterns[%d] is not a valid regex: %v", i, err)
		}
	}
	return nil
}

// validateCollectorDetection checks the detection sub-tree:
//   - weak_token_ttl_seconds >= 0 (0 means "disabled")
//   - every DetectorWindow: threshold/window_seconds never negative,
//     and when enabled both must be strictly positive
func validateCollectorDetection(detection bson.M) error {
	if raw, ok := detection["weak_token_ttl_seconds"]; ok && raw != nil {
		n, ok := numericValue(raw)
		if !ok {
			return fmt.Errorf("detection.weak_token_ttl_seconds must be a number")
		}
		if n < 0 {
			return fmt.Errorf("detection.weak_token_ttl_seconds cannot be negative")
		}
	}

	for _, key := range detectorWindowKeys {
		raw, ok := detection[key]
		if !ok || raw == nil {
			continue
		}
		win, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("detection.%s must be an object", key)
		}
		enabled, _ := win["enabled"].(bool)
		threshold, hasT := numericValue(win["threshold"])
		window, hasW := numericValue(win["window_seconds"])
		if hasT && threshold < 0 {
			return fmt.Errorf("detection.%s.threshold cannot be negative", key)
		}
		if hasW && window < 0 {
			return fmt.Errorf("detection.%s.window_seconds cannot be negative", key)
		}
		if enabled {
			if !hasT || threshold <= 0 {
				return fmt.Errorf("detection.%s is enabled — threshold must be > 0", key)
			}
			if !hasW || window <= 0 {
				return fmt.Errorf("detection.%s is enabled — window_seconds must be > 0", key)
			}
		}
	}
	return nil
}

// numericValue coerces a JSON-decoded numeric (encoding/json hands
// every number back as float64) to float64. Returns ok=false for
// non-numeric values so the caller can surface a type error.
func numericValue(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case int:
		return float64(n), true
	}
	return 0, false
}

// canWriteCollectorConfig gates the PUT to Admin / Owner. Uses the
// IsOwner boolean (never Role comparison) per the project's auth
// rules; Admin is allowed because collector config is base-project
// global, not group-scoped.
func (handler *AppHandler) canWriteCollectorConfig(c *gin.Context) bool {
	if owner, ok := c.Get("isOwner"); ok {
		if isOwner, _ := owner.(bool); isOwner {
			return true
		}
	}
	// `role` is set by the auth middleware via c.Set("role", claims.Role)
	// where claims.Role is a *models.Role — so the context value is a
	// pointer. Older call sites also stored it as models.Role or a plain
	// string; handle all three so the check is robust to either encoding.
	if role, ok := c.Get("role"); ok {
		switch r := role.(type) {
		case *models.Role:
			if r != nil && *r == models.RoleAdmin {
				return true
			}
		case models.Role:
			if r == models.RoleAdmin {
				return true
			}
		case string:
			if r == string(models.RoleAdmin) {
				return true
			}
		}
	}
	return false
}
