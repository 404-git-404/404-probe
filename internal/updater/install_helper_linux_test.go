//go:build linux

package updater

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/releasemetadata"
)

func TestInstallHelperRefreshOutcomes(t *testing.T) {
	const releasedHelper = "#!/usr/bin/env bash\necho refreshed\n"
	installer := []byte("[[ $1 == render-local-helper ]] || exit 1\ncat <<'HELPER'\n" + releasedHelper + "HELPER\n")
	digest := sha256.Sum256(installer)
	for _, tc := range []struct {
		name, warning string
	}{
		{"download failure", "下载 install.sh 失败"},
		{"checksum failure", "install.sh 校验失败"},
		{"disk write failure", "写入管理脚本失败"},
		{"missing", ""},
		{"preserve permissions and replace atomically", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "sbin")
			moved := dir + "-old"
			path := filepath.Join(dir, "404-probe-install")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			const old = "old helper"
			var original os.FileInfo
			if tc.name != "missing" {
				if err := os.WriteFile(path, []byte(old), 0751); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0751); err != nil {
					t.Fatal(err)
				}
				file, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				original, err = file.Stat()
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if data, err := io.ReadAll(file); err != nil || string(data) != old {
						t.Errorf("reader of original inode saw %q: %v", data, err)
					}
				}()
			}
			var requests atomic.Int32
			release := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/v1.4.1/install.sh" {
					t.Errorf("unexpected release path %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				switch tc.name {
				case "download failure":
					http.NotFound(w, r)
					return
				case "checksum failure":
					_, _ = w.Write([]byte("wrong asset"))
					return
				case "disk write failure":
					// Make CreateTemp deterministically fail with ENOTDIR as either root or non-root.
					if err := os.Rename(dir, moved); err != nil {
						t.Error(err)
					}
					if err := os.WriteFile(dir, nil, 0600); err != nil {
						t.Error(err)
					}
				}
				_, _ = w.Write(installer)
			}))
			defer release.Close()
			engine := helperFixtureEngine(t, root, path, release.URL)
			engine.state.Status = "health_check"
			if err := engine.finishSuccessfulUpgrade([]byte(fmt.Sprintf("%x  install.sh\n", digest))); err != nil {
				t.Fatal(err)
			}
			state, err := readStateFile(filepath.Join(engine.config.StateDirectory, "operation.json"))
			want := tc.warning
			if want != "" {
				want = protocol.UpgradeHelperWarningPrefix + want
			}
			if err != nil || state.Status != "succeeded" || !state.HelperUpdateComplete || state.FailureCode != "" || state.FailureMessage != want {
				t.Fatalf("local result %+v: %v", state, err)
			}
			if tc.name == "missing" {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) || requests.Load() != 0 {
					t.Fatalf("missing helper caused file creation or network request: %v / %d", err, requests.Load())
				}
				return
			}
			if tc.name == "disk write failure" {
				path = filepath.Join(moved, filepath.Base(path))
			}
			data, err := os.ReadFile(path)
			if tc.warning != "" {
				if err != nil || string(data) != old {
					t.Fatalf("old helper changed: %q, %v", data, err)
				}
			} else {
				if err != nil || string(data) != releasedHelper {
					t.Fatalf("new helper: %q, %v", data, err)
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				oldStat, newStat := original.Sys().(*syscall.Stat_t), info.Sys().(*syscall.Stat_t)
				if os.SameFile(info, original) || original.Mode() != info.Mode() || oldStat.Uid != newStat.Uid || oldStat.Gid != newStat.Gid {
					t.Fatal("replacement did not preserve ownership and mode on a new inode")
				}
			}
			leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".404-probe-install.*"))
			if err != nil || len(leftovers) != 0 {
				t.Fatalf("leftover staged files: %v, %v", leftovers, err)
			}
		})
	}
}

func helperFixtureEngine(t *testing.T, root, helper, base string) *Engine {
	t.Helper()
	e, err := NewEngine(EngineConfig{CurrentVersion: "v1.4.1", GOOS: "linux", GOARCH: "amd64", StateDirectory: root,
		LiveBinary: filepath.Join(root, "live"), StagedBinary: filepath.Join(root, "staged"), PreviousBinary: filepath.Join(root, "previous"),
		InstallHelper: helper, ReleaseBase: base, DownloadAttempts: 1,
		Inspect: func(string) (CandidateBuildInfo, error) { return CandidateBuildInfo{}, nil }, ServiceCommand: func(context.Context, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	e.state = State{OperationID: strings.Repeat("1", 32), TargetVersion: "v1.4.1", ReleaseCommit: strings.Repeat("2", 40)}
	return e
}

func TestInstallHelperRefreshAfterOlderUpdaterReexec(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "404-probe-install")
	if err := os.WriteFile(helper, []byte("old helper"), 0755); err != nil {
		t.Fatal(err)
	}
	installer, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	const asset = "404-probe-agent-linux-amd64"
	checksum := sha256.Sum256([]byte("fixture Agent"))
	metadata, err := releasemetadata.Encode(releasemetadata.Document{SchemaVersion: 1, Version: "v1.4.1", Commit: strings.Repeat("2", 40),
		Assets: []releasemetadata.Asset{{Name: asset, GOOS: "linux", GOARCH: "amd64", SHA256: fmt.Sprintf("%x", checksum)}}})
	if err != nil {
		t.Fatal(err)
	}
	release := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.4.1/RELEASE-METADATA.json":
			_, _ = w.Write(metadata)
		case "/v1.4.1/SHA256SUMS":
			_, _ = fmt.Fprintf(w, "%x  %s\n%x  install.sh\n", checksum, asset, sha256.Sum256(installer))
		case "/v1.4.1/install.sh":
			_, _ = w.Write(installer)
		default:
			http.NotFound(w, r)
		}
	}))
	defer release.Close()
	engine := helperFixtureEngine(t, root, helper, release.URL)
	if err := os.WriteFile(engine.config.LiveBinary, []byte("fixture Agent"), 0755); err != nil {
		t.Fatal(err)
	}
	engine.state.Status = "succeeded"
	engine.Recover()
	request := Request{ProtocolVersion: ProtocolVersion, Action: ActionStatus, OperationID: strings.Repeat("1", 32), TargetVersion: "v1.4.1"}
	deadline := time.Now().Add(5 * time.Second)
	for {
		state, err := engine.Status(request)
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("waiting for helper update: %+v %v", state, err)
		}
		if state.HelperUpdateComplete {
			if state.Status != "succeeded" || state.FailureMessage != "" {
				t.Fatal(state)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, err := os.ReadFile(helper)
	if err != nil || !strings.Contains(string(data), `setup-root-collector) shift; setup_root_collector "$@" ;;`) || !strings.Contains(string(data), `domains)`) {
		t.Fatalf("rendered helper did not retain management commands: %v", err)
	}
}
