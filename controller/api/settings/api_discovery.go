package settings

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
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

	// Cap the request body before binding — ShouldBindJSON unmarshals the
	// whole payload in-memory with no streaming, so an oversized upload
	// would OOM the controller before any validation runs.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCollectorConfigBytes)

	var body apiDiscoveryConfigUpdate
	if err := c.ShouldBindJSON(&body); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			c.JSON(http.StatusRequestEntityTooLarge,
				gin.H{"message": fmt.Sprintf("request body exceeds the %d-byte limit for collector config", maxCollectorConfigBytes)})
			return
		}
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
	"replay", "path_scan", "geo_spread", "ip_rate", "normalize_gap",
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

const (
	// maxCollectorConfigBytes caps the PUT body for the collector config
	// doc. The doc is a few KB even with generous operator lists; 1 MiB
	// is far above any legitimate payload while bounding in-memory
	// unmarshalling of a hostile or careless upload (ShouldBindJSON has
	// no streaming — it reads the whole body before validation runs).
	maxCollectorConfigBytes = 1 << 20

	// maxNormalizePatterns caps how many operator path-normalization
	// rules a config may carry — mirrors the collector's cap and bounds
	// the combined per-placeholder alternation the collector builds.
	maxNormalizePatterns = 64

	// maxConfigRegexLength / maxConfigRegexQuantifiers bound any
	// operator-supplied regex in the config (ingest_deny_patterns and
	// path_normalize_patterns). Mirror the collector's maxDenyPattern*.
	// Go's regexp is RE2 — linear-time, no catastrophic backtracking —
	// so these are belt-and-suspenders DoS bounds, not the only defence.
	maxConfigRegexLength      = 256
	maxConfigRegexQuantifiers = 4
)

// allowedNormalizePlaceholders is the set an operator may target with a
// path-normalization rule. Mirrors the collector's
// allowedNormalizePlaceholders — the built-in placeholder tokens MINUS
// `traversal`, which is a security signal, not an operator-selectable bucket.
var allowedNormalizePlaceholders = map[string]struct{}{
	"id": {}, "uuid": {}, "objectid": {}, "ulid": {}, "token": {}, "dynamic": {},
}

// normalizeBroadnessProbes are plainly-static path segments. A custom
// normalization regex that matches two or more of them is too broad — it
// would template real static route segments — and is rejected. Mirrors the
// collector's normalizeBroadnessProbes.
var normalizeBroadnessProbes = []string{"users", "api", "health", "orders", "v1", "a", "x"}

// validateCollectorPolicy checks the policy sub-tree:
//   - ingest_deny_patterns: operator path-deny regexes
//   - trusted_proxy_cidrs: NAT/CGNAT/LB egress CIDR prefixes
//   - path_normalize_patterns: {regex, placeholder} normalization rules
//   - exclude: six-dimension ingest-exclusion rules (policy.exclude)
//   - raw_sample_rate: benign raw-event sampling divisor (whole, >= 0)
//
// Each rule mirrors the collector's runtimeconfig Doc.Validate(); the
// collector remains the authority — this is the operator-facing subset
// that turns a bad config into an immediate 400 instead of a silent
// collector reject on the next poll.
func validateCollectorPolicy(policy bson.M) error {
	if err := validateConfigRegexList(policy["ingest_deny_patterns"], "policy.ingest_deny_patterns"); err != nil {
		return err
	}
	if err := validateConfigCIDRList(policy["trusted_proxy_cidrs"], "policy.trusted_proxy_cidrs"); err != nil {
		return err
	}
	if raw := policy["path_normalize_patterns"]; raw != nil {
		if err := validatePathNormalizePatterns(raw); err != nil {
			return err
		}
	}
	if raw := policy["exclude"]; raw != nil {
		if err := validateExcludeRules(raw); err != nil {
			return err
		}
	}
	if raw, ok := policy["raw_sample_rate"]; ok && raw != nil {
		// raw_sample_rate: 0/1 keeps every raw event; >=2 writes only
		// 1-in-N benign 2xx events. The collector's field is an int and
		// it rejects a negative value — mirror both checks.
		n, isNum := numericValue(raw)
		if !isNum {
			return fmt.Errorf("policy.raw_sample_rate must be a number")
		}
		if n != float64(int64(n)) {
			return fmt.Errorf("policy.raw_sample_rate must be a whole number")
		}
		if n < 0 {
			return fmt.Errorf("policy.raw_sample_rate must be >= 0 (< 2 keeps all raw events)")
		}
	}
	return nil
}

