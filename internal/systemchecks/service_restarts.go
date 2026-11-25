package systemchecks

import (
	"sort"
	"time"

	"404-probe/internal/protocol"
)

const serviceRestartWindow = 10 * time.Minute

type serviceRestartTracker struct {
	units map[string]serviceRestartState
	last  time.Time
}

type serviceRestartState struct {
	last     uint64
	observed bool
	events   []serviceRestartEvent
}

type serviceRestartEvent struct {
	at    time.Time
	count uint64
}

func (tracker *serviceRestartTracker) observe(now time.Time, counters map[string]uint64) []protocol.ServiceRestart {
	if tracker.units == nil || now.Sub(tracker.last) >= serviceRestartWindow {
		tracker.units = make(map[string]serviceRestartState, len(counters))
	}
	tracker.last = now
	cutoff := now.Add(-serviceRestartWindow)
	for unit := range counters {
		if _, exists := tracker.units[unit]; !exists {
			tracker.units[unit] = serviceRestartState{}
		}
	}
	var out []protocol.ServiceRestart
	for unit, state := range tracker.units {
		counter, present := counters[unit]
		if present && state.observed && counter > state.last {
			state.events = append(state.events, serviceRestartEvent{now, counter - state.last})
		}
		state.last, state.observed = counter, present
		expired := 0
		for expired < len(state.events) && !state.events[expired].at.After(cutoff) {
			expired++
		}
		state.events = state.events[expired:]
		if !present && len(state.events) == 0 {
			delete(tracker.units, unit)
			continue
		}
		tracker.units[unit] = state
		var count uint64
		for _, event := range state.events {
			count += event.count
		}
		if count > 0 {
			out = append(out, protocol.ServiceRestart{Unit: unit, Count: count})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Unit < out[j].Unit
	})
	return out[:min(len(out), protocol.MaxServiceRestarts)]
}
