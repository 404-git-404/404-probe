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
	var rx, tx, baselineRX, baselineTX int64
	var startedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT s.rx_total,s.tx_total,COALESCE(b.rx_total,0),COALESCE(b.tx_total,0),b.started_at
		FROM agent_state s LEFT JOIN agent_traffic_baselines b ON b.agent_id=s.agent_id WHERE s.agent_id=?`, agentID).
		Scan(&rx, &tx, &baselineRX, &baselineTX, &startedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TrafficTotals{}, ErrTrafficSampleUnavailable
		}
		return TrafficTotals{}, err
	}
	result := TrafficTotals{RXTotal: subtractStoredTotal(rx, baselineRX), TXTotal: subtractStoredTotal(tx, baselineTX)}
	if startedAt.Valid {
		value := startedAt.Int64
		result.StartedAt = &value
	}
	return result, nil
}

func (s *Store) ResetTrafficTotals(ctx context.Context, agentID, requestID string, now time.Time, offlineTimeout time.Duration) (TrafficTotals, error) {
	if !validStorageID(agentID, 128) || !validLowerHexStorageID(requestID, 32) || now.IsZero() || offlineTimeout <= 0 {
		return TrafficTotals{}, errors.New("invalid traffic baseline request")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TrafficTotals{}, err
	}
	defer tx.Rollback()
	var recordedAgent string
	var recordedRX, recordedTX, recordedStarted int64
	err = tx.QueryRowContext(ctx, `SELECT agent_id,rx_total,tx_total,started_at FROM agent_traffic_reset_requests WHERE request_id=?`, requestID).
		Scan(&recordedAgent, &recordedRX, &recordedTX, &recordedStarted)
	if err == nil {
		if recordedAgent != agentID {
			return TrafficTotals{}, errors.New("traffic reset request ID belongs to another agent")
		}
		if err := tx.Commit(); err != nil {
			return TrafficTotals{}, err
		}
		return TrafficTotals{RXTotal: uint64(recordedRX), TXTotal: uint64(recordedTX), StartedAt: &recordedStarted}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return TrafficTotals{}, err
	}
	var rx, txTotal, lastSeen, agentUpdated int64
	var revoked int
	var disabledAt sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT s.rx_total,s.tx_total,s.last_seen,a.updated_at,a.revoked,a.disabled_at
		FROM agents a JOIN agent_state s ON s.agent_id=a.id WHERE a.id=?`, agentID).
		Scan(&rx, &txTotal, &lastSeen, &agentUpdated, &revoked, &disabledAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TrafficTotals{}, ErrTrafficSampleUnavailable
		}
		return TrafficTotals{}, err
	}
	if rx < 0 || txTotal < 0 || revoked != 0 || disabledAt.Valid || lastSeen < now.Add(-offlineTimeout).UnixMilli() || lastSeen < agentUpdated {
		return TrafficTotals{}, ErrTrafficSampleUnavailable
	}
	stamp := now.UnixMilli()
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_traffic_baselines(agent_id,rx_total,tx_total,started_at,request_id) VALUES(?,?,?,?,?)
		ON CONFLICT(agent_id) DO UPDATE SET rx_total=excluded.rx_total,tx_total=excluded.tx_total,started_at=excluded.started_at,request_id=excluded.request_id`,
		agentID, rx, txTotal, stamp, requestID)
	if err != nil {
		return TrafficTotals{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_traffic_reset_requests(request_id,agent_id,rx_total,tx_total,started_at) VALUES(?,?,?,?,?)`,
		requestID, agentID, 0, 0, stamp); err != nil {
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
