package processor

import (
	"fmt"
	"path"
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

// maxInlineBundleBytes caps the total inline content of a bundle so the command
// (held in memory and sent over the CommandStream, which has gRPC's ~4 MiB default
// message cap) can't exceed it. Large artifacts must use a download ref instead.
const maxInlineBundleBytes = 3 << 20 // 3 MiB

// safeFilePath rejects absolute paths and any traversal that escapes the config
// root — the same guard the edge applies, enforced here as the single control-plane
// chokepoint shared by both the /api/op/clients and /api/v3/shield/deploy paths.
func safeFilePath(p string) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("path is required")
	}
	if path.IsAbs(p) {
		return fmt.Errorf("absolute path not allowed: %q", p)
	}
	clean := path.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("path escapes config root: %q", p)
	}
	return nil
}

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
	var inlineTotal int
	for i, f := range cfg.GetFiles() {
		if err := safeFilePath(f.GetPath()); err != nil {
			return nil, fmt.Errorf("shield.config.files[%d]: %w", i, err)
		}
		if f.GetSha256() == "" {
			return nil, fmt.Errorf("shield.config.files[%d].sha256 is required", i)
		}
		switch src := f.GetSource().(type) {
		case *client.ShieldFile_Inline:
			inlineTotal += len(src.Inline)
		case *client.ShieldFile_Download:
		default:
			return nil, fmt.Errorf("shield.config.files[%d] has no content source (inline or download)", i)
		}
	}
	if inlineTotal > maxInlineBundleBytes {
		return nil, fmt.Errorf("shield bundle inline content is %d bytes (max %d); use download refs for large files", inlineTotal, maxInlineBundleBytes)
	}

	p.Logger.Info("Built UPDATE_SHIELD_CONFIG request",
		"client_id", requestDetails.ClientID, "files", len(cfg.GetFiles()), "version", cfg.GetVersion())
	return &client.Command_Shield{Shield: req}, nil
}
