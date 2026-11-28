package systemchecks

import (
	"context"
	"sync"
	"time"

	"404-probe/internal/protocol"
)

const (
	DefaultAgentState = "/var/lib/404-probe"
	LocalAgentState   = "/var/lib/404-probe-local-agent"
	DefaultRootState  = "/var/lib/404-probe-root-collector"
	LocalRootState    = "/var/lib/404-probe-local-root-collector"
)

type Collector struct {
	mu          sync.Mutex
	key         string
	next        time.Time
	running     bool
	current     *protocol.SystemChecks
	scanKey     string
	scanNext    time.Time
	scan        protocol.CertificateScan
	restarts    serviceRestartTracker
	dnsFailures map[string]int
}

// Snapshot keeps filesystem walks and system commands off the report loop.
func (c *Collector) Snapshot(ctx context.Context, settings protocol.SystemCheckSettings, stateDir, rootDir, serverHost string) *protocol.SystemChecks {
	key := settingsKey(settings)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running && (c.key != key || time.Now().After(c.next)) {
		c.running = true
		go func() {
			dns := make(chan *protocol.LocalDNSCheck, 1)
			go func() { dns <- c.collectDNS(ctx, serverHost) }()
			result := c.collect(ctx, settings, stateDir, rootDir)
			result.LocalDNS = <-dns
			c.mu.Lock()
			defer c.mu.Unlock()
			c.current, c.key, c.next, c.running = &result, key, time.Now().Add(time.Minute), false
		}()
	}
	if c.key != key && c.current != nil {
		return nil
	}
	return c.current
}
