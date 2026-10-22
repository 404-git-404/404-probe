package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"404-probe/internal/releasemetadata"
)

func TestRunGeneratesConsistentReleaseFiles(t *testing.T) {
	for _, tc := range []struct {
		version string
		schema  int
	}{{"v0.8.1", 1}, {"v1.0.1", 1}, {"v1.0.2", 2}, {"v1.0.3", 2}, {"v1.0.3-rc.1", 2}, {"v1.0.4", 2}, {"v1.0.4-rc.1", 2}, {"v1.0.5", 2}, {"v1.0.5-rc.1", 2}, {"v1.0.6", 2}, {"v1.0.6-rc.1", 2}} {
		t.Run(tc.version, func(t *testing.T) {
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
			args := []string{"--version", tc.version, "--commit", "0123456789abcdef0123456789abcdef01234567", "--output", root}
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
			if document.Version != tc.version || document.SchemaVersion != tc.schema {
				t.Fatalf("wrong release identity: %+v", document)
			}
			metadataLine := fmt.Sprintf("%x  RELEASE-METADATA.json\n", sha256.Sum256(metadata))
			if tc.schema == 2 {
				minimum := "v1.0.1"
				// A release candidate needs a Server that understands release candidates (v1.0.3-rc.1 was the first).
				if strings.Contains(tc.version, "-rc.") {
					minimum = "v1.0.3-rc.1"
				}
				if document.Compatibility == nil || document.Compatibility.MinServerVersion != minimum || document.Compatibility.UpgradeProtocol != 2 || !strings.Contains(string(sums), metadataLine) {
					t.Fatalf("Stable compatibility or metadata authentication missing: %+v", document)
				}
			} else if document.Compatibility != nil || strings.Contains(string(sums), "RELEASE-METADATA.json") {
				t.Fatal("legacy schema1 release format changed")
			}
		})
	}

}
