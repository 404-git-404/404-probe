package updatecheck

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"404-probe/internal/buildinfo"
)

func TestStartupCleansOnlyOwnedRegularTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".update-check-owned.tmp", "keep.tmp", ".update-check-no-suffix", "target"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("preserved"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(filepath.Join(dir, ".update-check-readonly.tmp"), nil, 0400); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, ".update-check-directory.tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".update-check-link.tmp")
	hasLink := os.Symlink("target", link) == nil
	c := New(filepath.Join(dir, "update-check.json"), buildinfo.Info{}, nil)
	defer c.Close()
	if _, err := os.Lstat(filepath.Join(dir, ".update-check-owned.tmp")); !os.IsNotExist(err) {
		t.Fatal("owned temporary file remains", err)
	}
	if runtime.GOOS != "windows" {
		if _, err := os.Lstat(filepath.Join(dir, ".update-check-readonly.tmp")); !os.IsNotExist(err) {
			t.Fatal("readonly temporary file remains", err)
		}
	}
	for _, name := range []string{"keep.tmp", ".update-check-no-suffix", "target", ".update-check-directory.tmp"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			t.Fatal(name, err)
		}
	}
	if hasLink {
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatal("symlink removed", err)
		}
	}
	if body, err := os.ReadFile(filepath.Join(dir, "target")); err != nil || string(body) != "preserved" {
		t.Fatal("symlink target changed", err)
	}
}
