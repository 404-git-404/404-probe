package storage

import (
	"context"
	"database/sql"
)

// QualityAlertGate is the N6 gate of the global "alerting" state: the device has an open IPv4 TCP
// interval, is not revoked or paused, and has no removal in progress. It is the very SQL condition the
// v1.0.6 TCP Mini answer used for current_tcp, so a closed switch, a revoked device and so on keep
// meaning "no alert" (design 1.9).
func (s *Store) QualityAlertGate(ctx context.Context, agentID string) (bool, error) {
	return qualityAlertGate(ctx, s.db, agentID)
}

type qualityQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func qualityAlertGate(ctx context.Context, q qualityQuerier, agentID string) (bool, error) {
	var open bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM quality_intervals i JOIN agents a ON a.id=i.agent_id WHERE i.agent_id=? AND i.family='ipv4' AND i.protocol='tcp' AND i.end_at IS NULL AND a.revoked=0 AND a.disabled_at IS NULL AND NOT EXISTS(SELECT 1 FROM agent_removal_operations o WHERE o.agent_id=a.id))`, agentID).Scan(&open)
	return open, err
}
