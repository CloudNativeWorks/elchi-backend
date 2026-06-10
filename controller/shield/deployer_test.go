package shield

import (
	"context"
	"errors"
	"testing"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

func init() {
	_ = logger.Init(logger.Config{Level: "error", Format: "text", OutputPath: "stdout", Module: "test"})
}

// fakeStore is an in-memory policyStore.
type fakeStore struct {
	policies  []ShieldPolicy
	connected []string
	listErr   error
	connErr   error
}

func (f *fakeStore) List(_ context.Context, _ string) ([]ShieldPolicy, error) {
	return f.policies, f.listErr
}
func (f *fakeStore) ListConnectedClientIDs(_ context.Context, _ string) ([]string, error) {
	return f.connected, f.connErr
}

// fakeRunner captures the op it was handed.
type fakeRunner struct {
	gotOp   models.OperationClass
	gotReq  models.RequestDetails
	result  any
	err     error
	callCnt int
}

func (f *fakeRunner) HandleSendCommand(_ context.Context, op models.OperationClass, rd models.RequestDetails) (any, error) {
	f.callCnt++
	f.gotOp = op
	f.gotReq = rd
	return f.result, f.err
}

func newDeployer(store policyStore, runner CommandRunner) *Deployer {
	return &Deployer{store: store, runner: runner, logger: logger.NewLogger("test")}
}

func TestDeployProject_PushesMergedBundleToExplicitTargets(t *testing.T) {
	store := &fakeStore{policies: []ShieldPolicy{
		policy("a", file("api.yaml", []byte("A"), "", "")),
		policy("b", file("feeds/x.json", []byte("B"), "", "")),
	}}
	runner := &fakeRunner{result: "ok"}
	d := newDeployer(store, runner)

	res, err := d.DeployProject(context.Background(), "proj", []string{"c1", "c2"}, ReasonPolicyChange, models.UserDetails{UserID: "u", IsOwner: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != "ok" {
		t.Fatalf("want runner result passed through, got %v", res)
	}
	if runner.callCnt != 1 {
		t.Fatalf("runner called %d times, want 1", runner.callCnt)
	}
	if got := len(runner.gotOp.GetClients()); got != 2 {
		t.Fatalf("want 2 target clients, got %d", got)
	}
	if runner.gotOp.GetType() != "SHIELD" {
		t.Fatalf("want SHIELD command type, got %v", runner.gotOp.GetType())
	}
	shieldOp := runner.gotOp.GetShield()
	if shieldOp == nil || shieldOp.Config == nil {
		t.Fatal("op carries no shield config")
	}
	if !shieldOp.Config.FullSync || len(shieldOp.Config.Files) != 2 {
		t.Fatalf("want merged full-sync bundle of 2 files, got %+v", shieldOp.Config)
	}
	if shieldOp.Config.Version == "" {
		t.Fatal("merged bundle has no version")
	}
}

func TestDeployProject_EmptyTargetsResolvesConnected(t *testing.T) {
	store := &fakeStore{
		policies:  []ShieldPolicy{policy("a", file("api.yaml", []byte("A"), "", ""))},
		connected: []string{"c1", "c2", "c3"},
	}
	runner := &fakeRunner{result: "ok"}
	d := newDeployer(store, runner)

	if _, err := d.DeployProject(context.Background(), "proj", nil, ReasonPolicyChange, models.UserDetails{IsOwner: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(runner.gotOp.GetClients()); got != 3 {
		t.Fatalf("want 3 resolved clients, got %d", got)
	}
}

func TestDeployProject_NoConnectedClientsSkipsPush(t *testing.T) {
	store := &fakeStore{
		policies:  []ShieldPolicy{policy("a", file("api.yaml", []byte("A"), "", ""))},
		connected: nil,
	}
	runner := &fakeRunner{}
	d := newDeployer(store, runner)

	res, err := d.DeployProject(context.Background(), "proj", nil, ReasonPolicyChange, models.UserDetails{IsOwner: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner.callCnt != 0 {
		t.Fatal("runner must not be called when there are no connected clients")
	}
	m, ok := res.(map[string]any)
	if !ok || m["clients"] != 0 {
		t.Fatalf("want a zero-clients summary, got %v", res)
	}
}

func TestDeployProject_ConnectWithNoPoliciesSkipsPush(t *testing.T) {
	// A (re)connecting client in a project with no shield config must not get an
	// empty "clear" bundle pushed at it.
	store := &fakeStore{policies: nil}
	runner := &fakeRunner{}
	d := newDeployer(store, runner)

	if _, err := d.DeployProject(context.Background(), "proj", []string{"c1"}, ReasonClientConnect, models.UserDetails{IsOwner: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner.callCnt != 0 {
		t.Fatal("connect deploy with no shield config must not push")
	}
}

func TestDeployProject_PolicyChangeWithNoPoliciesClearsClients(t *testing.T) {
	// Deleting the last policy (reason policy_change) must still push the empty
	// full-sync bundle so connected clients are cleared.
	store := &fakeStore{policies: nil, connected: []string{"c1"}}
	runner := &fakeRunner{result: "ok"}
	d := newDeployer(store, runner)

	if _, err := d.DeployProject(context.Background(), "proj", nil, ReasonPolicyChange, models.UserDetails{IsOwner: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner.callCnt != 1 {
		t.Fatal("policy_change clear must push the empty full-sync bundle to connected clients")
	}
	shieldOp := runner.gotOp.GetShield()
	if shieldOp == nil || shieldOp.Config == nil || !shieldOp.Config.FullSync || len(shieldOp.Config.Files) != 0 {
		t.Fatalf("want an empty full-sync clear bundle, got %+v", shieldOp)
	}
}

func TestDeployProject_RunnerErrorFailsJob(t *testing.T) {
	store := &fakeStore{policies: []ShieldPolicy{policy("a", file("api.yaml", []byte("A"), "", ""))}}
	runner := &fakeRunner{err: errors.New("stream dead")}
	d := newDeployer(store, runner)

	if _, err := d.DeployProject(context.Background(), "proj", []string{"c1"}, ReasonPolicyChange, models.UserDetails{IsOwner: true}); err == nil {
		t.Fatal("a push error must surface so the job fails and can be retried")
	}
}

func TestDeployProject_ListErrorFailsJob(t *testing.T) {
	store := &fakeStore{listErr: errors.New("mongo down")}
	d := newDeployer(store, &fakeRunner{})
	if _, err := d.DeployProject(context.Background(), "proj", []string{"c1"}, ReasonPolicyChange, models.UserDetails{IsOwner: true}); err == nil {
		t.Fatal("a store list error must fail the deploy")
	}
}

func TestDeployProject_MergeCollisionFailsJob(t *testing.T) {
	// Two policies claiming the same path can't be merged.
	store := &fakeStore{policies: []ShieldPolicy{
		policy("a", file("same.yaml", []byte("1"), "", "")),
		policy("b", file("same.yaml", []byte("2"), "", "")),
	}}
	d := newDeployer(store, &fakeRunner{})
	if _, err := d.DeployProject(context.Background(), "proj", []string{"c1"}, ReasonPolicyChange, models.UserDetails{IsOwner: true}); err == nil {
		t.Fatal("a merge collision must fail the deploy")
	}
}
