package shield

import (
	"context"
	"errors"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/async/job"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/mongo"
)

// Deploy reasons recorded on SHIELD_DEPLOY jobs for observability.
const (
	ReasonPolicyChange  = "policy_change"
	ReasonClientConnect = "client_connect"
)

// ErrDeployAlreadyQueued reports that an identical deploy is already PENDING
// (rejected by the shield_deploy_lock_key_unique_pending partial index). Not a
// failure: the queued job reads the policy store at execution time, so it will
// deliver the caller's change too.
var ErrDeployAlreadyQueued = errors.New("a shield deploy for this target is already queued")

// JobCreator is the narrow slice of the async job system the enqueuer needs.
// Satisfied by async.AsyncJobSystem.
type JobCreator interface {
	CreateJob(ctx context.Context, req *job.CreateJobRequest) (*job.Job, error)
}

// Enqueuer turns shield deploy triggers (a policy CRUD mutation, a manual sync, or
// a client (re)connect) into asynchronous SHIELD_DEPLOY jobs. The actual render +
// push happens later in a worker via the injected Deployer, so the HTTP request /
// connect handshake never blocks on per-client command round-trips.
type Enqueuer struct {
	jobs   JobCreator
	logger *logger.Logger
}

// NewEnqueuer builds a shield deploy enqueuer over the async job system.
func NewEnqueuer(jobs JobCreator, logger *logger.Logger) *Enqueuer {
	return &Enqueuer{jobs: jobs, logger: logger}
}

// EnqueueProjectDeploy enqueues a job that deploys the project's merged policy set
// to all of its connected clients (TargetClients nil). Used after a policy
// create/update/delete or a manual sync. user is the admin who triggered it
// (carried so the worker's command authorization and forward-token minting run as
// that identity). Returns the human-friendly job id.
func (e *Enqueuer) EnqueueProjectDeploy(ctx context.Context, project string, user models.UserDetails, reason string) (string, error) {
	if e == nil || e.jobs == nil {
		return "", fmt.Errorf("shield deploy enqueuer not configured")
	}
	req := &job.CreateJobRequest{
		Type:   job.JobTypeShieldDeploy,
		Status: job.JobStatusPending,
		Metadata: &job.JobMetadata{
			TriggerUser: triggerUser(user),
			ShieldDeploy: &job.ShieldDeployMeta{
				Project: project,
				Reason:  reason,
				LockKey: "shield::" + project,
			},
		},
	}
	created, err := e.jobs.CreateJob(ctx, req)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			// A project-wide deploy is already PENDING; it reads the policy store
			// at execution time, so it will carry this change too.
			return "", ErrDeployAlreadyQueued
		}
		return "", err
	}
	return created.JobID, nil
}

// EnqueueClientConnect enqueues a job that deploys the project's current merged
// config to a single client that just (re)connected. Best-effort: it logs and
// swallows errors so a deploy hiccup never breaks the connect handshake. The job
// runs as the synthetic system owner (no human triggered it).
func (e *Enqueuer) EnqueueClientConnect(ctx context.Context, project, clientID string) {
	if e == nil || e.jobs == nil {
		return
	}
	if project == "" || clientID == "" {
		return
	}
	req := &job.CreateJobRequest{
		Type:   job.JobTypeShieldDeploy,
		Status: job.JobStatusPending,
		Metadata: &job.JobMetadata{
			TriggerUser: systemTriggerUser(),
			ShieldDeploy: &job.ShieldDeployMeta{
				Project:       project,
				TargetClients: []string{clientID},
				Reason:        ReasonClientConnect,
				LockKey:       "shield::" + project + "::" + clientID,
			},
		},
	}
	if _, err := e.jobs.CreateJob(ctx, req); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			// A connect deploy for this client is already queued (the client is
			// flapping); the queued one delivers the current desired state.
			if e.logger != nil {
				e.logger.Debugf("shield: connect deploy for client %s already queued; coalesced", clientID)
			}
			return
		}
		if e.logger != nil {
			e.logger.Warnf("shield: failed to enqueue connect deploy for client %s (project %s): %v", clientID, project, err)
		}
	}
}

// triggerUser snapshots the human trigger identity onto the job metadata.
func triggerUser(user models.UserDetails) *job.TriggerUser {
	role := "owner"
	if !user.IsOwner {
		role = string(user.Role)
	}
	return &job.TriggerUser{
		ID:          user.UserID,
		Username:    user.UserName,
		DisplayName: user.UserName,
		Role:        role,
		Projects:    user.Projects,
	}
}

// systemTriggerUser is the identity for system-initiated (connect) deploys.
func systemTriggerUser() *job.TriggerUser {
	return &job.TriggerUser{ID: "system", Username: "system", DisplayName: "system", Role: "owner"}
}
