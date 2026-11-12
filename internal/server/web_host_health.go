package server

import (
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

type webHostHealth struct {
	protocol.HostHealth
	FirstFaultAt int64 `json:"first_fault_at,omitempty"`
	LastFaultAt  int64 `json:"last_fault_at,omitempty"`
	Stale        bool  `json:"stale,omitempty"`
}

func reportStateStale(state storage.State, now time.Time, timeout time.Duration) bool {
	return state.CollectedAt <= 0 || state.CollectedAt < now.Add(-timeout).UnixMilli() || state.CollectedAt > now.Add(time.Minute).UnixMilli()
}

func newWebHostHealth(state *storage.HostHealthState, stale bool, now int64) *webHostHealth {
	if state == nil || state.Current == nil {
		return nil
	}
	disk := state.Current.Disk
	if disk.Status == protocol.DiskOK && (disk.CheckedAt < now-(150*time.Second).Milliseconds() || disk.CheckedAt > now+time.Minute.Milliseconds()) {
		stale = true
	}
	view := &webHostHealth{HostHealth: *state.Current, FirstFaultAt: state.FirstFaultAt, LastFaultAt: state.LastFaultAt, Stale: stale}
	if stale {
		view.Disk = protocol.DiskHealth{Status: protocol.DiskUnknown, CheckedAt: disk.CheckedAt, Container: disk.Container}
	}
	return view
}
