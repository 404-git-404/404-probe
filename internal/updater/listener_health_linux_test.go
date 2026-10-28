//go:build linux

package updater

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func healthFixture(t *testing.T) (*listenerManager, *Engine, string) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("INSTALLER-FIXTURE-SKIPPED: listener root checks require isolated root harness")
	}
	fixtureRoot := os.Getenv("INSTALLER_FIXTURE_ROOT")
	if fixtureRoot == "" {
		t.Fatal("root listener tests require the installer-fixture entry")
	}
	proof := exec.Command("python3", "-B", "/work/source/scripts/installer-fixture.py", "--root", fixtureRoot, "--profile", "multiuser", "--probe")
	if output, err := proof.CombinedOutput(); err != nil {
		t.Fatalf("isolated root boundary proof failed: %v %s", err, output)
	}
	root, err := os.MkdirTemp(os.Getenv("TMPDIR"), "uh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	// Never trust the marker: prove the same boundary used by installer harnesses.
	runtime := filepath.Join(root, "run")
	if err = os.Mkdir(runtime, 0755); err != nil {
		t.Fatal(err)
	}
	m, err := newListenerManager(filepath.Join(runtime, "s"), uint32(os.Getgid()))
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{config: EngineConfig{StateDirectory: filepath.Join(root, "state")}}
	if err = os.Mkdir(e.config.StateDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(e.config.StateDirectory, "operation.json")
	const contents = `{"status":"failed","operation_id":"saved"}`
	if err = os.WriteFile(journal, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if after, err := os.ReadFile(journal); err != nil || string(after) != contents {
			t.Error("health check changed transaction journal", err)
		}
	})
	t.Cleanup(m.close)
	return m, e, root
}

func serveHealthFixture(m *listenerManager, e *Engine) {
	for {
		l := m.current()
		c, err := l.Accept()
		if err != nil {
			if m.current() != l {
				continue
			}
			return
		}
		go handleConnection(c, e, uint32(os.Getuid()), m)
	}
}

func TestUpdaterHealthAuthenticatedAndMissingSocketRepair(t *testing.T) {
	m, e, _ := healthFixture(t)
	go serveHealthFixture(m, e)
	if h := m.check(context.Background()); h.Status != "healthy" {
		t.Fatal(h)
	}
	old := m.current()
	before := m.identity
	if err := os.Remove(m.path); err != nil {
		t.Fatal(err)
	}
	if h := InspectHealth(context.Background(), m.path, m.gid); h.Status != "missing" || h.Reason != "socket_missing" {
		t.Fatal(h)
	}
	if err := m.repair(e, time.Now()); err != nil {
		t.Fatal(err)
	}
	if m.identity == before {
		t.Fatal("socket identity did not change")
	}
	_ = old.Close()
	if h := m.check(context.Background()); h.Status != "healthy" {
		t.Fatal("old close deleted new socket", h)
	}
}

func TestUpdaterHealthLegacyAndInvalidProofResponses(t *testing.T) {
	for _, kind := range []string{"legacy", "rejected", "bad_nonce", "wrong_inode"} {
		t.Run(kind, func(t *testing.T) {
			m, _, _ := healthFixture(t)
			go func() {
				for {
					c, err := m.current().Accept()
					if err != nil {
						return
					}
					go func(c net.Conn) {
						defer c.Close()
						var request Request
						_ = json.NewDecoder(c).Decode(&request)
						response := Response{}
						if kind == "legacy" && request.Action == ActionCapabilities {
							response.Accepted = true
						}
						if kind == "bad_nonce" || kind == "wrong_inode" {
							response.Accepted = true
							response.Health = m.proof(request.Nonce)
							if kind == "bad_nonce" {
								response.Health.Nonce = "wrong"
							} else {
								response.Health.Inode++
							}
						}
						_ = json.NewEncoder(c).Encode(response)
					}(c)
				}
			}()
			want := map[string]string{"legacy": "healthy", "rejected": "unreachable", "bad_nonce": "identity_mismatch", "wrong_inode": "identity_mismatch"}[kind]
			if got := InspectHealth(context.Background(), m.path, m.gid); got.Status != want {
				t.Fatal(kind, got, want)
			}
		})
	}
}

