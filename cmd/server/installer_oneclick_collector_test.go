package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOneClickInstallKeepsAgentOnRootCollectorFailure(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is unavailable")
	}
	source := m12InstallerSource(t)
	const signature = "\ninstall_agent_oneclick() {\n"
	_, rest, found := strings.Cut(source, signature)
	body, _, closed := strings.Cut(rest, "\n}\n")
	if !found || !closed {
		t.Fatal("cannot locate installer one-click function")
	}
	for _, tc := range []struct {
		name, agent, collector, want string
		fails                        bool
	}{
		{"collector fails", "true", "echo 'systemctl failed' >&2; false; echo unreachable", "以后", false},
		{"agent fails", "false", "echo unreachable", "", true},
		{"collector succeeds", "true", "echo collector-enabled", "collector-enabled", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			harness := `set -Eeuo pipefail
INSTALL_INIT=systemd
PROBE_404_SETUP_ROOT_COLLECTOR=1
require_root_linux_systemd() { :; }
oneclick_load_inputs() { :; }
server_residue_exists() { return 1; }
agent_residue_exists() { return 1; }
note() { printf '%s\n' "$*"; }
` + "\ninstall_agent() { " + tc.agent + "; }\nsetup_root_collector() { " + tc.collector + "; }\n" +
				signature + body + "\n}\ninstall_agent_oneclick\n"
			path := filepath.Join(t.TempDir(), "oneclick-fixture.sh")
			if err := os.WriteFile(path, []byte(harness), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := installerFixtureCommand(t, bash, path)
			result, err := cmd.CombinedOutput()
			out := string(result)
			if (err != nil) != tc.fails || strings.Contains(out, "unreachable") {
				t.Fatalf("status %v: %s", err, out)
			}
			if tc.name == "collector fails" {
				for _, text := range []string{"systemctl failed", "root 采集启用失败", "404-probe-install setup-root-collector", "重试"} {
					if !strings.Contains(out, text) {
						t.Fatalf("missing %q: %s", text, out)
					}
				}
			} else if !strings.Contains(out, tc.want) {
				t.Fatalf("missing %q: %s", tc.want, out)
			}
		})
	}
}
