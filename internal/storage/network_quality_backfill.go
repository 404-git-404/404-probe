package storage

import (
	"404-probe/internal/protocol"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// BackfillQualityBatch bypasses upload age/authentication, not content integrity.
// These are already committed observations. Cursor and aggregates commit together.
func (s *Store) BackfillQualityBatch(ctx context.Context, now time.Time) (int, bool, error) {
	var complete bool
	if err := s.db.QueryRowContext(ctx, `SELECT complete FROM quality_backfill WHERE id=1`).Scan(&complete); err != nil {
		return 0, false, err
	}
	if complete {
		return 0, false, nil
	}
	if s.qualityPressure(ctx) {
		return 0, true, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	var cursor int64
	if err = tx.QueryRowContext(ctx, `SELECT last_raw_id FROM quality_backfill WHERE id=1`).Scan(&cursor); err != nil {
		return 0, false, err
	}
	type record struct {
		sample         protocol.QualitySample
		id, epoch      int64
		agent, session string
		hash           []byte
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+qualitySampleColumns+`,id,agent_id,epoch,session_id,content_hash FROM quality_raw WHERE id>? ORDER BY id LIMIT 100`, cursor)
	if err != nil {
		return 0, false, err
	}
	records := []record{}
	for rows.Next() {
		r := record{}
		r.sample, err = qualityReadSample(rows, &r.id, &r.agent, &r.epoch, &r.session, &r.hash)
		if err != nil {
			rows.Close()
			return 0, false, err
		}
		records = append(records, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, false, err
	}
	for _, r := range records {
		payload, err := json.Marshal(r.sample)
		if err != nil {
			return 0, false, err
		}
		digest := sha256.Sum256(payload)
		if !bytes.Equal(digest[:], r.hash) {
			return 0, false, fmt.Errorf("quality backfill hash mismatch at raw %d", r.id)
		}
		var target protocol.QualityTarget
		var wire string
		if err = tx.QueryRowContext(ctx, `SELECT payload FROM quality_targets WHERE id=? AND agent_id=?`, r.sample.TargetID, r.agent).Scan(&wire); err != nil {
			return 0, false, err
		}
		if err = json.Unmarshal([]byte(wire), &target); err != nil {
			return 0, false, err
		}
		if err = qualityRollupTx(ctx, tx, r.agent, r.epoch, r.session, r.id, r.sample, target, r.hash, len(payload), now); err != nil {
			return 0, false, err
		}
		cursor = r.id
	}
	more := len(records) == 100
	if _, err = tx.ExecContext(ctx, `UPDATE quality_backfill SET last_raw_id=?,complete=? WHERE id=1`, cursor, !more); err != nil {
		return 0, false, err
	}
	if err = tx.Commit(); err != nil {
		return 0, false, err
	}
	return len(records), more, nil
}

func qualityRollupReadyTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	var ready bool
	err := tx.QueryRowContext(ctx, `SELECT complete FROM quality_backfill WHERE id=1`).Scan(&ready)
	return ready, err
}
