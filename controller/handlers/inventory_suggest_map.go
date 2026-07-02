package handlers

import (
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// pathToRegex converts a normalized path (with optional {param} placeholders) into
// an anchored RE2 pattern that:
//   - is CASE-INSENSITIVE ((?i)) — so an attacker can't dodge the route (and its
//     engines) by varying path case (/users vs /Users), which would otherwise fall
//     through to the permissive file default;
//   - tolerates an optional TRAILING SLASH (/?$) — real traffic mixes /x and /x/;
//   - matches each {param} as exactly one path segment ([^/]+);
//   - regex-escapes literal runs so "/v1.0/{id}" is matched literally.
func pathToRegex(path string) string {
	p := strings.TrimRight(path, "/") // normalize away a trailing slash; /?$ re-adds tolerance
	var b strings.Builder
	b.WriteString("(?i)^")
	for i := 0; i < len(p); {
		if p[i] == '{' {
			j := strings.IndexByte(p[i:], '}')
			if j < 0 { // malformed: no closing brace — treat the rest as literal
				b.WriteString(regexp.QuoteMeta(p[i:]))
				i = len(p)
				break
			}
			b.WriteString("[^/]+")
			i += j + 1
			continue
		}
		k := strings.IndexByte(p[i:], '{')
		if k < 0 {
			b.WriteString(regexp.QuoteMeta(p[i:]))
			break
		}
		b.WriteString(regexp.QuoteMeta(p[i : i+k]))
		i += k
	}
	b.WriteString("/?$")
	return b.String()
}

// ── Suggested-policy YAML shape ──────────────────────────────────────────────
// These structs intentionally cover ONLY the field subset the UI policy Builder
// models (POLICY_SPEC_SCHEMA in src/pages/shield/state/model.ts). Emitting a field
// the Builder doesn't know would force it into raw-YAML mode. omitempty keeps the
// output minimal so the draft reads cleanly.

type yPolicy struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   yMeta  `yaml:"metadata"`
	Spec       ySpec  `yaml:"spec"`
}

type yMeta struct {
	Name string `yaml:"name"`
}

type ySpec struct {
	Defaults *yPolicySpec `yaml:"defaults,omitempty"`
	Domains  []yDomain    `yaml:"domains,omitempty"`
}

type yDomain struct {
	Hosts  []string `yaml:"hosts"`
	Routes []yRoute `yaml:"routes,omitempty"`
}

type yRoute struct {
	Match  yMatch       `yaml:"match"`
	Policy *yPolicySpec `yaml:"policy,omitempty"`
}

type yMatch struct {
	PathExact  string   `yaml:"path_exact,omitempty"`
	PathPrefix string   `yaml:"path_prefix,omitempty"`
	PathRegex  string   `yaml:"path_regex,omitempty"`
	Methods    []string `yaml:"methods,omitempty"`
}

type yPolicySpec struct {
	Mode                 string    `yaml:"mode,omitempty"`
	FailMode             string    `yaml:"fail_mode,omitempty"`
	InspectRequestBody   bool      `yaml:"inspect_request_body,omitempty"`
	InspectResponseBody  bool      `yaml:"inspect_response_body,omitempty"`
	MaxRequestBodyBytes  int64     `yaml:"max_request_body_bytes,omitempty"`
	MaxResponseBodyBytes int64     `yaml:"max_response_body_bytes,omitempty"`
	Engines              *yEngines `yaml:"engines,omitempty"`
	Checks               *yChecks  `yaml:"checks,omitempty"`
}

type yEngines struct {
	JWT          *yJWT       `yaml:"jwt,omitempty"`
	APIKey       *yAPIKey    `yaml:"api_key,omitempty"`
	XFCC         *yXFCC      `yaml:"xfcc,omitempty"`
	RateLimit    *yRateLimit `yaml:"rate_limit,omitempty"`
	Bot          *yBot       `yaml:"bot,omitempty"`
	IPReputation *yIPRep     `yaml:"ip_reputation,omitempty"`
	Coraza       *yCoraza    `yaml:"coraza,omitempty"`
}

type yChecks struct {
	Body *yBodyChecks `yaml:"body,omitempty"`
}
type yBodyChecks struct {
	DLP *yDLP `yaml:"dlp,omitempty"`
}
type yDLP struct {
	Direction string   `yaml:"direction,omitempty"`
	Block     []string `yaml:"block,omitempty"`
	Redact    []string `yaml:"redact,omitempty"`
}

