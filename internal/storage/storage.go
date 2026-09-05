package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
	_ "modernc.org/sqlite"
)

const currentSchemaVersion = 8

var (
	ErrUnauthorized             = errors.New("unauthorized")
	ErrAgentRevoked             = fmt.Errorf("agent revoked: %w", ErrUnauthorized)
	ErrAgentDisabled            = fmt.Errorf("agent disabled: %w", ErrUnauthorized)
	ErrUnsupportedSchemaVersion = errors.New("unsupported newer schema version")
	ErrUpgradeNotFound          = errors.New("upgrade operation not found")
	ErrUpgradeConflict          = errors.New("agent already has an active upgrade")
	ErrUpgradeTransition        = errors.New("invalid upgrade status transition")
)

type Store struct{ db *sql.DB }

type Agent struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Revoked    bool   `json:"revoked"`
	DisabledAt *int64 `json:"disabled_at"`
	CreatedAt  int64  `json:"created_at"`
}

type State struct {
	AgentID             string  `json:"agent_id"`
	Name                string  `json:"name"`
	Epoch               uint64  `json:"-"`
	SessionID           string  `json:"-"`
	Sequence            uint64  `json:"sequence"`
	BootID              string  `json:"boot_id"`
	Hostname            string  `json:"hostname"`
	OS                  string  `json:"os"`
	Arch                string  `json:"arch"`
	AgentVersion        string  `json:"agent_version"`
	AgentUpgradeCapable bool    `json:"agent_upgrade_capable"`
	Uptime              uint64  `json:"uptime"`
	CPUPercent          float64 `json:"cpu_percent"`
	Load1               float64 `json:"load1"`
	Load5               float64 `json:"load5"`
	Load15              float64 `json:"load15"`
	RAMUsed             uint64  `json:"ram_used"`
	RAMTotal            uint64  `json:"ram_total"`
	RAMPercent          float64 `json:"ram_percent"`
	SwapUsed            uint64  `json:"swap_used"`
	SwapTotal           uint64  `json:"swap_total"`
	SwapPercent         float64 `json:"swap_percent"`
	DiskUsed            uint64  `json:"disk_used"`
	DiskTotal           uint64  `json:"disk_total"`
	DiskPercent         float64 `json:"disk_percent"`
	RXBytes             uint64  `json:"raw_rx"`
	TXBytes             uint64  `json:"raw_tx"`
	RXRate              float64 `json:"rx_rate"`
	TXRate              float64 `json:"tx_rate"`
	RXTotal             uint64  `json:"rx_total"`
	TXTotal             uint64  `json:"tx_total"`
	CollectedAt         int64   `json:"collected_at"`
	LastSeen            int64   `json:"last_seen"`
}

type HistoryPoint struct {
	Timestamp   int64   `json:"timestamp"`
	CPU         float64 `json:"cpu"`
	RAMPercent  float64 `json:"ram_percent"`
	SwapPercent float64 `json:"swap_percent"`
	DiskPercent float64 `json:"disk_percent"`
	Load1       float64 `json:"load1"`
	Load5       float64 `json:"load5"`
	Load15      float64 `json:"load15"`
	RXRate      float64 `json:"rx_rate"`
	TXRate      float64 `json:"tx_rate"`
	RXTotal     uint64  `json:"rx_total"`
	TXTotal     uint64  `json:"tx_total"`
}

type OutboundSnapshot struct {
	AgentID   string                      `json:"agent_id"`
	Available bool                        `json:"available"`
	Status    protocol.OutboundStatus     `json:"status"`
	Selectors []protocol.OutboundSelector `json:"selectors"`
	CheckedAt int64                       `json:"checked_at"`
	UpdatedAt *int64                      `json:"updated_at"`
}

