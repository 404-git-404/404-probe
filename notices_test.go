package notices_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"404-probe"
	"404-probe/internal/buildinfo"
)

func noticeFile(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("THIRD_PARTY_NOTICES.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || bytes.ContainsRune(data, '\r') {
		t.Fatal("notices must be nonempty and use LF")
	}
	return data
}

func TestEmbeddedNoticesMatchFile(t *testing.T) {
	var output bytes.Buffer
	if err := notices.Write(&output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), noticeFile(t)) {
		t.Fatal("embedded notices differ from the repository file")
	}
}

type failedWriter struct{ err error }

func (w failedWriter) Write([]byte) (int, error) { return 0, w.err }

func TestNoticeWriteReturnsOutputError(t *testing.T) {
	want := errors.New("output unavailable")
	if err := notices.Write(failedWriter{want}); !errors.Is(err, want) {
		t.Fatalf("write error = %v, want %v", err, want)
	}
}

// Build and execute the actual entry points, outside the source directory and
// with invalid service settings, so a service/configuration fallthrough fails.
func TestBinaryLicensesAndExistingCLI(t *testing.T) {
	want := noticeFile(t)
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go"+suffix)
	for _, role := range []string{"server", "agent"} {
		t.Run(role, func(t *testing.T) {
			executable := filepath.Join(t.TempDir(), "404-probe-"+role+suffix)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if output, err := exec.CommandContext(ctx, goBinary, "build", "-o", executable, "./cmd/"+role).CombinedOutput(); err != nil {
				t.Fatalf("build %s: %v\n%s", role, err, output)
			}
			workingDirectory := t.TempDir()
			statePath := filepath.Join(workingDirectory, "agent.state")
			state := []byte("invalid state: licenses must leave this untouched\n")
			if err := os.WriteFile(statePath, state, 0o600); err != nil {
				t.Fatal(err)
			}
			environment := make([]string, 0, len(os.Environ()))
			for _, item := range os.Environ() {
				if !strings.HasPrefix(strings.ToUpper(item), "PROBE_404_") {
					environment = append(environment, item)
				}
			}
			environment = append(environment,
				"PROBE_404_SERVER=invalid", "PROBE_404_AGENT_ID=", "PROBE_404_TOKEN=",
				"PROBE_404_STATE="+statePath,
				"PROBE_404_SELECTOR_ORDER="+statePath,
				"PROBE_404_SECURITY_EXPORT="+statePath,
				"PROBE_404_SECURITY_ACKS="+statePath)
			type cliCase struct {
				name, stderr string
				args         []string
				exit         int
				stdout       []byte
				jsonVersion  bool
				textVersion  bool
				invalidEnv   bool
			}
			tests := []cliCase{
				{name: "licenses", args: []string{"licenses"}, stdout: want, invalidEnv: true},
				{name: "json version", args: []string{"version", "--json"}, jsonVersion: true, invalidEnv: true},
				{name: "version", args: []string{"version"}, textVersion: true, invalidEnv: true},
			}
			// Keep role-specific cases in the same table so every invocation also
			// checks that no state, database or configuration file was created.
			add := func(name string, args []string, exit int, stderr string) {
				tests = append(tests, cliCase{name: name, args: args, exit: exit, stderr: stderr})
			}
			if role == "server" {
				add("serve help", []string{"serve", "--help"}, 1, "Usage of serve:")
				add("licenses rejects arguments", []string{"licenses", "extra"}, 1, "usage: 404-probe-server licenses")
				add("licenses rejects flags", []string{"licenses", "--db", statePath}, 1, "usage: 404-probe-server licenses")
				add("version rejects arguments", []string{"version", "extra"}, 1, "usage: 404-probe-server version")
				add("web still validates subcommand", []string{"web", "password-hash", "extra"}, 1, "usage: 404-probe-server")
				add("serve still rejects flags", []string{"serve", "--interval=17s"}, 1, "flag provided but not defined")
				add("no implicit serve", nil, 1, "usage: 404-probe-server")
				add("no licenses flag alias", []string{"--licenses"}, 1, "unknown command")
			} else {
				add("licenses rejects arguments", []string{"licenses", "extra"}, 2, "usage: 404-probe-agent licenses")
				add("licenses rejects flags", []string{"licenses", "--state", statePath}, 2, "usage: 404-probe-agent licenses")
				add("interval still parses", []string{"--interval=17s", "--server=", "--agent-id=", "--token="}, 1, "configuration:")
				add("invalid interval still rejected", []string{"--interval=invalid"}, 2, "invalid value")
				add("country-code still validates", []string{"country-code", "invalid"}, 1, "country-code lookup")
				add("selector-order still validates", []string{"selector-order", "invalid"}, 1, "selector-order extract")
				add("no licenses flag alias", []string{"--licenses"}, 2, "flag provided but not defined")
			}
			var binaryVersion string
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					command := exec.CommandContext(ctx, executable, test.args...)
					command.Dir = workingDirectory
					removal := "false"
					if test.invalidEnv {
						removal = "invalid"
					}
					command.Env = append(append([]string(nil), environment...), "PROBE_404_ENABLE_REMOTE_REMOVAL="+removal)
					var stdout, stderr bytes.Buffer
					command.Stdout, command.Stderr = &stdout, &stderr
					err := command.Run()
					exit := 0
					if err != nil {
						var status *exec.ExitError
						if !errors.As(err, &status) {
							t.Fatal(err)
						}
						exit = status.ExitCode()
					}
					if exit != test.exit || (test.stderr == "" && stderr.Len() != 0) || !strings.Contains(stderr.String(), test.stderr) {
						t.Fatalf("exit=%d, want=%d; stderr=%s", exit, test.exit, &stderr)
					}
					if test.jsonVersion {
						var info buildinfo.Info
						if err := json.Unmarshal(stdout.Bytes(), &info); err != nil || info.Version == "" {
							t.Fatalf("version JSON=%s, err=%v", &stdout, err)
						}
						binaryVersion = info.Version
					} else if test.textVersion {
						// A test executable need not have the same VCS stamping as
						// the actual CLI binary. Its two version formats must agree.
						if binaryVersion == "" || stdout.String() != binaryVersion+"\n" {
							t.Fatalf("version text=%q, JSON version=%q", stdout.String(), binaryVersion)
						}
					} else if !bytes.Equal(stdout.Bytes(), test.stdout) {
						t.Fatalf("stdout differs: got %d bytes, want %d", stdout.Len(), len(test.stdout))
					}
					got, err := os.ReadFile(statePath)
					if err != nil || !bytes.Equal(got, state) {
						t.Fatalf("state changed: %v", err)
					}
					entries, err := os.ReadDir(workingDirectory)
					if err != nil || len(entries) != 1 || entries[0].Name() != "agent.state" {
						t.Fatalf("unexpected file creation: %v, %v", entries, err)
					}
				})
			}
		})
	}
}
