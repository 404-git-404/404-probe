package storage

import (
	"context"
	"database/sql"
	"time"
)

const (
	// ScheduledProbeJobRetention applies only to terminal scheduled probes.
	ScheduledProbeJobRetention          = 30 * 24 * time.Hour
	scheduledProbeJobRetentionBatchSize = 500
)

const (
	selectFinishedScheduledProbeJobsBatch = `SELECT id FROM probe_jobs INDEXED BY idx_probe_jobs_scheduled_finished_retention
		WHERE origin='scheduled' AND status='finished' AND finished_at < ?
		ORDER BY finished_at,id LIMIT ?`
	selectExpiredScheduledProbeJobsBatch = `SELECT id FROM probe_jobs INDEXED BY idx_probe_jobs_scheduled_expired_retention
		WHERE origin='scheduled' AND status='expired' AND expires_at < ?
		ORDER BY expires_at,id LIMIT ?`
	deleteFinishedScheduledProbeJobsBatch = `DELETE FROM probe_jobs WHERE id IN (` + selectFinishedScheduledProbeJobsBatch + `)`
	deleteExpiredScheduledProbeJobsBatch  = `DELETE FROM probe_jobs WHERE id IN (` + selectExpiredScheduledProbeJobsBatch + `)`
)

// CleanupScheduledProbeJobsBatch deletes at most 500 scheduled finished jobs
// and 500 scheduled expired jobs older than before. Result rows are removed by
// the existing foreign-key cascade. The returned more flag asks the caller to
// run another bounded batch if either status filled its limit.
func (s *Store) CleanupScheduledProbeJobsBatch(ctx context.Context, before time.Time) (deleted int64, more bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	cutoff := before.UnixMilli()
	finishedDeleted, err := execScheduledProbeJobRetentionDelete(ctx, tx, deleteFinishedScheduledProbeJobsBatch, cutoff)
	if err != nil {
		return 0, false, err
	}
	expiredDeleted, err := execScheduledProbeJobRetentionDelete(ctx, tx, deleteExpiredScheduledProbeJobsBatch, cutoff)
	if err != nil {
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return finishedDeleted + expiredDeleted,
		finishedDeleted == scheduledProbeJobRetentionBatchSize || expiredDeleted == scheduledProbeJobRetentionBatchSize,
		nil
}

func execScheduledProbeJobRetentionDelete(ctx context.Context, tx *sql.Tx, statement string, cutoff int64) (int64, error) {
	result, err := tx.ExecContext(ctx, statement, cutoff, scheduledProbeJobRetentionBatchSize)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
