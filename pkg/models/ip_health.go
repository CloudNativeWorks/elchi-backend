package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// GSLBIPHealth represents a single IP's health state in the GSLB system
// This is stored in a separate collection (gslb_ip_health) instead of nested in gslb_records
// for better MongoDB performance at scale (300k+ endpoints)
type GSLBIPHealth struct {
	ID       primitive.ObjectID `bson:"_id,omitempty" json:"id,omitempty"`
	RecordID primitive.ObjectID `bson:"record_id" json:"record_id"` // Parent GSLB record

	// Identity
	FQDN     string `bson:"fqdn" json:"fqdn"`           // Denormalized for fast DNS queries
	IP       string `bson:"ip" json:"ip"`               // Target IP address
	ClientID string `bson:"client_id" json:"client_id"` // Client that owns this IP

	// Sharding (two-tier: 128 top-level × 8 sub-shards = 1,024 logical shards)
	ShardID    int `bson:"shard_id" json:"shard_id"`         // 0-127 (top-level shard)
	SubShardID int `bson:"sub_shard_id" json:"sub_shard_id"` // 0-7 (sub-shard for load distribution)

	// Tri-state health model
	HealthState      HealthState `bson:"health_state" json:"health_state"`             // passing/warning/critical
	LastStatusChange time.Time   `bson:"last_status_change" json:"last_status_change"` // Updated ONLY on state transitions

	// Circuit breaker state (persisted across controller restarts)
	BackoffUntil   time.Time `bson:"backoff_until" json:"backoff_until"`     // Skip probes until this time (Hybrid strategy: only for Critical state)
	CurrentBackoff int64     `bson:"current_backoff" json:"current_backoff"` // Current backoff duration in seconds (0 = no backoff)

	// Manual reset tracking (prevents infinite manual reset detection loop)
	ManualResetAt time.Time `bson:"manual_reset_at,omitempty" json:"manual_reset_at,omitempty"` // Timestamp when admin manually changed health state

	// Status history (last 100 state changes, FIFO)
	StatusHistory []GSLBStatusHistory `bson:"status_history,omitempty" json:"status_history,omitempty"`

	// IP creation source
	IsManual bool `bson:"is_manual" json:"is_manual"` // true = manually added by admin, false = auto-generated from service deployment

	// Metadata
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}

// NewGSLBIPHealth creates a new IP health record with optimistic initial state
// Called when a client is deployed to a service (auto-generated)
func NewGSLBIPHealth(recordID primitive.ObjectID, fqdn, ip, clientID string, shardID, subShardID int) *GSLBIPHealth {
	now := time.Now()
	return &GSLBIPHealth{
		RecordID:   recordID,
		FQDN:       fqdn,
		IP:         ip,
		ClientID:   clientID,
		ShardID:    shardID,
		SubShardID: subShardID,

		// Optimistic initial state (assume healthy until first probe)
		HealthState:      HealthStatePassing,
		LastStatusChange: now,

		// No circuit breaker initially
		BackoffUntil:   time.Time{},
		CurrentBackoff: 0,

		// Empty history
		StatusHistory: []GSLBStatusHistory{},

		// Auto-generated from service deployment
		IsManual: false,

		CreatedAt: now,
		UpdatedAt: now,
	}
}

// IsHealthy returns true if this IP should be included in DNS responses
// Only Critical state is excluded from DNS
func (iph *GSLBIPHealth) IsHealthy() bool {
	return iph.HealthState.IsHealthy()
}

