package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallerExplicitBetaAndStableLatestBoundary(t *testing.T) {
	bash := "bash"
	if runtime.GOOS == "windows" {
		bash = `C:\Program Files\Git\bin\bash.exe`
	}
	if _, err := exec.LookPath(bash); err != nil {
		t.Fatal("Git Bash required for this channel check", err)
	}
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(raw), "\r\n", "\n")
	section := func(start, end string) string {
		a := strings.Index(script, start)
		if a < 0 {
			t.Fatal(start)
		}
		b := strings.Index(script[a:], end)
		if b < 0 {
			t.Fatal(end)
		}
		return script[a : a+b]
	}
	functions := section("release_version() {", "\ndownload_release_asset() {") + section("canonical_version() {", "\nusage() {") + section("compare_versions() {", "\ndownload_server_upgrade_candidate()")
	harness := `#!/usr/bin/env bash
set -Eeuo pipefail
REPOSITORY=404-git-404/404-probe
DEFAULT_VERSION=v1.0.0
die(){ printf '%s\n' "$*" >&2; exit 1; }
curl_with_retry(){ printf 'https://github.com/%s/releases/tag/%s' "$REPOSITORY" "${FIXTURE_LATEST}"; }
download_to_file(){ printf '#!/usr/bin/env bash\nprintf "%%s\\n" "$PROBE_404_VERSION"\n' > "$1"; }
download_release_asset(){ (cd "$(dirname "$3")"; sha256sum --text install.sh) > "$3"; }
` + functions + `
case "$1" in
 target) target_version ;;
 compare) compare_versions "$2" "$3" ;;
 bootstrap) bootstrap_latest_installer ;;
esac
`
	path := filepath.Join(t.TempDir(), "channels.sh")
	if err := os.WriteFile(path, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, pinned, latest, action, left, right, want string
		failure                                         bool
	}{
		{name: "explicit beta target", pinned: "v1.0.1-beta.1", action: "target", want: "v1.0.1-beta.1"},
		{name: "default target stays stable", action: "target", want: "v1.0.0"},
		{name: "explicit beta bootstrap", pinned: "v1.0.1-beta.1", action: "bootstrap", want: "install.sh: OK\nv1.0.1-beta.1"},
		{name: "latest stable bootstrap", latest: "v1.0.0", action: "bootstrap", want: "install.sh: OK\nv1.0.0"},
		{name: "malformed latest beta rejected", latest: "v1.0.1-beta.1", action: "bootstrap", failure: true},
		{name: "stable to beta ordering", action: "compare", left: "v1.0.0", right: "v1.0.1-beta.1", want: "-1"},
		{name: "beta to stable ordering", action: "compare", left: "v1.0.1-beta.2", right: "v1.0.1", want: "-1"},
		{name: "beta downgrade ordering", action: "compare", left: "v1.0.1-beta.2", right: "v1.0.1-beta.1", want: "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bash, shellTestPath(path), tc.action, tc.left, tc.right)
			cmd.Env = append(os.Environ(), "PROBE_404_VERSION="+tc.pinned, "FIXTURE_LATEST="+tc.latest)
			got, err := cmd.CombinedOutput()
			if tc.failure {
				if err == nil || !strings.Contains(string(got), "latest stable release returned") {
					t.Fatalf("latest boundary: %s %v", got, err)
				}
				return
			}
			if err != nil || strings.TrimSpace(string(got)) != tc.want {
				t.Fatalf("got=%s err=%v want=%s", got, err, tc.want)
			}
		})
	}
	oldFunctions, err := os.ReadFile(filepath.Join(filepath.Dir(file), "testdata", "install-bootstrap-v100.sh"))
	if err != nil {
		t.Fatal(err)
	}
	oldHarness := strings.Replace(harness, functions, string(oldFunctions), 1)
	oldPath := filepath.Join(t.TempDir(), "old-channels.sh")
	if err := os.WriteFile(oldPath, []byte(oldHarness), 0600); err != nil {
		t.Fatal(err)
	}
	oldCommand := exec.Command(bash, shellTestPath(oldPath), "bootstrap")
	oldCommand.Env = append(os.Environ(), "PROBE_404_VERSION=v1.0.1-beta.1", "FIXTURE_LATEST=v1.0.0")
	oldOutput, oldErr := oldCommand.CombinedOutput()
	if oldErr == nil || !strings.Contains(string(oldOutput), "explicit target version must use canonical") {
		t.Fatalf("old helper boundary: %s %v", oldOutput, oldErr)
	}
	t.Log("v1.0.0 helper rejects Beta; explicitly fetch/checksum/use the Beta installer, whose selected version and integrity are verified above")

}
