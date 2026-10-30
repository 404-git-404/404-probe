package server

import (
	"context"
	"sync"
	"time"
)

// CleanupLoop is started by the Server lifecycle. Warming is independent of
// startup and foreground requests: two workers, one pass, a 30s deadline, no
// refresh loop and no change to the five-second cache TTL.
func (a *App) startDashboardNetworkWarmup() {
	a.dashboardWarmupOnce.Do(func() {
		now, started := a.now(), time.Now()
		go func() {
			defer close(a.dashboardWarmupDone)
			ctx, cancel := context.WithTimeout(a.shutdown, 30*time.Second)
			defer cancel()
			agents, err := a.store.ListAgents(ctx)
			if err != nil {
				if ctx.Err() == nil {
					a.logger.Warn("dashboard cache warmup could not list devices")
				}
				return
			}
			jobs := make(chan string)
			var workers sync.WaitGroup
			for range 2 {
				workers.Go(func() {
					for id := range jobs {
						if ctx.Err() != nil {
							continue
						}
						if a.dashboardWarmupProbe != nil {
							a.dashboardWarmupProbe(id)
						}
						a.dashboardNetworkValue(ctx, id, now.Add(time.Since(started)), false)
					}
				})
			}
		feed:
			for _, agent := range agents {
				if agent.Revoked || agent.DisabledAt != nil {
					continue
				}
				select {
				case jobs <- agent.ID:
				case <-ctx.Done():
					break feed
				}
			}
			close(jobs)
			workers.Wait()
		}()
	})
}