func Open(ctx context.Context, path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("database path is required")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := checkSupportedSchemaVersion(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000"} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	if err := checkSupportedSchemaVersion(ctx, s.db); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("migration metadata: %w", err)
	}
	var version int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > currentSchemaVersion {
		return unsupportedSchemaVersionError(version)
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS agents (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, token_hash BLOB NOT NULL UNIQUE,
			revoked INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_agents_active_token_hash ON agents(token_hash) WHERE revoked=0`,
		`CREATE TABLE IF NOT EXISTS agent_sessions (
			agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			session_id TEXT NOT NULL, started_at INTEGER NOT NULL, active INTEGER NOT NULL, first_seen INTEGER NOT NULL,
			PRIMARY KEY(agent_id, session_id)
		)`,
		`CREATE TABLE IF NOT EXISTS agent_state (
			agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
			session_id TEXT NOT NULL, session_started_at INTEGER NOT NULL, sequence INTEGER NOT NULL, boot_id TEXT NOT NULL,
			hostname TEXT NOT NULL, os TEXT NOT NULL, arch TEXT NOT NULL, uptime INTEGER NOT NULL,
			cpu REAL NOT NULL, load1 REAL NOT NULL, load5 REAL NOT NULL, load15 REAL NOT NULL,
			ram_used INTEGER NOT NULL, ram_total INTEGER NOT NULL, ram_percent REAL NOT NULL,
			swap_used INTEGER NOT NULL, swap_total INTEGER NOT NULL, swap_percent REAL NOT NULL,
			disk_used INTEGER NOT NULL, disk_total INTEGER NOT NULL, disk_percent REAL NOT NULL,
			raw_rx INTEGER NOT NULL, raw_tx INTEGER NOT NULL, rx_rate REAL NOT NULL, tx_rate REAL NOT NULL,
			rx_total INTEGER NOT NULL, tx_total INTEGER NOT NULL,
			collected_at INTEGER NOT NULL, last_seen INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS minute_metrics (
			agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE, bucket INTEGER NOT NULL,
			samples INTEGER NOT NULL, cpu_sum REAL NOT NULL, ram_sum REAL NOT NULL, swap_sum REAL NOT NULL,
			disk_sum REAL NOT NULL, load1_sum REAL NOT NULL, load5_sum REAL NOT NULL, load15_sum REAL NOT NULL,
			rx_rate_sum REAL NOT NULL, tx_rate_sum REAL NOT NULL, rx_total INTEGER NOT NULL, tx_total INTEGER NOT NULL,
			PRIMARY KEY(agent_id, bucket)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_minute_metrics_bucket ON minute_metrics(bucket)`,
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(1, unixepoch())`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migration: %w", err)
		}
	}
	if version < 2 {
		for _, statement := range []string{
			`ALTER TABLE agent_sessions ADD COLUMN epoch INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE agent_state ADD COLUMN epoch INTEGER NOT NULL DEFAULT 0`,
			`INSERT INTO schema_migrations(version, applied_at) VALUES(2, unixepoch())`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migration 2: %w", err)
			}
		}
	}
	if version < 3 {
		for _, statement := range []string{
			`CREATE TABLE probe_schedules (
				id TEXT PRIMARY KEY,
				agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
				name TEXT NOT NULL,
				probe_type TEXT NOT NULL,
				config_json TEXT NOT NULL,
				timeout_ms INTEGER NOT NULL CHECK(timeout_ms BETWEEN 100 AND 30000),
				interval_seconds INTEGER NOT NULL CHECK(interval_seconds BETWEEN 30 AND 604800),
				enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
				next_run_at INTEGER NOT NULL,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE TABLE probe_jobs (
				id TEXT PRIMARY KEY,
				schedule_id TEXT REFERENCES probe_schedules(id) ON DELETE SET NULL,
				agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
				probe_type TEXT NOT NULL,
				config_json TEXT NOT NULL,
				timeout_ms INTEGER NOT NULL CHECK(timeout_ms BETWEEN 100 AND 30000),
				created_at INTEGER NOT NULL,
				scheduled_for INTEGER NOT NULL,
				not_before INTEGER NOT NULL,
				expires_at INTEGER NOT NULL,
				status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','leased','finished','expired')),
				attempt INTEGER NOT NULL DEFAULT 0 CHECK(typeof(attempt) = 'integer' AND attempt >= 0),
				lease_token TEXT,
				lease_epoch INTEGER,
				lease_session_id TEXT,
				leased_at INTEGER,
				lease_until INTEGER,
				finished_at INTEGER,
				CHECK(expires_at > not_before)
			)`,
			`CREATE TABLE probe_results (
				job_id TEXT PRIMARY KEY REFERENCES probe_jobs(id) ON DELETE CASCADE,
				attempt INTEGER NOT NULL CHECK(typeof(attempt) = 'integer' AND attempt > 0),
				received_at INTEGER NOT NULL,
				agent_epoch INTEGER NOT NULL CHECK(agent_epoch >= 0),
				session_id TEXT NOT NULL,
				agent_started_at INTEGER NOT NULL,
				agent_finished_at INTEGER NOT NULL,
				duration_ms REAL NOT NULL CHECK(duration_ms >= 0),
				success INTEGER NOT NULL CHECK(success IN (0,1)),
				resolved_ip TEXT,
				error_category TEXT,
				error_message TEXT,
				payload_json TEXT NOT NULL,
				result_hash BLOB NOT NULL
			)`,
			`CREATE INDEX idx_probe_schedules_due ON probe_schedules(next_run_at) WHERE enabled=1`,
			`CREATE UNIQUE INDEX idx_probe_jobs_schedule_slot ON probe_jobs(schedule_id,scheduled_for) WHERE schedule_id IS NOT NULL`,
			`CREATE INDEX idx_probe_jobs_claim ON probe_jobs(agent_id,not_before,created_at) WHERE status='queued'`,
			`CREATE INDEX idx_probe_jobs_lease_expiry ON probe_jobs(agent_id,lease_until) WHERE status='leased'`,
			`CREATE UNIQUE INDEX idx_probe_jobs_one_active_lease ON probe_jobs(agent_id) WHERE status='leased'`,
			`CREATE INDEX idx_probe_jobs_expiry ON probe_jobs(expires_at) WHERE status IN ('queued','leased')`,
			`CREATE INDEX idx_probe_jobs_agent_finished ON probe_jobs(agent_id,finished_at DESC) WHERE status='finished'`,
			`CREATE INDEX idx_probe_jobs_schedule_finished ON probe_jobs(schedule_id,finished_at DESC) WHERE status='finished'`,
			`INSERT INTO schema_migrations(version, applied_at) VALUES(3, unixepoch())`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migration 3: %w", err)
			}
		}
	}
	if version < 4 {
		for _, statement := range []string{
			`CREATE INDEX idx_agents_created_id ON agents(created_at DESC,id DESC)`,
			`CREATE INDEX idx_agents_revoked_created_id ON agents(revoked,created_at DESC,id DESC)`,
			`CREATE INDEX idx_probe_schedules_created_id ON probe_schedules(created_at DESC,id DESC)`,
			`CREATE INDEX idx_probe_schedules_agent_created_id ON probe_schedules(agent_id,created_at DESC,id DESC)`,
			`CREATE INDEX idx_probe_schedules_enabled_created_id ON probe_schedules(enabled,created_at DESC,id DESC)`,
			`CREATE INDEX idx_probe_schedules_type_created_id ON probe_schedules(probe_type,created_at DESC,id DESC)`,
			`CREATE INDEX idx_probe_jobs_created_id ON probe_jobs(created_at DESC,id DESC)`,
			`CREATE INDEX idx_probe_jobs_agent_created_id ON probe_jobs(agent_id,created_at DESC,id DESC)`,
			`CREATE INDEX idx_probe_jobs_schedule_created_id ON probe_jobs(schedule_id,created_at DESC,id DESC) WHERE schedule_id IS NOT NULL`,
			`CREATE INDEX idx_probe_jobs_type_created_id ON probe_jobs(probe_type,created_at DESC,id DESC)`,
			`CREATE INDEX idx_probe_jobs_status_created_id ON probe_jobs(status,created_at DESC,id DESC)`,
			`INSERT INTO schema_migrations(version, applied_at) VALUES(4, unixepoch())`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migration 4: %w", err)
			}
		}
	}
	if version < 5 {
		for _, statement := range []string{
			`ALTER TABLE agents ADD COLUMN disabled_at INTEGER`,
			`CREATE INDEX idx_agents_disabled_created_id ON agents(disabled_at,created_at DESC,id DESC)`,
			`INSERT INTO schema_migrations(version, applied_at) VALUES(5, unixepoch())`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migration 5: %w", err)
			}
		}
	}
	if version < 6 {
		for _, statement := range []string{
			`CREATE TABLE agent_outbound_snapshots (
				agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
				available INTEGER NOT NULL CHECK(available IN (0,1)),
				payload_json TEXT NOT NULL,
				checked_at INTEGER NOT NULL,
				updated_at INTEGER
			)`,
			`INSERT INTO schema_migrations(version, applied_at) VALUES(6, unixepoch())`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migration 6: %w", err)
			}
		}
	}
	if version < 7 {
		for _, statement := range []string{
			`ALTER TABLE agent_state ADD COLUMN agent_version TEXT NOT NULL DEFAULT 'unknown'`,
			`ALTER TABLE agent_state ADD COLUMN agent_upgrade_capable INTEGER NOT NULL DEFAULT 0 CHECK(agent_upgrade_capable IN (0,1))`,
			`CREATE TABLE agent_upgrade_operations (
				operation_id TEXT PRIMARY KEY,
				agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
				from_version TEXT NOT NULL,
				target_version TEXT NOT NULL,
				status TEXT NOT NULL CHECK(status IN ('requested','claimed','downloading','verifying','staging','installing','restarting','health_check','succeeded','failed','rolled_back')),
				failure_code TEXT,
				failure_message TEXT,
				created_at INTEGER NOT NULL,
				started_at INTEGER,
				finished_at INTEGER,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE UNIQUE INDEX idx_agent_upgrade_one_active ON agent_upgrade_operations(agent_id) WHERE status IN ('requested','claimed','downloading','verifying','staging','installing','restarting','health_check')`,
			`CREATE INDEX idx_agent_upgrade_agent_created ON agent_upgrade_operations(agent_id,created_at DESC)`,
			`INSERT INTO schema_migrations(version, applied_at) VALUES(7, unixepoch())`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migration 7: %w", err)
			}
		}
	}
	if version < 8 {
		for _, statement := range []string{
			`ALTER TABLE agent_outbound_snapshots ADD COLUMN status TEXT NOT NULL DEFAULT 'unavailable' CHECK(status IN ('connected','not_detected','auth_required','unavailable'))`,
			`UPDATE agent_outbound_snapshots SET status=CASE WHEN available=1 THEN 'connected' ELSE 'unavailable' END`,
			`DROP INDEX IF EXISTS idx_probe_jobs_one_active_lease`,
			`CREATE UNIQUE INDEX idx_probe_jobs_one_active_probe_lease ON probe_jobs(agent_id) WHERE status='leased' AND probe_type<>'singbox_selector_switch'`,
			`CREATE UNIQUE INDEX idx_probe_jobs_one_active_selector_lease ON probe_jobs(agent_id) WHERE status='leased' AND probe_type='singbox_selector_switch'`,
			`INSERT INTO schema_migrations(version, applied_at) VALUES(8, unixepoch())`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migration 8: %w", err)
			}
		}
	}
	return tx.Commit()
}

