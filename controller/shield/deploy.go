package shield

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// MergePolicies folds all of a project's policies into a single full-sync config
// bundle (the complete desired state the edge mirrors). File paths must be unique
// across policies (collisions are rejected, though crud.collisionCheck prevents
// storing them in the first place). The bundle Version is a deterministic digest of
// the merged content, so the edge's active_config_version changes iff the content
// does. An empty policy set yields an empty full-sync bundle (clears the edge dir).
func MergePolicies(policies []ShieldPolicy) (models.ShieldConfigJSON, error) {
	ordered := make([]ShieldPolicy, len(policies))
	copy(ordered, policies)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })

	cfg := models.ShieldConfigJSON{FullSync: true}
	owner := make(map[string]string)
	for pi := range ordered {
		for fi := range ordered[pi].Files {
			f := ordered[pi].Files[fi]
			clean := path.Clean(f.Path)
			if prev, clash := owner[clean]; clash {
				return cfg, fmt.Errorf("file path %q is defined by both policy %q and %q", clean, prev, ordered[pi].Name)
			}
			owner[clean] = ordered[pi].Name
			cfg.Files = append(cfg.Files, f)
		}
	}

	// Deterministic version: hash the sorted (path, effective-sha256) pairs.
	sort.Slice(cfg.Files, func(i, j int) bool { return cfg.Files[i].Path < cfg.Files[j].Path })
	h := sha256.New()
	for i := range cfg.Files {
		f := cfg.Files[i]
		eff := f.Sha256
		if eff == "" {
			sum := sha256.Sum256(f.Content)
			eff = hex.EncodeToString(sum[:])
		}
		_, _ = h.Write([]byte(f.Path))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(eff))
		_, _ = h.Write([]byte{0})
	}
	cfg.Version = hex.EncodeToString(h.Sum(nil))[:12]
	return cfg, nil
}
