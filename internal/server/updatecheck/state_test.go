package updatecheck

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"404-probe/internal/buildinfo"
)

func TestPersistedSuccessSurvivesRestartAndReenable(t *testing.T) {
	c, count := testChecker(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"tag_name":"v1.1.0"}`) })
	path := filepath.Join(t.TempDir(), "update-check.json")
	c.persist = func(s State) error { return save(path, s) }
	if err := c.check(t.Context()); err != nil {
		t.Fatal(err)
	}
	first := c.Snapshot()
	restarted := New(path, c.info, nil)
	defer restarted.Close()
	restarted.client.Transport = c.client.Transport
	restarted.fetchURL = c.fetchURL
	restarted.now = func() time.Time { return time.UnixMilli(first.CheckedAt).Add(time.Hour) }
	if restarted.Snapshot() != first {
		t.Fatal(restarted.Snapshot(), first)
	}
	if _, err := restarted.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if restarted.Snapshot().Newer {
		t.Fatal("disabled hint")
	}
	if _, err := restarted.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := restarted.check(t.Context()); err != nil || count.Load() != 1 {
		t.Fatal(err, count.Load())
	}
	if delay := restarted.initialDelay(); delay < 11*time.Hour {
		t.Fatal(delay)
	}
}

func TestStatePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-check.json")
	c := New(path, buildinfo.Info{}, nil)
	defer c.Close()
	if !c.Snapshot().Enabled {
		t.Fatal("missing file must default on")
	}
	if _, err := c.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	loaded, err := load(path)
	if err != nil || loaded.Enabled {
		t.Fatal(loaded, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	broken := New(path, buildinfo.Info{}, nil)
	defer broken.Close()
	if broken.Snapshot().Enabled {
		t.Fatal("corrupt must fail closed")
	}
	if _, err := broken.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	if s, err := load(path); err != nil || !s.Enabled {
		t.Fatal(s, err)
	}
	for _, raw := range []string{`{}`, `{"enabled":null}`, `{"enabled":true,"latest":"v1.1.0-rc.1","checked_at":1}`, `{"enabled":true,"latest":"v1.1.0","checked_at":0}`, `{"enabled":true} {}`} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := load(path); err == nil {
			t.Fatal(raw)
		}
	}
}

func TestSymlinkNeverFollowed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	path := filepath.Join(dir, "update-check.json")
	original := []byte(`{"enabled":true}`)
	if err := os.WriteFile(target, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("Windows symlink privilege unavailable")
		}
		t.Fatal(err)
	}
	c := New(path, buildinfo.Info{}, nil)
	defer c.Close()
	if c.Snapshot().Enabled {
		t.Fatal("symlink read allowed")
	}
	if _, err := c.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(target)
	if err != nil || string(body) != string(original) {
		t.Fatal("target modified", err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal(info, err)
	}
}

func TestStatePermissionsAndAtomicReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update-check.json")
	if err := save(path, State{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := load(path); err == nil {
			t.Fatal("unsafe permissions accepted")
		}
		if err := os.Chmod(path, 0000); err != nil {
			t.Fatal(err)
		}
		if _, err := load(path); err == nil {
			t.Fatal("unreadable accepted")
		}
		if err := os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			if err := save(path, State{Enabled: i%2 == 0, Latest: "v1.1.0", CheckedAt: 123}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for i := 0; i < 100; i++ {
		if _, err := load(path); err != nil { // replacement between lstat/open is deliberately fail closed
			if err.Error() != "state file changed" {
				t.Error(err)
			}
		}
	}
	wg.Wait()
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	if err := save(filepath.Join(dir, "absent", "file"), State{}); err == nil {
		t.Fatal("missing directory")
	}
}
