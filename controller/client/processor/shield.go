package processor

import (
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

// ShieldProcessor turns a SHIELD operation into a client.Command_Shield payload.
// elchi-shield self-watches its config dir and hot-reloads, so UPDATE only ships
// files; GET_CONFIG/GET_STATUS carry no body.
type ShieldProcessor struct {
	Logger *logger.Logger
}

func (p *ShieldProcessor) ValidateAndTransform(op models.OperationClass, requestDetails models.RequestDetails, _ models.ServiceClients) (any, error) {
	switch op.GetSubTypeNum() {
	case client.SubCommandType_UPDATE_SHIELD_CONFIG:
		return p.buildUpdate(op, requestDetails)
	case client.SubCommandType_GET_SHIELD_CONFIG, client.SubCommandType_GET_SHIELD_STATUS:
		return &client.Command_Shield{Shield: &client.RequestShield{}}, nil
	default:
		return nil, fmt.Errorf("unsupported shield subcommand: %v", op.GetSubTypeNum())
	}
}

func (p *ShieldProcessor) buildUpdate(op models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	req := op.GetShield()
	if req == nil || req.GetConfig() == nil {
		return nil, fmt.Errorf("shield.config is required for UPDATE_SHIELD_CONFIG")
	}
	cfg := req.GetConfig()
	if len(cfg.GetFiles()) == 0 {
		// An empty full_sync (clear the dir) is intentional; an empty non-full_sync
		// bundle is a no-op the caller almost certainly didn't intend.
		if !cfg.GetFullSync() {
			return nil, fmt.Errorf("shield.config.files is empty (set full_sync to clear the directory)")
		}
	}
	for i, f := range cfg.GetFiles() {
		if f.GetPath() == "" {
			return nil, fmt.Errorf("shield.config.files[%d].path is required", i)
		}
		if f.GetSha256() == "" {
			return nil, fmt.Errorf("shield.config.files[%d].sha256 is required", i)
		}
		switch f.GetSource().(type) {
		case *client.ShieldFile_Inline, *client.ShieldFile_Download:
		default:
			return nil, fmt.Errorf("shield.config.files[%d] has no content source (inline or download)", i)
		}
	}

	p.Logger.Info("Built UPDATE_SHIELD_CONFIG request",
		"client_id", requestDetails.ClientID, "files", len(cfg.GetFiles()), "version", cfg.GetVersion())
	return &client.Command_Shield{Shield: req}, nil
}
