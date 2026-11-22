package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"404-probe/internal/protocol"
)

var ErrDiskFaultChanged = errors.New("disk fault is no longer the current recovered fault")

type AgentControls struct {
	CountryLookupEnabled    bool  `json:"country_lookup_enabled"`
	YouTubeCheckEnabled     bool  `json:"youtube_check_enabled"`
	DiskFaultAcknowledgedAt int64 `json:"disk_fault_acknowledged_at"`
}

const agentControlColumns = `country_lookup_enabled,youtube_check_enabled,disk_fault_acknowledged_at`

func migrateAgentControls39Tx(ctx context.Context, tx *sql.Tx) error {
	for _, column := range []struct{ table, name, definition string }{
		{"agents", "country_lookup_enabled", "INTEGER NOT NULL DEFAULT 1 CHECK(country_lookup_enabled IN (0,1))"},
		{"agents", "youtube_check_enabled", "INTEGER NOT NULL DEFAULT 1 CHECK(youtube_check_enabled IN (0,1))"},
		{"agents", "disk_fault_acknowledged_at", "INTEGER NOT NULL DEFAULT 0 CHECK(disk_fault_acknowledged_at>=0)"},
		{"probe_jobs", "cancelled", "INTEGER NOT NULL DEFAULT 0 CHECK(cancelled IN (0,1))"},
		{"agent_country_code_lookups", "cancelled", "INTEGER NOT NULL DEFAULT 0 CHECK(cancelled IN (0,1))"},
	} {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info(?) WHERE name=?)`, column.table, column.name).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE `+column.table+` ADD COLUMN `+column.name+` `+column.definition); err != nil {
				return err
			}
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(39,unixepoch())`)
	return err
}

func (s *Store) ReadAgentControls(ctx context.Context, id string) (AgentControls, error) {
	var controls AgentControls
	err := s.db.QueryRowContext(ctx, `SELECT `+agentControlColumns+` FROM agents WHERE id=?`, id).
		Scan(&controls.CountryLookupEnabled, &controls.YouTubeCheckEnabled, &controls.DiskFaultAcknowledgedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrAgentNotFound
	}
	return controls, err
}

type AgentCheck string

const (
	CountryLookupCheck AgentCheck = "country"
	YouTubeCheck       AgentCheck = "youtube"
)

func (s *Store) SetAgentCheckEnabled(ctx context.Context, id string, check AgentCheck, enabled bool) error {
	var column, cancel string
	switch check {
	case CountryLookupCheck:
		column = "country_lookup_enabled"
		cancel = `UPDATE agent_country_code_lookups SET cancelled=1 WHERE agent_id=? AND status IN ('requested','delivered')`
	case YouTubeCheck:
		column = "youtube_check_enabled"
		// Preserve cancelled jobs in history without turning them into an expired observation.
		cancel = `UPDATE probe_jobs SET status='expired',cancelled=1,lease_token=NULL,lease_epoch=NULL,lease_session_id=NULL,
			leased_at=NULL,lease_until=NULL WHERE agent_id=? AND probe_type='google_status' AND status IN ('queued','leased')`
	default:
		return errors.New("invalid Agent check")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE agents SET `+column+`=? WHERE id=? AND revoked=0`, enabled, id)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count == 0 {
		return ErrAgentNotFound
	}
	if !enabled {
		if _, err := tx.ExecContext(ctx, cancel, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) AcknowledgeDiskFault(ctx context.Context, id string, faultAt int64, now time.Time, timeout time.Duration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireActiveAgentTx(ctx, tx, id); err != nil {
		return err
	}
	var raw sql.NullString
	var collectedAt, lastSeen int64
	err = tx.QueryRowContext(ctx, `SELECT host_health_json,collected_at,last_seen FROM agent_state WHERE agent_id=?`, id).
		Scan(&raw, &collectedAt, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDiskFaultChanged
	}
	if err != nil {
		return err
	}
	health, err := decodeHostHealth(raw)
	if err != nil {
		return err
	}
	nowMS := now.UnixMilli()
	if health == nil || health.Current == nil || health.Current.Disk.Status != protocol.DiskOK ||
		faultAt <= 0 || health.LastFaultAt != faultAt || faultAt > nowMS || faultAt < now.Add(-7*24*time.Hour).UnixMilli() ||
		collectedAt < now.Add(-timeout).UnixMilli() || lastSeen < now.Add(-timeout).UnixMilli() ||
		collectedAt > now.Add(time.Minute).UnixMilli() || health.Current.Disk.CheckedAt > now.Add(time.Minute).UnixMilli() ||
		health.Current.Disk.CheckedAt < now.Add(-150*time.Second).UnixMilli() {
		return ErrDiskFaultChanged
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET disk_fault_acknowledged_at=? WHERE id=?`, faultAt, id); err != nil {
		return err
	}
	return tx.Commit()
}
