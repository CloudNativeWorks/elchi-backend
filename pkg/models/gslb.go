package models

import (
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// GSLBRecord represents a Global Server Load Balancing DNS record
// NOTE: IP health data is stored in separate gslb_ip_health collection
type GSLBRecord struct {
	ID           primitive.ObjectID `bson:"_id,omitempty" json:"id,omitempty"`
	FQDN         string             `bson:"fqdn" json:"fqdn"`
	ServiceID    string             `bson:"service_id,omitempty" json:"service_id,omitempty"` // Reference to services collection ObjectID (empty for manual records)
	Project      string             `bson:"project" json:"project"`
	Version      string             `bson:"version" json:"version"`
	Zone         string             `bson:"zone" json:"zone"`
	FailoverZone string             `bson:"failover_zone,omitempty" json:"failover_zone,omitempty"` // Per-record failover zone (e.g., "asya-gslb.elchi") - defaults to first zone in settings.FailoverZones
	ShardID      int                `bson:"shard_id" json:"shard_id"`
	Enabled      bool               `bson:"enabled" json:"enabled"`
	TTL          uint32             `bson:"ttl" json:"ttl"`                         // REQUIRED - User must provide for manual records, auto-set from DefaultTTL for auto-created
	Probe        *GSLBProbe         `bson:"probe,omitempty" json:"probe,omitempty"` // Optional health check configuration
	CreatedAt    time.Time          `bson:"created_at" json:"created_at"`
	UpdatedAt    time.Time          `bson:"updated_at" json:"updated_at"`
	CreatedBy    string             `bson:"created_by" json:"created_by"`
	// NOTE: No Permissions field - Access control: Only Admin/Owner can modify, all roles can view
}

// GSLBStatusHistory represents a single status change event for an IP
type GSLBStatusHistory struct {
	State        string    `bson:"state" json:"state"`                                     // "healthy" or "unhealthy"
	DateTime     time.Time `bson:"datetime" json:"datetime"`                               // Timestamp of the status change
	ResponseCode int       `bson:"response_code,omitempty" json:"response_code,omitempty"` // HTTP status code (200, 500, etc.) or 0 for connection errors
	ResponseTime float64   `bson:"response_time,omitempty" json:"response_time,omitempty"` // Response time in seconds (e.g., 0.125 = 125ms)
	ErrorMessage string    `bson:"error_message,omitempty" json:"error_message,omitempty"` // Error message for failed probes (helps with troubleshooting)
}

// GSLBProbe represents health check configuration for a GSLB record
// Uses tri-state health model (passing/warning/critical)
// NOTE: ConsecutiveFailures/Successes are IN-MEMORY ONLY (not stored in MongoDB)
// health_checker.go maintains map[string]*IPHealthCounter for these counters
type GSLBProbe struct {
	Type       string  `bson:"type" json:"type"`                                   // "http", "https", "tcp" (NO "icmp" - security risk)
	Port       int     `bson:"port,omitempty" json:"port,omitempty"`               // Target port for health check
	Path       string  `bson:"path,omitempty" json:"path,omitempty"`               // HTTP/HTTPS only - request path
	HostHeader string  `bson:"host_header,omitempty" json:"host_header,omitempty"` // HTTP/HTTPS only - Host header for virtual hosting
	Interval   int     `bson:"interval" json:"interval"`                           // Seconds - STRICT intervals only (10, 20, 30, 60, 90, 120, 180, 300)
	Timeout    float64 `bson:"timeout" json:"timeout"`                             // Seconds with ms precision (0.1-3.0s, e.g., 0.5 = 500ms)
	Enabled    *bool   `bson:"enabled,omitempty" json:"enabled,omitempty"`         // Enable/disable probe execution (nil or true = enabled, false = disabled, keeps config when disabled)

	// Tri-state thresholds (REQUIRED - no defaults)
	WarningThreshold  int `bson:"warning_threshold" json:"warning_threshold" validate:"required,min=1,max=10"`   // Failures before warning state (e.g., 1-3)
	CriticalThreshold int `bson:"critical_threshold" json:"critical_threshold" validate:"required,min=2,max=20"` // Failures before critical state (e.g., 3-10)
	PassingThreshold  int `bson:"passing_threshold,omitempty" json:"passing_threshold,omitempty"`                // Successes before passing state (default: 1, max: 10) - anti-flapping

	ExpectedStatusCodes []string            `bson:"expected_status_codes,omitempty" json:"expected_status_codes,omitempty"` // HTTP/HTTPS only - Expected status codes (e.g., ["200-299", "301", "302"]) - defaults to ["200-399"] if empty
	FollowRedirects     *bool               `bson:"follow_redirects" json:"follow_redirects"`                               // HTTP/HTTPS only - Follow HTTP redirects (default: true if nil)
	SkipSSLVerify       *bool               `bson:"skip_ssl_verify" json:"skip_ssl_verify"`                                 // HTTPS only - Skip SSL certificate verification (default: false if nil, use true for self-signed certs)
	compiledStatusCodes []statusCodeMatcher `bson:"-" json:"-"`                                                             // Compiled status code matchers (not stored in DB, computed on load)
}

// statusCodeMatcher represents a compiled status code matcher for fast matching
type statusCodeMatcher struct {
	isRange bool
	start   int
	end     int
	exact   int
}

// MatchesStatusCode checks if a status code matches the expected codes
// Uses pre-compiled matchers for performance
func (p *GSLBProbe) MatchesStatusCode(statusCode int) bool {
	// Lazy compilation if not yet done
	if p.compiledStatusCodes == nil && len(p.ExpectedStatusCodes) > 0 {
		p.CompileStatusCodes()
	}

	// Default to 200-399 if no matchers
	if len(p.compiledStatusCodes) == 0 {
		return statusCode >= 200 && statusCode < 400
	}

	// Fast matching using compiled matchers
	for _, matcher := range p.compiledStatusCodes {
		if matcher.isRange {
			if statusCode >= matcher.start && statusCode <= matcher.end {
				return true
			}
		} else {
			if statusCode == matcher.exact {
				return true
			}
		}
	}

	return false
}

// IsEnabled returns true if probe is enabled (nil or true)
// When Enabled is nil, it defaults to true for backward compatibility
// When Enabled is false, probe config is kept but probing is skipped
func (p *GSLBProbe) IsEnabled() bool {
	if p.Enabled == nil {
		return true // Default: enabled
	}
	return *p.Enabled
}

// GetPassingThreshold returns the passing threshold with default value
// Default is 1 (single success = PASSING) for backward compatibility
// When > 1, IP must have consecutive successes to transition from CRITICAL/WARNING to PASSING
// This prevents flapping (rapid UP/DOWN oscillation) for unstable endpoints
func (p *GSLBProbe) GetPassingThreshold() int {
	if p.PassingThreshold <= 0 {
		return 1 // Default: single success = PASSING
	}
	if p.PassingThreshold > 10 {
		return 10 // Cap at 10 to prevent excessive recovery time
	}
	return p.PassingThreshold
}

// CompileStatusCodes pre-compiles status code matchers for fast matching
// This is called automatically on first use (lazy compilation)
func (p *GSLBProbe) CompileStatusCodes() {
	if len(p.ExpectedStatusCodes) == 0 {
		return
	}

	p.compiledStatusCodes = make([]statusCodeMatcher, 0, len(p.ExpectedStatusCodes))

	for _, code := range p.ExpectedStatusCodes {
		trimmed := trimSpace(code)
		if contains(trimmed, "-") {
			// Range format: "200-299"
			parts := splitString(trimmed, "-")
			if len(parts) == 2 {
				start := parseIntSafe(trimSpace(parts[0]))
				end := parseIntSafe(trimSpace(parts[1]))
				if start > 0 && end > 0 {
					p.compiledStatusCodes = append(p.compiledStatusCodes, statusCodeMatcher{
						isRange: true,
						start:   start,
						end:     end,
					})
				}
			}
		} else {
			// Exact match: "200"
			exact := parseIntSafe(trimmed)
			if exact > 0 {
				p.compiledStatusCodes = append(p.compiledStatusCodes, statusCodeMatcher{
					isRange: false,
					exact:   exact,
				})
			}
		}
	}
}

// Helper functions to avoid importing strings/strconv in models package
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func splitString(s, sep string) []string {
	var result []string
	start := 0
	for i := 0; i <= len(s)-len(sep); i++ {
		if s[i:i+len(sep)] == sep {
			result = append(result, s[start:i])
			start = i + len(sep)
		}
	}
	result = append(result, s[start:])
	return result
}

func trimSpace(s string) string {
	start := 0
	end := len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

func parseIntSafe(s string) int {
	if s == "" {
		return 0
	}
	result := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0
		}
		result = result*10 + int(s[i]-'0')
	}
	return result
}