// validateConfigRegexList validates an optional array of operator regexes
// (ingest_deny_patterns, exclude.hosts, exclude.user_agents): each entry a
// string within the length / quantifier caps that compiles. Blank entries
// are skipped — the collector's Normalize() drops them. A nil raw means the
// dimension is absent (feature off for that key).
func validateConfigRegexList(raw any, field string) error {
	if raw == nil {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("%s must be an array", field)
	}
	for i, v := range list {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s[%d] must be a string", field, i)
		}
		if strings.TrimSpace(s) == "" {
			continue
		}
		if len(s) > maxConfigRegexLength {
			return fmt.Errorf("%s[%d] regex too long: %d > %d", field, i, len(s), maxConfigRegexLength)
		}
		if n := countRegexQuantifiers(s); n > maxConfigRegexQuantifiers {
			return fmt.Errorf("%s[%d] has too many quantifiers (%d > %d) — likely a DoS pattern", field, i, n, maxConfigRegexQuantifiers)
		}
		if _, err := regexp.Compile(s); err != nil {
			return fmt.Errorf("%s[%d] is not a valid regex: %v", field, i, err)
		}
	}
	return nil
}

// validateConfigCIDRList validates an optional array of CIDR prefixes
// (trusted_proxy_cidrs, exclude.source_cidrs). Blank entries are skipped —
// the collector's Normalize() drops them.
func validateConfigCIDRList(raw any, field string) error {
	if raw == nil {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("%s must be an array", field)
	}
	for i, v := range list {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s[%d] must be a string", field, i)
		}
		if strings.TrimSpace(s) == "" {
			continue
		}
		if _, err := netip.ParsePrefix(strings.TrimSpace(s)); err != nil {
			return fmt.Errorf("%s[%d] is not a valid CIDR: %v", field, i, err)
		}
	}
	return nil
}

// validateConfigStringList validates an optional array of plain exact-match
// strings (exclude.methods / listeners / projects). The collector applies
// no content rule beyond trimming, so this only enforces the
// array-of-strings shape.
func validateConfigStringList(raw any, field string) error {
	if raw == nil {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("%s must be an array", field)
	}
	for i, v := range list {
		if _, ok := v.(string); !ok {
			return fmt.Errorf("%s[%d] must be a string", field, i)
		}
	}
	return nil
}

// validateExcludeRules mirrors the collector's validateExcludeRules for the
// policy.exclude sub-tree: the host / user-agent regexes obey the regex caps
// and compile, the source CIDRs parse, and the method / listener / project
// entries are strings (the collector applies no further rule to those).
func validateExcludeRules(raw any) error {
	ex, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("policy.exclude must be an object")
	}
	if err := validateConfigRegexList(ex["hosts"], "policy.exclude.hosts"); err != nil {
		return err
	}
	if err := validateConfigRegexList(ex["user_agents"], "policy.exclude.user_agents"); err != nil {
		return err
	}
	if err := validateConfigCIDRList(ex["source_cidrs"], "policy.exclude.source_cidrs"); err != nil {
		return err
	}
	for _, dim := range []string{"methods", "listeners", "projects"} {
		if err := validateConfigStringList(ex[dim], "policy.exclude."+dim); err != nil {
			return err
		}
	}
	return nil
}

// validatePathNormalizePatterns mirrors the collector's path-normalization
// rule validation (runtimeconfig Doc.Validate + validateNormalizePattern).
// Each rule is a {regex, placeholder} pair: the regex matches a whole path
// segment the built-in detectors left literal, and the segment collapses to
// the chosen placeholder. Blank-regex entries are skipped — the collector's
// Normalize() drops them. The collector remains the authority; this catches
// the common operator mistakes BEFORE the write so they surface as a 400.
func validatePathNormalizePatterns(raw any) error {
	patterns, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("policy.path_normalize_patterns must be an array")
	}
	if len(patterns) > maxNormalizePatterns {
		return fmt.Errorf("policy.path_normalize_patterns: %d patterns exceeds cap %d", len(patterns), maxNormalizePatterns)
	}
	for i, p := range patterns {
		obj, ok := p.(map[string]any)
		if !ok {
			return fmt.Errorf("policy.path_normalize_patterns[%d] must be an object", i)
		}
		// Type-check explicitly: a non-string regex/placeholder must be a
		// hard 400, not silently coerced — the collector BSON-decodes the
		// doc into []NormalizePattern and a non-string value there would
		// fail the decode and make it reject the whole config.
		regex, err := stringField(obj, "regex", i)
		if err != nil {
			return err
		}
		placeholder, err := stringField(obj, "placeholder", i)
		if err != nil {
			return err
		}
		regex = strings.TrimSpace(regex)
		placeholder = strings.TrimSpace(placeholder)
		if regex == "" {
			continue
		}
		if err := validateNormalizePatternEntry(i, regex, placeholder); err != nil {
			return err
		}
	}
	return nil
}

