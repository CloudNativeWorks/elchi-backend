package shield

import (
	"testing"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

func file(p string, content []byte, url, sha string) models.ShieldFileJSON {
	return models.ShieldFileJSON{Path: p, Content: content, DownloadURL: url, Sha256: sha}
}

func TestValidate(t *testing.T) {
	good64 := "0000000000000000000000000000000000000000000000000000000000000000"
	cases := []struct {
		name    string
		req     ShieldPolicyRequest
		wantErr bool
	}{
		{"ok inline", ShieldPolicyRequest{Files: []models.ShieldFileJSON{file("api.yaml", []byte("x"), "", "")}}, false},
		{"ok download", ShieldPolicyRequest{Files: []models.ShieldFileJSON{file("geo/c.mmdb", nil, "https://x/y", good64)}}, false},
		{"clear-dir (empty + full_sync)", ShieldPolicyRequest{FullSync: true}, false},
		{"empty without full_sync", ShieldPolicyRequest{}, true},
		{"abs path", ShieldPolicyRequest{Files: []models.ShieldFileJSON{file("/etc/passwd", []byte("x"), "", "")}}, true},
		{"traversal", ShieldPolicyRequest{Files: []models.ShieldFileJSON{file("../x.yaml", []byte("x"), "", "")}}, true},
		{"deep traversal", ShieldPolicyRequest{Files: []models.ShieldFileJSON{file("a/../../b", []byte("x"), "", "")}}, true},
		{"empty path", ShieldPolicyRequest{Files: []models.ShieldFileJSON{file("", []byte("x"), "", "")}}, true},
		{"dup path", ShieldPolicyRequest{Files: []models.ShieldFileJSON{file("a.yaml", []byte("1"), "", ""), file("a.yaml", []byte("2"), "", "")}}, true},
		{"no source", ShieldPolicyRequest{Files: []models.ShieldFileJSON{file("a.yaml", nil, "", "")}}, true},
		{"both sources", ShieldPolicyRequest{Files: []models.ShieldFileJSON{file("a.yaml", []byte("x"), "https://x/y", good64)}}, true},
		{"download no sha", ShieldPolicyRequest{Files: []models.ShieldFileJSON{file("a.mmdb", nil, "https://x/y", "")}}, true},
		{"download bad scheme", ShieldPolicyRequest{Files: []models.ShieldFileJSON{file("a.mmdb", nil, "ftp://x/y", good64)}}, true},
		{"bad sha format", ShieldPolicyRequest{Files: []models.ShieldFileJSON{file("a.yaml", []byte("x"), "", "nothex")}}, true},
	}
	for _, tc := range cases {
		err := validate(&tc.req)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
}