type yJWT struct {
	Issuer     string   `yaml:"issuer,omitempty"`
	Audience   string   `yaml:"audience,omitempty"`
	Algorithms []string `yaml:"algorithms,omitempty"`
	HMACSecret string   `yaml:"hmac_secret,omitempty"`
}
type yAPIKeyEntry struct {
	SHA256  string `yaml:"sha256"`
	Subject string `yaml:"subject,omitempty"`
}
type yAPIKey struct {
	Source string         `yaml:"source,omitempty"`
	Name   string         `yaml:"name,omitempty"`
	Keys   []yAPIKeyEntry `yaml:"keys,omitempty"`
}
type yXFCC struct {
	RequirePresent bool `yaml:"require_present,omitempty"`
}
type yRateLimit struct {
	RequestsPerSecond int    `yaml:"requests_per_second,omitempty"`
	Burst             int    `yaml:"burst,omitempty"`
	Key               string `yaml:"key,omitempty"`
}
type yBotUA struct {
	DenySubstrings []string `yaml:"deny_substrings,omitempty"`
	BlockEmpty     bool     `yaml:"block_empty,omitempty"`
}
type yBotHeur struct {
	RequireAccept         bool `yaml:"require_accept,omitempty"`
	RequireAcceptLanguage bool `yaml:"require_accept_language,omitempty"`
	ScorePerAnomaly       int  `yaml:"score_per_anomaly,omitempty"`
}
type yBot struct {
	ScoreThreshold int       `yaml:"score_threshold,omitempty"`
	UserAgent      *yBotUA   `yaml:"user_agent,omitempty"`
	Heuristics     *yBotHeur `yaml:"heuristics,omitempty"`
}
type yIPRepFeed struct {
	Name     string `yaml:"name"`
	File     string `yaml:"file"`
	Format   string `yaml:"format"`
	Severity string `yaml:"severity,omitempty"`
}
type yIPRep struct {
	DenyCIDRs []string     `yaml:"deny_cidrs,omitempty"`
	Feeds     []yIPRepFeed `yaml:"feeds,omitempty"`
}
type yCoraza struct {
	IncludeOwasp bool `yaml:"include_owasp,omitempty"`
}

// ── Rationale (the "why" surfaced in the UI) ─────────────────────────────────

type suggestRationale struct {
	Host         string             `json:"host"`
	Path         string             `json:"path"`
	Methods      []string           `json:"methods"`
	Mode         string             `json:"mode"`
	MatchedFlags []string           `json:"matched_flags"`
	Engines      []suggestEngineWhy `json:"engines"`
	Notes        []string           `json:"notes,omitempty"`
}
type suggestEngineWhy struct {
	Key string `json:"key"`
	Why string `json:"why"`
}

// postureFlags are config-hygiene findings handled at the Envoy/HCM layer, not by
// a shield engine. We surface them as a note rather than inventing an engine.
var postureFlags = map[string]string{
	"permissive_cors":                "Tighten Access-Control-Allow-Origin at the Envoy HCM (CORS filter).",
	"missing_hsts":                   "Add Strict-Transport-Security at the listener (Envoy).",
	"missing_csp":                    "Add a Content-Security-Policy header (Envoy).",
	"missing_x_frame_options":        "Add X-Frame-Options to defend against clickjacking (Envoy).",
	"missing_x_content_type_options": "Add X-Content-Type-Options: nosniff (Envoy).",
	"weak_tls_version":               "Disable TLS 1.0/1.1 on the listener (Envoy TLS context).",
	"plain_text_transport":           "Serve over TLS / redirect HTTP→HTTPS (Envoy).",
	"version_disclosure":             "Strip Server / X-Powered-By headers (Envoy).",
}

func has(set map[string]struct{}, k string) bool { _, ok := set[k]; return ok }
func hasAny(set map[string]struct{}, keys ...string) bool {
	for _, k := range keys {
		if has(set, k) {
			return true
		}
	}
	return false
}

// routeGroup merges all selected docs that share (host, normalized_path).
type routeGroup struct {
	host     string
	path     string
	methods  map[string]struct{}
	flags    map[string]struct{}
	pii      map[string]struct{}
	cats     map[string]struct{}
	schemes  map[string]struct{}
	noauth   bool
	maxScore int
}

