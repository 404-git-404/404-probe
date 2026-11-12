//go:build disk_health_fixture

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"404-probe/internal/hosthealth"
	"404-probe/internal/protocol"
	"golang.org/x/sys/unix"
)

type fixtureDisk struct {
	*hosthealth.Monitor
	control, dir string
	stop         context.CancelFunc
	done         chan struct{}
	err          error
}

func (d *fixtureDisk) Run(ctx context.Context) {
	defer close(d.done)
	defer d.stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var last string
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		raw, err := os.ReadFile(filepath.Join(d.control, "command"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			d.err = err
			return
		}
		command := strings.TrimSpace(string(raw))
		if command == "" || command == last {
			continue
		}
		last = command
		_, action, _ := strings.Cut(command, " ")
		ro := false
		var errno unix.Errno
		switch action {
		case "probe":
			d.CheckFixture()
		case "readonly":
			d.CheckFixture()
			file, err := os.OpenFile(filepath.Join(d.dir, hosthealth.ProbeFilename), os.O_WRONLY|os.O_CREATE, 0600)
			if err == nil {
				file.Close()
			}
			ro = errors.Is(err, unix.EROFS)
			errors.As(err, &errno)
		case "age":
			d.AgeExt4Fixture()
			d.CheckFixture()
		case "stop":
			return
		default:
			d.err = fmt.Errorf("unexpected fixture action %q", action)
			return
		}
		d.err = writeDiskFixtureJSON(d.control, "result", struct {
			Command string              `json:"command"`
			Disk    protocol.DiskHealth `json:"disk"`
			EROFS   bool                `json:"erofs"`
			Errno   uint64              `json:"write_open_errno,omitempty"`
		}{command, d.Snapshot(), ro, uint64(errno)})
		if d.err != nil {
			return
		}
	}
}

func writeDiskFixtureJSON(dir, name string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path+".tmp", payload, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func TestDiskHealthFaultFixture(t *testing.T) {
	control, dir := os.Getenv("R48_DISK_FIXTURE_CONTROL"), os.Getenv("R48_DISK_FIXTURE_DIR")
	if control == "" || dir == "" || os.Geteuid() == 0 {
		t.Fatal("run with scripts/test-disk-health-linux.sh; fixture Agent must be unprivileged")
	}
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/report" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var report protocol.Report
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil || report.Validate() != nil || report.RAMTotal == 0 {
			t.Errorf("invalid collected report: %v %v", err, report.Validate())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if report.HostHealth != nil {
			mu.Lock()
			err := writeDiskFixtureJSON(control, "report", struct {
				Sequence uint64              `json:"sequence"`
				Disk     protocol.DiskHealth `json:"disk"`
			}{report.Sequence, report.HostHealth.Disk})
			mu.Unlock()
			if err != nil {
				t.Error(err)
			}
		}
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{HostHealthReport: true}})
	}))
	defer server.Close()
	runner, err := NewWithExecutor(Config{ServerURL: server.URL, AgentID: "disk-fixture", Token: "synthetic", Interval: 250 * time.Millisecond,
		Timeout: 2 * time.Second, StatePath: filepath.Join(control, "epoch"), AllowInsecureHTTP: true}, slog.New(slog.NewTextHandler(io.Discard, nil)), UnsupportedExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	disk := &fixtureDisk{Monitor: hosthealth.New(dir), control: control, dir: dir, stop: cancel, done: make(chan struct{})}
	runner.diskHealth = disk
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-disk.done:
		if disk.err != nil {
			t.Fatal(disk.err)
		}
	case <-time.After(time.Second):
		t.Fatal("probe worker failed to exit after recovery")
	}
}
