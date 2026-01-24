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
	// Circuit breaker: Graduated backoff (1m -> 5m CAP) based on consecutive failures
	HealthStateCritical HealthState = "critical"

	// HealthStateRecovery indicates IP is recovering (has consecutive successes but not yet PASSING)
	// DNS behavior: EXCLUDED from A records (treated like CRITICAL until fully recovered)
	// Scheduling: Uses interval/2 for faster recovery verification
	// This prevents flapping by requiring multiple consecutive successes before PASSING
	// Only used when passing_threshold > 1
	HealthStateRecovery HealthState = "recovery"
)

// String returns the string representation of HealthState
func (hs HealthState) String() string {
	return string(hs)
}

// IsHealthy returns true if the state should be included in DNS responses
// Critical and Recovery states are excluded from DNS
func (hs HealthState) IsHealthy() bool {
	return hs != HealthStateCritical && hs != HealthStateRecovery
}

// IsValid checks if the HealthState is one of the valid values
func (hs HealthState) IsValid() bool {
	switch hs {
	case HealthStatePassing, HealthStateWarning, HealthStateCritical, HealthStateRecovery:
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
// NOTE: Legacy version without RECOVERY support. Use DetermineHealthStateWithRecovery for full support.
func DetermineHealthState(consecutiveFailures int, probe *GSLBProbe, currentState HealthState) HealthState {
	return DetermineHealthStateWithRecovery(consecutiveFailures, 0, probe, currentState)
}

// DetermineHealthStateWithRecovery calculates health state with RECOVERY support
// GSLB: Adds passing_threshold for anti-flapping protection
//
// State Transition Rules:
//   - Forward only: PASSING -> WARNING -> CRITICAL
//   - Recovery path: CRITICAL/WARNING -> RECOVERY -> PASSING (when passing_threshold > 1)
//   - Direct recovery: CRITICAL/WARNING -> PASSING (when passing_threshold = 1)
//   - RECOVERY + failure -> CRITICAL (reset recovery progress)
//
// Example with WarningThreshold=1, CriticalThreshold=3, PassingThreshold=2:
//
//	CRITICAL + success -> RECOVERY (1/2)
//	RECOVERY + success -> PASSING (2/2)
//	RECOVERY + failure -> CRITICAL (reset)
func DetermineHealthStateWithRecovery(consecutiveFailures, consecutiveSuccesses int, probe *GSLBProbe, currentState HealthState) HealthState {
	// Validate probe config
	if probe == nil || probe.WarningThreshold <= 0 || probe.CriticalThreshold <= 0 {
		return HealthStateCritical // Fail-safe
	}

	// Handle probe success (consecutiveFailures == 0)
	if consecutiveFailures == 0 {
		return determineRecoveryState(consecutiveSuccesses, probe, currentState)
	}

	// Handle probe failure
	return determineFailureState(consecutiveFailures, probe, currentState)
}

// determineRecoveryState handles state transitions when probe succeeds
func determineRecoveryState(consecutiveSuccesses int, probe *GSLBProbe, currentState HealthState) HealthState {
	passingThreshold := probe.GetPassingThreshold()

	// Fast path: single success = PASSING (default behavior)
	if passingThreshold <= 1 {
		return HealthStatePassing
	}

	// Already healthy - stay PASSING
	if currentState == HealthStatePassing {
		return HealthStatePassing
	}

	// Check if enough successes to fully recover
	if consecutiveSuccesses >= passingThreshold {
		return HealthStatePassing
	}

	// Not enough successes yet - enter/stay in RECOVERY
	return HealthStateRecovery
}

// determineFailureState handles state transitions when probe fails
func determineFailureState(consecutiveFailures int, probe *GSLBProbe, currentState HealthState) HealthState {
	// RECOVERY + failure -> CRITICAL (reset recovery progress)
	if currentState == HealthStateRecovery {
		return HealthStateCritical
	}

	// Calculate threshold-based state
	thresholdState := calculateThresholdState(consecutiveFailures, probe)

	// Apply one-way progression rule
	return applyProgressionRule(currentState, thresholdState)
}

// calculateThresholdState determines state based on failure count alone
func calculateThresholdState(consecutiveFailures int, probe *GSLBProbe) HealthState {
	switch {
	case consecutiveFailures < probe.WarningThreshold:
		return HealthStatePassing
	case consecutiveFailures < probe.CriticalThreshold:
		return HealthStateWarning
	default:
		return HealthStateCritical
	}
}

// applyProgressionRule enforces one-way state progression (no backwards movement while failing)
func applyProgressionRule(currentState, thresholdState HealthState) HealthState {
	switch currentState {
	case HealthStateCritical:
		// CRITICAL can only stay CRITICAL (recovery handled separately)
		return HealthStateCritical

	case HealthStateWarning:
		// WARNING can progress to CRITICAL, but not back to PASSING
		if thresholdState == HealthStateCritical {
			return HealthStateCritical
		}
		return HealthStateWarning

	case HealthStatePassing:
		// PASSING can progress normally
		return thresholdState

	default:
		return thresholdState
	}
}

// ShouldWriteToDatabase determines if a state transition should trigger a MongoDB write
// This reduces write flapping by only writing on meaningful transitions
//
// Write triggers:
//   - passing -> warning: YES WRITE (buffered)
//   - warning -> critical: YES WRITE (immediate) - DNS exclusion
//   - critical -> recovery: YES WRITE (immediate) - state tracking
//   - recovery -> recovery: NO WRITE (still recovering)
//   - recovery -> passing: YES WRITE (immediate) - DNS inclusion
//   - recovery -> critical: YES WRITE (immediate) - recovery failed
//   - critical -> passing: YES WRITE (immediate) - DNS inclusion
//
// Returns: (shouldWrite bool, isImmediate bool)
func ShouldWriteToDatabase(oldState, newState HealthState) (bool, bool) {
	// No state change
	if oldState == newState {
		return false, false
	}

	// Determine if this is a DNS-affecting transition (needs immediate write)
	isDNSTransition := isDNSAffectingTransition(oldState, newState)

	// passing -> warning: Buffered write (DNS not affected yet)
	if oldState == HealthStatePassing && newState == HealthStateWarning {
		return true, false
	}

	return true, isDNSTransition
}

// isDNSAffectingTransition returns true if the transition affects DNS responses
// These transitions require immediate writes for DNS consistency
func isDNSAffectingTransition(oldState, newState HealthState) bool {
	oldHealthy := oldState.IsHealthy()
	newHealthy := newState.IsHealthy()

	// Transition between healthy and unhealthy states affects DNS
	return oldHealthy != newHealthy
}
