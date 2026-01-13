package gslb

import (
	"maps"
	"runtime"
)

// WorkerLimits defines minimum and maximum worker counts for a bucket
type WorkerLimits struct {
	MinWorkers int
	MaxWorkers int
}

// CPUConfig manages worker pool configuration
// Uses fixed, well-calibrated limits that work across all environments
// Auto-scaling handles dynamic load adjustments
type CPUConfig struct {
	// Worker limits per bucket interval (fixed configuration)
	workerLimits map[int]WorkerLimits // key: interval in seconds
}

// These values are environment-agnostic and rely on auto-scaling for dynamic adjustments
// Baseline: ~380 workers at startup, ~1,500 workers at full scale
var fixedWorkerLimits = map[int]WorkerLimits{
	10:  {MinWorkers: 100, MaxWorkers: 500}, // CRITICAL priority (10s) - most frequent, highest load
	20:  {MinWorkers: 40, MaxWorkers: 200},  // High priority
	30:  {MinWorkers: 30, MaxWorkers: 150},  // High priority
	60:  {MinWorkers: 50, MaxWorkers: 250},  // Medium priority (high IP count expected)
	90:  {MinWorkers: 20, MaxWorkers: 100},  // Medium priority
	120: {MinWorkers: 20, MaxWorkers: 100},  // Low priority
	180: {MinWorkers: 10, MaxWorkers: 50},   // Low priority
	300: {MinWorkers: 10, MaxWorkers: 50},   // Lowest priority
}

// NewCPUConfig creates a new worker pool configuration
// Uses fixed limits that work well across all environments (containers, VMs, bare metal)
// Auto-scaling dynamically adjusts worker counts based on queue pressure
func NewCPUConfig() *CPUConfig {
	return &CPUConfig{
		workerLimits: fixedWorkerLimits,
	}
}

// GetWorkerLimits returns the calculated worker limits for a given interval
// Returns zero values if interval is not configured
func (c *CPUConfig) GetWorkerLimits(interval int) WorkerLimits {
	if limits, exists := c.workerLimits[interval]; exists {
		return limits
	}

	// Return zero values for unknown intervals
	return WorkerLimits{MinWorkers: 0, MaxWorkers: 0}
}

// GetNumCPU returns the detected number of CPU cores (for logging only)
func (c *CPUConfig) GetNumCPU() int {
	return runtime.NumCPU()
}

// GetAllWorkerLimits returns worker limits for all configured intervals
// Useful for logging and monitoring
func (c *CPUConfig) GetAllWorkerLimits() map[int]WorkerLimits {
	result := make(map[int]WorkerLimits, len(c.workerLimits))
	maps.Copy(result, c.workerLimits)
	return result
}

// GetTotalMaxWorkers returns the sum of max workers across all buckets
// Useful for capacity planning and monitoring
func (c *CPUConfig) GetTotalMaxWorkers() int {
	total := 0
	for _, limits := range c.workerLimits {
		total += limits.MaxWorkers
	}
	return total
}

// GetTotalMinWorkers returns the sum of min workers across all buckets
// Represents baseline resource usage
func (c *CPUConfig) GetTotalMinWorkers() int {
	total := 0
	for _, limits := range c.workerLimits {
		total += limits.MinWorkers
	}
	return total
}