func (s *Store) SaveOutboundSnapshot(ctx context.Context, agentID string, snapshot protocol.OutboundSnapshot, now time.Time) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var revoked int
	var disabledAt sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT revoked,disabled_at FROM agents WHERE id=?`, agentID).Scan(&revoked, &disabledAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnauthorized
		}
		return err
	}
	if revoked != 0 {
		return ErrAgentRevoked
	}
	if disabledAt.Valid {
		return ErrAgentDisabled
	}
	checkedAt := now.UnixMilli()
	status := snapshot.Status
	if status == "" {
		if snapshot.Available {
			status = protocol.OutboundStatusConnected
		} else {
			status = protocol.OutboundStatusUnavailable
		}
	}
	if snapshot.Available {
		payload, err := json.Marshal(snapshot.Selectors)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_outbound_snapshots(agent_id,available,payload_json,checked_at,updated_at,status)
			VALUES(?,1,?,?,?,?) ON CONFLICT(agent_id) DO UPDATE SET available=1,payload_json=excluded.payload_json,checked_at=excluded.checked_at,updated_at=excluded.updated_at,status=excluded.status`,
			agentID, string(payload), checkedAt, checkedAt, status)
		if err != nil {
			return err
		}
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_outbound_snapshots(agent_id,available,payload_json,checked_at,updated_at,status)
			VALUES(?,0,'[]',?,NULL,?) ON CONFLICT(agent_id) DO UPDATE SET available=0,checked_at=excluded.checked_at,status=excluded.status`, agentID, checkedAt, status)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) GetOutboundSnapshot(ctx context.Context, agentID string) (OutboundSnapshot, bool, error) {
	var result OutboundSnapshot
	var available int
	var payload string
	var updatedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT agent_id,available,status,payload_json,checked_at,updated_at FROM agent_outbound_snapshots WHERE agent_id=?`, agentID).
		Scan(&result.AgentID, &available, &result.Status, &payload, &result.CheckedAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return OutboundSnapshot{}, false, nil
	}
	if err != nil {
		return OutboundSnapshot{}, false, err
	}
	result.Available = available != 0
	if updatedAt.Valid {
		result.UpdatedAt = &updatedAt.Int64
	}
	if err := json.Unmarshal([]byte(payload), &result.Selectors); err != nil {
		return OutboundSnapshot{}, false, fmt.Errorf("decode outbound snapshot: %w", err)
	}
	return result, true, nil
}

