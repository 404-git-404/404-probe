// Package hosthealth monitors the storage backing an Agent.
package hosthealth

import (
	"context"
	"slices"
	"sync"
	"time"

	"404-probe/internal/protocol"
)

const (
	ProbeInterval = time.Minute
	ProbeFilename = ".disk-probe"
	probeSize     = 4096
	slowAfter     = 15 * time.Second
	stallAfter    = 30 * time.Second
	ext4Window    = 10 * time.Minute
)

type filesystem struct {
	key string
	protocol.DiskFilesystem
}

type kernelSample struct {
	readOnly    *bool
	container   bool
	filesystems []filesystem
}

type probeResult struct {
	verified *bool
	reason   protocol.DiskHealthReason
}

type Monitor struct {
	probe  func() probeResult
	kernel func() kernelSample
	now    func() time.Time
	remove func()

	mu           sync.Mutex
	stopping     bool
	inFlight     time.Time
	last         protocol.DiskHealth
	started      time.Time
	ioFault      protocol.DiskHealthReason
	successes    int
	ext4Observed time.Time
	counters     map[string]protocol.DiskFilesystem
}

func New(dir string) *Monitor {
	probe, kernel := platformProbe(dir)
	return &Monitor{probe: probe, kernel: kernel, now: time.Now, remove: func() { removeProbe(dir) }}
}

// Run keeps the kernel call on a single background goroutine. A deadline can
// describe a blocked system call, but it cannot interrupt one.
func (m *Monitor) Run(ctx context.Context) {
	if m.probe == nil {
		return
	}
	ticker := time.NewTicker(ProbeInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		m.check()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Cleanup stops new probes and gives an idle monitor a bounded chance to remove
// its file. A blocked filesystem call must not hold up process exit.
func (m *Monitor) Cleanup() {
	m.mu.Lock()
	idle := !m.stopping && m.inFlight.IsZero()
	m.stopping = true
	m.mu.Unlock()
	if !idle || m.probe == nil || m.remove == nil {
		return
	}
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.remove()
	}()
	select {
	case <-done:
	case <-timeout.C:
	}
}

func (m *Monitor) check() {
	m.mu.Lock()
	if m.stopping || !m.inFlight.IsZero() {
		m.mu.Unlock()
		return
	}
	start := m.now()
	m.inFlight = start
	first := m.counters == nil
	m.mu.Unlock()

	var baseline kernelSample
	if first {
		baseline = m.kernel()
	}
	result := m.probe()
	sample := m.kernel()
	end := m.now()

	m.mu.Lock()
	defer m.mu.Unlock()
	if first {
		m.counters = make(map[string]protocol.DiskFilesystem, 2)
		for _, fs := range baseline.filesystems {
			m.counters[fs.key] = fs.DiskFilesystem
		}
	}
	scsiChanged := m.updateCounters(sample, end)
	m.inFlight = time.Time{}
	d := protocol.DiskHealth{Status: protocol.DiskOK, CheckedAt: end.UnixMilli(), ProbeMS: end.Sub(start).Milliseconds(),
		ReadBackVerified: result.verified, ReadOnlySuper: sample.readOnly, Container: sample.container}
	for _, fs := range sample.filesystems {
		d.Filesystems = append(d.Filesystems, fs.DiskFilesystem)
	}
	if result.reason == "" {
		if m.ioFault != "" {
			m.successes++
			if m.successes >= 2 {
				m.ioFault = ""
			}
		}
	} else {
		m.successes = 0
		if result.reason == protocol.DiskWriteEIO || result.reason == protocol.DiskReadEIO {
			m.ioFault = result.reason
		} else {
			d.Status = protocol.DiskUnknown
			if result.reason != protocol.DiskUnwritable || sample.readOnly == nil || !*sample.readOnly {
				d.Reasons = append(d.Reasons, result.reason)
			}
		}
	}
	if m.ioFault != "" {
		d.Reasons = append(d.Reasons, m.ioFault)
	}
	if sample.readOnly != nil && *sample.readOnly {
		d.Reasons = append(d.Reasons, protocol.DiskReadOnly)
	}
	if scsiChanged {
		d.Reasons = append(d.Reasons, protocol.DiskSCSIErrors)
	}
	if end.Sub(start) > slowAfter {
		d.Reasons = append(d.Reasons, protocol.DiskSlow)
	}
	if hasFault(d.Reasons) || m.ext4Active(end) {
		if m.started.IsZero() {
			m.started = end
			if m.ioFault != "" || end.Sub(start) > stallAfter {
				m.started = start
			}
		}
	} else {
		m.started = time.Time{}
	}
	m.last = d
}

func (m *Monitor) updateCounters(sample kernelSample, now time.Time) bool {
	changed := false
	for _, fs := range sample.filesystems {
		previous := m.counters[fs.key]
		if fs.Ext4Errors != nil {
			if previous.Ext4Errors != nil && *fs.Ext4Errors > *previous.Ext4Errors {
				m.ext4Observed = now
			}
			previous.Ext4Errors = fs.Ext4Errors
		}
		if fs.SCSIErrors != nil {
			changed = changed || (previous.SCSIErrors != nil && *fs.SCSIErrors > *previous.SCSIErrors)
			previous.SCSIErrors = fs.SCSIErrors
		}
		m.counters[fs.key] = previous
	}
	return changed
}

func (m *Monitor) ext4Active(now time.Time) bool {
	return !m.ext4Observed.IsZero() && now.Sub(m.ext4Observed) <= ext4Window
}

func hasFault(reasons []protocol.DiskHealthReason) bool {
	return slices.ContainsFunc(reasons, protocol.DiskHealthReason.Fault)
}

// Snapshot performs no filesystem operations and never waits for the probe.
func (m *Monitor) Snapshot() protocol.DiskHealth {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.last
	if d.Status == "" {
		d.Status = protocol.DiskUnknown
	}
	now := m.now()
	d.Reasons = slices.Clone(d.Reasons)
	started := m.started
	if m.ext4Active(now) {
		d.Reasons = append(d.Reasons, protocol.DiskExt4Errors)
	}
	if !m.inFlight.IsZero() && now.Sub(m.inFlight) > stallAfter {
		d.Reasons = append(d.Reasons, protocol.DiskStalled)
		d.StalledSince = m.inFlight.UnixMilli()
		d.ProbeMS = now.Sub(m.inFlight).Milliseconds()
		if started.IsZero() || !hasFault(m.last.Reasons) && !m.ext4Active(now) {
			started = m.inFlight
		}
	}
	if hasFault(d.Reasons) {
		d.Status = protocol.DiskError
		d.StartedAt = started.UnixMilli()
	}
	return d
}
