package hosthealth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"404-probe/internal/protocol"
	"golang.org/x/sys/unix"
)

func TestDiskProbeCleanupAndRecreation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ProbeFilename)
	for range 2 {
		m := New(dir)
		m.check()
		if info, err := os.Stat(path); err != nil || info.Size() != 4096 {
			t.Fatalf("probe file after startup: %v, %v", info, err)
		}
		m.Cleanup()
		m.check()
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("probe file after shutdown: %v", err)
		}
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, link := range []func(string, string) error{os.Symlink, os.Link} {
		if err := link(target, path); err != nil {
			t.Fatal(err)
		}
		New(dir).Cleanup()
		if _, err := os.Lstat(path); err != nil {
			t.Fatal("cleanup removed an unsafe probe file:", err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDiskProbeCleanupDoesNotWaitForStalledProbeOrUnlink(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		m := Monitor{
			probe:  func() probeResult { <-release; return probeResult{} },
			kernel: func() kernelSample { return kernelSample{} },
			now:    time.Now,
			remove: func() { t.Error("cleanup attempted while probe is in flight") },
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go m.Run(ctx)
		synctest.Wait()
		start := time.Now()
		m.Cleanup()
		if time.Since(start) != 0 {
			t.Fatal("cleanup waited for probe")
		}
		close(release)
	})
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		defer close(release)
		m := Monitor{probe: func() probeResult { return probeResult{} }, remove: func() { <-release }}
		start := time.Now()
		m.Cleanup()
		if elapsed := time.Since(start); elapsed != 2*time.Second {
			t.Fatalf("blocked cleanup took %v", elapsed)
		}
	})
}

type testFile struct {
	writeErr, syncErr, readErr error
	sync                       func()
	verified                   bool
}

func (f testFile) WriteAt(p []byte, _ int64) (int, error) { return len(p), f.writeErr }
func (f testFile) Sync() error {
	if f.sync != nil {
		f.sync()
	}
	return f.syncErr
}
func (f testFile) ReadBack([]byte) (bool, error) { return f.verified, f.readErr }

func TestProbeErrorsAndReadback(t *testing.T) {
	for _, tc := range []struct {
		name   string
		file   testFile
		reason protocol.DiskHealthReason
	}{
		{"write", testFile{writeErr: unix.EIO}, protocol.DiskWriteEIO},
		{"sync", testFile{syncErr: unix.EIO}, protocol.DiskWriteEIO},
		{"read", testFile{readErr: unix.EIO}, protocol.DiskReadEIO},
		{"space", testFile{writeErr: unix.ENOSPC}, protocol.DiskFull},
		{"quota", testFile{syncErr: unix.EDQUOT}, protocol.DiskFull},
		{"read-only", testFile{writeErr: unix.EROFS}, protocol.DiskUnwritable},
		{"direct unsupported", testFile{}, ""},
		{"verified", testFile{verified: true}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := checkFile(tc.file, make([]byte, probeSize))
			if result.reason != tc.reason || tc.reason == "" && (result.verified == nil || *result.verified != tc.file.verified) {
				t.Fatalf("probe result: %+v", result)
			}
		})
	}
	if !directUnsupported(unix.EINVAL) || !directUnsupported(unix.EOPNOTSUPP) || directUnsupported(unix.EIO) {
		t.Fatal("unexpected O_DIRECT fallback classification")
	}
}