func TestUpdaterHealthPreservesReplacementTypesAndMetadata(t *testing.T) {
	for _, kind := range []string{"file", "symlink", "foreign_listener", "wrong_mode", "wrong_owner", "wrong_group"} {
		t.Run(kind, func(t *testing.T) {
			m, e, root := healthFixture(t)
			if kind == "wrong_mode" {
				if err := os.Chmod(m.path, 0600); err != nil {
					t.Fatal(err)
				}
			} else if kind == "wrong_owner" {
				if err := os.Chown(m.path, 973, int(m.gid)); err != nil {
					t.Fatal(err)
				}
			} else if kind == "wrong_group" {
				if err := os.Chown(m.path, 0, 973); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(m.path); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "file":
					if err := os.WriteFile(m.path, []byte("foreign"), 0600); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					if err := os.Symlink(filepath.Join(root, "outside"), m.path); err != nil {
						t.Fatal(err)
					}
				case "foreign_listener":
					l, err := net.ListenUnix("unix", &net.UnixAddr{Name: m.path, Net: "unix"})
					if err != nil {
						t.Fatal(err)
					}
					l.SetUnlinkOnClose(false)
					defer l.Close()
					if err = os.Chmod(m.path, 0660); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, s, err := pathIdentity(m.path)
			if err != nil {
				t.Fatal(err)
			}
			if h := InspectHealth(context.Background(), m.path, m.gid); kind != "foreign_listener" && h.Status != "identity_mismatch" {
				t.Fatal(h)
			}
			if kind == "foreign_listener" {
				if h := m.check(context.Background()); h.Status != "identity_mismatch" {
					t.Fatal("foreign listener not detected", h)
				}
			}
			if err = m.repair(e, time.Now()); err != nil {
				t.Fatal(err)
			}
			m.close()
			after, current, err := pathIdentity(m.path)
			if err != nil || after != before || current.Mode != s.Mode || current.Uid != s.Uid || current.Gid != s.Gid {
				t.Fatal("foreign object changed", err)
			}
		})
	}
}

func TestUpdaterHealthDirectoryLossAndReplacementNeverRecreates(t *testing.T) {
	m, e, _ := healthFixture(t)
	if err := os.Remove(m.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(m.path)); err != nil {
		t.Fatal(err)
	}
	if h := InspectHealth(context.Background(), m.path, m.gid); h.Status != "missing" || h.Reason != "runtime_directory_missing" {
		t.Fatal(h)
	}
	if err := m.repair(e, time.Now()); err == nil {
		t.Fatal("missing directory repair allowed")
	}
	if _, err := os.Lstat(filepath.Dir(m.path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Dir(m.path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := m.repair(e, time.Now()); err == nil {
		t.Fatal("replacement directory repair allowed")
	}
}

func TestUpdaterHealthUnsafeRuntimeDirectoryPreserved(t *testing.T) {
	for _, kind := range []string{"symlink", "permissions", "owner"} {
		t.Run(kind, func(t *testing.T) {
			m, e, root := healthFixture(t)
			if err := os.Remove(m.path); err != nil {
				t.Fatal(err)
			}
			directory := filepath.Dir(m.path)
			switch kind {
			case "symlink":
				if err := os.Rename(directory, filepath.Join(root, "old")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "old"), directory); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(directory, 0777); err != nil {
					t.Fatal(err)
				}
			case "owner":
				if err := os.Chown(directory, 973, 973); err != nil {
					t.Fatal(err)
				}
			}
			before, s, err := pathIdentity(directory)
			if err != nil {
				t.Fatal(err)
			}
			if err = m.repair(e, time.Now()); err == nil {
				t.Fatal("unsafe directory repaired")
			}
			after, current, err := pathIdentity(directory)
			if err != nil || before != after || s.Mode != current.Mode || s.Uid != current.Uid {
				t.Fatal("unsafe directory modified", err)
			}
		})
	}
}

func TestUpdaterHealthRepairBackoffAndTransactionMutualExclusion(t *testing.T) {
	m, e, _ := healthFixture(t)
	if err := os.Remove(m.path); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"claimed", "installing", "health_check"} {
		e.state.Status = status
		if err := m.repair(e, time.Now()); err != nil || m.attempts != 0 {
			t.Fatal(status, err)
		}
	}
	e.state.Status = ""
	e.running = true
	_ = m.repair(e, time.Now())
	if m.attempts != 0 {
		t.Fatal("recovery not excluded")
	}
	e.running = false
	e.removing = true
	_ = m.repair(e, time.Now())
	if m.attempts != 0 {
		t.Fatal("removal not excluded")
	}
	e.removing = false
	if err := os.Mkdir(filepath.Join(e.config.StateDirectory, "removal"), 0700); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(e.config.StateDirectory, "removal/request.json")
	if err := os.WriteFile(pending, []byte("pending"), 0600); err != nil {
		t.Fatal(err)
	}
	_ = m.repair(e, time.Now())
	if m.attempts != 0 {
		t.Fatal("persistent removal not excluded")
	}
	_ = os.Remove(pending)
	calls := 0
	m.listen = func(string, *net.UnixAddr) (*net.UnixListener, error) {
		calls++
		return nil, errors.New("injected bind failure")
	}
	now := time.Now()
	for i := 0; i < socketRepairLimit; i++ {
		if err := m.repair(e, now); err == nil {
			t.Fatal("missing injected error")
		}
		if err := m.repair(e, now); err != nil {
			t.Fatal("retry ignored backoff")
		}
		now = m.nextAttempt
	}
	_ = m.repair(e, now.Add(time.Hour))
	if calls != socketRepairLimit {
		t.Fatal("retry limit", calls)
	}
	// Real concurrency: both the transaction lock and the listener lock serialize
	// twenty repair attempts; at most one new listener can be published.
	m.attempts = 0
	m.nextAttempt = time.Time{}
	m.listen = net.ListenUnix
	e.mu.Lock()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = m.repair(e, time.Now()) }()
	}
	if _, _, err := pathIdentity(m.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("repair crossed engine lock")
	}
	e.mu.Unlock()
	wg.Wait()
	if _, _, err := pathIdentity(m.path); err != nil {
		t.Fatal(err)
	}
}
