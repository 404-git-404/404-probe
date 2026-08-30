package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallerSecureAgentEntryContract(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate installer contract test")
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(content), "\r\n", "\n")
	for _, required := range []string{
		`readonly DEFAULT_VERSION="v0.5.0"`,
		`404-probe-install agent --server <origin>`,
		`[[ $# -eq 2 && "$1" == "--server" ]]`,
		`IFS= read -r -s enrollment </dev/tty`,
		`install_agent "$2"`,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("installer missing secure Agent contract %q", required)
		}
	}
	if strings.Contains(script, "--enrollment") || strings.Contains(script, "--token PERMANENT_SECRET") {
		t.Fatal("installer accepts an Agent credential through command arguments")
	}
}

func TestInstallerAgentServiceRestartsOnlyAfterFailure(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate installer contract test")
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(content), "\r\n", "\n")
	start := strings.Index(script, `ExecStart=${AGENT_BINARY}`)
	if start < 0 {
		t.Fatal("installer is missing the Agent service ExecStart")
	}
	end := strings.Index(script[start:], "\n[Install]")
	if end < 0 {
		t.Fatal("installer is missing the Agent service install section")
	}
	service := script[start : start+end]
	if !strings.Contains(service, "\nRestart=on-failure\n") {
		t.Fatalf("Agent service must restart crashes but not a clean revoked exit:\n%s", service)
	}
	if strings.Contains(service, "Restart=always") {
		t.Fatalf("Agent service would restart after a clean revoked exit:\n%s", service)
	}
}
