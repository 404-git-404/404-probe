package agent

import (
	"404-probe/internal/protocol"
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"strconv"
	"sync"
	"time"
)

type qualityLane struct {
	target protocol.QualityTarget
	next   time.Time
	cancel context.CancelFunc
	busy   bool
}
type qualitySampleDone struct {
	lane    *qualityLane
	sample  protocol.QualitySample
	auth    uint64
	clockOK bool
	anchor  uint64
}

func qualityClockValid(anchorWall, startWall, finishWall int64, sinceAnchor, duration time.Duration) bool {
	if startWall <= 0 || finishWall < startWall || duration < 0 || duration > 6*time.Second {
		return false
	}
	wallDuration := time.Duration(finishWall-startWall) * time.Millisecond
	anchorDelta := time.Duration(startWall-anchorWall) * time.Millisecond
	return math.Abs(float64(wallDuration-duration)) <= float64(time.Second) && math.Abs(float64(anchorDelta-sinceAnchor)) <= float64(time.Second)
}

type qualityClockFence struct {
	point      time.Time
	generation uint64
}

func (f *qualityClockFence) accept(generation uint64, valid bool, now time.Time) bool {
	if generation != f.generation {
		return false
	}
	if !valid {
		f.point = now
		f.generation++
		return false
	}
	return true
}

type qualityHTTPDone struct {
	upload           *qualityUpload
	config           *protocol.QualityConfig
	ack              protocol.QualityBatchResponse
	err              error
	generation, auth uint64
}
type qualitySchedule struct{ lanes map[string]*qualityLane }

func qualityDelay(base time.Duration, random func() float64) time.Duration {
	x := random()
	if x < 0 {
		x = 0
	}
	if x > 1 {
		x = 1
	}
	return time.Duration(float64(base) * (0.8 + 0.4*x))
}
func qualityLaneKey(t protocol.QualityTarget) string { return t.Slot + "/" + t.Family }
func qualitySameLane(a, b protocol.QualityTarget) bool {
	return a.ID == b.ID && a.SlotRevision == b.SlotRevision && a.Protocol == b.Protocol && a.Host == b.Host && a.Port == b.Port && a.Family == b.Family
}
func (s *qualitySchedule) reconcile(c *protocol.QualityConfig, now time.Time, random func() float64) {
	if s.lanes == nil {
		s.lanes = map[string]*qualityLane{}
	}
	wanted := map[string]protocol.QualityTarget{}
	if c != nil && c.Supported && c.Enabled {
		for _, t := range c.Targets {
			if t.Status == "active" && (t.Family == "ipv4" || c.IPv6) {
				wanted[qualityLaneKey(t)] = t
			}
		}
	}
	for key, l := range s.lanes {
		t, ok := wanted[key]
		if !ok || !qualitySameLane(l.target, t) {
			if l.cancel != nil {
				l.cancel()
			}
			delete(s.lanes, key)
		}
	}
	for key, t := range wanted {
		if _, ok := s.lanes[key]; !ok {
			s.lanes[key] = &qualityLane{target: t, next: now.Add(qualityDelay(30*time.Second, random))}
		}
	}
}
func qualityBackoff(failures int, base time.Duration, random func() float64) time.Duration {
	if failures > 6 {
		failures = 6
	}
	delay := base * time.Duration(1<<max(0, failures-1))
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	delay = qualityDelay(delay, random)
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	return delay
}

