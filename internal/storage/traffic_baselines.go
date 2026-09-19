package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrTrafficSampleUnavailable = errors.New("traffic sample unavailable")

type TrafficTotals struct {
	RXTotal   uint64 `json:"rx_total"`
	TXTotal   uint64 `json:"tx_total"`
	StartedAt *int64 `json:"started_at,omitempty"`
}

func (s *Store) TrafficTotals(ctx context.Context, agentID string) (TrafficTotals, error) {
	var rx, tx int64
	if err := s.db.QueryRowContext(ctx, `SELECT rx_total,tx_total FROM agent_state WHERE agent_id=?`, agentID).Scan(&rx, &tx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TrafficTotals{}, ErrTrafficSampleUnavailable
		}
		return TrafficTotals{}, err
	}
	var baselineRX, baselineTX int64
	var startedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT rx_total,tx_total,started_at FROM agent_traffic_baselines WHERE agent_id=?`, agentID).Scan(&baselineRX, &baselineTX, &startedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return TrafficTotals{}, err
	}
	result := TrafficTotals{RXTotal: subtractStoredTotal(rx, baselineRX), TXTotal: subtractStoredTotal(tx, baselineTX)}
	if startedAt.Valid {
		value := startedAt.Int64
		result.StartedAt = &value
	}
	return result, nil
}

func (s *Store) ResetTrafficTotals(ctx context.Context, agentID, requestID string, now time.Time) (TrafficTotals, error) {
	if !validStorageID(agentID, 128) || !validLowerHexStorageID(requestID, 32) || now.IsZero() {
		return TrafficTotals{}, errors.New("invalid traffic baseline request")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TrafficTotals{}, err
	}
	defer tx.Rollback()
	var rx, txTotal int64
	if err := tx.QueryRowContext(ctx, `SELECT rx_total,tx_total FROM agent_state WHERE agent_id=?`, agentID).Scan(&rx, &txTotal); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TrafficTotals{}, ErrTrafficSampleUnavailable
		}
		return TrafficTotals{}, err
	}
	if rx < 0 || txTotal < 0 {
		return TrafficTotals{}, ErrTrafficSampleUnavailable
	}
	var existingRequest string
	var existingRX, existingTX, existingStarted int64
	err = tx.QueryRowContext(ctx, `SELECT request_id,rx_total,tx_total,started_at FROM agent_traffic_baselines WHERE agent_id=?`, agentID).Scan(&existingRequest, &existingRX, &existingTX, &existingStarted)
	if err == nil && existingRequest == requestID {
		if err := tx.Commit(); err != nil {
			return TrafficTotals{}, err
		}
		return TrafficTotals{RXTotal: subtractStoredTotal(rx, existingRX), TXTotal: subtractStoredTotal(txTotal, existingTX), StartedAt: &existingStarted}, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return TrafficTotals{}, err
	}
	stamp := now.UnixMilli()
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_traffic_baselines(agent_id,rx_total,tx_total,started_at,request_id) VALUES(?,?,?,?,?)
		ON CONFLICT(agent_id) DO UPDATE SET rx_total=excluded.rx_total,tx_total=excluded.tx_total,started_at=excluded.started_at,request_id=excluded.request_id`,
		agentID, rx, txTotal, stamp, requestID)
	if err != nil {
		return TrafficTotals{}, err
	}
	if err := tx.Commit(); err != nil {
		return TrafficTotals{}, err
	}
	return TrafficTotals{StartedAt: &stamp}, nil
}

func subtractStoredTotal(total, baseline int64) uint64 {
	if total <= baseline || total < 0 || baseline < 0 {
		return 0
	}
	return uint64(total - baseline)
}

func validLowerHexStorageID(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
