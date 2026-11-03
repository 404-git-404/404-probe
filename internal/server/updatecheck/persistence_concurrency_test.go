package updatecheck

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"
)

type updateCheckLogWriter func([]byte) (int, error)

func (w updateCheckLogWriter) Write(p []byte) (int, error) { return w(p) }

func TestSnapshotDoesNotWaitForWarningOutput(t *testing.T) {
	c, _ := testChecker(t, func(http.ResponseWriter, *http.Request) {})
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	c.logger = slog.New(slog.NewTextHandler(updateCheckLogWriter(func(p []byte) (int, error) { close(entered); <-release; return len(p), nil }), nil))
	go func() { defer close(done); c.warn("simulated persistence failure") }()
	<-entered
	snapshotPromptly(t, c)
	once.Do(func() { close(release) })
	<-done
}

func snapshotPromptly(t *testing.T, c *Checker) State {
	t.Helper()
	done := make(chan State, 1)
	go func() { done <- c.Snapshot() }()
	select {
	case state := <-done:
		return state
	case <-time.After(time.Second):
		t.Fatal("Snapshot blocked by persistence")
		return State{}
	}
}

func TestSnapshotDoesNotWaitForBlockedPersistence(t *testing.T) {
	for _, check := range []bool{false, true} {
		t.Run(fmt.Sprint(check), func(t *testing.T) {
			c, _ := testChecker(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"tag_name":"v1.1.0"}`) })
			before := c.Snapshot()
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			c.persist = func(State) error { close(entered); <-release; return nil }
			done := make(chan error, 1)
			go func() {
				if check {
					done <- c.check(t.Context())
				} else {
					_, err := c.SetEnabled(false)
					done <- err
				}
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("writer did not start")
			}
			if got := snapshotPromptly(t, c); got != before {
				t.Fatal("state changed before persistence", got, before)
			}
			once.Do(func() { close(release) })
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if c.Snapshot() == before {
				t.Fatal("successful write not committed")
			}
		})
	}
}

func TestPersistenceFailureKeepsSettingButUpdatesHintAndRequestSpacing(t *testing.T) {
	c, count := testChecker(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"tag_name":"v1.1.0"}`) })
	before := c.Snapshot()
	c.persist = func(State) error { return errors.New("disk unavailable") }
	if _, err := c.SetEnabled(false); err == nil || c.Snapshot() != before {
		t.Fatal("failed setting changed state", err)
	}
	if err := c.check(t.Context()); err != nil {
		t.Fatal(err)
	}
	after := c.Snapshot()
	if after.Enabled != before.Enabled || after.Latest != "v1.1.0" || after.CheckedAt != c.now().UnixMilli() || !after.Newer {
		t.Fatal("fresh hint not available after failed cache persistence", after)
	}
	if _, err := c.SetEnabled(false); err == nil || c.Snapshot() != after {
		t.Fatal("failed setting changed fresh hint or enabled", err, c.Snapshot())
	}
	if err := c.check(t.Context()); err != nil || count.Load() != 1 {
		t.Fatal("successful HTTP request repeated", err, count.Load())
	}
}

func TestConcurrentSettingWritesRemainOrdered(t *testing.T) {
	c, _ := testChecker(t, func(http.ResponseWriter, *http.Request) {})
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 2)
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	disk := []bool{}
	c.persist = func(s State) error {
		disk = append(disk, s.Enabled)
		if len(disk) == 1 {
			close(entered)
			<-release
		}
		return nil
	}
	go func() { _, err := c.SetEnabled(false); done <- err }()
	<-entered
	go func() { _, err := c.SetEnabled(true); done <- err }()
	if !snapshotPromptly(t, c).Enabled {
		t.Fatal("uncommitted setting visible")
	}
	once.Do(func() { close(release) })
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if len(disk) != 2 || disk[0] || !disk[1] || !c.Snapshot().Enabled {
		t.Fatal(disk, c.Snapshot())
	}
}

func TestToggleCancellationIsNotARequestFailure(t *testing.T) {
	started := make(chan struct{})
	c, _ := testChecker(t, func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() })
	var logs bytes.Buffer
	c.logger = slog.New(slog.NewTextHandler(&logs, nil))
	done := make(chan error, 1)
	go func() { done <- c.check(t.Context()) }()
	<-started
	if _, err := c.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal("intentional cancel counted as failure", err)
	}
	if logs.Len() != 0 {
		t.Fatal(logs.String())
	}
}