// buildSuggestedPolicy maps inventory findings to a draft SecurityPolicy.
func buildSuggestedPolicy(name string, docs []suggestInvDoc) (string, []suggestRationale, error) {
	// Group by (host, path); one route per group covering the selected methods.
	groups := map[string]*routeGroup{}
	order := []string{}
	for _, d := range docs {
		key := d.Host + "\x00" + d.NormalizedPath
		g := groups[key]
		if g == nil {
			g = &routeGroup{
				host: d.Host, path: d.NormalizedPath,
				methods: map[string]struct{}{}, flags: map[string]struct{}{},
				pii: map[string]struct{}{}, cats: map[string]struct{}{}, schemes: map[string]struct{}{},
			}
			groups[key] = g
			order = append(order, key)
		}
		if d.Method != "" {
			g.methods[strings.ToUpper(d.Method)] = struct{}{}
		}
		for _, f := range d.RiskFlags {
			g.flags[f] = struct{}{}
		}
		for _, p := range d.PIICategories {
			g.pii[p] = struct{}{}
		}
		for _, ec := range d.EndpointCats {
			g.cats[ec] = struct{}{}
		}
		for _, s := range d.AuthSchemes {
			g.schemes[s] = struct{}{}
		}
		g.noauth = g.noauth || d.NoauthObserved
		if d.MaxRiskScore > g.maxScore {
			g.maxScore = d.MaxRiskScore
		}
	}

	// Group routes by host → domain.
	domainOf := map[string]*yDomain{}
	var domainOrder []string
	var rationale []suggestRationale

	for _, key := range order {
		g := groups[key]
		route, why := buildRoute(g)

		dom := domainOf[g.host]
		if dom == nil {
			dom = &yDomain{Hosts: []string{hostOrWildcard(g.host)}}
			domainOf[g.host] = dom
			domainOrder = append(domainOrder, g.host)
		}
		dom.Routes = append(dom.Routes, route)
		rationale = append(rationale, why)
	}

	pol := yPolicy{
		APIVersion: "sentinel.elchi.io/v1",
		Kind:       "SecurityPolicy",
		Metadata:   yMeta{Name: name},
		Spec: ySpec{
			// Safe baseline; each route overrides mode by severity.
			Defaults: &yPolicySpec{Mode: "detect", FailMode: "fail_open"},
		},
	}
	for _, h := range domainOrder {
		pol.Spec.Domains = append(pol.Spec.Domains, *domainOf[h])
	}

	out, err := yaml.Marshal(&pol)
	if err != nil {
		return "", nil, err
	}
	return string(out), rationale, nil
}

// Placeholders emitted by auth suggestions: they LOAD (so the draft can be saved
// and deployed) and FAIL CLOSED (block all traffic) until the operator supplies
// the real credential — never a working backdoor.
const (
	// placeholderJWTSecret is a non-empty HS256 secret so the jwt engine builds; no
	// real token verifies against it, so every request is blocked until replaced.
	placeholderJWTSecret = "CHANGE_ME__set_a_real_HS256_secret_or_switch_to_public_key_file_or_jwks"
	// placeholderNoKeySHA256 is a sha256 with no practical pre-image (all zeros), so
	// no presented API key ever matches — the engine loads and denies all until the
	// operator adds real key hashes.
	placeholderNoKeySHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
)

