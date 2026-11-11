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
	read    func() platformsupport.Identity
	timeout time.Duration
	latest  atomic.Pointer[identitySample]
}

func (m *platformMonitor) run(ctx context.Context, wakeQuality func()) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for ctx.Err() == nil {
		m.refresh(wakeQuality)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *platformMonitor) refresh(wakeQuality func()) {
	latest := m.snapshot()
	latest.started = time.Now()
	m.publish(latest)
	timer := time.AfterFunc(m.timeout, wakeQuality)
	identity := m.read()
	timer.Stop()
	if time.Since(latest.started) >= m.timeout {
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