// stringField extracts obj[key] as a string. An absent key yields "" with
// no error (the caller treats a blank regex as a skip); a present key whose
// value is not a string yields a typed error so the caller can 400.
func stringField(obj map[string]any, key string, idx int) (string, error) {
	v, ok := obj[key]
	if !ok {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("policy.path_normalize_patterns[%d].%s must be a string", idx, key)
	}
	return s, nil
}

// validateNormalizePatternEntry checks one {regex, placeholder} rule: a known
// placeholder, a regex within the length / quantifier caps that compiles in
// the anchored form the collector runs (`^(?:body)$`), and a regex narrow
// enough that it would not template genuine static route segments.
func validateNormalizePatternEntry(idx int, regex, placeholder string) error {
	if _, ok := allowedNormalizePlaceholders[placeholder]; !ok {
		return fmt.Errorf("policy.path_normalize_patterns[%d]: placeholder %q is not one of id|uuid|objectid|ulid|token|dynamic", idx, placeholder)
	}
	if len(regex) > maxConfigRegexLength {
		return fmt.Errorf("policy.path_normalize_patterns[%d]: regex too long: %d > %d", idx, len(regex), maxConfigRegexLength)
	}
	if n := countRegexQuantifiers(regex); n > maxConfigRegexQuantifiers {
		return fmt.Errorf("policy.path_normalize_patterns[%d]: regex has too many quantifiers (%d > %d) — likely a DoS pattern", idx, n, maxConfigRegexQuantifiers)
	}
	body := stripRegexAnchors(regex)
	if body == "" {
		return fmt.Errorf("policy.path_normalize_patterns[%d]: regex is empty", idx)
	}
	re, err := regexp.Compile("^(?:" + body + ")$")
	if err != nil {
		return fmt.Errorf("policy.path_normalize_patterns[%d]: invalid regex: %v", idx, err)
	}
	hits := 0
	for _, probe := range normalizeBroadnessProbes {
		if re.MatchString(probe) {
			hits++
		}
	}
	if hits >= 2 {
		return fmt.Errorf("policy.path_normalize_patterns[%d]: regex is too broad — it would template static path segments", idx)
	}
	return nil
}

// countRegexQuantifiers counts unescaped quantifier metacharacters (+ * ? {)
// outside character classes — a DoS-pattern heuristic. Mirrors the
// collector's countQuantifiers.
func countRegexQuantifiers(p string) int {
	n := 0
	inClass := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c == '\\' && i+1 < len(p) {
			i++
			continue
		}
		if c == '[' {
			inClass = true
			continue
		}
		if c == ']' {
			inClass = false
			continue
		}
		if inClass {
			continue
		}
		switch c {
		case '+', '*', '?', '{':
			n++
		}
	}
	return n
}

// stripRegexAnchors drops a single leading `^` and trailing unescaped `$` —
// the collector strips them before splicing the body into its per-placeholder
// alternation. Mirrors the collector's stripRegexAnchors.
func stripRegexAnchors(s string) string {
	s = strings.TrimPrefix(s, "^")
	if strings.HasSuffix(s, "$") && !strings.HasSuffix(s, `\$`) {
		s = s[:len(s)-1]
	}
	return s
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
		// threshold_ip is the source-IP-keyed fallback threshold (brute_force);
		// the collector's validateWindow rejects a negative value.
		if tip, hasTIP := numericValue(win["threshold_ip"]); hasTIP && tip < 0 {
			return fmt.Errorf("detection.%s.threshold_ip cannot be negative", key)
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
