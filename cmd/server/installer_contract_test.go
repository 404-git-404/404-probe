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
	script := string(content)
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