// GSLBNode tracks elchi-gslb instances that fetch DNS records
type GSLBNode struct {
	ID              primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	NodeIP          string             `bson:"node_ip" json:"node_ip"`
	Zone            string             `bson:"zone" json:"zone"`
	FirstSeen       time.Time          `bson:"first_seen" json:"first_seen"`
	LastSeen        time.Time          `bson:"last_seen" json:"last_seen"`
	RequestCount    int64              `bson:"request_count" json:"request_count"`
	LastVersionHash string             `bson:"last_version_hash" json:"last_version_hash"`
}

// GSLBShard represents ownership of a shard by a controller
type GSLBShard struct {
	ShardID       int       `bson:"shard_id" json:"shard_id"`
	ControllerID  string    `bson:"controller_id" json:"controller_id"`
	LeaseExpiry   time.Time `bson:"lease_expiry" json:"lease_expiry"`
	LastHeartbeat time.Time `bson:"last_heartbeat" json:"last_heartbeat"`
}

// GSLB System Constants
const (
	// Shard Configuration
	GSLBNumShards = 128 // GLOBAL constant, NOT project-specific (optimal balance for 10k+ FQDNs)

	// Lease Management
	GSLBShardLeaseTTL = 60 // Seconds - lease expiration time

	// Health Check Timing
	GSLBHealthCheckInterval = 30 // Seconds - FIXED controller cycle interval
	GSLBHealthCheckTimeout  = 25 // Seconds - 83% of interval, maximum cycle duration
	GSLBMaxConcurrentProbes = 50 // Parallel probe workers for performance

	// Probe Configuration Constraints (with millisecond precision)
	MinProbeTimeout = 0.1 // Seconds - minimum user-configurable timeout (100ms)
	MaxProbeTimeout = 3.0 // Seconds - maximum user-configurable timeout (3000ms)

	// DNS TTL Configuration Constraints
	MinTTL = 1     // Seconds - minimum DNS TTL (must be positive)
	MaxTTL = 86400 // Seconds - maximum DNS TTL (24 hours)

	// Status History Configuration
	GSLBMaxStatusHistorySize = 50 // Maximum number of status change events to keep per IP (FIFO)
)

