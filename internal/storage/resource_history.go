package storage

import (
	"context"
	"errors"
	"time"
)

const (
	ResourceHistoryIntervalMS int64 = 60000
	ResourceHistoryPointLimit       = 1441
	ResourceHistoryMaxSpanMS  int64 = 24 * 60 * 60 * 1000
)

var ErrResourceHistoryRange = errors.New("resource history range is invalid")

// ResourceHistory reads only existing arithmetic minute means. The closed
// requested interval includes both intersecting edge buckets; missing minutes
// are not manufactured. The SQL itself bounds both time and returned rows.
func (s *Store) ResourceHistory(ctx context.Context, id string, from, to time.Time) ([]HistoryPoint, error) {
	lower, upper := from.UnixMilli(), to.UnixMilli()
	if lower < 0 || upper <= lower || upper-lower > ResourceHistoryMaxSpanMS {
		return nil, ErrResourceHistoryRange
	}
	lower = (lower / ResourceHistoryIntervalMS) * ResourceHistoryIntervalMS
	upper = (upper / ResourceHistoryIntervalMS) * ResourceHistoryIntervalMS
	rows, err := s.db.QueryContext(ctx, `SELECT bucket,cpu_sum/samples,ram_sum/samples,swap_sum/samples,disk_sum/samples,load1_sum/samples,load5_sum/samples,load15_sum/samples,rx_rate_sum/samples,tx_rate_sum/samples,rx_total,tx_total
 FROM minute_metrics WHERE agent_id=? AND bucket>=? AND bucket<=? ORDER BY bucket LIMIT 1441`, id, lower, upper)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := make([]HistoryPoint, 0, ResourceHistoryPointLimit)
	for rows.Next() {
		p, err := scanHistoryPoint(rows)
		if err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

// Exactly the original History scan/conversion, shared without altering its
// query, nil-empty result or persisted total/rate semantics.
func scanHistoryPoint(row interface{ Scan(...any) error }) (HistoryPoint, error) {
	var p HistoryPoint
	var rx, tx int64
	err := row.Scan(&p.Timestamp, &p.CPU, &p.RAMPercent, &p.SwapPercent, &p.DiskPercent, &p.Load1, &p.Load5, &p.Load15, &p.RXRate, &p.TXRate, &rx, &tx)
	if err != nil {
		return HistoryPoint{}, err
	}
	p.RXTotal = uint64(rx)
	p.TXTotal = uint64(tx)
	return p, nil
}
