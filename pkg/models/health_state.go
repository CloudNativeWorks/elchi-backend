package models

// HealthState represents the tri-state health model for GSLB IP health tracking
// This replaces the binary healthy/unhealthy model to reduce write flapping
type HealthState string

const (
	// HealthStatePassing indicates all health checks are passing
	// DNS behavior: Included in A records
	HealthStatePassing HealthState = "passing"

	// HealthStateWarning indicates 1-2 consecutive failures (grace period)
	// DNS behavior: Still included in A records (considered healthy)
	// Circuit breaker: NO backoff, continue probing every 30s for fast recovery
	HealthStateWarning HealthState = "warning"

	// HealthStateCritical indicates 3+ consecutive failures (unhealthy)
	// DNS behavior: EXCLUDED from A records
	// Circuit breaker: Graduated backoff (1m → 5m CAP) based on consecutive failures
	HealthStateCritical HealthState = "critical"
)

// String returns the string representation of HealthState
func (hs HealthState) String() string {
	return string(hs)
}

// IsHealthy returns true if the state should be included in DNS responses
// Only Critical state is excluded from DNS
func (hs HealthState) IsHealthy() bool {
	return hs != HealthStateCritical
}

// IsValid checks if the HealthState is one of the valid values
func (hs HealthState) IsValid() bool {
	switch hs {
	case HealthStatePassing, HealthStateWarning, HealthStateCritical:
		return true
	default:
		return false
	}
}

// Validate returns an error if the HealthState is invalid
func (hs HealthState) Validate() error {
	if !hs.IsValid() {
		return ErrInvalidHealthState
	}
	return nil
}

// DetermineHealthState calculates the appropriate health state based on consecutive failures
// GSLB V2.0: Configurable thresholds from probe configuration (REQUIRED)
//
// Parameters:
//   - consecutiveFailures: Number of consecutive probe failures
//   - probe: MUST be provided with WarningThreshold and CriticalThreshold set
//   - currentState: Current health state (used to prevent backwards state transitions)
//
// State Transition Rules (ONE-WAY PROGRESSION):
//   - Forward only: PASSING → WARNING → CRITICAL ✅
//   - Reset to start: Any state → PASSING (on probe success) ✅
//   - NO backwards: CRITICAL → WARNING ❌ or WARNING → PASSING (while failing) ❌
//
// Logic:
//   - 0 failures → Always PASSING (reset to healthy)
//   - < WarningThreshold → Stay in current state if already WARNING/CRITICAL (no backwards)
//   - >= WarningThreshold AND < CriticalThreshold → WARNING (if PASSING) or stay in CRITICAL
//   - >= CriticalThreshold → CRITICAL
//
// Example with WarningThreshold=1, CriticalThreshold=3:
//   PASSING + 0 failures → PASSING ✅
//   PASSING + 1 failure → WARNING ✅
//   WARNING + 1 failure → WARNING ✅ (stay, don't go back to PASSING)
//   WARNING + 3 failures → CRITICAL ✅
//   CRITICAL + 1 failure → CRITICAL ✅ (stay, don't go back to WARNING)
//   CRITICAL + 0 failures → PASSING ✅ (probe success resets)
func DetermineHealthState(consecutiveFailures int, probe *GSLBProbe, currentState HealthState) HealthState {
	// Probe success → always reset to PASSING
	if consecutiveFailures == 0 {
		return HealthStatePassing
	}

	// GSLB V2.0: Thresholds are REQUIRED - probe must be provided
	if probe == nil || probe.WarningThreshold <= 0 || probe.CriticalThreshold <= 0 {
		// CRITICAL: This should never happen in V2.0
		// Log error and use emergency fallback to prevent system failure
		return HealthStateCritical // Fail-safe: mark as critical if config is invalid
	}

	// Determine threshold-based state (what state WOULD be based on failures alone)
	var thresholdState HealthState
	switch {
	case consecutiveFailures < probe.WarningThreshold:
		thresholdState = HealthStatePassing
	case consecutiveFailures < probe.CriticalThreshold:
		thresholdState = HealthStateWarning
	default:
		thresholdState = HealthStateCritical
	}

	// ✅ ONE-WAY PROGRESSION RULE: Only allow forward state transitions
	// Never go backwards: CRITICAL → WARNING or WARNING → PASSING (while failing)
	// This prevents manual CRITICAL from being downgraded to WARNING on first probe failure
	switch currentState {
	case HealthStateCritical:
		// From CRITICAL: Can only stay CRITICAL or reset to PASSING (already handled above)
		// If threshold says WARNING, stay CRITICAL (no backwards movement)
		if thresholdState == HealthStateWarning || thresholdState == HealthStatePassing {
			return HealthStateCritical // Stay critical, don't go backwards
		}
		return HealthStateCritical

	case HealthStateWarning:
		// From WARNING: Can only go to CRITICAL or reset to PASSING (already handled above)
		// If threshold says PASSING, stay WARNING (no backwards movement while failing)
		if thresholdState == HealthStatePassing {
			return HealthStateWarning // Stay warning, don't go back to passing while failing
		}
		return thresholdState // Can progress to CRITICAL

	case HealthStatePassing:
		// From PASSING: Can progress normally to WARNING or CRITICAL
		return thresholdState

	default:
		// Unknown state, use threshold-based state
		return thresholdState
	}
}

// ShouldWriteToDatabase determines if a state transition should trigger a MongoDB write
// This reduces write flapping by only writing on meaningful transitions
//
// Write triggers:
//   - passing → warning: YES WRITE (buffered) - Track state, DNS still sees as healthy
//   - warning → warning: NO WRITE (still in grace)
//   - warning → critical: YES WRITE (immediate) - became unhealthy
//   - critical → warning: YES WRITE (immediate) - recovering
//   - warning → passing: YES WRITE (buffered) - fully recovered
//   - critical → passing: YES WRITE (immediate) - fully recovered
//
// Returns: (shouldWrite bool, isImmediate bool)
//   - shouldWrite: true if transition should be persisted
//   - isImmediate: true if write should bypass WriteBuffer for DNS consistency
//
// IMPORTANT: WARNING state is written to DB for state tracking consistency
// DNS filtering happens separately - WARNING IPs are still included in DNS responses
func ShouldWriteToDatabase(oldState, newState HealthState) (shouldWrite bool, isImmediate bool) {
	// No state change
	if oldState == newState {
		return false, false
	}

	// Transitions TO or FROM Critical state require immediate writes for DNS consistency
	isCriticalTransition := (oldState == HealthStateCritical) || (newState == HealthStateCritical)

	// passing → warning: Write to DB (buffered) to track state, DNS filtering happens separately
	// This prevents state/counter mismatch issues after manual resets
	if oldState == HealthStatePassing && newState == HealthStateWarning {
		return true, false // shouldWrite=true, but not immediate (buffered)
	}

	// All other transitions require write
	return true, isCriticalTransition
}