// buildRoute turns one (host, path) group into a route + its rationale.
func buildRoute(g *routeGroup) (yRoute, suggestRationale) {
	// Observed methods are kept for the rationale (context for the operator) but are
	// deliberately NOT put on the route match. SECURITY: the engines the suggestion
	// emits are negative-security (auth/WAF/rate-limit) that must apply to EVERY
	// method on the endpoint. Restricting to the observed methods would let an
	// attacker hit an unobserved method (e.g. POST/DELETE on an endpoint the
	// collector only ever saw via GET) — the request would fail to match the route
	// and fall through to the permissive file default, bypassing auth entirely. So
	// the match covers all methods; per-method positive security is a separate,
	// explicit choice the user can add in the Builder.
	methods := sortedKeys(g.methods)
	match := yMatch{}
	// Target the discovered endpoint with a precise, case-insensitive, trailing-
	// slash-tolerant regex (see pathToRegex). Case-insensitivity matters for
	// SECURITY too: a case-sensitive route would let an attacker send "/Users/1" to
	// dodge the route's engines. An empty path degenerates to an explicit "/" prefix
	// rather than a silent catch-all.
	if g.path == "" {
		match.PathPrefix = "/"
	} else {
		match.PathRegex = pathToRegex(g.path)
	}

	mode := "detect"
	if g.maxScore >= suggestBlockScoreThreshold {
		mode = "block"
	}
	authEndpoint := has(g.cats, "auth_endpoint") || has(g.cats, "payment_endpoint") || has(g.cats, "admin_endpoint")

	spec := &yPolicySpec{Mode: mode}
	eng := &yEngines{}
	var engines []suggestEngineWhy

	// 1) Authentication — missing/inconsistent auth, or BOLA/BFLA probing.
	if g.noauth || hasAny(g.flags, "bola_suspect", "bfla_suspect", "auth_inconsistent") {
		switch {
		case has(g.schemes, "jwt"):
			eng.JWT = &yJWT{Issuer: "https://issuer.example.com/", Audience: "api", Algorithms: []string{"HS256"}, HMACSecret: placeholderJWTSecret}
			engines = append(engines, suggestEngineWhy{"jwt", "JWT seen but not enforced. This HS256 placeholder blocks ALL tokens until you set your real secret — or switch to public_key_file (RS256) / jwks (OIDC) and upload the key via Data Files."})
		case has(g.schemes, "apikey"):
			eng.APIKey = &yAPIKey{Source: "header", Name: "X-Api-Key", Keys: []yAPIKeyEntry{{SHA256: placeholderNoKeySHA256, Subject: "placeholder-replace-me"}}}
			engines = append(engines, suggestEngineWhy{"api_key", "API-key consumers seen. This placeholder key hash matches nothing (blocks all) until you replace it with your real key hashes."})
		case has(g.schemes, "mtls"):
			eng.XFCC = &yXFCC{RequirePresent: true}
			engines = append(engines, suggestEngineWhy{"xfcc", "mTLS consumers seen — require a verified client certificate (XFCC). Envoy must forward XFCC."})
		default:
			eng.JWT = &yJWT{Issuer: "https://issuer.example.com/", Audience: "api", Algorithms: []string{"HS256"}, HMACSecret: placeholderJWTSecret}
			engines = append(engines, suggestEngineWhy{"jwt", "Endpoint accepts unauthenticated requests. This HS256 placeholder blocks all tokens until configured — switch to api_key / xfcc / jwks as fits your setup."})
		}
		// Auth should fail closed ONLY when the route enforces (block). In detect
		// mode fail_close would still return a real 403 on an engine error/timeout,
		// breaking the "detect never blocks" contract — so detect routes stay
		// fail_open (the default). Once the user promotes the route to block, flip
		// fail_mode to fail_close.
		if mode == "block" {
			spec.FailMode = "fail_close"
		}
	}

	// 2) Abuse / volume → rate limit.
	if hasAny(g.flags, "rate_anomaly", "ip_rate_anomaly", "brute_force_suspect", "payment_abuse_suspect") {
		rps, burst := 100, 200
		if authEndpoint || has(g.flags, "brute_force_suspect") {
			rps, burst = 10, 20
		}
		eng.RateLimit = &yRateLimit{RequestsPerSecond: rps, Burst: burst, Key: "ip"}
		engines = append(engines, suggestEngineWhy{"rate_limit", "Volume/brute-force signal — throttle per client IP (needs use_remote_address on the HCM)."})
	}

	// 3) Scanners / probes → bot scoring. score_threshold MUST be reachable by the
	// heuristics we emit, or the header-anomaly layer is dead: with two anomaly
	// sources at score_per_anomaly=25 the max achievable score is 50, so the
	// threshold is 50 (a request missing BOTH Accept and Accept-Language — a strong,
	// low-false-positive bot signal, since browsers send both — blocks). The UA
	// deny-list and empty-UA check are independent hard blocks.
	if hasAny(g.flags, "scanner_user_agent", "path_scan_suspect", "vuln_probe_path") {
		eng.Bot = &yBot{
			ScoreThreshold: 50,
			UserAgent:      &yBotUA{DenySubstrings: []string{"sqlmap", "nikto", "masscan", "nuclei"}, BlockEmpty: true},
			Heuristics:     &yBotHeur{RequireAccept: true, RequireAcceptLanguage: true, ScorePerAnomaly: 25},
		}
		engines = append(engines, suggestEngineWhy{"bot", "Scanner/probe traffic seen — block bad/empty user-agents and block requests missing both Accept and Accept-Language (a bot signal)."})
	}

	// 4) Threat-intel hit → IP reputation. Emit a harmless TEST-NET placeholder CIDR
	// (RFC 5737 — matches no real client) so the engine loads without a feed file;
	// the operator swaps in the malicious ranges or adds a feed via Data Files.
	if has(g.flags, "threat_intel_hit") {
		eng.IPReputation = &yIPRep{DenyCIDRs: []string{"192.0.2.0/24"}}
		engines = append(engines, suggestEngineWhy{"ip_reputation", "Source IP matched a threat feed. Replace this placeholder CIDR (192.0.2.0/24, blocks nothing real) with the malicious ranges, or add a threat feed via Data Files. Needs Envoy to supply the client IP (use_remote_address / XFF), else it can't match."})
	}

	// 5) Sensitive data → DLP. The collector's pii_categories/secret_in_path signals
	// are derived from the REQUEST URL (ALS logs carry no bodies), so the evidence is
	// request-side: a write endpoint receives PII/secrets inbound, a read echoes them
	// in the response. Redact in BOTH directions so neither leg leaks, and BLOCK hard
	// secrets inbound when a credential was seen in the URL (secret_in_path/jwt_in_path).
	redact := mapPIIRedact(g.pii)
	credInURL := has(g.pii, "secret_in_path") || has(g.pii, "jwt_in_path")
	if len(redact) > 0 || credInURL {
		spec.InspectRequestBody = true
		if spec.MaxRequestBodyBytes == 0 {
			spec.MaxRequestBodyBytes = 1048576
		}
		spec.InspectResponseBody = true
		spec.MaxResponseBodyBytes = 1048576
		spec.ensureChecksBody()
		dlp := &yDLP{Direction: "both"}
		if len(redact) > 0 {
			dlp.Redact = redact
		}
		why := "Sensitive data seen — redact " + strings.Join(redact, ", ") + " in request and response bodies."
		if credInURL {
			dlp.Block = []string{"private_key", "aws_access_key", "google_api_key", "slack_token", "github_token"}
			why = "Credential/PII exposure seen — block hard secrets and redact PII in request and response bodies."
		}
		spec.Checks.Body.DLP = dlp
		engines = append(engines, suggestEngineWhy{"dlp", why})
	}

	// 6) Payment/admin/auth surface or high risk → WAF (OWASP CRS).
	if authEndpoint || g.maxScore >= suggestBlockScoreThreshold {
		eng.Coraza = &yCoraza{IncludeOwasp: true}
		spec.InspectRequestBody = true
		if spec.MaxRequestBodyBytes == 0 {
			spec.MaxRequestBodyBytes = 1048576
		}
		engines = append(engines, suggestEngineWhy{"coraza", "Sensitive/high-risk surface — run the full OWASP Core Rule Set on the request body."})
	}

	if !engEmpty(eng) {
		spec.Engines = eng
	}
	route := yRoute{Match: match, Policy: spec}

	why := suggestRationale{
		Host: g.host, Path: g.path, Methods: methods, Mode: mode,
		MatchedFlags: sortedKeys(g.flags), Engines: engines, Notes: postureNotes(g.flags),
	}
	return route, why
}

