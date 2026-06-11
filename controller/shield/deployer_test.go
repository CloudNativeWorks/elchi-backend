package shield

import (
	"context"
	"errors"
	"testing"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	client "github.com/CloudNativeWorks/elchi-proto/client"
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

// fakeRunner captures the op it was handed and returns a canned result.
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

// okResult mimics the ShieldResponser map for a client that applied the bundle.
func okResult(id string) map[string]any {
	return map[string]any{
		"success":   true,
		"error":     "",
		"client_id": id,
		"shield":    map[string]any{"success": true, "reload_ok": true, "applied_version": "v"},
	}
}

// rejectedResult mimics a client that received the command but whose shield
// rejected the bundle (reload_ok=false).
func rejectedResult(id string) map[string]any {
	return map[string]any{
		"success":   true,
		"client_id": id,
		"shield":    map[string]any{"success": false, "reload_ok": false, "error": "policy invalid"},
	}
}

func TestDeployProject_PushesMergedBundleToExplicitTargets(t *testing.T) {
	store := &fakeStore{policies: []ShieldPolicy{
		policy("a", file("api.yaml", []byte("A"), "", "")),
		policy("b", file("feeds/x.json", []byte("B"), "", "")),
	}}
	runner := &fakeRunner{result: []any{okResult("c1"), okResult("c2")}}
	d := newDeployer(store, runner)

	res, err := d.DeployProject(context.Background(), "proj", []string{"c1", "c2"}, ReasonPolicyChange, models.UserDetails{UserID: "u", IsOwner: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Total != 2 || res.Succeeded != 2 || res.Failed != 0 {
		t.Fatalf("want 2/2 ok, got %+v", res)
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
	if shieldOp == nil || shieldOp.Config == nil || !shieldOp.Config.FullSync || len(shieldOp.Config.Files) != 2 {
		t.Fatalf("want merged full-sync bundle of 2 files, got %+v", shieldOp)
	}
	if res.Version == "" {
		t.Fatal("result has no version")
	}
}

func TestDeployProject_EmptyTargetsResolvesConnected(t *testing.T) {
	store := &fakeStore{
		policies:  []ShieldPolicy{policy("a", file("api.yaml", []byte("A"), "", ""))},
		connected: []string{"c1", "c2", "c3"},
	}
	runner := &fakeRunner{result: []any{okResult("c1"), okResult("c2"), okResult("c3")}}
	d := newDeployer(store, runner)

	res, err := d.DeployProject(context.Background(), "proj", nil, ReasonPolicyChange, models.UserDetails{IsOwner: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(runner.gotOp.GetClients()); got != 3 {
		t.Fatalf("want 3 resolved clients, got %d", got)
	}
	if res.Total != 3 || res.Succeeded != 3 {
		t.Fatalf("want 3/3 ok, got %+v", res)
	}
}

func TestDeployProject_PartialFailureCountsPerClient(t *testing.T) {
	store := &fakeStore{policies: []ShieldPolicy{policy("a", file("api.yaml", []byte("A"), "", ""))}}
	// c1 applied, c2's shield rejected, c3 sent no response at all.
	runner := &fakeRunner{result: []any{okResult("c1"), rejectedResult("c2")}}
	d := newDeployer(store, runner)

	res, err := d.DeployProject(context.Background(), "proj", []string{"c1", "c2", "c3"}, ReasonPolicyChange, models.UserDetails{IsOwner: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Total != 3 || res.Succeeded != 1 || res.Failed != 2 {
		t.Fatalf("want 1 ok / 2 failed of 3, got %+v", res)
	}
	// c3 had no response → recorded as a failure with a clear reason.
	found := false
	for _, c := range res.Clients {
		if c.ClientID == "c3" {
			found = true
			if c.Ok || c.Error == "" {
				t.Fatalf("c3 with no response must be a failure with an error, got %+v", c)
			}
		}
	}
	if !found {
		t.Fatal("c3 missing from per-client results")
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
	if res.Total != 0 || res.Note == "" {
		t.Fatalf("want a zero-target result with a note, got %+v", res)
	}
}

func TestDeployProject_ConnectWithNoPoliciesSkipsPush(t *testing.T) {
	store := &fakeStore{policies: nil}
	runner := &fakeRunner{}
	d := newDeployer(store, runner)

	res, err := d.DeployProject(context.Background(), "proj", []string{"c1"}, ReasonClientConnect, models.UserDetails{IsOwner: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner.callCnt != 0 {
		t.Fatal("connect deploy with no shield config must not push")
	}
	if res.Total != 0 {
		t.Fatalf("want zero-target result, got %+v", res)
	}
}

func TestDeployProject_PolicyChangeWithNoPoliciesClearsClients(t *testing.T) {
	store := &fakeStore{policies: nil, connected: []string{"c1"}}
	runner := &fakeRunner{result: []any{okResult("c1")}}
	d := newDeployer(store, runner)

	res, err := d.DeployProject(context.Background(), "proj", nil, ReasonPolicyChange, models.UserDetails{IsOwner: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner.callCnt != 1 {
		t.Fatal("policy_change clear must push the empty full-sync bundle to connected clients")
	}
	shieldOp := runner.gotOp.GetShield()
	if shieldOp == nil || shieldOp.Config == nil || !shieldOp.Config.FullSync || len(shieldOp.Config.Files) != 0 {
		t.Fatalf("want an empty full-sync clear bundle, got %+v", shieldOp)
	}
	if res.Succeeded != 1 {
		t.Fatalf("want the clear pushed to 1 client, got %+v", res)
	}
}

func TestDeployProject_RunnerErrorFailsJob(t *testing.T) {
	store := &fakeStore{policies: []ShieldPolicy{policy("a", file("api.yaml", []byte("A"), "", ""))}}
	runner := &fakeRunner{err: errors.New("stream dead")}
	d := newDeployer(store, runner)

	if _, err := d.DeployProject(context.Background(), "proj", []string{"c1"}, ReasonPolicyChange, models.UserDetails{IsOwner: true}); err == nil {
		t.Fatal("a total delivery error must surface so the job fails and can be retried")
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
	store := &fakeStore{policies: []ShieldPolicy{
		policy("a", file("same.yaml", []byte("1"), "", "")),
		policy("b", file("same.yaml", []byte("2"), "", "")),
	}}
	d := newDeployer(store, &fakeRunner{})
	if _, err := d.DeployProject(context.Background(), "proj", []string{"c1"}, ReasonPolicyChange, models.UserDetails{IsOwner: true}); err == nil {
		t.Fatal("a merge collision must fail the deploy")
	}
}

// parseClientResult handles a raw *pb.CommandResponse (client errored before
// producing a ResponseShield) as a failure with the envelope error + client id.
func TestParseClientResult_RawCommandResponseIsFailure(t *testing.T) {
	resp := &client.CommandResponse{
		Success:  false,
		Error:    "boom",
		Identity: &client.Identity{ClientId: "c9"},
	}
	cr := parseClientResult(resp)
	if cr.ClientID != "c9" || cr.Ok || cr.Error != "boom" {
		t.Fatalf("raw error response must map to a failed client result, got %+v", cr)
	}
}
