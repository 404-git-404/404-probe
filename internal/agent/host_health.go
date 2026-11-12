package agent

import (
	"context"
	"sync/atomic"
	"time"

	"404-probe/internal/platformsupport"
	"404-probe/internal/protocol"
)

type identitySample struct {
	identity platformsupport.Identity
	lastOS   string
	started  time.Time
}

type platformMonitor struct {
	read    func(context.Context) platformsupport.Identity
	timeout time.Duration
	latest  atomic.Pointer[identitySample]
}

type diskUsageCollector interface {
	RunDiskUsage(context.Context, time.Duration, chan<- struct{})
}

func (r *Runner) startReportSampling(ctx context.Context) {
	deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var diskReady, platformReady chan struct{}
	if c, ok := r.collector.(diskUsageCollector); ok {
		diskReady = make(chan struct{})
		go c.RunDiskUsage(ctx, r.config.Interval, diskReady)
	}
	if r.platform != nil {
		platformReady = make(chan struct{})
		go r.platform.run(ctx, r.quality.signal, platformReady)
		if r.config.UpdaterSocket != "" {
			go r.runUpdaterReportSampling(ctx)
		}
	}
	for _, ready := range []chan struct{}{diskReady, platformReady} {
		if ready != nil {
			select {
			case <-ready:
			case <-deadline.Done():
				return
			}
		}
	}
}

func (m *platformMonitor) run(ctx context.Context, wakeQuality func(), firstSample chan<- struct{}) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for ctx.Err() == nil {
		m.refresh(ctx, wakeQuality)
		if firstSample != nil {
			close(firstSample)
			firstSample = nil
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *platformMonitor) refresh(ctx context.Context, wakeQuality func()) {
	latest := m.snapshot()
	latest.started = time.Now()
	m.publish(latest)
	timer := time.AfterFunc(m.timeout, wakeQuality)
	deadline, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	identity := m.read(deadline)
	timer.Stop()
	if deadline.Err() != nil || time.Since(latest.started) >= m.timeout {
		identity = platformsupport.UnknownIdentity()
	}
	latest.started = time.Time{}
	latest.identity = identity
	if identity.OS != "" {
		latest.lastOS = identity.OS
	}
	m.publish(latest)
	wakeQuality()
}

func (m *platformMonitor) publish(s identitySample) { m.latest.Store(&s) }

func (m *platformMonitor) snapshot() identitySample {
	latest := m.latest.Load()
	if latest == nil {
		return identitySample{identity: platformsupport.UnknownIdentity()}
	}
	s := *latest
	if !s.started.IsZero() && time.Since(s.started) >= m.timeout {
		s.identity = platformsupport.UnknownIdentity()
	}
	return s
}

func (r *Runner) platformUnsupported() bool {
	if r.platform != nil {
		return r.platform.snapshot().identity.Deferred.DeferredUnsupported
	}
	return r.deferredUnsupported
}

func (r *Runner) qualityExclusion() string {
	if r.platform != nil {
		return r.platform.snapshot().identity.Quality.Reason
	}
	if r.quality != nil {
		return r.quality.excludedReason
	}
	return ""
}

type updaterReportSample struct {
	health  protocol.UpdaterHealth
	removal bool
}

func (r *Runner) runUpdaterReportSampling(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		health := r.updaterHealth(ctx)
		r.updaterReport.Store(&updaterReportSample{health: health, removal: r.remoteRemovalCapability(ctx)})
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runner) reportUpdaterSample(ctx context.Context) updaterReportSample {
	if r.platformUnsupported() || r.config.UpdaterSocket == "" {
		return updaterReportSample{health: protocol.UpdaterHealth{Status: "unsupported", Reason: "system_unsupported"}}
	}
	if r.platform == nil {
		// In-process fixtures with an explicit platform use their injected peer.
		return updaterReportSample{health: r.updaterHealth(ctx), removal: r.remoteRemovalCapability(ctx)}
	}
	if sample := r.updaterReport.Load(); sample != nil {
		return *sample
	}
	return updaterReportSample{health: protocol.UpdaterHealth{Status: "unsupported", Reason: "system_unsupported"}}
}
