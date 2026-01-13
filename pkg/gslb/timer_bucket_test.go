package gslb

import (
	"testing"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/stretchr/testify/assert"
)

// TestBuildProbeKey tests the BuildProbeKey helper function
func TestBuildProbeKey(t *testing.T) {
	tests := []struct {
		name     string
		ip       string
		probe    *models.GSLBProbe
		expected ProbeKey
	}{
		{
			name: "HTTP probe with path",
			ip:   "1.2.3.4",
			probe: &models.GSLBProbe{
				Type:     "http",
				Port:     80,
				Path:     "/health",
				Interval: 10,
			},
			expected: ProbeKey{
				IP:       "1.2.3.4",
				Type:     "http",
				Port:     80,
				Path:     "/health",
				Interval: 10,
			},
		},
		{
			name: "HTTPS probe with custom port",
			ip:   "5.6.7.8",
			probe: &models.GSLBProbe{
				Type:     "https",
				Port:     8443,
				Path:     "/api/status",
				Interval: 30,
			},
			expected: ProbeKey{
				IP:       "5.6.7.8",
				Type:     "https",
				Port:     8443,
				Path:     "/api/status",
				Interval: 30,
			},
		},
		{
			name: "TCP probe",
			ip:   "10.0.0.1",
			probe: &models.GSLBProbe{
				Type:     "tcp",
				Port:     3306,
				Path:     "", // TCP probes don't use path
				Interval: 60,
			},
			expected: ProbeKey{
				IP:       "10.0.0.1",
				Type:     "tcp",
				Port:     3306,
				Path:     "",
				Interval: 60,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildProbeKey(tt.ip, tt.probe)
			assert.Equal(t, tt.expected, result, "ProbeKey should match expected value")
		})
	}
}

// TestProbeKeyMatches tests the ProbeKey.Matches() method
func TestProbeKeyMatches(t *testing.T) {
	tests := []struct {
		name     string
		key1     ProbeKey
		key2     ProbeKey
		expected bool
		reason   string
	}{
		{
			name: "Identical configs - should match",
			key1: ProbeKey{
				IP:       "1.2.3.4",
				Type:     "http",
				Port:     80,
				Path:     "/health",
				Interval: 10,
			},
			key2: ProbeKey{
				IP:       "1.2.3.4",
				Type:     "http",
				Port:     80,
				Path:     "/health",
				Interval: 10,
			},
			expected: true,
			reason:   "All fields match",
		},
		{
			name: "Different IPs same config - should match (IP not compared)",
			key1: ProbeKey{
				IP:       "1.2.3.4",
				Type:     "http",
				Port:     80,
				Path:     "/health",
				Interval: 10,
			},
			key2: ProbeKey{
				IP:       "5.6.7.8", // Different IP
				Type:     "http",
				Port:     80,
				Path:     "/health",
				Interval: 10,
			},
			expected: true,
			reason:   "IP is not compared in Matches()",
		},
		{
			name: "Different types - should NOT match",
			key1: ProbeKey{
				IP:       "1.2.3.4",
				Type:     "http",
				Port:     80,
				Path:     "/health",
				Interval: 10,
			},
			key2: ProbeKey{
				IP:       "1.2.3.4",
				Type:     "https", // Different type
				Port:     80,
				Path:     "/health",
				Interval: 10,
			},
			expected: false,
			reason:   "Type differs",
		},
		{
			name: "Different ports - should NOT match",
			key1: ProbeKey{
				IP:       "1.2.3.4",
				Type:     "http",
				Port:     80,
				Path:     "/health",
				Interval: 10,
			},
			key2: ProbeKey{
				IP:       "1.2.3.4",
				Type:     "http",
				Port:     8080, // Different port
				Path:     "/health",
				Interval: 10,
			},
			expected: false,
			reason:   "Port differs",
		},
		{
			name: "Different paths - should NOT match",
			key1: ProbeKey{
				IP:       "1.2.3.4",
				Type:     "http",
				Port:     80,
				Path:     "/health",
				Interval: 10,
			},
			key2: ProbeKey{
				IP:       "1.2.3.4",
				Type:     "http",
				Port:     80,
				Path:     "/api/status", // Different path
				Interval: 10,
			},
			expected: false,
			reason:   "Path differs",
		},
		{
			name: "Different intervals - should NOT match",
			key1: ProbeKey{
				IP:       "1.2.3.4",
				Type:     "http",
				Port:     80,
				Path:     "/health",
				Interval: 10,
			},
			key2: ProbeKey{
				IP:       "1.2.3.4",
				Type:     "http",
				Port:     80,
				Path:     "/health",
				Interval: 30, // Different interval
			},
			expected: false,
			reason:   "Interval differs",
		},
		{
			name: "TCP probes with same port - should match",
			key1: ProbeKey{
				IP:       "10.0.0.1",
				Type:     "tcp",
				Port:     3306,
				Path:     "",
				Interval: 60,
			},
			key2: ProbeKey{
				IP:       "10.0.0.2", // Different IP
				Type:     "tcp",
				Port:     3306,
				Path:     "",
				Interval: 60,
			},
			expected: true,
			reason:   "TCP probe config matches (IP not compared)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.key1.Matches(tt.key2)
			assert.Equal(t, tt.expected, result, "Match result incorrect: %s", tt.reason)
		})
	}
}

// TestProbeKeyDeduplication tests the real-world deduplication scenario
func TestProbeKeyDeduplication(t *testing.T) {
	// Scenario: 3 GSLB records using same IP with same config
	// Should deduplicate to 1 probe task
	recordA := &models.GSLBProbe{
		Type:     "https",
		Port:     443,
		Path:     "/health",
		Interval: 10,
	}

	recordB := &models.GSLBProbe{
		Type:     "https",
		Port:     443,
		Path:     "/health",
		Interval: 10,
	}

	recordC := &models.GSLBProbe{
		Type:     "https",
		Port:     443,
		Path:     "/health",
		Interval: 10,
	}

	ip := "1.2.3.4"

	keyA := BuildProbeKey(ip, recordA)
	keyB := BuildProbeKey(ip, recordB)
	keyC := BuildProbeKey(ip, recordC)

	// All keys should be identical
	assert.Equal(t, keyA, keyB, "Key A and B should be identical")
	assert.Equal(t, keyA, keyC, "Key A and C should be identical")
	assert.Equal(t, keyB, keyC, "Key B and C should be identical")

	// All keys should match
	assert.True(t, keyA.Matches(keyB), "Key A should match B")
	assert.True(t, keyA.Matches(keyC), "Key A should match C")
	assert.True(t, keyB.Matches(keyC), "Key B should match C")
}

// TestProbeKeyNoDeduplication tests when probes should NOT be deduplicated
func TestProbeKeyNoDeduplication(t *testing.T) {
	// Scenario: 2 records with same IP but different probe configs
	// Should create 2 separate probe tasks
	recordA := &models.GSLBProbe{
		Type:     "https",
		Port:     443,
		Path:     "/health", // Different path
		Interval: 10,
	}

	recordB := &models.GSLBProbe{
		Type:     "https",
		Port:     443,
		Path:     "/api/status", // Different path
		Interval: 10,
	}

	ip := "1.2.3.4"

	keyA := BuildProbeKey(ip, recordA)
	keyB := BuildProbeKey(ip, recordB)

	// Keys should be different
	assert.NotEqual(t, keyA, keyB, "Keys should be different due to different paths")

	// Keys should NOT match
	assert.False(t, keyA.Matches(keyB), "Keys should not match due to different paths")
}