func checkSupportedSchemaVersion(ctx context.Context, db *sql.DB) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM sqlite_master WHERE type='table' AND name='schema_migrations'
	)`).Scan(&exists); err != nil {
		return fmt.Errorf("inspect schema version: %w", err)
	}
	if !exists {
		return nil
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > currentSchemaVersion {
		return unsupportedSchemaVersionError(version)
	}
	return nil
}

func unsupportedSchemaVersionError(version int) error {
	return fmt.Errorf("%w: database is version %d, binary supports up to version %d", ErrUnsupportedSchemaVersion, version, currentSchemaVersion)
}

func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version)
	return version, err
}

func (s *Store) AddAgent(ctx context.Context, id, name string, tokenHash []byte, now time.Time) error {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(name) == "" || len(tokenHash) == 0 {
		return errors.New("invalid agent")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO agents(id,name,token_hash,created_at,updated_at) VALUES(?,?,?,?,?)`, id, name, tokenHash, now.UnixMilli(), now.UnixMilli())
	return err
}

func (s *Store) ListAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,revoked,disabled_at,created_at FROM agents ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Agent
	for rows.Next() {
		var a Agent
		var revoked int
		var disabledAt sql.NullInt64
		if err := rows.Scan(&a.ID, &a.Name, &revoked, &disabledAt, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.Revoked = revoked != 0
		if disabledAt.Valid {
			a.DisabledAt = &disabledAt.Int64
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

func (s *Store) RevokeAgent(ctx context.Context, id string, now time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE agents SET revoked=1,updated_at=? WHERE id=? AND revoked=0`, now.UnixMilli(), id)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if n > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE probe_jobs SET status='expired' WHERE agent_id=? AND probe_type=? AND status='queued'`, id, string(protocol.ProbeTypeSelectorSwitch)); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) DisableAgent(ctx context.Context, id string, now time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var revoked int
	if err := tx.QueryRowContext(ctx, `SELECT revoked FROM agents WHERE id=?`, id).Scan(&revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrAgentNotFound
		}
		return false, err
	}
	if revoked != 0 {
		return false, ErrAgentRevoked
	}
	result, err := tx.ExecContext(ctx, `UPDATE agents SET disabled_at=?,updated_at=? WHERE id=? AND revoked=0 AND disabled_at IS NULL`, now.UnixMilli(), now.UnixMilli(), id)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if n > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE probe_jobs SET status='expired' WHERE agent_id=? AND probe_type=? AND status='queued'`, id, string(protocol.ProbeTypeSelectorSwitch)); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) EnableAgent(ctx context.Context, id string, now time.Time) (bool, error) {
	var revoked int
	if err := s.db.QueryRowContext(ctx, `SELECT revoked FROM agents WHERE id=?`, id).Scan(&revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrAgentNotFound
		}
		return false, err
	}
	if revoked != 0 {
		return false, ErrAgentRevoked
	}
	result, err := s.db.ExecContext(ctx, `UPDATE agents SET disabled_at=NULL,updated_at=? WHERE id=? AND revoked=0 AND disabled_at IS NOT NULL`, now.UnixMilli(), id)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

func (s *Store) Authenticate(ctx context.Context, token string) (string, error) {
	want := auth.Hash(token)
	var id string
	var revoked int
	var disabledAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id,revoked,disabled_at FROM agents WHERE token_hash=?`, want).Scan(&id, &revoked, &disabledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUnauthorized
	}
	if err == nil && revoked != 0 {
		return "", ErrAgentRevoked
	}
	if err == nil && disabledAt.Valid {
		return "", ErrAgentDisabled
	}
	return id, err
}

