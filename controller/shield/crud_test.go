package shield

import (
	"testing"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

func file(p string, content []byte, url, sha string) models.ShieldFileJSON {
	return models.ShieldFileJSON{Path: p, Content: content, DownloadURL: url, Sha256: sha}
}

func req(files ...models.ShieldFileJSON) ShieldPolicyRequest {
	return ShieldPolicyRequest{Name: "p", Project: "proj", Files: files}
}

func fileWithMode(p, mode string) models.ShieldFileJSON {
	return models.ShieldFileJSON{Path: p, Content: []byte("x"), Mode: mode}
}

func TestValidate(t *testing.T) {
	good64 := "0000000000000000000000000000000000000000000000000000000000000000"
	cases := []struct {
		name    string
		req     ShieldPolicyRequest
		wantErr bool
	}{
		{"ok inline", req(file("api.yaml", []byte("x"), "", "")), false},
		{"ok download", req(file("geo/c.mmdb", nil, "https://x/y", good64)), false},
		{"empty files", req(), true},
		{"abs path", req(file("/etc/passwd", []byte("x"), "", "")), true},
		{"traversal", req(file("../x.yaml", []byte("x"), "", "")), true},
		{"deep traversal", req(file("a/../../b", []byte("x"), "", "")), true},
		{"empty path", req(file("", []byte("x"), "", "")), true},
		{"dup path", req(file("a.yaml", []byte("1"), "", ""), file("a.yaml", []byte("2"), "", "")), true},
		{"no source", req(file("a.yaml", nil, "", "")), true},
		{"both sources", req(file("a.yaml", []byte("x"), "https://x/y", good64)), true},
		{"download no sha", req(file("a.mmdb", nil, "https://x/y", "")), true},
		{"download bad scheme", req(file("a.mmdb", nil, "ftp://x/y", good64)), true},
		{"bad sha format", req(file("a.yaml", []byte("x"), "", "nothex")), true},
		{"reserved clear-marker name", req(file(ClearMarkerFile, []byte("x"), "", "")), true},
		{"ok mode 0644", req(fileWithMode("a.yaml", "0644")), false},
		{"ok mode 640", req(fileWithMode("a.yaml", "640")), false},
		{"bad mode symbolic", req(fileWithMode("a.yaml", "rw-r--r--")), true},
		{"bad mode over 0777", req(fileWithMode("a.yaml", "1777")), true},
	}
	for _, tc := range cases {
		if err := validate(&tc.req); (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
}
