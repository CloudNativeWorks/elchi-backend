package monitoring

import (
	"context"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/async/job"
)

// Metrics holds async system performance metrics
type Metrics struct {
	mu sync.RWMutex

	// Job metrics
	JobsTotal           int64                   `json:"jobs_total"`
	JobsByStatus        map[job.JobStatus]int64 `json:"jobs_by_status"`
	JobsByType          map[job.JobType]int64   `json:"jobs_by_type"`
	AverageJobDuration  time.Duration           `json:"average_job_duration"`
	JobThroughputPerMin float64                 `json:"job_throughput_per_min"`

	// Worker metrics
	ActiveWorkers     int     `json:"active_workers"`
	ProcessingJobs    int     `json:"processing_jobs"`
	QueueSize         int     `json:"queue_size"`
	WorkerUtilization float64 `json:"worker_utilization"`

	// Performance metrics
	AverageAnalysisDuration time.Duration `json:"average_analysis_duration"`
	TotalListenersUpdated   int64         `json:"total_listeners_updated"`
	FailureRate             float64       `json:"failure_rate"`

	// System metrics
	LastUpdate    time.Time `json:"last_update"`
	UptimeSeconds int64     `json:"uptime_seconds"`
}

// Monitor provides async system monitoring capabilities
type Monitor struct {
	metrics     *Metrics
	startTime   time.Time
	jobHistory  []JobHistoryEntry
	historySize int
	mu          sync.RWMutex
}

// JobHistoryEntry represents a job completion record
type JobHistoryEntry struct {
	JobID          string        `json:"job_id"`
	Status         job.JobStatus `json:"status"`
	Type           job.JobType   `json:"type"`
	Duration       time.Duration `json:"duration"`
	ListenersCount int           `json:"listeners_count"`
	CompletedAt    time.Time     `json:"completed_at"`
	Error          string        `json:"error,omitempty"`
}

// NewMonitor creates a new async system monitor
func NewMonitor(historySize int) *Monitor {
	if historySize <= 0 {
		historySize = 1000 // Default history size
	}

	return &Monitor{
		metrics: &Metrics{
			JobsByStatus: make(map[job.JobStatus]int64),
			JobsByType:   make(map[job.JobType]int64),
			LastUpdate:   time.Now(),
		},
		startTime:   time.Now(),
		jobHistory:  make([]JobHistoryEntry, 0, historySize),
		historySize: historySize,
	}
}

// RecordJobStart records the start of a job
func (m *Monitor) RecordJobStart(jobID string, jobType job.JobType) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.metrics.JobsTotal++
	m.metrics.JobsByType[jobType]++
	m.metrics.ProcessingJobs++
	m.metrics.LastUpdate = time.Now()
}

// RecordJobCompletion records the completion of a job
func (m *Monitor) RecordJobCompletion(ctx context.Context, entry JobHistoryEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Update status metrics
	m.metrics.JobsByStatus[entry.Status]++
	m.metrics.ProcessingJobs--
	if m.metrics.ProcessingJobs < 0 {
		m.metrics.ProcessingJobs = 0
	}

	// Update performance metrics
	m.metrics.TotalListenersUpdated += int64(entry.ListenersCount)

	// Add to history
	if len(m.jobHistory) >= m.historySize {
		// Remove oldest entry
		m.jobHistory = m.jobHistory[1:]
	}
	m.jobHistory = append(m.jobHistory, entry)

	// Calculate average duration and throughput
	m.calculateAverages()

	m.metrics.LastUpdate = time.Now()
}

// UpdateWorkerMetrics updates worker-related metrics
func (m *Monitor) UpdateWorkerMetrics(activeWorkers, queueSize int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.metrics.ActiveWorkers = activeWorkers
	m.metrics.QueueSize = queueSize

	// Calculate worker utilization
	if activeWorkers > 0 {
		m.metrics.WorkerUtilization = float64(m.metrics.ProcessingJobs) / float64(activeWorkers) * 100
	} else {
		m.metrics.WorkerUtilization = 0
	}

	m.metrics.LastUpdate = time.Now()
}