func (s *Store) ProcessReport(ctx context.Context, authenticatedID string, r protocol.Report, received time.Time) (State, bool, string, error) {
	if authenticatedID != r.AgentID {
		return State{}, false, "agent ID does not match token", ErrUnauthorized
	}
	if r.Epoch > math.MaxInt64 || r.Sequence > math.MaxInt64 || exceedsInt64(r) {
		return State{}, false, "counter is too large", errors.New("report value exceeds SQLite integer range")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return State{}, false, "", err
	}
	defer tx.Rollback()
	var name string
	var revoked int
	var disabledAt sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT name,revoked,disabled_at FROM agents WHERE id=?`, authenticatedID).Scan(&name, &revoked, &disabledAt); err != nil || revoked != 0 || disabledAt.Valid {
		if err == nil {
			if revoked != 0 {
				err = ErrAgentRevoked
			} else {
				err = ErrAgentDisabled
			}
		}
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrUnauthorized
		}
		return State{}, false, "unauthorized", err
	}
	previous, exists, err := readStateTx(ctx, tx, authenticatedID, name)
	if err != nil {
		return State{}, false, "", err
	}
	if exists {
		if r.Epoch < previous.Epoch {
			return previous, false, "stale epoch", nil
		}
		if r.Epoch == previous.Epoch {
			if r.SessionID != previous.SessionID {
				return previous, false, "session does not match epoch", nil
			}
			if r.Sequence <= previous.Sequence {
				return previous, false, "duplicate or stale sequence", nil
			}
		} else {
			if _, err = tx.ExecContext(ctx, `UPDATE agent_sessions SET active=0 WHERE agent_id=?`, authenticatedID); err != nil {
				return State{}, false, "", err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO agent_sessions(agent_id,session_id,started_at,active,first_seen,epoch) VALUES(?,?,?,1,?,?)`, authenticatedID, r.SessionID, r.CollectedAt, received.UnixMilli(), int64(r.Epoch)); err != nil {
				return State{}, false, "", err
			}
			if err = requeueSupersededSessionLeasesTx(ctx, tx, authenticatedID, r.Epoch, r.SessionID, received.UnixMilli()); err != nil {
				return State{}, false, "", err
			}
		}
	} else {
		if _, err = tx.ExecContext(ctx, `INSERT INTO agent_sessions(agent_id,session_id,started_at,active,first_seen,epoch) VALUES(?,?,?,1,?,?)`, authenticatedID, r.SessionID, r.CollectedAt, received.UnixMilli(), int64(r.Epoch)); err != nil {
			return State{}, false, "", err
		}
	}
	state, err := calculateState(name, previous, exists, r, received)
	if err != nil {
		return State{}, false, "", err
	}
	if err := writeStateTx(ctx, tx, state); err != nil {
		return State{}, false, "", err
	}
	if err := aggregateMinuteTx(ctx, tx, state); err != nil {
		return State{}, false, "", err
	}
	if err := tx.Commit(); err != nil {
		return State{}, false, "", err
	}
	return state, true, "", nil
}