func TestDiskStateTransitions(t *testing.T) {
	now := time.Unix(1800000000, 0)
	var result probeResult
	var ro bool
	var ext4, scsi uint64
	m := Monitor{now: func() time.Time { return now }, probe: func() probeResult { return result }, kernel: func() kernelSample {
		readOnly, ext4Errors, scsiErrors := ro, ext4, scsi
		return kernelSample{readOnly: &readOnly, filesystems: []filesystem{{key: "device", DiskFilesystem: protocol.DiskFilesystem{
			Scope: "root", Ext4Errors: &ext4Errors, SCSIErrors: &scsiErrors,
		}}}}
	}}
	check := func(want protocol.DiskHealthStatus, reasons ...protocol.DiskHealthReason) {
		t.Helper()
		disk := m.Snapshot()
		if disk.Status != want || !slices.Equal(disk.Reasons, reasons) {
			t.Fatalf("status %+v, want %s %v", disk, want, reasons)
		}
		if err := (protocol.HostHealth{Disk: disk}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
	check(protocol.DiskUnknown)
	ext4, scsi = 7, 8
	m.check()
	check(protocol.DiskOK)
	result.reason = protocol.DiskWriteEIO
	m.check()
	check(protocol.DiskError, protocol.DiskWriteEIO)
	result.reason = ""
	m.check()
	check(protocol.DiskError, protocol.DiskWriteEIO)
	m.check()
	check(protocol.DiskOK)
	result.reason = protocol.DiskUnwritable
	m.check()
	check(protocol.DiskUnknown, protocol.DiskUnwritable)
	ro = true
	m.check()
	check(protocol.DiskError, protocol.DiskReadOnly)
	ro, result.reason = false, ""
	ext4++
	scsi++
	m.check()
	check(protocol.DiskError, protocol.DiskSCSIErrors, protocol.DiskExt4Errors)
	now = now.Add(ext4Window)
	m.check()
	check(protocol.DiskError, protocol.DiskExt4Errors)
	now = now.Add(time.Millisecond)
	check(protocol.DiskOK)
}

func TestDiskFaultPersistsOnEROFSWithWritableSuperblock(t *testing.T) {
	for _, fault := range []protocol.DiskHealthReason{protocol.DiskExt4Errors, protocol.DiskWriteEIO, protocol.DiskReadEIO} {
		t.Run(string(fault), func(t *testing.T) {
			now := time.Unix(1800000000, 0)
			ro, ext4 := false, uint64(1)
			readOnly := probeFailure(unix.EROFS, protocol.DiskWriteEIO)
			result := readOnly
			m := Monitor{now: func() time.Time { return now }, probe: func() probeResult { return result }, kernel: func() kernelSample {
				errors := ext4
				return kernelSample{readOnly: &ro, filesystems: []filesystem{{key: "root", DiskFilesystem: protocol.DiskFilesystem{
					Scope: "root", Ext4Errors: &errors,
				}}}}
			}}
			check := func(status protocol.DiskHealthStatus, reason protocol.DiskHealthReason) {
				t.Helper()
				m.check()
				disk := m.Snapshot()
				if disk.Status != status || reason != "" && !slices.Contains(disk.Reasons, reason) {
					t.Fatalf("after %s: got %+v, want %s/%s", now.Sub(time.Unix(1800000000, 0)), disk, status, reason)
				}
			}
			check(protocol.DiskUnknown, protocol.DiskUnwritable)
			now = now.Add(time.Minute)
			if fault == protocol.DiskExt4Errors {
				ext4++
			} else {
				result = probeResult{reason: fault}
			}
			check(protocol.DiskError, fault)
			start := m.Snapshot().StartedAt
			result = readOnly
			for range 14 {
				now = now.Add(time.Minute)
				check(protocol.DiskError, fault)
				if m.Snapshot().StartedAt != start {
					t.Fatal("persistent fault was restarted")
				}
			}
			result = probeResult{}
			check(protocol.DiskError, fault)
			result = readOnly
			check(protocol.DiskError, fault)
			result = probeResult{}
			check(protocol.DiskError, fault)
			check(protocol.DiskOK, "")
			result = readOnly
			check(protocol.DiskUnknown, protocol.DiskUnwritable)
		})
	}
}

func TestBlockedSyncHasOneFlightAndSnapshotNeverWaits(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var elapsed, probes atomic.Int64
	start := time.Unix(1800000000, 0)
	m := Monitor{now: func() time.Time { return start.Add(time.Duration(elapsed.Load())) }, kernel: func() kernelSample { return kernelSample{} }, probe: func() probeResult {
		probes.Add(1)
		return checkFile(testFile{sync: func() { close(entered); <-release }}, make([]byte, probeSize))
	}}
	go func() { m.check(); close(done) }()
	<-entered
	elapsed.Store(int64(stallAfter + time.Second))
	observed := make(chan protocol.DiskHealth, 1)
	go func() { m.check(); observed <- m.Snapshot() }()
	select {
	case disk := <-observed:
		if disk.Status != protocol.DiskError || disk.StalledSince != start.UnixMilli() || !slices.Contains(disk.Reasons, protocol.DiskStalled) || probes.Load() != 1 {
			t.Fatalf("blocked sync: %+v, probes %d", disk, probes.Load())
		}
	case <-time.After(time.Second):
		t.Error("snapshot blocked behind fsync")
	}
	close(release)
	<-done
	disk := m.Snapshot()
	if disk.Status != protocol.DiskOK || !slices.Equal(disk.Reasons, []protocol.DiskHealthReason{protocol.DiskSlow}) {
		t.Fatalf("finished sync: %+v", disk)
	}
}

func TestMountInfoSeparatesPerMountAndSuperOptions(t *testing.T) {
	const sample = `256 152 254:1 / / ro,nosuid,relatime shared:78 master:1 - ext4 /dev/vda1 rw,discard,errors=remount-ro
658 256 254:1 /var/lib/404-probe /var/lib/404-probe rw,nosuid,relatime shared:78 - ext4 /dev/vda1 rw,discard,errors=remount-ro
292 256 254:15 / /boot/efi ro,nosuid,relatime shared:78 - vfat /dev/vda15 rw,fmask=0077,errors=remount-ro
659 256 8:1 / /disk\040data rw - ext4 /dev/sda1 ro,errors=remount-ro
`
	root, data, err := parseMounts(strings.NewReader(sample), "/var/lib/404-probe")
	if err != nil || root.readOnly || data.readOnly || root.device != data.device || data.path != "/var/lib/404-probe" {
		t.Fatalf("systemd namespace: %+v %+v %v", root, data, err)
	}
	_, data, err = parseMounts(strings.NewReader(sample), "/disk data/agent")
	if err != nil || !data.readOnly || data.device != "8:1" {
		t.Fatalf("read-only superblock: %+v %v", data, err)
	}
	root, _, err = parseMounts(strings.NewReader("40 39 0:76 / / rw,relatime - overlay overlay rw,lowerdir=/layer\n"), "/var/lib/404-probe")
	if err != nil || !inContainer(root) {
		t.Fatalf("overlay namespace: %+v %v", root, err)
	}
}

func TestKernelCountersByMountDevice(t *testing.T) {
	sys := t.TempDir()
	write := func(rel, contents string) {
		t.Helper()
		path := filepath.Join(sys, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("devices/block/sda/sda1/partition", "1\n")
	write("devices/block/sda/device/ioerr_cnt", "0x1a\n")
	write("devices/block/sda/device/state", "running\n")
	write("fs/ext4/sda1/errors_count", "9\n")
	write("fs/ext4/sda1/last_error_time", "1800000000\n")
	if err := os.MkdirAll(filepath.Join(sys, "dev/block"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../devices/block/sda/sda1", filepath.Join(sys, "dev/block/8:1")); err != nil {
		t.Fatal(err)
	}
	f := readFilesystem(sys, "data", mount{device: "8:1", fsType: "ext4"})
	if f.Ext4Errors == nil || *f.Ext4Errors != 9 || f.Ext4LastErrorAt != 1800000000 || f.SCSIErrors == nil || *f.SCSIErrors != 26 || f.SCSIState != "running" {
		t.Fatalf("wrong counter parsing: %+v", f)
	}
	write("fs/ext4/sda1/errors_count", "-1\n")
	if f := readFilesystem(sys, "data", mount{device: "8:1", fsType: "ext4"}); f.Ext4Errors != nil {
		t.Fatal("negative count reported as available")
	}
}
