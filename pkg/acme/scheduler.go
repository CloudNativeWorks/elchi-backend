package acme

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// ACMEJobRequest represents a request to create a Let's Encrypt verification job
// Duplicated here to avoid import cycle with pkg/async
type ACMEJobRequest struct {
	CertificateID   string
	CertificateName string
	Domains         []string
	Environment     string
	DNSCredentialID string
	ACMEAccountID   string
	Project         string
	Versions        []string
	TriggerUser     models.UserDetails
}

// ACMEJob represents a created job (minimal interface)
type ACMEJob struct {
	JobID string
}

// AsyncJobCreator is an interface for creating Let's Encrypt verification jobs
// This avoids circular dependency with pkg/async
type AsyncJobCreator interface {
	CreateACMEVerificationJob(ctx context.Context, req *ACMEJobRequest) (*ACMEJob, error)
}

// RenewalScheduler handles automatic certificate renewal checks
type RenewalScheduler struct {
	manager       *CertificateManager
	asyncCreator  AsyncJobCreator
	logger        *logger.Logger
	interval      time.Duration
	stopCh        chan struct{}
	wg            sync.WaitGroup
	running       bool
	mu            sync.Mutex
	projects      []string // List of projects to check
	checkInterval time.Duration
	stopOnce      sync.Once // Ensures Stop() is only executed once
}

// NewRenewalScheduler creates a new renewal scheduler
func NewRenewalScheduler(manager *CertificateManager, logger *logger.Logger, interval time.Duration) *RenewalScheduler {
	return &RenewalScheduler{
		manager:       manager,
		logger:        logger,
		interval:      interval,
		stopCh:        make(chan struct{}),
		projects:      []string{}, // Will be populated dynamically
		checkInterval: 24 * time.Hour,
	}
}

// SetAsyncJobCreator sets the async job creator after initialization
// This is called after the scheduler is created to avoid import cycles
func (s *RenewalScheduler) SetAsyncJobCreator(creator AsyncJobCreator) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asyncCreator = creator
}

// Start begins the renewal scheduler
func (s *RenewalScheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("scheduler already running")
	}
	s.running = true
	s.mu.Unlock()

	s.logger.Info("Starting Acme renewal scheduler")

	// Start the renewal check loop
	s.wg.Add(1)
	go s.renewalLoop(ctx)

	return nil
}

// Stop gracefully stops the renewal scheduler
// Safe to call multiple times - uses sync.Once to prevent double-close panic
func (s *RenewalScheduler) Stop() error {
	var stopErr error

	s.stopOnce.Do(func() {
		s.mu.Lock()
		if !s.running {
			s.mu.Unlock()
			stopErr = fmt.Errorf("scheduler not running")
			return
		}
		s.running = false
		s.mu.Unlock()

		s.logger.Info("Stopping Acme renewal scheduler")
		close(s.stopCh)
		s.wg.Wait()
		s.logger.Info("Acme renewal scheduler stopped")
	})

	return stopErr
}

// renewalLoop runs the periodic renewal checks
func (s *RenewalScheduler) renewalLoop(ctx context.Context) {
	defer s.wg.Done()

	// Run immediately on startup
	s.checkRenewals(ctx)

	ticker := time.NewTicker(s.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("Renewal scheduler context canceled")
			return
		case <-s.stopCh:
			s.logger.Info("Renewal scheduler stop signal received")
			return
		case <-ticker.C:
			s.checkRenewals(ctx)
		}
	}
}

// checkRenewals checks all projects for certificates that need renewal
func (s *RenewalScheduler) checkRenewals(ctx context.Context) {
	s.logger.Info("Starting certificate renewal check")
	startTime := time.Now()

	// Get all unique projects from acme_certificates collection
	projects, err := s.getActiveProjects(ctx)
	if err != nil {
		s.logger.Errorf("Failed to get active projects: %v", err)
		return
	}

	if len(projects) == 0 {
		s.logger.Info("No projects with certificates found")
		return
	}

	s.logger.Infof("Checking %d projects for certificate renewals", len(projects))

	totalChecked := 0
	totalRenewed := 0
	totalFailed := 0
	totalExpired := 0

	// Check each project
	for _, project := range projects {
		// First, mark expired certificates
		expired := s.markExpiredCertificates(ctx, project)
		totalExpired += expired

		// Then check for renewals
		checked, renewed, failed := s.checkProjectRenewals(ctx, project)
		totalChecked += checked
		totalRenewed += renewed
		totalFailed += failed
	}

	duration := time.Since(startTime)
	s.logger.Infof("Renewal check completed in %v - Expired: %d, Checked: %d, Renewed: %d, Failed: %d",
		duration, totalExpired, totalChecked, totalRenewed, totalFailed)
}

// getActiveProjects retrieves all unique projects that have certificates
func (s *RenewalScheduler) getActiveProjects(ctx context.Context) ([]string, error) {
	collection := s.manager.db.Collection("acme_certificates")

	// Get distinct projects
	projects, err := collection.Distinct(ctx, "project", map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("failed to get distinct projects: %w", err)
	}

	// Convert []interface{} to []string
	projectList := make([]string, 0, len(projects))
	for _, p := range projects {
		if projectStr, ok := p.(string); ok {
			projectList = append(projectList, projectStr)
		}
	}

	return projectList, nil
}