func exceedsInt64(r protocol.Report) bool {
	max := uint64(math.MaxInt64)
	return r.Uptime > max || r.RAMUsed > max || r.RAMTotal > max || r.SwapUsed > max || r.SwapTotal > max || r.DiskUsed > max || r.DiskTotal > max || r.RXBytes > max || r.TXBytes > max
}

func calculateState(name string, old State, exists bool, r protocol.Report, received time.Time) (State, error) {
	rxTotal, txTotal := old.RXTotal, old.TXTotal
	var rxRate, txRate float64
	if exists {
		rxDelta, rxReset := delta(old.RXBytes, r.RXBytes, old.BootID != r.BootID)
		txDelta, txReset := delta(old.TXBytes, r.TXBytes, old.BootID != r.BootID)
		max := uint64(math.MaxInt64)
		if rxTotal > max || txTotal > max || rxDelta > max-rxTotal || txDelta > max-txTotal {
			return State{}, errors.New("permanent traffic counter exceeds SQLite integer range")
		}
		rxTotal += rxDelta
		txTotal += txDelta
		dt := float64(received.UnixMilli()-old.LastSeen) / 1000
		if dt > 0 {
			if !rxReset {
				rxRate = float64(rxDelta) / dt
			}
			if !txReset {
				txRate = float64(txDelta) / dt
			}
		}
	}
	agentVersion := r.AgentVersion
	if agentVersion == "" {
		agentVersion = "unknown"
	}
	return State{
		AgentID: r.AgentID, Name: name, Epoch: r.Epoch, SessionID: r.SessionID, Sequence: r.Sequence, BootID: r.BootID,
		Hostname: r.Hostname, OS: r.OS, Arch: r.Arch, AgentVersion: agentVersion, AgentUpgradeCapable: r.AgentUpgradeCapable, Uptime: r.Uptime, CPUPercent: r.CPUPercent,
		Load1: r.Load1, Load5: r.Load5, Load15: r.Load15, RAMUsed: r.RAMUsed, RAMTotal: r.RAMTotal, RAMPercent: r.RAMPercent,
		SwapUsed: r.SwapUsed, SwapTotal: r.SwapTotal, SwapPercent: r.SwapPercent, DiskUsed: r.DiskUsed, DiskTotal: r.DiskTotal, DiskPercent: r.DiskPercent,
		RXBytes: r.RXBytes, TXBytes: r.TXBytes, RXRate: rxRate, TXRate: txRate, RXTotal: rxTotal, TXTotal: txTotal,
		CollectedAt: r.CollectedAt, LastSeen: received.UnixMilli(),
	}, nil
}

