package models

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	client "github.com/CloudNativeWorks/elchi-proto/client"
)

func TestShieldConfigToPB_InlineComputesSha256(t *testing.T) {
	content := []byte("hosts: [\"*\"]\n")
	cfg := &ShieldConfigJSON{
		FullSync: true,
		Version:  "v7",
		Files: []ShieldFileJSON{
			{Path: "api.yaml", Content: content, Mode: "0640"},
		},
	}
	pb := cfg.ToPB()
	if pb.GetVersion() != "v7" || !pb.GetFullSync() || len(pb.GetFiles()) != 1 {
		t.Fatalf("config header wrong: %+v", pb)
	}
	f := pb.GetFiles()[0]
	sum := sha256.Sum256(content)
	if f.GetSha256() != hex.EncodeToString(sum[:]) {
		t.Fatalf("inline sha256 not derived: %s", f.GetSha256())
	}
	in, ok := f.GetSource().(*client.ShieldFile_Inline)
	if !ok || string(in.Inline) != string(content) {
		t.Fatalf("inline source wrong: %+v", f.GetSource())
	}
	if f.GetMode() != "0640" || f.GetPath() != "api.yaml" {
		t.Fatalf("path/mode wrong: %+v", f)
	}
}

func TestShieldConfigToPB_DownloadKeepsSha(t *testing.T) {
	cfg := &ShieldConfigJSON{Files: []ShieldFileJSON{
		{Path: "geo/Country.mmdb", DownloadURL: "https://x/y.mmdb", Sha256: "abc123"},
	}}
	f := cfg.ToPB().GetFiles()[0]
	dl, ok := f.GetSource().(*client.ShieldFile_Download)
	if !ok || dl.Download.GetUrl() != "https://x/y.mmdb" {
		t.Fatalf("download source wrong: %+v", f.GetSource())
	}
	if f.GetSha256() != "abc123" {
		t.Fatalf("download sha should be preserved: %s", f.GetSha256())
	}
}

func TestRequestShieldJSON_NilConfig(t *testing.T) {
	// GET ops carry no config.
	r := &RequestShieldJSON{Operation: "GET_SHIELD_STATUS"}
	if r.ToPB().GetConfig() != nil {
		t.Fatal("GET op should have nil config")
	}
}
