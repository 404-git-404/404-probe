package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"404-probe/internal/protocol"
)

type HostHealthState struct {
	Current      *protocol.HostHealth `json:"current,omitempty"`
	FirstFaultAt int64                `json:"first_fault_at,omitempty"`
	LastFaultAt  int64                `json:"last_fault_at,omitempty"`
}

func observeHostHealth(previous *HostHealthState, report *protocol.HostHealth, received int64) *HostHealthState {
	state := HostHealthState{Current: report}
	if previous != nil {
		state.FirstFaultAt, state.LastFaultAt = previous.FirstFaultAt, previous.LastFaultAt
	}
	if report != nil {
		switch report.Disk.Status {
		case protocol.DiskError:
			if state.FirstFaultAt == 0 {
				state.FirstFaultAt = received
			}
			state.LastFaultAt = max(state.LastFaultAt, received)
		case protocol.DiskOK:
			state.FirstFaultAt = 0
		}
	}
	if state.Current == nil && state.LastFaultAt == 0 {
		return nil
	}
	return &state
}

func decodeHostHealth(raw sql.NullString) (*HostHealthState, error) {
	if !raw.Valid {
		return nil, nil
	}
	var state *HostHealthState
	if err := json.Unmarshal([]byte(raw.String), &state); err != nil {
		return nil, fmt.Errorf("decode stored host health: %w", err)
	}
	return state, nil
}
