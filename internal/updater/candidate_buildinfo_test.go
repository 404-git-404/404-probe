package updater

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInspectCandidateBuildReadsGoExecutable(t *testing.T) {
	candidate := filepath.Join(t.TempDir(), "candidate")
	if runtime.GOOS == "windows" {
		candidate += ".exe"
	}
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", candidate, "./cmd/agent")
	command.Dir = filepath.Clean(filepath.Join("..", ".."))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build candidate: %v: %s", err, output)
	}
	info, err := inspectCandidateBuild(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if info.Path != "404-probe/cmd/agent" || info.GOOS != runtime.GOOS || info.GOARCH != runtime.GOARCH || info.Commit == "" {
		t.Fatalf("unexpected build info %+v", info)
	}
}

func TestInspectCandidateBuildNeverExecutesCandidate(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "executed")
	source := filepath.Join(root, "candidate.go")
	program := fmt.Sprintf("package main\nimport \"os\"\nfunc init(){ _ = os.WriteFile(%q, []byte(\"executed\"), 0600) }\nfunc main(){}\n", marker)
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(root, "candidate")
	if runtime.GOOS == "windows" {
		candidate += ".exe"
	}
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-buildvcs=false", "-o", candidate, source)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v: %s", err, output)
	}
	_, _ = inspectCandidateBuild(candidate)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("candidate was executed during inspection: %v", err)
	}
}

func TestInspectCandidateBuildRejectsNonGoFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidate")
	if err := os.WriteFile(path, []byte("not a Go executable"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectCandidateBuild(path); err == nil {
		t.Fatal("non-Go candidate accepted")
	}
}
