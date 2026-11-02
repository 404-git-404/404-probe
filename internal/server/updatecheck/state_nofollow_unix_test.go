//go:build !windows

package updatecheck

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestOpenStateFileRefusesSymlinkAndDoesNotBlockOnFIFO(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.WriteFile(filepath.Join(dir, "target"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if f, err := openStateFile(root, "link"); err == nil {
		f.Close()
		t.Fatal("symlink followed")
	}
	if err := unix.Mkfifo(filepath.Join(dir, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		f, err := openStateFile(root, "fifo")
		if err != nil {
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err == nil && safeStateFile(info) {
			t.Error("FIFO accepted")
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("FIFO open blocked")
	}
}
