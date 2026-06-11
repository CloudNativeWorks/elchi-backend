package worker

import (
	"context"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/async/job"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// ShieldDeployer renders a project's merged elchi-shield policy set into one
// full-sync bundle and pushes it to the project's clients. It is an interface to
// avoid an import cycle with controller/shield; the implementation lives in
// controller/shield/deployer.go and reuses the standard client command path
// (processor/responser/fan-out + multi-pod forwarding).
//
// clientIDs scopes the push: empty = all currently-connected clients in the
// project (a project-wide deploy); non-empty = exactly those clients (a single
// client that just (re)connected). reason is the job's trigger ("policy_change" or
// "client_connect"); a connect-triggered deploy with no shield config for the
// project is a no-op (we don't push an empty "clear" bundle to a client that has
// no desired state). user is the identity the deploy runs as, used for command
// authorization and forward-token minting on the target pod.
type ShieldDeployer interface {
	DeployProject(ctx context.Context, project string, clientIDs []string, reason string, user models.UserDetails) (*job.ShieldDeployResult, error)
}

// processShieldDeployJob renders and pushes a project's merged shield config.
// It mirrors waf_job.go: validate metadata, reconstruct the trigger user, call
// the injected deployer, record progress, and complete/fail the job. A push that
// reaches no client (all sends failed) fails the job so it can be retried; a
// store/merge error also fails it. Shield is idempotent (it skips unchanged
// files), so a retried or repeated deploy is cheap and safe.
func (w *Worker) processShieldDeployJob(ctx context.Context, j *job.Job) {
	w.logger.Infof("Starting shield deploy job %s", j.JobID)

	if j.Metadata == nil || j.Metadata.ShieldDeploy == nil {
		w.logger.Errorf("Shield deploy job %s has invalid metadata", j.JobID)
		if err := w.jobManager.FailJob(ctx, j.ID, fmt.Errorf("invalid job metadata")); err != nil {
			w.logger.Errorf("Failed to mark job as failed: %v", err)
		}
		return
	}

	deployer := w.shieldDeployer
	if deployer == nil {
		w.logger.Errorf("Shield deployer not available for job %s", j.JobID)
		if err := w.jobManager.FailJob(ctx, j.ID, fmt.Errorf("shield deployer not configured")); err != nil {
			w.logger.Errorf("Failed to mark job as failed: %v", err)
		}
		return
	}

	meta := j.Metadata.ShieldDeploy

	// Reconstruct the trigger identity (mirrors waf_job.go). A connect-triggered
	// job carries the synthetic system owner; a CRUD-triggered job carries the
	// admin who mutated the policy. Fall back to a system owner if absent so the
	// deploy still authorizes rather than silently dropping.
	user := models.UserDetails{UserID: "system", UserName: "system", Role: models.RoleAdmin, IsOwner: true}
	if tu := j.Metadata.TriggerUser; tu != nil && tu.ID != "" {
		user = models.UserDetails{
			UserID:   tu.ID,
			UserName: tu.Username,
			Role:     models.Role(tu.Role),
			IsOwner:  tu.Role == "owner",
			Projects: tu.Projects,
		}
	}

	result, err := deployer.DeployProject(ctx, meta.Project, meta.TargetClients, meta.Reason, user)
	if err != nil {
		w.logger.Errorf("Shield deploy job %s failed: %v", j.JobID, err)
		if failErr := w.jobManager.FailJob(ctx, j.ID, err); failErr != nil {
			w.logger.Errorf("Failed to mark job as failed: %v", failErr)
		}
		return
	}
	if result == nil {
		result = &job.ShieldDeployResult{}
	}

	// Record per-client outcome + progress so the deploy is inspectable via the job.
	pct := 100.0
	if result.Total > 0 {
		pct = float64(result.Succeeded) / float64(result.Total) * 100.0
	}
	if err := w.jobManager.UpdateJobProgress(ctx, j.ID, &job.JobProgress{
		Total:      result.Total,
		Completed:  result.Succeeded,
		Failed:     result.Failed,
		Percentage: pct,
	}); err != nil {
		w.logger.Errorf("Failed to update job progress: %v", err)
	}
	exec := &job.ExecutionDetails{ShieldResult: result}

	// Every targeted client failed (e.g. all disconnected, or shield rejected the
	// bundle everywhere) → fail the job so it surfaces and can be retried. Persist
	// the result first so the per-client detail survives on the failed job.
	if result.Total > 0 && result.Succeeded == 0 {
		if uerr := w.jobManager.UpdateJob(ctx, j.ID.Hex(), map[string]any{
			"$set": map[string]any{"execution_details": exec},
		}); uerr != nil {
			w.logger.Errorf("Failed to persist shield deploy result: %v", uerr)
		}
		failErr := fmt.Errorf("shield deploy: all %d targeted client(s) failed", result.Total)
		w.logger.Errorf("Shield deploy job %s: %v", j.JobID, failErr)
		if ferr := w.jobManager.FailJob(ctx, j.ID, failErr); ferr != nil {
			w.logger.Errorf("Failed to mark job as failed: %v", ferr)
		}
		return
	}

	if err := w.jobManager.CompleteJob(ctx, j.ID, exec); err != nil {
		w.logger.Errorf("Failed to complete job: %v", err)
	}
	if result.Failed > 0 {
		w.logger.Warnf("Shield deploy job %s completed with partial failures (project=%s, reason=%s, %d/%d ok, version=%s)",
			j.JobID, meta.Project, meta.Reason, result.Succeeded, result.Total, result.Version)
	} else {
		w.logger.Infof("Shield deploy job %s completed (project=%s, reason=%s, clients=%d, version=%s)",
			j.JobID, meta.Project, meta.Reason, result.Total, result.Version)
	}
}
