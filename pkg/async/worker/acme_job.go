package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/controller/poker"
	"github.com/CloudNativeWorks/elchi-backend/pkg/acme"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async/job"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// ACMEProcessor is an interface to avoid import cycles with pkg/acme
type ACMEProcessor interface {
	VerifyCertificateWithDNSProviderAsync(
		ctx context.Context,
		certID string,
		project string,
		progressCallback func(phase string, domain string, completed int, total int),
	) (*acme.ACMECertificate, error)
}

// processACMEVerificationJob handles DNS verification in background
func (w *Worker) processACMEVerificationJob(ctx context.Context, j *job.Job) {
	w.logger.Infof("Processing ACME verification job: %s", j.JobID)

	// 1. Validate job metadata
	if j.Metadata == nil || j.Metadata.ACMEMetadata == nil {
		w.logger.Errorf("Job %s has invalid metadata", j.JobID)
		if err := w.jobManager.FailJob(ctx, j.ID, fmt.Errorf("invalid job metadata")); err != nil {
			w.logger.Errorf("Failed to mark job as failed: %v", err)
		}
		return
	}

	metadata := j.Metadata.ACMEMetadata

	// 2. Initialize progress tracking
	totalDomains := len(metadata.Domains)
	if err := w.jobManager.UpdateJobProgress(ctx, j.ID, &job.JobProgress{
		Total:      totalDomains,
		Completed:  0,
		Percentage: 5, // Starting
	}); err != nil {
		w.logger.Errorf("Failed to update job progress: %v", err)
	}

	// 3. Get certificate manager from dbContext
	// Note: We need MongoDB database, not AppContext
	db := w.dbContext.Client
	acmeCfg := &w.dbContext.Config.ACME
	caProviders := &w.dbContext.Config.CAProviders
	jwtSecret := w.dbContext.Config.ElchiJWTSecret

	certManager, err := acme.NewCertificateManager(db, jwtSecret, acmeCfg, caProviders, w.logger)
	if err != nil {
		w.logger.Errorf("Failed to create certificate manager: %v", err)
		if failErr := w.jobManager.FailJob(ctx, j.ID, fmt.Errorf("failed to initialize certificate manager: %w", err)); failErr != nil {
			w.logger.Errorf("Failed to mark job as failed: %v", failErr)
		}
		return
	}

	// Set dependency services for snapshot updates on certificate renewal
	certManager.SetDependencyServices(w.dbContext, w.pokeService)

	// 4. Create context with dynamic timeout based on domain count
	// Base timeout: 6 minutes for first domain
	// Additional: 3 minutes per additional domain
	// Examples: 1 domain=6min, 2 domains=9min, 3 domains=12min, 5 domains=18min
	baseTimeout := 6 * time.Minute
	additionalTimeout := time.Duration(totalDomains-1) * 3 * time.Minute
	totalTimeout := baseTimeout + additionalTimeout

	verificationCtx, cancel := context.WithTimeout(ctx, totalTimeout)
	defer cancel()

	w.logger.Infof("🕐 Job %s: Timeout set to %.0f minutes for %d domain(s)",
		j.JobID, totalTimeout.Minutes(), totalDomains)

	// 5. Define progress callback
	progressCallback := func(phase string, domain string, completed int, total int) {
		// Calculate percentage based on phase
		var basePercentage float64
		switch phase {
		case "dns_setup":
			basePercentage = 10
		case "dns_propagation":
			basePercentage = 20 + (float64(completed)/float64(total))*40 // 20-60%
		case "challenge_validation":
			basePercentage = 60 + (float64(completed)/float64(total))*20 // 60-80%
		case "certificate_download":
			basePercentage = 80
		case "storing_secrets":
			basePercentage = 85 + (float64(completed)/float64(total))*10 // 85-95%
		default:
			basePercentage = 5
		}

		// Update job progress
		if err := w.jobManager.UpdateJobProgress(ctx, j.ID, &job.JobProgress{
			Total:      total,
			Completed:  completed,
			Percentage: basePercentage,
		}); err != nil {
			w.logger.Errorf("Failed to update job progress: %v", err)
		}

		w.logger.Infof("Job %s: %s - Domain %s (%d/%d) %.1f%%",
			j.JobID, phase, domain, completed, total, basePercentage)
	}

	// 6. Call verification function with progress tracking and renewal flag
	cert, err := certManager.VerifyCertificateWithDNSProviderAsync(
		verificationCtx,
		metadata.CertificateID,
		metadata.Project,
		progressCallback,
		metadata.IsRenewal, // Pass renewal flag - skips status check for active certificates
	)
	if err != nil {
		// 7. Handle failure
		w.logger.Errorf("ACME verification failed for job %s: %v", j.JobID, err)

		// Check if it was a timeout
		if verificationCtx.Err() == context.DeadlineExceeded {
			w.logger.Errorf("DNS verification timeout after %.0f minutes for job %s", totalTimeout.Minutes(), j.JobID)
			err = fmt.Errorf("DNS verification timeout after %.0f minutes", totalTimeout.Minutes())
		}

		// Update certificate status to verification_failed in MongoDB
		// IMPORTANT: Use background context if job context is canceled to ensure DB update succeeds
		updateCtx := ctx
		if ctx.Err() != nil {
			w.logger.Warnf("Job context canceled, using background context for certificate status update")
			updateCtx = context.Background()
		}

		certObjID, parseErr := primitive.ObjectIDFromHex(metadata.CertificateID)
		if parseErr == nil {
			collection := w.dbContext.Client.Collection("acme_certificates")
			filter := bson.M{
				"_id":     certObjID,
				"project": metadata.Project,
			}
			update := bson.M{
				"$set": bson.M{
					"status":             "verification_failed",
					"last_renewal_error": err.Error(),
				},
			}
			_, dbErr := collection.UpdateOne(updateCtx, filter, update)
			if dbErr != nil {
				w.logger.Errorf("Failed to update certificate status in DB: %v", dbErr)
			} else {
				w.logger.Infof("Updated certificate %s status to verification_failed in DB", metadata.CertificateID)
			}
		}

		// Update job execution details
		errMsg := err.Error()
		executionDetails := &job.ExecutionDetails{
			ACMEResult: &job.ACMEExecution{
				CertificateID: metadata.CertificateID,
				SecretName:    metadata.CertificateName,
				Domains:       metadata.Domains,
				Status:        "verification_failed",
				ErrorMessage:  &errMsg,
			},
		}

		// Fail the job with execution details
		if failErr := w.jobManager.FailJob(ctx, j.ID, err); failErr != nil {
			w.logger.Errorf("Failed to mark job as failed: %v", failErr)
		}

		// Store execution details even on failure
		objID, _ := primitive.ObjectIDFromHex(j.ID.Hex())
		if err := w.jobManager.UpdateJob(ctx, objID.Hex(), map[string]any{
			"$set": map[string]any{
				"execution_details": executionDetails,
			},
		}); err != nil {
			w.logger.Errorf("Failed to update job execution details: %v", err)
		}

		return
	}

	// 8. Success - certificate is now "active"
	w.logger.Infof("ACME verification completed for job %s", j.JobID)

	// Store execution details
	executionDetails := &job.ExecutionDetails{
		ACMEResult: &job.ACMEExecution{
			CertificateID: metadata.CertificateID,
			SecretName:    metadata.CertificateName,
			Domains:       metadata.Domains,
			Status:        "active",
			IssuedAt:      &cert.IssuedAt,
			ExpiresAt:     &cert.ExpiresAt,
		},
	}

	// Update progress to 100%
	if err := w.jobManager.UpdateJobProgress(ctx, j.ID, &job.JobProgress{
		Total:      totalDomains,
		Completed:  totalDomains,
		Percentage: 100,
	}); err != nil {
		w.logger.Errorf("Failed to update job progress: %v", err)
	}

	// Complete the job
	if err := w.jobManager.CompleteJob(ctx, j.ID, executionDetails); err != nil {
		w.logger.Errorf("Failed to complete job: %v", err)
	}

	w.logger.Infof("🎉 ACME job %s completed successfully for certificate %s",
		j.JobID, metadata.CertificateName)

	// 9. Create snapshot update jobs for each version of the certificate
	// This triggers Envoy configuration updates for listeners using this certificate
	w.createSnapshotUpdateJobs(ctx, cert, metadata, j)
}

