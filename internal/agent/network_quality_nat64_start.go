package agent

import (
	"context"
	"net"
	"time"

	"404-probe/internal/protocol"
)

// nat64FirstTimeout is how long user targets wait for the first prefix discovery. A resolver that
// answers does so in milliseconds and the query itself gives up after five seconds, so ten seconds
// cover a failing resolver as well; after that the targets are probed with whatever is known (nothing,
// exactly as when a discovery fails) and the discovery carries on in the background.
const nat64FirstTimeout = 10 * time.Second

// startNAT64Discovery makes sure a discovery loop is running for the current worker and reports
// whether user targets may be probed: true when there are none to protect, or the first discovery has
// completed (answer, failure or timeout) in this process.
//
// The loop lives as long as the context it was started with. A worker that was stopped and resumed
// (a disabled device that is enabled again) has a new context, so the loop is started again; the old
// one has ended with its context and cannot overwrite the result of the new one.
func (r *Runner) startNAT64Discovery(ctx context.Context, config *protocol.QualityConfig) bool {
	if config == nil || r.quality == nil {
		return true
	}
	user := false
	for _, t := range config.Targets {
		if t.Source == "user" && t.Status == "active" {
			user = true
		}
	}
	if !user {
		return true
	}
	q := r.quality
	q.mu.Lock()
	launch := q.nat64Ctx == nil || q.nat64Ctx.Err() != nil
	var generation uint64
	if launch {
		generation = nat64NextGeneration()
		q.nat64Ctx = ctx
		if !q.nat64Done && q.nat64Timer == nil {
			timeout := q.nat64Timeout
			if timeout <= 0 {
				timeout = nat64FirstTimeout
			}
			q.nat64Timer = time.AfterFunc(timeout, q.markNAT64Done)
		}
	}
	done := q.nat64Done
	resolver, wait := q.nat64Resolver, q.nat64Wait
	q.mu.Unlock()
	if launch {
		if resolver == nil {
			resolver = net.DefaultResolver
		}
		if wait == nil {
			wait = time.After
		}
		go runNAT64DiscoveryAs(ctx, resolver, wait, q.markNAT64Done, generation)
	}
	return done
}

// markNAT64Done records that the first discovery has finished or timed out and wakes the worker.
func (q *qualityControl) markNAT64Done() {
	q.mu.Lock()
	if !q.nat64Done {
		q.nat64Done = true
		if q.nat64Timer != nil {
			q.nat64Timer.Stop()
		}
	}
	q.mu.Unlock()
	q.signal()
}