// IsInBackoff returns true if the circuit breaker is active (probe should be skipped)
// Uses ±5 second tolerance to allow Time Wheel scheduling to catch IPs near backoff expiry
func (iph *GSLBIPHealth) IsInBackoff() bool {
	if iph.BackoffUntil.IsZero() {
		return false
	}

	// TOLERANCE WINDOW: Allow probing up to 5 seconds before backoff expires
	// Problem: Graduated backoff (10s, 20s, 30s...) expires at arbitrary times
	// Time Wheel schedules IPs dynamically based on their individual backoff state
	// Without tolerance, IP might be scheduled 1-5s after backoff expires (scheduling granularity)
	//
	// Example:
	//   Probe at 23:12:45 + 10s backoff = expire at 23:12:55
	//   Time Wheel slot: IP scheduled for 23:12:58 (3s after expiry)
	//   Without tolerance: Skip at 23:12:58, wait for next reschedule (additional delay)
	//   With 5s tolerance: Probe at 23:12:58 (considered "close enough" to 23:12:55)
	//
	// Tolerance = 5 seconds (half of minimum 10s interval)
	const toleranceSeconds = 5
	now := time.Now()
	toleranceWindow := iph.BackoffUntil.Add(-toleranceSeconds * time.Second)

	// In backoff if now is MORE than 5 seconds before expiry
	return now.Before(toleranceWindow)
}

// GetLatestHistoryEntry returns the most recent status history entry, or nil if empty
func (iph *GSLBIPHealth) GetLatestHistoryEntry() *GSLBStatusHistory {
	if len(iph.StatusHistory) == 0 {
		return nil
	}
	return &iph.StatusHistory[len(iph.StatusHistory)-1]
}

// GetLastChangeTime returns when the health state last changed
// Falls back to CreatedAt if no history exists
func (iph *GSLBIPHealth) GetLastChangeTime() time.Time {
	if !iph.LastStatusChange.IsZero() {
		return iph.LastStatusChange
	}
	if entry := iph.GetLatestHistoryEntry(); entry != nil {
		return entry.DateTime
	}
	return iph.CreatedAt
}

// CalculateGraduatedBackoff returns the backoff duration based on consecutive failures and probe interval
//
// Adaptive Backoff Strategy:
//   - Warning state: NO backoff (returns 0)
//   - Critical state: Graduated backoff based on probe interval with 5-minute cap
//
// Backoff scales with probe interval using graduated multipliers: [1.0, 2.0, 3.0, 5.0, 8.0, 12.0]
// This provides progressive backoff escalation and prevents probe storms on degraded backends.
//
// Examples by interval:
//   - 10s interval: 10s -> 20s -> 30s -> 50s -> 80s -> 120s (max)
//   - 30s interval: 30s -> 60s -> 90s -> 150s -> 240s -> 300s (capped)
//   - 60s interval: 60s -> 120s -> 180s -> 300s (capped)
//
// Parameters:
//   - healthState: Current health state (passing/warning/critical)
//   - consecutiveFailures: Total consecutive failure count
//   - criticalThreshold: Threshold to reach CRITICAL state
//   - probeInterval: Probe interval in seconds
func CalculateGraduatedBackoff(healthState HealthState, consecutiveFailures int, criticalThreshold int, probeInterval int) time.Duration {
	// Warning state: NO backoff (fast recovery detection)
	if healthState == HealthStateWarning {
		return 0
	}

	// Critical state: Graduated backoff based on probe interval
	if healthState == HealthStateCritical {
		// Calculate failures beyond the critical threshold
		failuresSinceCritical := consecutiveFailures - criticalThreshold
		if failuresSinceCritical < 0 {
			failuresSinceCritical = 0
		}

		// Graduated multipliers for progressive backoff escalation
		multipliers := []float64{1.0, 2.0, 3.0, 5.0, 8.0, 12.0}

		multiplierIndex := failuresSinceCritical
		if multiplierIndex >= len(multipliers) {
			multiplierIndex = len(multipliers) - 1
		}
		multiplier := multipliers[multiplierIndex]

		backoffSeconds := int64(float64(probeInterval) * multiplier)

		// Cap at 5 minutes
		const maxBackoffSeconds int64 = 300
		if backoffSeconds > maxBackoffSeconds {
			backoffSeconds = maxBackoffSeconds
		}

		return time.Duration(backoffSeconds) * time.Second
	}

	// Passing state: No backoff
	return 0
}

