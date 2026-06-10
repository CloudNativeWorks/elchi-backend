package shield

import (
	"context"
	"fmt"

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
// project". It returns the aggregated per-client command result.
//
// It fails (returns an error, so the job fails and can be retried) only when the
// store/merge fails or when there were targets but every push errored — a partial
// success is reported but not failed, since shield is idempotent and a later
// connect-triggered deploy re-pushes to any straggler.
func (d *Deployer) DeployProject(ctx context.Context, project string, clientIDs []string, reason string, user models.UserDetails) (any, error) {
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
		return map[string]any{"deployed": true, "clients": 0, "version": cfg.Version, "message": "no shield config for project"}, nil
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
		return map[string]any{"deployed": true, "clients": 0, "version": cfg.Version, "message": "no connected clients in project"}, nil
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

	result, err := d.runner.HandleSendCommand(ctx, op, requestDetails)
	if err != nil {
		return result, fmt.Errorf("push shield config to project %s (%d clients): %w", project, len(targets), err)
	}

	d.logger.Infof("shield deploy: pushed version %s to %d client(s) in project %s", cfg.Version, len(targets), project)
	return result, nil
}
