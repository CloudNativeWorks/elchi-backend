package shield

import (
	"context"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/async/job"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

// CommandRunner is the narrow slice of the client command handler the deployer
// needs. It is satisfied by *controller/client/handlers.Client and reuses that
// path's processor/responser, parallel per-client fan-out, command authorization
// and multi-pod registry forwarding — so a deploy reaches a client regardless of
// which controller pod it is connected to. Defined here (consumer side) to avoid
// importing controller/client.
type CommandRunner interface {
	HandleSendCommand(ctx context.Context, op models.OperationClass, requestDetails models.RequestDetails) (any, error)
}

// policyStore is the slice of the policy store the deployer reads. Satisfied by
// *CRUDService; an interface so DeployProject is unit-testable without Mongo.
type policyStore interface {
	List(ctx context.Context, project string) ([]ShieldPolicy, error)
	ListConnectedClientIDs(ctx context.Context, project string) ([]string, error)
}

// Deployer renders a project's merged elchi-shield policy set into one full-sync
// bundle and pushes it to the project's clients. It implements the async worker's
// ShieldDeployer interface and is invoked from SHIELD_DEPLOY jobs.
type Deployer struct {
	store  policyStore
	runner CommandRunner
	logger *logger.Logger
}

// NewDeployer builds a shield deployer over the policy store and client command
// handler.
func NewDeployer(crud *CRUDService, runner CommandRunner, logger *logger.Logger) *Deployer {
	return &Deployer{store: crud, runner: runner, logger: logger}
}

// DeployProject lists the project's policies, merges them into one full-sync
// bundle, resolves the target clients, and pushes the bundle over the client
// command path. clientIDs empty means "all currently-connected clients in the
// project". It returns a structured per-client result (persisted on the job).
//
// It returns an error (so the job fails and can be retried) only when the
// store/merge fails or when the whole command delivery errored. A delivery that
// reached clients but where some/all reported a logical failure is returned as a
// non-error result with per-client detail; the worker fails the job iff EVERY
// targeted client failed (all-failed is retryable; a partial is left for the next
// connect-triggered deploy to heal, since shield is idempotent).
func (d *Deployer) DeployProject(ctx context.Context, project string, clientIDs []string, reason string, user models.UserDetails) (*job.ShieldDeployResult, error) {
	policies, err := d.store.List(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("list shield policies for project %s: %w", project, err)
	}

	cfg, err := MergePolicies(policies)
	if err != nil {
		return nil, fmt.Errorf("merge shield policies for project %s: %w", project, err)
	}

	// A connect-triggered deploy brings ONE (re)connecting client up to the
	// project's desired state. If the project has no shield config, there is
	// nothing to bring it up to — skip rather than push an empty "clear" bundle
	// to a client that has no desired state (and may not even run elchi-shield).
	// A policy_change that removed the last policy still clears all clients: it
	// arrives here with reason policy_change and a non-empty target set.
	if reason == ReasonClientConnect && len(cfg.Files) == 0 {
		d.logger.Debugf("shield deploy: project %s has no shield config; skipping connect deploy", project)
		return &job.ShieldDeployResult{Version: cfg.Version, Note: "no shield config for project"}, nil
	}

	targets := clientIDs
	if len(targets) == 0 {
		targets, err = d.store.ListConnectedClientIDs(ctx, project)
		if err != nil {
			return nil, fmt.Errorf("list connected clients for project %s: %w", project, err)
		}
	}
	if len(targets) == 0 {
		d.logger.Infof("shield deploy: project %s has no connected clients (version %s); nothing to push", project, cfg.Version)
		return &job.ShieldDeployResult{Version: cfg.Version, Note: "no connected clients in project"}, nil
	}

	clients := make([]models.ServiceClients, 0, len(targets))
	for _, cid := range targets {
		clients = append(clients, models.ServiceClients{ClientID: cid})
	}

	op := &models.Operations{
		Type:     models.CommandTypeJSON(client.CommandType_SHIELD),
		SubType:  models.SubCommandTypeJSON(client.SubCommandType_UPDATE_SHIELD_CONFIG),
		Clients:  clients,
		Command:  models.Command{Project: project},
		ShieldOp: &models.RequestShieldJSON{Operation: "UPDATE_SHIELD_CONFIG", Config: &cfg},
	}

	requestDetails := models.RequestDetails{User: user, Project: project}

	raw, err := d.runner.HandleSendCommand(ctx, op, requestDetails)
	if err != nil {
		// Total delivery failure (e.g. every client unreachable). Surface as a job
		// error so it fails + can be retried.
		return nil, fmt.Errorf("push shield config to project %s (%d clients): %w", project, len(targets), err)
	}

	result := buildDeployResult(cfg.Version, targets, raw)
	d.logger.Infof("shield deploy: pushed version %s to project %s (%d/%d ok)", cfg.Version, project, result.Succeeded, result.Total)
	return result, nil
}

// buildDeployResult folds the heterogeneous per-client command responses into a
// structured outcome. HandleSendCommand returns a []any whose entries are either
// the ShieldResponser's map[string]any (local or JSON-forwarded) or, when a client
// errored before producing a ResponseShield, a *pb.CommandResponse. Unmatched
// targets (no response at all) are recorded as failed. Parsing is defensive: any
// shape it can't read counts as a failure rather than a panic.
func buildDeployResult(version string, targets []string, raw any) *job.ShieldDeployResult {
	res := &job.ShieldDeployResult{Version: version, Total: len(targets)}
	seen := make(map[string]job.ClientShieldResult, len(targets))

	if list, ok := raw.([]any); ok {
		for _, entry := range list {
			cr := parseClientResult(entry)
			if cr.ClientID == "" {
				continue
			}
			seen[cr.ClientID] = cr
		}
	}

	for _, id := range targets {
		cr, ok := seen[id]
		if !ok {
			cr = job.ClientShieldResult{ClientID: id, Ok: false, Error: "no response from client"}
		}
		if cr.Ok {
			res.Succeeded++
		} else {
			res.Failed++
		}
		res.Clients = append(res.Clients, cr)
	}
	return res
}

// parseClientResult extracts one client's outcome from a single command-response
// entry. Ok requires the envelope success AND the edge's reported shield success
// AND its reload confirmation, so a delivered-but-rejected bundle counts as failed.
func parseClientResult(entry any) job.ClientShieldResult {
	switch v := entry.(type) {
	case map[string]any:
		cr := job.ClientShieldResult{
			ClientID: asString(v["client_id"]),
			Error:    asString(v["error"]),
		}
		envOK := asBool(v["success"])
		shieldOK, reloadOK := false, false
		if sh, ok := v["shield"].(map[string]any); ok {
			shieldOK = asBool(sh["success"])
			reloadOK = asBool(sh["reload_ok"])
			cr.AppliedVersion = asString(sh["applied_version"])
			if cr.Error == "" {
				cr.Error = asString(sh["error"])
			}
		}
		cr.ReloadOk = reloadOK
		cr.Ok = envOK && shieldOK && reloadOK
		return cr
	case *client.CommandResponse:
		// A pre-ResponseShield client error (NewErrorResponse leaves Shield nil).
		cr := job.ClientShieldResult{Error: v.GetError(), Ok: false}
		if id := v.GetIdentity(); id != nil {
			cr.ClientID = id.GetClientId()
		}
		return cr
	default:
		return job.ClientShieldResult{}
	}
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}