// checkProjectRenewals checks certificates for a specific project
func (s *RenewalScheduler) checkProjectRenewals(ctx context.Context, project string) (checked, renewed, failed int) {
	s.logger.Infof("Checking renewals for project: %s", project)

	// Get certificates that need renewal
	certificates, err := s.manager.GetCertificatesForRenewal(ctx, project)
	if err != nil {
		s.logger.Errorf("Failed to get certificates for renewal (project: %s): %v", project, err)
		return 0, 0, 0
	}

	checked = len(certificates)
	if checked == 0 {
		s.logger.Debugf("No certificates need renewal in project: %s", project)
		return checked, 0, 0
	}

	s.logger.Infof("Found %d certificates needing renewal in project: %s", checked, project)

	// Attempt to renew each certificate
	for _, cert := range certificates {
		s.logger.Infof("Attempting to renew certificate: %s (domains: %v, expires: %s)",
			cert.SecretName, cert.Domains, cert.ExpiresAt.Format(time.RFC3339))

		err := s.renewCertificate(ctx, cert, project)
		if err != nil {
			s.logger.Errorf("Failed to renew certificate %s: %v", cert.SecretName, err)
			failed++
		} else {
			s.logger.Infof("Successfully renewed certificate: %s", cert.SecretName)
			renewed++
		}
	}

	return checked, renewed, failed
}

// renewCertificate attempts to renew a single certificate
func (s *RenewalScheduler) renewCertificate(ctx context.Context, cert *ACMECertificate, project string) error {
	// Validate certificate has DNS provider configured
	if cert.DNSVerification.Provider == "manual" {
		s.logger.Warnf("Skipping certificate %s - manual DNS verification not supported for automatic renewal", cert.SecretName)
		return fmt.Errorf("certificate uses manual DNS verification - automatic renewal not supported")
	}

	if cert.DNSVerification.DNSCredentialID == nil || cert.DNSVerification.DNSCredentialID.IsZero() {
		s.logger.Warnf("Skipping certificate %s - no DNS credential configured", cert.SecretName)
		return fmt.Errorf("no DNS credential configured for automatic renewal")
	}

	// Check if we've exceeded max renewal attempts
	maxAttempts := 3
	if cert.RenewalAttempts >= maxAttempts {
		s.logger.Warnf("Skipping certificate %s - exceeded maximum renewal attempts (%d/%d)", cert.SecretName, cert.RenewalAttempts, maxAttempts)
		return fmt.Errorf("exceeded maximum renewal attempts (%d)", maxAttempts)
	}

	// Validate ACME account reference
	if cert.ACME.AccountID == nil || cert.ACME.AccountID.IsZero() {
		s.logger.Warnf("Skipping certificate %s - no ACME account reference", cert.SecretName)
		return fmt.Errorf("certificate has no ACME account reference")
	}

	// Check if async creator is set
	if s.asyncCreator == nil {
		s.logger.Errorf("Async job creator not set for scheduler - cannot create renewal job")
		return fmt.Errorf("async job creator not configured")
	}

	// Create async job for renewal (non-blocking)
	s.logger.Infof("Creating async renewal job for certificate %s (domains: %v)", cert.SecretName, cert.Domains)

	// Create system user details for job
	systemUser := models.UserDetails{
		UserID:   "system",
		UserName: "Let's Encrypt Auto-Renewal Scheduler",
		Role:     models.RoleOwner,
		IsOwner:  true,
	}

	// Create job request
	jobRequest := &ACMEJobRequest{
		CertificateID:   cert.ID.Hex(),
		CertificateName: cert.SecretName,
		Domains:         cert.Domains,
		Environment:     cert.ACME.Environment,
		DNSCredentialID: cert.DNSVerification.DNSCredentialID.Hex(),
		ACMEAccountID:   cert.ACME.AccountID.Hex(),
		Project:         project,
		Versions:        cert.SecretVersions,
		TriggerUser:     systemUser,
	}

	// Create async job
	job, err := s.asyncCreator.CreateACMEVerificationJob(ctx, jobRequest)
	if err != nil {
		s.logger.Errorf("Failed to create renewal job for certificate %s: %v", cert.SecretName, err)
		return fmt.Errorf("failed to create renewal job: %w", err)
	}

	s.logger.Infof("Created renewal job %s for certificate %s - renewal will run in background", job.JobID, cert.SecretName)

	// Update certificate with job ID
	err = s.manager.UpdateCertificateJobID(ctx, cert.ID.Hex(), project, job.JobID)
	if err != nil {
		s.logger.Warnf("Failed to update certificate %s with job ID: %v", cert.SecretName, err)
		// Don't fail renewal, job ID is optional
	}

	return nil
}

// markExpiredCertificates updates the status of expired certificates to "expired"
func (s *RenewalScheduler) markExpiredCertificates(ctx context.Context, project string) int {
	collection := s.manager.db.Collection("acme_certificates")

	// Find active certificates that have expired
	filter := map[string]any{
		"project":    project,
		"status":     "active",
		"expires_at": map[string]any{"$lt": time.Now()},
	}

	update := map[string]any{
		"$set": map[string]any{
			"status": "expired",
		},
	}

	result, err := collection.UpdateMany(ctx, filter, update)
	if err != nil {
		s.logger.Errorf("Failed to mark expired certificates for project %s: %v", project, err)
		return 0
	}

	if result.ModifiedCount > 0 {
		s.logger.Infof("Marked %d expired certificate(s) in project %s", result.ModifiedCount, project)
	}

	return int(result.ModifiedCount)
}
