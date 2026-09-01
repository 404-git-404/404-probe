package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"404-probe/internal/releasemetadata"
)

func TestRunGeneratesConsistentReleaseFiles(t *testing.T) {
	root := t.TempDir()
	var paths []string
	for _, name := range []string{
		"404-probe-agent-linux-amd64",
		"404-probe-agent-linux-arm64",
		"404-probe-server-linux-amd64",
		"404-probe-server-linux-arm64",
	} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("asset:"+name), 0600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	args := []string{"--version", "v0.8.1", "--commit", "0123456789abcdef0123456789abcdef01234567", "--output", root}
	if err := run(append(args, paths...)); err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(filepath.Join(root, "RELEASE-METADATA.json"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := releasemetadata.Decode(metadata)
	if err != nil || len(document.Assets) != len(paths) {
		t.Fatalf("document=%+v err=%v", document, err)
	}
	sums, err := os.ReadFile(filepath.Join(root, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	for _, asset := range document.Assets {
		if !strings.Contains(string(sums), asset.SHA256+"  "+asset.Name+"\n") {
			t.Fatalf("checksum for %s is inconsistent", asset.Name)
		}
	}
}