func delta(previous, current uint64, bootChanged bool) (uint64, bool) {
	if bootChanged || current < previous {
		return current, true
	}
	return current - previous, false
}

func readStateTx(ctx context.Context, tx *sql.Tx, id, name string) (State, bool, error) {
	s := State{AgentID: id, Name: name}
	var epoch, sequence, uptime, ramUsed, ramTotal, swapUsed, swapTotal, diskUsed, diskTotal, rawRX, rawTX, rxTotal, txTotal int64
	var upgradeCapable int
	err := tx.QueryRowContext(ctx, `SELECT epoch,session_id,sequence,boot_id,hostname,os,arch,agent_version,agent_upgrade_capable,uptime,cpu,load1,load5,load15,ram_used,ram_total,ram_percent,swap_used,swap_total,swap_percent,disk_used,disk_total,disk_percent,raw_rx,raw_tx,rx_rate,tx_rate,rx_total,tx_total,collected_at,last_seen FROM agent_state WHERE agent_id=?`, id).Scan(
		&epoch, &s.SessionID, &sequence, &s.BootID, &s.Hostname, &s.OS, &s.Arch, &s.AgentVersion, &upgradeCapable, &uptime, &s.CPUPercent, &s.Load1, &s.Load5, &s.Load15, &ramUsed, &ramTotal, &s.RAMPercent, &swapUsed, &swapTotal, &s.SwapPercent, &diskUsed, &diskTotal, &s.DiskPercent, &rawRX, &rawTX, &s.RXRate, &s.TXRate, &rxTotal, &txTotal, &s.CollectedAt, &s.LastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	assignUnsigned(&s, epoch, sequence, uptime, ramUsed, ramTotal, swapUsed, swapTotal, diskUsed, diskTotal, rawRX, rawTX, rxTotal, txTotal)
	s.AgentUpgradeCapable = upgradeCapable == 1
	return s, true, nil
}

func assignUnsigned(s *State, v ...int64) {
	s.Epoch = uint64(v[0])
	s.Sequence = uint64(v[1])
	s.Uptime = uint64(v[2])
	s.RAMUsed = uint64(v[3])
	s.RAMTotal = uint64(v[4])
	s.SwapUsed = uint64(v[5])
	s.SwapTotal = uint64(v[6])
	s.DiskUsed = uint64(v[7])
	s.DiskTotal = uint64(v[8])
	s.RXBytes = uint64(v[9])
	s.TXBytes = uint64(v[10])
	s.RXTotal = uint64(v[11])
	s.TXTotal = uint64(v[12])
}

func writeStateTx(ctx context.Context, tx *sql.Tx, s State) error {
	if err := validatePermanentTotals(s); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_state(agent_id,session_id,session_started_at,sequence,boot_id,hostname,os,arch,agent_version,agent_upgrade_capable,uptime,cpu,load1,load5,load15,ram_used,ram_total,ram_percent,swap_used,swap_total,swap_percent,disk_used,disk_total,disk_percent,raw_rx,raw_tx,rx_rate,tx_rate,rx_total,tx_total,collected_at,last_seen,epoch)
	VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(agent_id) DO UPDATE SET session_id=excluded.session_id,sequence=excluded.sequence,boot_id=excluded.boot_id,hostname=excluded.hostname,os=excluded.os,arch=excluded.arch,agent_version=excluded.agent_version,agent_upgrade_capable=excluded.agent_upgrade_capable,uptime=excluded.uptime,cpu=excluded.cpu,load1=excluded.load1,load5=excluded.load5,load15=excluded.load15,ram_used=excluded.ram_used,ram_total=excluded.ram_total,ram_percent=excluded.ram_percent,swap_used=excluded.swap_used,swap_total=excluded.swap_total,swap_percent=excluded.swap_percent,disk_used=excluded.disk_used,disk_total=excluded.disk_total,disk_percent=excluded.disk_percent,raw_rx=excluded.raw_rx,raw_tx=excluded.raw_tx,rx_rate=excluded.rx_rate,tx_rate=excluded.tx_rate,rx_total=excluded.rx_total,tx_total=excluded.tx_total,collected_at=excluded.collected_at,last_seen=excluded.last_seen,epoch=excluded.epoch`,
		s.AgentID, s.SessionID, s.CollectedAt, int64(s.Sequence), s.BootID, s.Hostname, s.OS, s.Arch, s.AgentVersion, boolInt(s.AgentUpgradeCapable), int64(s.Uptime), s.CPUPercent, s.Load1, s.Load5, s.Load15, int64(s.RAMUsed), int64(s.RAMTotal), s.RAMPercent, int64(s.SwapUsed), int64(s.SwapTotal), s.SwapPercent, int64(s.DiskUsed), int64(s.DiskTotal), s.DiskPercent, int64(s.RXBytes), int64(s.TXBytes), s.RXRate, s.TXRate, int64(s.RXTotal), int64(s.TXTotal), s.CollectedAt, s.LastSeen, int64(s.Epoch))
	return err
}

func aggregateMinuteTx(ctx context.Context, tx *sql.Tx, s State) error {
	if err := validatePermanentTotals(s); err != nil {
		return err
	}
	bucket := (s.LastSeen / 60000) * 60000
	_, err := tx.ExecContext(ctx, `INSERT INTO minute_metrics(agent_id,bucket,samples,cpu_sum,ram_sum,swap_sum,disk_sum,load1_sum,load5_sum,load15_sum,rx_rate_sum,tx_rate_sum,rx_total,tx_total) VALUES(?,?,1,?,?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(agent_id,bucket) DO UPDATE SET samples=samples+1,cpu_sum=cpu_sum+excluded.cpu_sum,ram_sum=ram_sum+excluded.ram_sum,swap_sum=swap_sum+excluded.swap_sum,disk_sum=disk_sum+excluded.disk_sum,load1_sum=load1_sum+excluded.load1_sum,load5_sum=load5_sum+excluded.load5_sum,load15_sum=load15_sum+excluded.load15_sum,rx_rate_sum=rx_rate_sum+excluded.rx_rate_sum,tx_rate_sum=tx_rate_sum+excluded.tx_rate_sum,rx_total=excluded.rx_total,tx_total=excluded.tx_total`,
		s.AgentID, bucket, s.CPUPercent, s.RAMPercent, s.SwapPercent, s.DiskPercent, s.Load1, s.Load5, s.Load15, s.RXRate, s.TXRate, int64(s.RXTotal), int64(s.TXTotal))
	return err
}

func validatePermanentTotals(s State) error {
	max := uint64(math.MaxInt64)
	if s.RXTotal > max || s.TXTotal > max {
		return errors.New("permanent traffic counter exceeds SQLite integer range")
	}
	return nil
}

func (s *Store) ListStates(ctx context.Context) ([]State, error) {
	agents, err := s.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]State, 0, len(agents))
	for _, a := range agents {
		if a.Revoked {
			continue
		}
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return nil, err
		}
		state, exists, readErr := readStateTx(ctx, tx, a.ID, a.Name)
		rollbackErr := tx.Rollback()
		if readErr != nil {
			return nil, readErr
		}
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return nil, rollbackErr
		}
		if exists {
			result = append(result, state)
		} else {
			result = append(result, State{AgentID: a.ID, Name: a.Name})
		}
	}
	return result, nil
}

func (s *Store) History(ctx context.Context, id string, since time.Time) ([]HistoryPoint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT bucket,cpu_sum/samples,ram_sum/samples,swap_sum/samples,disk_sum/samples,load1_sum/samples,load5_sum/samples,load15_sum/samples,rx_rate_sum/samples,tx_rate_sum/samples,rx_total,tx_total FROM minute_metrics WHERE agent_id=? AND bucket>=? ORDER BY bucket`, id, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var points []HistoryPoint
	for rows.Next() {
		var p HistoryPoint
		var rx, tx int64
		if err := rows.Scan(&p.Timestamp, &p.CPU, &p.RAMPercent, &p.SwapPercent, &p.DiskPercent, &p.Load1, &p.Load5, &p.Load15, &p.RXRate, &p.TXRate, &rx, &tx); err != nil {
			return nil, err
		}
		p.RXTotal = uint64(rx)
		p.TXTotal = uint64(tx)
		points = append(points, p)
	}
	return points, rows.Err()
}

func (s *Store) CleanupHistory(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM minute_metrics WHERE bucket < ?`, before.UnixMilli())
	return err
}