// GetMetrics returns current metrics (thread-safe copy)
func (m *Monitor) GetMetrics() Metrics {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Create a copy to avoid race conditions
	statusCopy := make(map[job.JobStatus]int64)
	for k, v := range m.metrics.JobsByStatus {
		statusCopy[k] = v
	}

	typeCopy := make(map[job.JobType]int64)
	for k, v := range m.metrics.JobsByType {
		typeCopy[k] = v
	}

	m.metrics.UptimeSeconds = int64(time.Since(m.startTime).Seconds())

	return Metrics{
		JobsTotal:               m.metrics.JobsTotal,
		JobsByStatus:            statusCopy,
		JobsByType:              typeCopy,
		AverageJobDuration:      m.metrics.AverageJobDuration,
		JobThroughputPerMin:     m.metrics.JobThroughputPerMin,
		ActiveWorkers:           m.metrics.ActiveWorkers,
		ProcessingJobs:          m.metrics.ProcessingJobs,
		QueueSize:               m.metrics.QueueSize,
		WorkerUtilization:       m.metrics.WorkerUtilization,
		AverageAnalysisDuration: m.metrics.AverageAnalysisDuration,
		TotalListenersUpdated:   m.metrics.TotalListenersUpdated,
		FailureRate:             m.metrics.FailureRate,
		LastUpdate:              m.metrics.LastUpdate,
		UptimeSeconds:           m.metrics.UptimeSeconds,
	}
}

// GetJobHistory returns recent job history
func (m *Monitor) GetJobHistory(limit int) []JobHistoryEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if limit <= 0 || limit > len(m.jobHistory) {
		limit = len(m.jobHistory)
	}

	// Return most recent entries
	start := len(m.jobHistory) - limit
	if start < 0 {
		start = 0
	}

	history := make([]JobHistoryEntry, limit)
	copy(history, m.jobHistory[start:])
	return history
}

// calculateAverages calculates average durations and failure rates
func (m *Monitor) calculateAverages() {
	if len(m.jobHistory) == 0 {
		return
	}

	var totalDuration time.Duration
	var failedJobs int64
	recentJobs := m.jobHistory

	// Consider only last hour for throughput calculation
	oneHourAgo := time.Now().Add(-time.Hour)
	var recentJobCount int64

	for _, entry := range recentJobs {
		totalDuration += entry.Duration

		if entry.Status == job.JobStatusFailed {
			failedJobs++
		}

		if entry.CompletedAt.After(oneHourAgo) {
			recentJobCount++
		}
	}

	// Average job duration
	if len(recentJobs) > 0 {
		m.metrics.AverageJobDuration = totalDuration / time.Duration(len(recentJobs))
	}

	// Failure rate
	if len(recentJobs) > 0 {
		m.metrics.FailureRate = float64(failedJobs) / float64(len(recentJobs)) * 100
	}

	// Throughput per minute (jobs completed in last hour / 60)
	m.metrics.JobThroughputPerMin = float64(recentJobCount) / 60.0
}

// GetHealthStatus returns system health status
func (m *Monitor) GetHealthStatus() HealthStatus {
	metrics := m.GetMetrics()

	status := HealthStatus{
		Overall: "healthy",
		Checks:  make(map[string]CheckResult),
	}

	// Check failure rate
	if metrics.FailureRate > 20.0 {
		status.Checks["failure_rate"] = CheckResult{
			Status:  "critical",
			Message: "High failure rate detected",
			Value:   metrics.FailureRate,
		}
		status.Overall = "critical"
	} else if metrics.FailureRate > 10.0 {
		status.Checks["failure_rate"] = CheckResult{
			Status:  "warning",
			Message: "Elevated failure rate",
			Value:   metrics.FailureRate,
		}
		if status.Overall == "healthy" {
			status.Overall = "warning"
		}
	} else {
		status.Checks["failure_rate"] = CheckResult{
			Status:  "healthy",
			Message: "Normal failure rate",
			Value:   metrics.FailureRate,
		}
	}

	// Check queue size
	if metrics.QueueSize > 100 {
		status.Checks["queue_size"] = CheckResult{
			Status:  "warning",
			Message: "Large queue size",
			Value:   float64(metrics.QueueSize),
		}
		if status.Overall == "healthy" {
			status.Overall = "warning"
		}
	} else {
		status.Checks["queue_size"] = CheckResult{
			Status:  "healthy",
			Message: "Normal queue size",
			Value:   float64(metrics.QueueSize),
		}
	}

	// Check worker utilization
	if metrics.WorkerUtilization > 90.0 {
		status.Checks["worker_utilization"] = CheckResult{
			Status:  "warning",
			Message: "High worker utilization",
			Value:   metrics.WorkerUtilization,
		}
		if status.Overall == "healthy" {
			status.Overall = "warning"
		}
	} else {
		status.Checks["worker_utilization"] = CheckResult{
			Status:  "healthy",
			Message: "Normal worker utilization",
			Value:   metrics.WorkerUtilization,
		}
	}

	return status
}

// HealthStatus represents system health
type HealthStatus struct {
	Overall string                 `json:"overall"`
	Checks  map[string]CheckResult `json:"checks"`
}

// CheckResult represents individual health check result
type CheckResult struct {
	Status  string  `json:"status"`
	Message string  `json:"message"`
	Value   float64 `json:"value"`
}
