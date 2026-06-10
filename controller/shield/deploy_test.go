package shield

import (
	"testing"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

func policy(name string, files ...models.ShieldFileJSON) ShieldPolicy {
	return ShieldPolicy{Name: name, Project: "proj", Files: files}
}

func TestMergePolicies_UnionAndDeterministicVersion(t *testing.T) {
	p1 := policy("a", file("api.yaml", []byte("A"), "", ""))
	p2 := policy("b", file("feeds/x.json", []byte("B"), "", ""))

	cfg, err := MergePolicies([]ShieldPolicy{p2, p1}) // unordered input
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.FullSync || len(cfg.Files) != 2 {
		t.Fatalf("want full_sync union of 2, got %+v", cfg)
	}
	// version stable regardless of input order
	cfg2, _ := MergePolicies([]ShieldPolicy{p1, p2})
	if cfg.Version != cfg2.Version || cfg.Version == "" {
		t.Fatalf("version not deterministic: %q vs %q", cfg.Version, cfg2.Version)
	}
	// changing content changes the version
	p1b := policy("a", file("api.yaml", []byte("A-changed"), "", ""))
	cfg3, _ := MergePolicies([]ShieldPolicy{p1b, p2})
	if cfg3.Version == cfg.Version {
		t.Fatal("version should change when content changes")
	}
}

func TestMergePolicies_RejectsPathCollision(t *testing.T) {
	a := policy("a", file("same.yaml", []byte("1"), "", ""))
	b := policy("b", file("same.yaml", []byte("2"), "", ""))
	if _, err := MergePolicies([]ShieldPolicy{a, b}); err == nil {
		t.Fatal("path collision across policies must error")
	}
}

func TestMergePolicies_EmptyClears(t *testing.T) {
	cfg, err := MergePolicies(nil)
	if err != nil || !cfg.FullSync || len(cfg.Files) != 0 {
		t.Fatalf("empty set should be an empty full-sync bundle: %+v err=%v", cfg, err)
	}
}
