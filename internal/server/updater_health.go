package server

import "404-probe/internal/storage"

// Health is transient and tied to the exact accepted report, so a stale cache
// entry cannot override a new session or a newer DB snapshot. After restart the
// established legacy boolean gates apply until the next negotiated report.
func (a *App) withUpdaterHealth(record storage.AgentSnapshot) storage.AgentSnapshot {
	if record.State == nil {
		return record
	}
	a.mu.RLock()
	current, ok := a.states[record.Agent.ID]
	a.mu.RUnlock()
	if ok && current.Epoch == record.State.Epoch && current.SessionID == record.State.SessionID && current.Sequence == record.State.Sequence {
		copy := *record.State
		copy.AgentUpdaterHealth = current.AgentUpdaterHealth
		record.State = &copy
	}
	return record
}
