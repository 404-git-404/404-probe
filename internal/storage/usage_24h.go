package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Usage24h is the traffic of one device over (about) the last 24 hours, per direction, taken from the permanent
// totals of the minute history: two primary-key lookups, no scan, nothing stored.
type Usage24h struct {
	RX uint64 `json:"rx"`
	TX uint64 `json:"tx"`
	// CoverageMS is the time the two readings are apart. It is below 24 hours when the history is shorter,
	// and by up to a minute below it on a complete history; the page says "last N hours" for the shorter case.
	CoverageMS int64 `json:"coverage_ms"`
}

// GetUsage24h returns the traffic since the first minute at or after now-24h up to the latest minute. It
// reports ok=false when there is no usable pair of readings (no history, a single minute, or counters that
// went backwards).
func (s *Store) GetUsage24h(ctx context.Context, agentID string, now time.Time) (Usage24h, bool, error) {
	var firstBucket, firstRX, firstTX, lastBucket, lastRX, lastTX int64
	err := s.db.QueryRowContext(ctx, `SELECT bucket,rx_total,tx_total FROM minute_metrics WHERE agent_id=? AND bucket>=? ORDER BY bucket LIMIT 1`, agentID, now.Add(-24*time.Hour).UnixMilli()).Scan(&firstBucket, &firstRX, &firstTX)
	if errors.Is(err, sql.ErrNoRows) {
		return Usage24h{}, false, nil
	}
	if err != nil {
		return Usage24h{}, false, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT bucket,rx_total,tx_total FROM minute_metrics WHERE agent_id=? ORDER BY bucket DESC LIMIT 1`, agentID).Scan(&lastBucket, &lastRX, &lastTX)
	if errors.Is(err, sql.ErrNoRows) {
		return Usage24h{}, false, nil
	}
	if err != nil {
		return Usage24h{}, false, err
	}
	if lastBucket <= firstBucket || lastRX < firstRX || lastTX < firstTX {
		return Usage24h{}, false, nil
	}
	return Usage24h{RX: uint64(lastRX - firstRX), TX: uint64(lastTX - firstTX), CoverageMS: lastBucket - firstBucket}, true, nil
}