// SetBackoff applies circuit breaker backoff based on current state and consecutive failures
// Time Wheel scheduler handles dynamic rescheduling based on backoff expiry
func (iph *GSLBIPHealth) SetBackoff(consecutiveFailures int, criticalThreshold int, probeInterval int) {
	backoffDuration := CalculateGraduatedBackoff(iph.HealthState, consecutiveFailures, criticalThreshold, probeInterval)

	if backoffDuration > 0 {
		backoffSeconds := int64(backoffDuration.Seconds())

		// Safety validation
		if backoffSeconds > 300 {
			backoffSeconds = 300
			backoffDuration = 300 * time.Second
		}
		if backoffSeconds < 0 {
			backoffSeconds = 0
			backoffDuration = 0
		}

		// Simple backoff: now + graduated duration
		// IsInBackoff() uses tolerance to handle Time Wheel scheduling granularity
		// No special alignment needed - Time Wheel reschedules based on backoff expiry
		iph.BackoffUntil = time.Now().Add(backoffDuration)
		iph.CurrentBackoff = backoffSeconds
	} else {
		iph.ResetBackoff()
	}
}

// ResetBackoff clears circuit breaker state (called when transitioning to Warning or Passing)
func (iph *GSLBIPHealth) ResetBackoff() {
	iph.BackoffUntil = time.Time{}
	iph.CurrentBackoff = 0
}

// AddStatusHistory appends a status change event to history (FIFO, max 100 entries)
func (iph *GSLBIPHealth) AddStatusHistory(state HealthState, responseCode int, responseTime float64) {
	entry := GSLBStatusHistory{
		State:        state.String(),
		DateTime:     time.Now(),
		ResponseCode: responseCode,
		ResponseTime: responseTime,
	}

	// Append to history
	iph.StatusHistory = append(iph.StatusHistory, entry)

	// Enforce FIFO cap at 50 entries
	if len(iph.StatusHistory) > GSLBMaxStatusHistorySize {
		iph.StatusHistory = iph.StatusHistory[1:] // Remove oldest entry
	}
}

// UpdateHealthState updates the health state and related fields
// Returns (stateChanged bool, shouldWrite bool, isImmediate bool)
func (iph *GSLBIPHealth) UpdateHealthState(newState HealthState, responseCode int, responseTime float64) (bool, bool, bool) {
	oldState := iph.HealthState

	// Check if state changed
	if oldState == newState {
		return false, false, false
	}

	// Update state
	iph.HealthState = newState
	iph.LastStatusChange = time.Now()
	iph.UpdatedAt = time.Now()

	// Add to history
	iph.AddStatusHistory(newState, responseCode, responseTime)

	// Determine if write is needed and if it should be immediate
	shouldWrite, isImmediate := ShouldWriteToDatabase(oldState, newState)

	return true, shouldWrite, isImmediate
}

// GetLogicalShardID returns the logical shard ID (0-1023)
// Formula: shard_id * 8 + sub_shard_id
func (iph *GSLBIPHealth) GetLogicalShardID() int {
	return iph.ShardID*8 + iph.SubShardID
}

// Validate performs validation on GSLBIPHealth fields
func (iph *GSLBIPHealth) Validate() error {
	if iph.RecordID.IsZero() {
		return ErrInvalidRecordID
	}

	if iph.FQDN == "" {
		return ErrInvalidFQDN
	}

	if iph.IP == "" {
		return ErrInvalidIP
	}

	if iph.ShardID < 0 || iph.ShardID >= GSLBNumShards {
		return ErrInvalidShardID
	}

	if iph.SubShardID < 0 || iph.SubShardID >= 8 {
		return ErrInvalidSubShardID
	}

	if err := iph.HealthState.Validate(); err != nil {
		return err
	}

	return nil
}