// ensureChecksBody lazily builds the checks.body tree so DLP can attach.
func (s *yPolicySpec) ensureChecksBody() {
	if s.Checks == nil {
		s.Checks = &yChecks{}
	}
	if s.Checks.Body == nil {
		s.Checks.Body = &yBodyChecks{}
	}
}

// mapPIIRedact maps the collector's observed PII categories to shield DLP redact
// kinds. The collector emits email/ssn/credit_card/iban/phone plus jwt_in_path; of
// these shield's DLP can redact email/ssn/credit_card/jwt (iban/phone have no DLP
// kind and are dropped). Note the collector's URL-JWT signal is "jwt_in_path", not
// a bare "jwt" — mapping it here is what makes the jwt redact actually fire.
func mapPIIRedact(pii map[string]struct{}) []string {
	out := []string{}
	for _, k := range []string{"email", "ssn", "credit_card"} {
		if has(pii, k) {
			out = append(out, k)
		}
	}
	if has(pii, "jwt_in_path") {
		out = append(out, "jwt")
	}
	return out
}

func postureNotes(flags map[string]struct{}) []string {
	var notes []string
	for f := range flags {
		if n, ok := postureFlags[f]; ok && n != "" {
			notes = append(notes, n)
		}
	}
	sort.Strings(notes)
	return notes
}

func engEmpty(e *yEngines) bool {
	return e.JWT == nil && e.APIKey == nil && e.XFCC == nil && e.RateLimit == nil &&
		e.Bot == nil && e.IPReputation == nil && e.Coraza == nil
}

func hostOrWildcard(h string) string {
	if strings.TrimSpace(h) == "" {
		return "*"
	}
	return h
}