func (r *Runner) runQualityWorker(ctx context.Context) {
	if r.quality == nil {
		return
	}
	control := r.quality
	clock := control.now
	if clock == nil {
		clock = time.Now
	}
	random := control.random
	if random == nil {
		random = rand.Float64
	}
	probe := control.probe
	if probe == nil {
		tcp := NewTCPExecutor()
		var icmp *ICMPExecutor
		if e, ok := r.executor.(*ProbeExecutor); ok {
			tcp, _ = e.tcp.(*TCPExecutor)
			icmp, _ = e.icmp.(*ICMPExecutor)
		}
		if tcp == nil {
			tcp = NewTCPExecutor()
		}
		if icmp == nil {
			icmp = NewICMPExecutor()
		}
		probe = func(ctx context.Context, t protocol.QualityTarget) qualityProbeResult {
			if t.Protocol == "icmp" {
				return icmp.qualityProbe(ctx, t)
			}
			return tcp.qualityProbe(ctx, t)
		}
	}
	queue := qualityQueue{}
	control.mu.Lock()
	queue.sequence = control.sequence
	initialStop := control.stopSerial
	control.mu.Unlock()
	var schedule qualitySchedule
	var workers sync.WaitGroup
	finished := make(chan qualitySampleDone, 6)
	httpDone := make(chan qualityHTTPDone, 2)
	flights := map[*qualityLane]bool{}
	var uploadCancel, fetchCancel context.CancelFunc
	defer func() {
		for l := range flights {
			l.cancel()
		}
		if uploadCancel != nil {
			uploadCancel()
		}
		if fetchCancel != nil {
			fetchCancel()
		}
		workers.Wait()
		queue.clear()
		control.mu.Lock()
		control.sequence = queue.sequence
		control.mu.Unlock()
	}()
	var config *protocol.QualityConfig
	var allowed bool
	var generation, auth uint64
	auth = initialStop
	var nextUpload, nextFetch time.Time
	var uploadFailures, fetchFailures int
	startup := clock()
	anchor := qualityClockFence{point: startup}
	queue.addGap(startup.UnixMilli(), startup.UnixMilli(), "restart", nil) // Known startup only, no invented prior downtime.
	for {
		if ctx.Err() != nil {
			return
		}
		control.mu.Lock()
		currentAllowed, wantFetch, gen := control.allowed, control.fetch, control.generation
		cfg := control.config
		stopSerial := control.stopSerial
		control.mu.Unlock()
		now := clock()
		if stopSerial != auth || (!currentAllowed && allowed) {
			auth = stopSerial
			queue.clear()
			for l := range flights {
				l.cancel()
			}
			if uploadCancel != nil {
				uploadCancel()
			}
			if fetchCancel != nil {
				fetchCancel()
			}
			config = nil
			schedule.reconcile(nil, now, random)
			r.logger.Info("quality queue discarded", "reason", "authorization_stopped")
		}
		allowed = currentAllowed
		generation = gen
		if allowed && cfg != nil {
			config = cfg
			if !config.Supported {
				r.stopQuality("session_unsupported")
				continue
			}
		}
		if allowed {
			schedule.reconcile(config, now, random)
		} else {
			schedule.reconcile(nil, now, random)
		}
		if control.scheduleObserved != nil {
			control.scheduleObserved(auth, len(schedule.lanes))
		}
		if !wantFetch && fetchCancel != nil {
			fetchCancel()
		}
		queue.expire(now)
		for _, lane := range schedule.lanes {
			if now.Before(lane.next) {
				continue
			}
			scheduled := lane.next
			lane.next = now.Add(qualityDelay(30*time.Second, random)) // Never catch up missed ticks.
			if lane.busy || len(flights) >= 6 {
				queue.addGap(scheduled.UnixMilli(), now.UnixMilli(), "unknown_gap", nil)
				continue
			}
			if now.Sub(scheduled) > 5*time.Second {
				queue.addGap(scheduled.UnixMilli(), now.UnixMilli(), "unknown_gap", nil)
			}
			probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			lane.cancel = cancel
			lane.busy = true
			flights[lane] = true
			target, revision, frozenAuth := lane.target, config.Revision, auth
			frozenAnchor, frozenAnchorGeneration := anchor.point, anchor.generation
			workers.Add(1)
			go func(l *qualityLane) {
				defer workers.Done()
				defer cancel()
				started := clock()
				result := probe(probeCtx, target)
				finishedAt := clock()
				elapsed := finishedAt.Sub(started)
				ok := qualityClockValid(frozenAnchor.UnixMilli(), started.UnixMilli(), finishedAt.UnixMilli(), started.Sub(frozenAnchor), elapsed) && started.UnixMilli() >= scheduled.UnixMilli()
				sample := protocol.QualitySample{TargetID: target.ID, ConfigRevision: revision, SlotRevision: target.SlotRevision, ScheduledAt: scheduled.UnixMilli(), StartedAt: started.UnixMilli(), FinishedAt: finishedAt.UnixMilli(), DurationMS: float64(elapsed) / float64(time.Millisecond), Outcome: result.outcome, ResolvedIP: result.ip, LatencyMS: result.latency, Sent: result.sent, Received: result.received}
				finished <- qualitySampleDone{lane: l, sample: sample, auth: frozenAuth, clockOK: ok, anchor: frozenAnchorGeneration}
			}(lane)
		}
		if allowed && wantFetch && fetchCancel == nil && !now.Before(nextFetch) {
			fetchCtx, cancel := context.WithCancel(ctx)
			fetchCancel = cancel
			token := generation
			workers.Add(1)
			go func() {
				defer workers.Done()
				c, err := r.fetchQualityConfig(fetchCtx)
				httpDone <- qualityHTTPDone{config: &c, err: err, generation: token}
			}()
			nextFetch = now.Add(30 * time.Second)
		}
		if allowed && uploadCancel == nil && (len(queue.rows) > 0 || len(queue.gaps) > 0) && !now.Before(nextUpload) {
			u, err := queue.pack(strconv.FormatUint(r.epoch, 10), r.sessionID)
			if err != nil {
				r.logger.Warn("quality batch encode", "error", err)
				nextUpload = now.Add(30 * time.Second)
			} else {
				uploadCtx, cancel := context.WithCancel(ctx)
				uploadCancel = cancel
				frozenAuth := auth
				workers.Add(1)
				go func() {
					defer workers.Done()
					var ack protocol.QualityBatchResponse
					err := r.qualityHTTP(uploadCtx, "/api/v1/agent/network-quality", u.body, protocol.QualityResponseLimit, &ack)
					httpDone <- qualityHTTPDone{upload: &u, ack: ack, err: err, auth: frozenAuth}
				}()
				nextUpload = now.Add(30 * time.Second)
			}
		}
		wakeAt := now.Add(time.Hour)
		for _, l := range schedule.lanes {
			if l.next.Before(wakeAt) {
				wakeAt = l.next
			}
		}
		if allowed && wantFetch && fetchCancel == nil && nextFetch.Before(wakeAt) {
			wakeAt = nextFetch
		}
		if allowed && uploadCancel == nil && (len(queue.rows) > 0 || len(queue.gaps) > 0) && nextUpload.Before(wakeAt) {
			wakeAt = nextUpload
		}
		delay := wakeAt.Sub(now)
		if delay < 0 {
			delay = 0
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-control.wake:
		case <-timer.C:
		case done := <-finished:
			delete(flights, done.lane)
			done.lane.busy = false
			if allowed && done.auth == auth {
				if anchor.accept(done.anchor, done.clockOK, clock()) {
					if err := queue.append(done.sample, clock()); err != nil {
						r.stopQuality("sample_sequence_or_encoding")
						r.logger.Warn("quality sample rejected locally", "error", err)
					} else if control.sampleQueued != nil {
						control.sampleQueued(strconv.FormatInt(queue.sequence, 10), done.sample.Outcome)
					}
				} else {
					// A jump retires every flight sharing the old anchor. New
					// stable measurements use a real current-wall anchor;
					// neither timestamps nor the system clock are rewritten.
					queue.addGap(done.sample.StartedAt, done.sample.StartedAt, "unknown_gap", nil)
					r.logger.Warn("quality wall clock drift; sample isolated")
				}
			}
		case done := <-httpDone:
			if done.upload != nil {
				uploadCancel()
				uploadCancel = nil
				if !allowed || done.auth != auth {
					break
				}
				if errors.Is(done.err, errQualityAuthorization) {
					r.stopQuality("upload_authorization")
					break
				}
				complete := false
				if done.err == nil {
					var err error
					complete, err = queue.acknowledge(*done.upload, done.ack, func(reason string) { r.logger.Warn("quality sample permanently rejected", "reason", reason) })
					done.err = err
				}
				if done.err != nil || !complete {
					uploadFailures++
					nextUpload = clock().Add(qualityBackoff(uploadFailures, 10*time.Second, random))
				} else {
					uploadFailures = 0
					delay := 30 * time.Second
					if len(queue.rows) > 0 {
						delay = 10 * time.Second
					}
					nextUpload = clock().Add(delay)
				}
				if control.uploadScheduled != nil {
					control.uploadScheduled(nextUpload)
				}
			} else {
				fetchCancel()
				fetchCancel = nil
				control.mu.Lock()
				fresh := control.generation == done.generation && control.allowed
				if fresh && errors.Is(done.err, errQualityAuthorization) {
					control.allowed = false
					control.fetch = false
					control.config = nil
					control.generation++
					control.stopSerial++
				}
				control.mu.Unlock()
				if !fresh || !allowed {
					break
				}
				if errors.Is(done.err, errQualityAuthorization) {
					r.logger.Info("quality stopped", "reason", "config_authorization")
					break
				}
				if done.err == nil {
					control.mu.Lock()
					if control.generation == done.generation && control.allowed && control.install(*done.config) {
						control.fetch = false
					}
					control.mu.Unlock()
					fetchFailures = 0
				} else {
					fetchFailures++
					nextFetch = clock().Add(max(30*time.Second, qualityBackoff(fetchFailures, 30*time.Second, random)))
				}
			}
		}
		timer.Stop()
	}
}