// AllowedProbeIntervals defines the valid probe interval values for Time Wheel system (in seconds)
// These intervals are the standard probe frequencies supported by the system
// User MUST select one of these values when configuring health checks
//
// Time Wheel Strategy:
//   - Each record+IP is scheduled independently based on its interval
//   - 10s interval is most common (default priority)
//   - Time Wheel handles all intervals dynamically with 1-second granularity
//   - NO custom intervals allowed - strict validation enforced
var AllowedProbeIntervals = []int{10, 20, 30, 60, 90, 120, 180, 300}

// GSLB Error Definitions
var (
	ErrInvalidHealthState   = errors.New("invalid health state")
	ErrInvalidRecordID      = errors.New("invalid record ID")
	ErrInvalidFQDN          = errors.New("invalid FQDN")
	ErrInvalidIP            = errors.New("invalid IP address")
	ErrInvalidShardID       = errors.New("invalid shard ID")
	ErrInvalidSubShardID    = errors.New("invalid sub-shard ID")
	ErrInvalidProbeInterval = errors.New("invalid probe interval - must be one of: 10, 20, 30, 60, 90, 120, 180, 300 seconds")
)

// ValidateProbeInterval validates that the interval is one of the allowed intervals
// Returns error if interval is not in AllowedProbeIntervals
func (p *GSLBProbe) ValidateProbeInterval() error {
	for _, allowed := range AllowedProbeIntervals {
		if p.Interval == allowed {
			return nil
		}
	}
	return ErrInvalidProbeInterval
}
