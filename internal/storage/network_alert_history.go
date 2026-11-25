package storage

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

type NetworkAlert struct {
	NetworkLine
	TargetID     string   `json:"target_id"`
	Start        int64    `json:"start"`
	End          *int64   `json:"end"`
	DurationMS   int64    `json:"duration_ms"`
	WorstP95     *float64 `json:"worst_p95_ms"`
	WorstFailure *float64 `json:"worst_failure_percent"`
	WorstLoss    *float64 `json:"worst_packet_loss_percent"`
	Reason       string   `json:"reason"`
}

func (s *Store) NetworkAlerts(ctx context.Context, id string, now time.Time) ([]NetworkAlert, error) {
	tx, err := s.beginNetworkStatusRead(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := networkAgentExists(ctx, tx, id); err != nil {
		return nil, err
	}
	alerts, err := readNetworkAlerts(ctx, tx, id, now.UnixMilli(), false)
	if err != nil {
		return nil, err
	}
	return alerts, tx.Commit()
}

func readNetworkAlerts(ctx context.Context, tx *sql.Tx, id string, now int64, onlyOpen bool) ([]NetworkAlert, error) {
	rows, err := tx.QueryContext(ctx, `SELECT slot,family,protocol,target_id,start_at,end_at,worst_p95,worst_failure,worst_loss,reason
		FROM quality_alert_history WHERE agent_id=? AND start_at<=?
		AND (end_at IS NULL OR (?=0 AND end_at>=?)) ORDER BY start_at DESC,id DESC`,
		id, now, onlyOpen, now-QualityHistoryRetention.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	alerts := []NetworkAlert{}
	for rows.Next() {
		var a NetworkAlert
		if err := rows.Scan(&a.Slot, &a.Family, &a.Protocol, &a.TargetID, &a.Start, &a.End, &a.WorstP95, &a.WorstFailure, &a.WorstLoss, &a.Reason); err != nil {
			return nil, err
		}
		end := now
		if a.End != nil {
			end = *a.End
		}
		a.DurationMS = max(0, end-a.Start)
		alerts = append(alerts, a)
	}
	return alerts, rows.Err()
}

// SweepNetworkAlerts finalizes each newly evaluable minute using the same
// TCP red rule as the card. The cursor and transitions commit together.
func (s *Store) SweepNetworkAlerts(ctx context.Context, now time.Time) error {
	rows, err := s.db.QueryContext(ctx, `WITH candidates AS (
		SELECT agent_id FROM quality_intervals WHERE protocol='tcp' AND end_at IS NULL
		UNION SELECT agent_id FROM quality_alert_history WHERE end_at IS NULL
	)
	SELECT a.agent_id FROM candidates a LEFT JOIN quality_alert_cursor c ON c.agent_id=a.agent_id
	WHERE c.eval_end IS NULL OR c.eval_end<? ORDER BY a.agent_id`, QualityEvalEnd(now.UnixMilli()))
	if err != nil {
		return err
	}
	var devices []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		devices = append(devices, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(devices) == 0 {
		return nil
	}
	budget := s.qualityBudget(ctx)
	var errs []error
	for _, id := range devices {
		if err := s.sweepAgentNetworkAlerts(ctx, id, now, budget); err != nil && !errors.Is(err, ErrAgentNotFound) {
			errs = append(errs, err)
			if ctx.Err() != nil {
				break
			}
		}
	}
	return errors.Join(errs...)
}

func (s *Store) sweepAgentNetworkAlerts(ctx context.Context, id string, now time.Time, budget qualityBudget) error {
	end := QualityEvalEnd(now.UnixMilli())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previous int64
	err = tx.QueryRowContext(ctx, `SELECT eval_end FROM quality_alert_cursor WHERE agent_id=?`, id).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		previous = end - qualityMinuteMS
	} else if err != nil {
		return err
	}
	if previous >= end {
		return nil
	}
	oldest := end - 1440*qualityMinuteMS
	if previous < oldest {
		if _, err := tx.ExecContext(ctx, `UPDATE quality_alert_history SET end_at=MAX(start_at,?)
			WHERE agent_id=? AND end_at IS NULL`, previous, id); err != nil {
			return err
		}
		previous = oldest
	}
	reader, err := s.newQualityJudgeReader(ctx, tx, budget, id, now, previous/qualityMinuteMS-qualityJudgePrefetchMinutes, end/qualityMinuteMS)
	if err != nil {
		return err
	}
	reader.alertBuckets = map[string]map[int64]qualityBucketData{}
	for _, config := range reader.cfg.Lanes {
		lane := qualityLane{config.Slot, config.Family, config.Protocol}
		if lane.Protocol != "tcp" {
			continue
		}
		tl, active, err := reader.timelineOf(ctx, lane.Slot, lane.Family, lane.Protocol)
		if err != nil {
			return err
		}
		if !active {
			if _, err := tx.ExecContext(ctx, `UPDATE quality_alert_history SET end_at=MAX(start_at,?)
				WHERE agent_id=? AND slot=? AND family=? AND protocol=? AND end_at IS NULL`,
				now.UnixMilli(), id, lane.Slot, lane.Family, lane.Protocol); err != nil {
				return err
			}
			continue
		}
		revision, err := strconv.ParseInt(config.SlotRevision, 10, 64)
		if err != nil {
			return err
		}
		if err := advanceNetworkAlerts(ctx, tx, id, NetworkLine(lane), *config.TargetID, revision, &tl,
			reader.alertBuckets[lane.key()], reader.cfg.EffectiveIntervalMS, previous/qualityMinuteMS, end/qualityMinuteMS); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO quality_alert_cursor VALUES(?,?)
		ON CONFLICT(agent_id) DO UPDATE SET eval_end=excluded.eval_end`, id, end)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func advanceNetworkAlerts(ctx context.Context, tx *sql.Tx, id string, line NetworkLine, target string, revision int64,
	tl *qualityTimeline, buckets map[int64]qualityBucketData, interval int, first, last int64) error {
	var alertID int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM quality_alert_history
		WHERE agent_id=? AND slot=? AND family=? AND protocol=? AND end_at IS NULL`,
		id, line.Slot, line.Family, line.Protocol).Scan(&alertID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for minute := first; minute < last; minute++ {
		at := (minute + 1) * qualityMinuteMS
		lossAlert := tl.lossRedAt(minute, interval)
		if !lossAlert && !tl.latencyFailureRedAt(minute, interval) {
			if alertID != 0 {
				if _, err := tx.ExecContext(ctx, `UPDATE quality_alert_history SET end_at=MAX(start_at,?) WHERE id=?`, at, alertID); err != nil {
					return err
				}
				alertID = 0
			}
			continue
		}
		var p95, failure, loss *float64
		reason := "latency_failure"
		if lossAlert {
			reason = "loss"
		}
		if b, ok := buckets[minute]; ok && tl.at(minute).Kind == minuteData {
			stats := networkStats(b, "tcp")
			p95, failure, loss = stats.P95, stats.FailurePercent, stats.PacketLossPercent
		}
		if alertID == 0 {
			inserted, err := tx.ExecContext(ctx, `INSERT INTO quality_alert_history
				(agent_id,slot,family,protocol,target_id,revision,start_at,worst_p95,worst_failure,worst_loss,reason) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
				id, line.Slot, line.Family, line.Protocol, target, revision, at, p95, failure, loss, reason)
			if err != nil {
				return err
			}
			alertID, err = inserted.LastInsertId()
			if err != nil {
				return err
			}
		} else if p95 != nil || failure != nil || loss != nil || lossAlert {
			if _, err := tx.ExecContext(ctx, `UPDATE quality_alert_history SET
				worst_p95=CASE WHEN worst_p95 IS NULL OR worst_p95<? THEN ? ELSE worst_p95 END,
				worst_failure=CASE WHEN worst_failure IS NULL OR worst_failure<? THEN ? ELSE worst_failure END,
    worst_loss=CASE WHEN worst_loss IS NULL OR worst_loss<? THEN ? ELSE worst_loss END,
    reason=CASE WHEN ? THEN 'loss' ELSE reason END WHERE id=?`,
				p95, p95, failure, failure, loss, loss, lossAlert, alertID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) cleanupNetworkStatus(ctx context.Context, now time.Time) error {
	cutoff := now.Add(-QualityHistoryRetention).UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM quality_alert_history WHERE end_at<?`, cutoff); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM quality_target_history AS h WHERE end_at<? AND revision<(
		SELECT MAX(p.revision) FROM quality_target_history p WHERE p.agent_id=h.agent_id AND p.slot=h.slot
			AND p.family=h.family AND p.protocol=h.protocol AND p.start_at<?)`, cutoff, cutoff); err != nil {
		return err
	}
	return tx.Commit()
}
