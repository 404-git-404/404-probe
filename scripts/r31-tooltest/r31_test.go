package r31tooltest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Keep the tool outside all product packages. Tiny integration uses an exact
// previous-release Server, supplied by the caller; never the current migrations.
func TestR31Tools(t *testing.T) {
	python := os.Getenv("R31_PYTHON")
	if python == "" {
		var err error
		python, err = exec.LookPath("python3")
		if err != nil {
			t.Skip("Python 3 not available; set R31_PYTHON to run tool tests")
		}
	}
	cmd := exec.Command(python, "-B", filepath.Join("..", "test-r31.py"), "-v")
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(err)
	}
}