// createSnapshotUpdateJobs creates async snapshot update jobs for certificate renewal
func (w *Worker) createSnapshotUpdateJobs(
	ctx context.Context,
	_ *acme.ACMECertificate,
	metadata *job.ACMEJobMetadata,
	parentJob *job.Job,
) {
	w.logger.Infof("📡 Creating snapshot update jobs for certificate %s across %d version(s)",
		metadata.CertificateName, len(metadata.Versions))

	jobsCreated := 0
	for _, version := range metadata.Versions {
		// Run dependency analysis BEFORE creating the job
		// This is the same approach as HandleResourceChange in controller/crud/base.go
		w.logger.Infof("Running dependency analysis for certificate %s (version: %s)",
			metadata.CertificateName, version)

		analysisStart := time.Now()
		initialProcessed := poker.Processed{
			Listeners:          []string{},
			ProcessedResources: []string{},
			Depends:            []string{},
		}

		// Use poker.DetectChangedResource to find affected listeners
		// Pass nil for poke service - we only want analysis, actual pokes will happen in worker
		result := poker.DetectChangedResource(
			ctx,
			models.TLSCertificate,
			version,
			metadata.CertificateName,
			metadata.Project,
			w.dbContext,
			&initialProcessed,
			nil,   // No poke service - we'll poke later in the snapshot worker
			false, // not managed - we'll handle multi-client in snapshot worker
		)

		analysisDuration := time.Since(analysisStart).Milliseconds()

		// Get unique listeners
		uniqueListeners := make(map[string]bool)
		for _, listener := range result.Listeners {
			uniqueListeners[listener] = true
		}
		affectedListeners := make([]string, 0, len(uniqueListeners))
		for listener := range uniqueListeners {
			affectedListeners = append(affectedListeners, listener)
		}

		w.logger.Infof("Dependency analysis completed in %dms: found %d affected listener(s): %v",
			analysisDuration, len(affectedListeners), affectedListeners)

		// Create snapshot update job with dependency analysis results
		jobReq := &job.CreateJobRequest{
			Type:   job.JobTypeSnapshotUpdate,
			Status: job.JobStatusPending,
			Metadata: &job.JobMetadata{
				SourceResource: &job.SourceResource{
					Type:       models.TLSCertificate.String(),
					Name:       metadata.CertificateName,
					Collection: "secrets",
					Action:     job.ActionType("UPDATE"),
					ProjectID:  metadata.Project,
					Version:    version,
				},
				TriggerUser: &job.TriggerUser{
					ID:       "system",
					Username: "letsencrypt-auto-renewal",
					Role:     "owner",
				},
				AffectedListeners: affectedListeners,      // Populated by dependency analysis
				TotalAffected:     len(affectedListeners), // Set from analysis
				AnalysisDuration:  analysisDuration,       // Record how long analysis took
			},
		}

		// Create the job with parent reference for dependency tracking
		createdJob, err := w.jobManager.CreateJobWithParent(ctx, jobReq, &parentJob.ID)
		if err != nil {
			w.logger.Errorf("Failed to create snapshot update job for version %s: %v", version, err)
			continue
		}

		jobsCreated++
		w.logger.Infof("Created snapshot update job %s for certificate %s (version: %s, %d listeners, parent: %s)",
			createdJob.JobID, metadata.CertificateName, version, len(affectedListeners), parentJob.JobID)
	}

	if jobsCreated > 0 {
		w.logger.Infof("Created %d snapshot update job(s) for certificate %s renewal",
			jobsCreated, metadata.CertificateName)
	} else {
		w.logger.Warnf("No snapshot update jobs created for certificate %s (may not be in use yet)",
			metadata.CertificateName)
	}
}
