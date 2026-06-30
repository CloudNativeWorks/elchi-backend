package handlers

import (
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

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
	Issuer        string   `yaml:"issuer,omitempty"`
	Audience      string   `yaml:"audience,omitempty"`
	Algorithms    []string `yaml:"algorithms,omitempty"`
	PublicKeyFile string   `yaml:"public_key_file,omitempty"`
}
type yAPIKey struct {
	Source string `yaml:"source,omitempty"`
	Name   string `yaml:"name,omitempty"`
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
	Feeds []yIPRepFeed `yaml:"feeds,omitempty"`
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

// buildRoute turns one (host, path) group into a route + its rationale.
func buildRoute(g *routeGroup) (yRoute, suggestRationale) {
	methods := sortedKeys(g.methods)
	match := yMatch{Methods: methods}
	if i := strings.IndexByte(g.path, '{'); i >= 0 {
		// Parameterised path (e.g. /users/{id}) — match by the literal prefix.
		match.PathPrefix = g.path[:i]
	} else {
		match.PathExact = g.path
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
			eng.JWT = &yJWT{Issuer: "https://issuer.example.com/", Audience: "api", Algorithms: []string{"RS256"}, PublicKeyFile: "/etc/elchi/elchi-shield/keys/jwt-pub.pem"}
			engines = append(engines, suggestEngineWhy{"jwt", "JWT seen on this endpoint but not enforced — require a valid token. Fill issuer/audience/key."})
		case has(g.schemes, "apikey"):
			eng.APIKey = &yAPIKey{Source: "header", Name: "X-Api-Key"}
			engines = append(engines, suggestEngineWhy{"api_key", "API-key consumers seen — enforce hashed keys. Add your keys in the API Key form."})
		case has(g.schemes, "mtls"):
			eng.XFCC = &yXFCC{RequirePresent: true}
			engines = append(engines, suggestEngineWhy{"xfcc", "mTLS consumers seen — require a verified client certificate (XFCC)."})
		default:
			eng.JWT = &yJWT{Issuer: "https://issuer.example.com/", Audience: "api", Algorithms: []string{"RS256"}, PublicKeyFile: "/etc/elchi/elchi-shield/keys/jwt-pub.pem"}
			engines = append(engines, suggestEngineWhy{"jwt", "Endpoint accepts unauthenticated requests — add authentication (JWT shown; switch to api_key/xfcc as needed)."})
		}
		spec.FailMode = "fail_close" // auth should fail closed
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

	// 3) Scanners / probes → bot scoring.
	if hasAny(g.flags, "scanner_user_agent", "path_scan_suspect", "vuln_probe_path") {
		eng.Bot = &yBot{
			ScoreThreshold: 100,
			UserAgent:      &yBotUA{DenySubstrings: []string{"sqlmap", "nikto", "masscan", "nuclei"}, BlockEmpty: true},
			Heuristics:     &yBotHeur{RequireAccept: true, RequireAcceptLanguage: true, ScorePerAnomaly: 25},
		}
		engines = append(engines, suggestEngineWhy{"bot", "Scanner/probe traffic seen — block bad user-agents and score header anomalies."})
	}

	// 4) Threat-intel hit → IP reputation feed.
	if has(g.flags, "threat_intel_hit") {
		eng.IPReputation = &yIPRep{Feeds: []yIPRepFeed{{Name: "threat-intel", File: "/etc/elchi/elchi-shield/feeds/threat-intel.netset", Format: "firehol_netset", Severity: "high"}}}
		engines = append(engines, suggestEngineWhy{"ip_reputation", "Source IP matched a threat feed — block by reputation. Upload the feed in the Data Files tab."})
	}

	// 5) PII observed → DLP redaction on the response.
	if redact := mapPIIRedact(g.pii); len(redact) > 0 {
		spec.InspectResponseBody = true
		spec.MaxResponseBodyBytes = 1048576
		spec.ensureChecksBody()
		spec.Checks.Body.DLP = &yDLP{Direction: "response", Redact: redact}
		engines = append(engines, suggestEngineWhy{"dlp", "Sensitive data seen in traffic — redact " + strings.Join(redact, ", ") + " in the response body."})
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

func mapPIIRedact(pii map[string]struct{}) []string {
	// Only the PII kinds DLP can redact (model.ts allowlist): email, ssn, credit_card, jwt.
	out := []string{}
	for _, k := range []string{"email", "ssn", "credit_card", "jwt"} {
		if has(pii, k) {
			out = append(out, k)
		}
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
