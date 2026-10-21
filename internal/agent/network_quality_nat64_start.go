package agent

import (
	"context"
	"net"
	"time"

	"404-probe/internal/protocol"
)

// startNAT64Discovery starts the prefix discovery the first time a user-entered target is active.
// Agents that never run one never send the query.
func (r *Runner) startNAT64Discovery(ctx context.Context, config *protocol.QualityConfig) {
	if config == nil || r.quality == nil {
		return
	}
	user := false
	for _, t := range config.Targets {
		if t.Source == "user" && t.Status == "active" {
			user = true
		}
	}
	q := r.quality
	q.mu.Lock()
	if !user || q.nat64Started {
		q.mu.Unlock()
		return
	}
	q.nat64Started = true
	resolver, wait := q.nat64Resolver, q.nat64Wait
	q.mu.Unlock()
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if wait == nil {
		wait = time.After
	}
	go runNAT64Discovery(ctx, resolver, wait)
}
