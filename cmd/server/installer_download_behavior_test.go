package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallerDownloadResumesAcrossOfficialAPIAssetFallback(t *testing.T) {
	bash := "bash"
	if runtime.GOOS == "windows" {
		bash = `C:\Program Files\Git\bin\bash.exe`
	}
	if _, err := os.Stat(bash); runtime.GOOS == "windows" && err != nil {
		t.Skip("Git Bash is unavailable")
	}
	if runtime.GOOS != "windows" {
		if _, err := exec.LookPath(bash); err != nil {
			t.Skip("bash is unavailable")
		}
	}
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate installer")
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(content), "\r\n", "\n")
	start := strings.Index(script, "curl_with_retry() {")
	end := strings.Index(script[start:], "\ncanonical_version() {")
	if start < 0 || end < 0 {
		t.Fatal("could not extract installer download implementation")
	}
	functionSource := script[start : start+end]
	rootNative := t.TempDir()
	root := shellTestPath(rootNative)
	functionPath := filepath.Join(rootNative, "download-functions.sh")
	if err := os.WriteFile(functionPath, []byte(functionSource), 0600); err != nil {
		t.Fatal(err)
	}
	harness := `#!/usr/bin/env bash
set -Eeuo pipefail
task_root='` + root + `'
REPOSITORY=404-git-404/404-probe
DOWNLOAD_CONNECT_TIMEOUT_SECONDS=1
DOWNLOAD_ATTEMPT_TIMEOUT_SECONDS=2
DOWNLOAD_RETRY_MAX_SECONDS=10
DOWNLOAD_RETRIES=0
die() { printf '%s\n' "$*" >&2; return 1; }
note() { :; }
canonical_version() { [[ "$1" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; }
curl() {
  local output='' url='' accept='' argument
  while (($#)); do
    argument="$1"; shift
    case "${argument}" in
      --output) output="$1"; shift ;;
      --header) accept="$1"; shift ;;
      http*) url="${argument}" ;;
    esac
  done
  case "${url}" in
    https://github.com/*/asset)
      printf 'partial' >"${output}"
      if [[ "${TEST_MODE:-}" == deadline ]]; then sleep 2; fi
      return 22
      ;;
    https://api.github.com/repos/404-git-404/404-probe/releases/tags/v1.2.3)
      [[ "${TEST_MODE:-}" != deadline ]] || { touch "${task_root}/unexpected-api"; return 1; }
      printf '%s\n' '{' '  "tag_name": "v1.2.3",' '  "assets": [' '    {' '      "url": "https://api.github.com/repos/404-git-404/404-probe/releases/assets/42",' '      "name": "asset"' '    }' '  ]' '}' >"${output}"
      ;;
    https://api.github.com/repos/404-git-404/404-probe/releases/assets/42)
      [[ "${accept}" == 'Accept: application/octet-stream' ]]
      [[ "$(<"${output}")" == partial ]]
      printf '%s' '-completed' >>"${output}"
      ;;
    *) printf 'unexpected URL: %s\n' "${url}" >&2; return 1 ;;
  esac
}
source '` + shellTestPath(functionPath) + `'
download_release_asset v1.2.3 asset "${task_root}/asset"
[[ "$(<"${task_root}/asset")" == 'partial-completed' ]]
[[ ! -e "${task_root}/asset.partial" && ! -e "${task_root}/asset.release-api.json" ]]
rm -f "${task_root}/asset"
TEST_MODE=deadline
DOWNLOAD_RETRY_MAX_SECONDS=1
if download_release_asset v1.2.3 asset "${task_root}/asset"; then exit 1; fi
[[ ! -e "${task_root}/asset.partial" && ! -e "${task_root}/unexpected-api" ]]
`
	harnessPath := filepath.Join(rootNative, "download.sh")
	if err := os.WriteFile(harnessPath, []byte(harness), 0700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(bash, shellTestPath(harnessPath))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("download harness failed: %v\n%s", err, output)
	}
}
