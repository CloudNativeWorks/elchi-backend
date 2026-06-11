package shield

import (
	"context"
	"errors"
	"testing"

	"github.com/CloudNativeWorks/elchi-backend/pkg/async/job"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

// fakeJobs captures CreateJob requests and returns a canned outcome.
type fakeJobs struct {
	got *job.CreateJobRequest
	err error
}

func (f *fakeJobs) CreateJob(_ context.Context, req *job.CreateJobRequest) (*job.Job, error) {
	f.got = req
	if f.err != nil {
		return nil, f.err
	}
	return &job.Job{JobID: "EC-42"}, nil
}

// dupErr builds a real mongo duplicate-key error (code 11000) so the enqueue
// path's mongo.IsDuplicateKeyError detection is exercised for real.
func dupErr() error {
	return mongo.WriteException{WriteErrors: []mongo.WriteError{{Code: 11000}}}
}

func TestEnqueueProjectDeploy_SetsLockKey(t *testing.T) {
	jobs := &fakeJobs{}
	e := NewEnqueuer(jobs, logger.NewLogger("test"))
	id, err := e.EnqueueProjectDeploy(context.Background(), "proj", models.UserDetails{UserID: "u", IsOwner: true}, ReasonPolicyChange)
	if err != nil || id != "EC-42" {
		t.Fatalf("unexpected: id=%q err=%v", id, err)
	}
	meta := jobs.got.Metadata.ShieldDeploy
	if meta.LockKey != "shield::proj" || meta.Reason != ReasonPolicyChange {
		t.Fatalf("bad meta: %+v", meta)
	}
}

func TestEnqueueProjectDeploy_DuplicateCoalesces(t *testing.T) {
	jobs := &fakeJobs{err: dupErr()}
	e := NewEnqueuer(jobs, logger.NewLogger("test"))
	_, err := e.EnqueueProjectDeploy(context.Background(), "proj", models.UserDetails{UserID: "u", IsOwner: true}, ReasonPolicyChange)
	if !errors.Is(err, ErrDeployAlreadyQueued) {
		t.Fatalf("a duplicate-key insert must map to ErrDeployAlreadyQueued, got: %v", err)
	}
}

func TestEnqueueClientConnect_SetsPerClientLockKeyAndSwallowsDuplicate(t *testing.T) {
	jobs := &fakeJobs{}
	e := NewEnqueuer(jobs, logger.NewLogger("test"))
	e.EnqueueClientConnect(context.Background(), "proj", "c1")
	meta := jobs.got.Metadata.ShieldDeploy
	if meta.LockKey != "shield::proj::c1" || meta.Reason != ReasonClientConnect {
		t.Fatalf("bad meta: %+v", meta)
	}

	// A flapping client's duplicate insert must be swallowed (best-effort path).
	jobs.err = dupErr()
	e.EnqueueClientConnect(context.Background(), "proj", "c1") // must not panic/log-error
}
