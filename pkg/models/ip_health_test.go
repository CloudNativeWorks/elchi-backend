package models

import (
	"testing"
	"time"
)

func TestGSLBIPHealth_IsInBackoff(t *testing.T) {
	now := time.Now()
	
	tests := []struct {
		name          string
		backoffUntil  time.Time
		probeInterval int
		wantBackoff   bool
		desc          string
	}{
		// 10s Interval Validation (Tolerance = 2s)
		{
			name:          "10s Interval - 3s remaining (Should be Backoff)",
			backoffUntil:  now.Add(3 * time.Second),
			probeInterval: 10,
			wantBackoff:   true,
			desc:          "With 2s tolerance, 3s remaining is too early to probe (3s > 2s)",
		},
		{
			name:          "10s Interval - 1.5s remaining (Should probe)",
			backoffUntil:  now.Add(1500 * time.Millisecond),
			probeInterval: 10,
			wantBackoff:   false,
			desc:          "With 2s tolerance, 1.5s remaining is within window (1.5s < 2s)",
		},
		{
			name:          "10s Interval - 5s remaining (Should be Backoff)",
			backoffUntil:  now.Add(5 * time.Second),
			probeInterval: 10,
			wantBackoff:   true,
			desc:          "Old logic allowed this (5s tolerance), new logic should block",
		},

		// 30s Interval Validation (Tolerance = 5s)
		{
			name:          "30s Interval - 6s remaining (Should be Backoff)",
			backoffUntil:  now.Add(6 * time.Second),
			probeInterval: 30,
			wantBackoff:   true,
			desc:          "With 5s tolerance, 6s remaining is too early",
		},
		{
			name:          "30s Interval - 4s remaining (Should probe)",
			backoffUntil:  now.Add(4 * time.Second),
			probeInterval: 30,
			wantBackoff:   false,
			desc:          "With 5s tolerance, 4s remaining is within window",
		},

		// Edge Cases
		{
			name:          "Zero Backoff",
			backoffUntil:  time.Time{},
			probeInterval: 10,
			wantBackoff:   false,
			desc:          "No backoff set should never block",
		},
		{
			name:          "Expired Backoff",
			backoffUntil:  now.Add(-1 * time.Second),
			probeInterval: 10,
			wantBackoff:   false,
			desc:          "Expired backoff should never block",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			iph := &GSLBIPHealth{
				BackoffUntil: tt.backoffUntil,
			}
			
			got := iph.IsInBackoff(tt.probeInterval)
			if got != tt.wantBackoff {
				t.Errorf("IsInBackoff(%d) = %v, want %v. Desc: %s. (BackoffUntil: %v, Now: %v)", 
					tt.probeInterval, got, tt.wantBackoff, tt.desc, tt.backoffUntil.Sub(now), "0s")
			}
		})
	}
}
