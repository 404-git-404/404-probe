package storage

import (
	"404-probe/internal/protocol"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

// Observation, not a hard physical disk ceiling. No checkpoint inside a write tx.
func (s *Store) qualityPressure(ctx context.Context) bool {
	if s.databasePath == "" {
		return false
	}
	db, err := sqliteFileSize(s.databasePath, "database", false)
	if err != nil {
		return true
	}
	wal, err := sqliteFileSize(s.databasePath+"-wal", "WAL", true)
	if err != nil {
		return true
	}
	if db+wal >= 2<<30 {
		return true
	}
	if wal < 128<<20 {
		return false
	}
	health, err := s.CheckpointWAL(ctx)
	return qualityPressureHealth(health, err)
}

func qualityPressureHealth(health WALHealth, err error) bool {
	return err != nil || health.CheckpointBusy != 0 || health.RemainingFrames > 0
}

func (s *Store) SaveQualityBatch(ctx context.Context, id string, b protocol.QualityBatch, now time.Time) (protocol.QualityBatchResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out := protocol.QualityBatchResponse{Results: []protocol.QualityAck{}}
	if err := b.Validate(); err != nil {
		return out, err
	}
	epoch, _ := protocol.QualityInteger(b.Epoch)
	pressure := s.qualityPressure(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err := requireActiveAgentTx(ctx, tx, id); err != nil {
		return out, err
	}
	if err := requireNoAgentRemovalTx(ctx, tx, id); err != nil {
		return out, err
	}
	var supported bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM quality_capability c JOIN agent_state s ON c.agent_id=s.agent_id AND c.epoch=s.epoch AND c.session_id=s.session_id WHERE c.agent_id=? AND c.epoch=? AND c.session_id=? AND c.supported=1)`, id, epoch, b.SessionID).Scan(&supported); err != nil {
		return out, err
	}
	if !supported {
		return out, ErrQualityUnsupported
	}
	var rawLocal, rawGlobal, minuteLocal, minuteGlobal int
	if err := qualityCountTx(ctx, tx, id, "raw", &rawLocal, &rawGlobal); err != nil {
		return out, err
	}
	if err := qualityCountTx(ctx, tx, id, "minute", &minuteLocal, &minuteGlobal); err != nil {
		return out, err
	}
	_, currentRev, _, _, err := qualityChoicesTx(ctx, tx, id)
	if err != nil {
		return out, err
	}
	for i, sample := range b.Samples {
		ack := protocol.QualityAck{strconv.Itoa(i), sample.Sequence, "invalid"}
		seq, err := protocol.QualityInteger(sample.Sequence)
		if err != nil {
			ack[1] = ""
			out.Results = append(out.Results, ack)
			continue
		}
		if sample.DurationMS == 0 {
			sample.DurationMS = 0
		}
		if sample.LatencyMS != nil && *sample.LatencyMS == 0 {
			zero := 0.0
			sample.LatencyMS = &zero
		}
		payloadBytes, err := json.Marshal(sample)
		if err != nil {
			out.Results = append(out.Results, ack)
			continue
		}
		digest := sha256.Sum256(payloadBytes)
		var oldDigest []byte
		old, err := qualityReadSample(tx.QueryRowContext(ctx, `SELECT `+qualitySampleColumns+`,content_hash FROM quality_raw WHERE agent_id=? AND epoch=? AND session_id=? AND seq=?`, id, epoch, b.SessionID, seq), &oldDigest)
		if err == nil {
			ack[2] = "conflict"
			oldCanonical, encodeErr := json.Marshal(old)
			if encodeErr == nil && bytes.Equal(digest[:], oldDigest) && bytes.Equal(oldCanonical, payloadBytes) {
				ack[2] = "duplicate"
			}
			out.Results = append(out.Results, ack)
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return protocol.QualityBatchResponse{}, err
		}
		var targetJSON string
		err = tx.QueryRowContext(ctx, `SELECT payload FROM quality_targets WHERE id=? AND agent_id=?`, sample.TargetID, id).Scan(&targetJSON)
		if errors.Is(err, sql.ErrNoRows) {
			ack[2] = "unauthorized"
			out.Results = append(out.Results, ack)
			continue
		}
		if err != nil {
			return protocol.QualityBatchResponse{}, err
		}
		var target protocol.QualityTarget
		if err := json.Unmarshal([]byte(targetJSON), &target); err != nil {
			return protocol.QualityBatchResponse{}, err
		}
		if reason := sample.Validate(now, target); reason != "" {
			ack[2] = reason
			out.Results = append(out.Results, ack)
			continue
		}
		slotRev, _ := protocol.QualityInteger(sample.SlotRevision)
		cfgRev, _ := protocol.QualityInteger(sample.ConfigRevision)
		var start, from int64
		var end, until sql.NullInt64
		err = tx.QueryRowContext(ctx, `SELECT start_at,end_at,config_from,config_until FROM quality_intervals WHERE agent_id=? AND target_id=? AND slot=? AND family=? AND revision=?`, id, target.ID, target.Slot, target.Family, slotRev).Scan(&start, &end, &from, &until)
		if errors.Is(err, sql.ErrNoRows) {
			ack[2] = "unauthorized"
			out.Results = append(out.Results, ack)
			continue
		}
		if err != nil {
			return protocol.QualityBatchResponse{}, err
		}
		maxRev := currentRev
		if until.Valid {
			maxRev = until.Int64
		}
		// Revision is the fence; wall-time comparison only applies a bounded clock tolerance.
		if cfgRev < from || cfgRev > maxRev || sample.StartedAt < start-120000 || (end.Valid && sample.StartedAt > end.Int64+120000) {
			ack[2] = "unauthorized"
			out.Results = append(out.Results, ack)
			continue
		}
		if pressure {
			ack[2] = "storage_pressure"
			out.Results = append(out.Results, ack)
			continue
		}
		bucket := sample.StartedAt / 60000 * 60000
		var minuteExists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM quality_minute WHERE target_id=? AND bucket=?)`, target.ID, bucket).Scan(&minuteExists); err != nil {
			return protocol.QualityBatchResponse{}, err
		}
		if rawLocal >= qualityRawDevice || rawGlobal >= qualityRawGlobal || (!minuteExists && (minuteLocal >= qualityMinuteDevice || minuteGlobal >= qualityMinuteGlobal)) {
			ack[2] = "quota"
			out.Results = append(out.Results, ack)
			continue
		}
		attempt, success := boolInt(sample.Attempt()), boolInt(sample.Outcome == "success")
		var latency any
		var sum float64
		if sample.LatencyMS != nil {
			latency = *sample.LatencyMS
			sum = *sample.LatencyMS
		}
		sent, received := 0, 0
		if sample.PacketDenominator() {
			sent = sample.Sent
			received = sample.Received
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO quality_raw(agent_id,target_id,epoch,session_id,seq,slot_revision,config_revision,sample_time,received_at,scheduled_at,finished_at,duration_ms,outcome,resolved_ip,raw_sent,raw_received,content_hash,attempt,success,latency,packet_sent,packet_received) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, target.ID, epoch, b.SessionID, seq, slotRev, cfgRev, sample.StartedAt, now.UnixMilli(), sample.ScheduledAt, sample.FinishedAt, sample.DurationMS, sample.Outcome, sample.ResolvedIP, sample.Sent, sample.Received, digest[:], attempt, success, latency, sent, received); err != nil {
			return protocol.QualityBatchResponse{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO quality_minute(agent_id,target_id,bucket,count,attempts,successes,latency_sum,sent,received) VALUES(?,?,?,1,?,?,?,?,?) ON CONFLICT(target_id,bucket) DO UPDATE SET count=count+1,attempts=attempts+excluded.attempts,successes=successes+excluded.successes,latency_sum=latency_sum+excluded.latency_sum,sent=sent+excluded.sent,received=received+excluded.received`, id, target.ID, bucket, attempt, success, sum, sent, received); err != nil {
			return protocol.QualityBatchResponse{}, err
		}
		rawLocal++
		rawGlobal++
		if !minuteExists {
			minuteLocal++
			minuteGlobal++
		}
		ack[2] = "committed"
		out.Results = append(out.Results, ack)
	}
	var gapsLocal, gapsGlobal int
	if err := qualityCountTx(ctx, tx, id, "gaps", &gapsLocal, &gapsGlobal); err != nil {
		return protocol.QualityBatchResponse{}, err
	}
	for i, gap := range b.Gaps {
		ack := protocol.QualityAck{strconv.Itoa(i), "", "invalid"}
		validReason := gap.Reason == "queue_drop" || gap.Reason == "restart" || gap.Reason == "unknown_gap"
		if !validReason || gap.Start <= 0 || gap.End < gap.Start || gap.Start < now.Add(-6*time.Hour).UnixMilli() || gap.End > now.Add(2*time.Minute).UnixMilli() || (gap.Dropped != nil && (*gap.Dropped < 0 || *gap.Dropped > 6000)) {
			out.Gaps = append(out.Gaps, ack)
			continue
		}
		var prior sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT dropped FROM quality_gaps WHERE agent_id=? AND epoch=? AND session_id=? AND start_at=? AND end_at=? AND reason=?`, id, epoch, b.SessionID, gap.Start, gap.End, gap.Reason).Scan(&prior)
		if err == nil {
			ack[2] = "conflict"
			if (gap.Dropped == nil && !prior.Valid) || (gap.Dropped != nil && prior.Valid && int64(*gap.Dropped) == prior.Int64) {
				ack[2] = "duplicate"
			}
			out.Gaps = append(out.Gaps, ack)
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return protocol.QualityBatchResponse{}, err
		}
		if pressure {
			ack[2] = "storage_pressure"
		} else if gapsLocal >= 4096 {
			ack[2] = "quota"
		} else {
			var dropped any
			if gap.Dropped != nil {
				dropped = *gap.Dropped
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO quality_gaps(agent_id,epoch,session_id,start_at,end_at,reason,dropped) VALUES(?,?,?,?,?,?,?)`, id, epoch, b.SessionID, gap.Start, gap.End, gap.Reason, dropped); err != nil {
				return protocol.QualityBatchResponse{}, err
			}
			gapsLocal++
			ack[2] = "committed"
		}
		out.Gaps = append(out.Gaps, ack)
	}
	// Encoding must be proven bounded before commit, not truncated after data persisted.
	wire, err := json.Marshal(out)
	if err != nil || len(wire)+1 > protocol.QualityResponseLimit {
		return protocol.QualityBatchResponse{}, errors.New("quality ACK exceeds budget")
	}
	if err := tx.Commit(); err != nil {
		return protocol.QualityBatchResponse{}, err
	}
	return out, nil
}
