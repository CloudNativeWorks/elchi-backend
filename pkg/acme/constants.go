package acme

import "time"

// ACME certificate management constants
// These values are part of the application logic and should not be configurable
const (
	// RenewalCheckInterval - How often the scheduler checks for certificates to renew
	RenewalCheckInterval = 24 * time.Hour

	// RenewalDaysBefore - Start renewal this many days before certificate expiry
	RenewalDaysBefore = 14

	// DNSPropagationTimeout - Maximum time to wait for DNS TXT records to propagate
	DNSPropagationTimeout = 10 * time.Minute

	// ChallengeTimeout - Maximum time for user to add manual TXT records (manual DNS verification)
	ChallengeTimeout = 1 * time.Hour

	// TempKeyRetention - How long to keep temporary private keys before cleanup
	TempKeyRetention = 24 * time.Hour

	// AsyncJobTimeout - Maximum time for async DNS verification job (automatic DNS)
	AsyncJobTimeout = 5 * time.Minute
)
