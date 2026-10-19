package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"404-probe/internal/protocol"
)

var (
	ErrPublicEgressUnsupported = errors.New("agent does not support public egress checks")
	ErrPublicEgressPending     = errors.New("public egress check is already pending")
	ErrPublicEgressDisabled    = errors.New("public egress detection is switched off")
)

// PublicEgressCooldownError carries the first millisecond at which a manual check is allowed again.
type PublicEgressCooldownError struct{ Until int64 }

func (e *PublicEgressCooldownError) Error() string {
	return "manual public egress check is cooling down until " + strconv.FormatInt(e.Until, 10)
}

const (
	// Two manual checks (switching the detection on counts as one) are at least a day apart,
	// measured from the creation of the previous manual job whatever its outcome.
	PublicEgressManualCooldown = 24 * time.Hour
	PublicEgressJobTimeout     = 20 * time.Second
	// A reconnect brings a check forward at most this often (see EnsurePublicEgressJob).
	PublicEgressReconnectSpacing = 6 * time.Hour
	publicEgressJobLife          = 10 * time.Minute
)

// After a failed check the next attempts come after 1, 5, 15 and 30 minutes; the attempt after
// that falls back to the seven-day cycle.
var publicEgressRetrySteps = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}

func migratePublicEgressControlTx(ctx context.Context, tx *sql.Tx) error {
	// No rows are created for existing Agents: a missing row means "off", so every machine that
	// upgrades from v1.0.3 starts with the detection disabled.
	for _, q := range []string{
		`CREATE TABLE agent_public_egress_control(
			agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
			enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN (0,1)),
			supported INTEGER NOT NULL DEFAULT 0 CHECK(supported IN (0,1)),
			last_manual_at INTEGER NOT NULL DEFAULT 0 CHECK(last_manual_at>=0),
			next_check_at INTEGER NOT NULL DEFAULT 0 CHECK(next_check_at>=0),
			failure_step INTEGER NOT NULL DEFAULT 0 CHECK(failure_step BETWEEN 0 AND 4),
			session_epoch INTEGER NOT NULL DEFAULT 0 CHECK(session_epoch>=0),
			session_id TEXT NOT NULL DEFAULT '',
			last_job_id TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE UNIQUE INDEX idx_probe_jobs_one_pending_public_egress ON probe_jobs(agent_id)
			WHERE probe_type='public_egress' AND status IN ('queued','leased')`,
		`INSERT INTO schema_migrations(version,applied_at) VALUES(30,unixepoch())`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// PublicEgressControl is the per-Agent switch and schedule state shown on the detail page.
type PublicEgressControl struct {
	Enabled       bool
	Supported     bool
	Pending       bool
	LastManualAt  int64
	NextCheckAt   int64
	CooldownUntil int64
	FailureStep   int
}

type publicEgressRow struct {
	enabled, supported                      bool
	lastManualAt, nextCheckAt, sessionEpoch int64
	failureStep                             int
	sessionID, lastJobID                    string
	exists                                  bool
}

func readPublicEgressRowTx(ctx context.Context, tx *sql.Tx, agentID string) (publicEgressRow, error) {
	var row publicEgressRow
	var enabled, supported int
	err := tx.QueryRowContext(ctx, `SELECT enabled,supported,last_manual_at,next_check_at,failure_step,session_epoch,session_id,last_job_id
		FROM agent_public_egress_control WHERE agent_id=?`, agentID).Scan(&enabled, &supported, &row.lastManualAt, &row.nextCheckAt, &row.failureStep, &row.sessionEpoch, &row.sessionID, &row.lastJobID)
	if errors.Is(err, sql.ErrNoRows) {
		return row, nil
	}
	if err != nil {
		return row, err
	}
	row.enabled, row.supported, row.exists = enabled == 1, supported == 1, true
	return row, nil
}

func (s *Store) GetPublicEgressControl(ctx context.Context, agentID string, now time.Time) (PublicEgressControl, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PublicEgressControl{}, err
	}
	defer tx.Rollback()
	row, err := readPublicEgressRowTx(ctx, tx, agentID)
	if err != nil {
		return PublicEgressControl{}, err
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM probe_jobs WHERE agent_id=? AND probe_type='public_egress' AND status IN ('queued','leased') AND expires_at>?)`, agentID, now.UnixMilli()).Scan(&pending); err != nil {
		return PublicEgressControl{}, err
	}
	view := PublicEgressControl{Enabled: row.enabled, Supported: row.supported, Pending: pending == 1, LastManualAt: row.lastManualAt, NextCheckAt: row.nextCheckAt, FailureStep: row.failureStep}
	if row.lastManualAt > 0 {
		view.CooldownUntil = row.lastManualAt + PublicEgressManualCooldown.Milliseconds()
	}
	return view, tx.Commit()
}

// NegotiatePublicEgressCapability fences the write against a concurrent new report/session in the
// same way as Google Status.
func (s *Store) NegotiatePublicEgressCapability(ctx context.Context, agentID string, supported bool, epoch uint64, sessionID string, now time.Time) (bool, error) {
	if epoch > math.MaxInt64 || now.UnixMilli() <= 0 {
		return false, ErrUnauthorized
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO agent_public_egress_control(agent_id,supported,updated_at)
		SELECT a.id,?,? FROM agents a JOIN agent_state s ON s.agent_id=a.id
		WHERE a.id=? AND a.revoked=0 AND a.disabled_at IS NULL AND s.epoch=? AND s.session_id=?
		ON CONFLICT(agent_id) DO UPDATE SET supported=excluded.supported,updated_at=excluded.updated_at`,
		boolInt(supported), now.UnixMilli(), agentID, int64(epoch), sessionID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) EgressTelemetryEnabled(ctx context.Context, agentID string) (bool, error) {
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT enabled FROM agent_public_egress_control WHERE agent_id=?`, agentID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return enabled == 1, err
}

// insertPublicEgressJobTx creates the job (no parameters) unless the queue is full.
func insertPublicEgressJobTx(ctx context.Context, tx *sql.Tx, agentID string, nowMS int64) (string, error) {
	var outstanding int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM probe_jobs WHERE agent_id=? AND status IN ('queued','leased')`, agentID).Scan(&outstanding); err != nil {
		return "", err
	}
	if outstanding >= MaxOutstandingJobsPerAgent {
		return "", ErrOutstandingJobsFull
	}
	jobID, err := googleStatusJobID()
	if err != nil {
		return "", err
	}
	expires := nowMS + publicEgressJobLife.Milliseconds()
	if _, err := tx.ExecContext(ctx, `INSERT INTO probe_jobs(id,origin,schedule_id,agent_id,probe_type,config_json,timeout_ms,created_at,scheduled_for,not_before,expires_at,status,attempt)
		VALUES(?,'manual',NULL,?,'public_egress','{}',?,?,?,?,?,'queued',0)`, jobID, agentID, int(PublicEgressJobTimeout/time.Millisecond), nowMS, nowMS, nowMS, expires); err != nil {
		return "", err
	}
	return jobID, nil
}

func publicEgressPendingTx(ctx context.Context, tx *sql.Tx, agentID string) (bool, error) {
	var pending int
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM probe_jobs WHERE agent_id=? AND probe_type='public_egress' AND status IN ('queued','leased'))`, agentID).Scan(&pending)
	return pending == 1, err
}

func agentOnlineTx(ctx context.Context, tx *sql.Tx, agentID string, nowMS int64, offlineTimeout time.Duration) (revoked, disabled, online bool, err error) {
	var r int
	var disabledAt, lastSeen sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT a.revoked,a.disabled_at,s.last_seen FROM agents a LEFT JOIN agent_state s ON s.agent_id=a.id WHERE a.id=?`, agentID).Scan(&r, &disabledAt, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, false, ErrAgentNotFound
	}
	if err != nil {
		return false, false, false, err
	}
	return r != 0, disabledAt.Valid, offlineTimeout > 0 && lastSeen.Valid && lastSeen.Int64 >= nowMS-offlineTimeout.Milliseconds(), nil
}

// SetPublicEgressEnabled switches the detection. Turning it on makes one immediate check that counts
// as a manual check for the cooldown; inside the cooldown, or when the Agent cannot run it now, no
// immediate check is made. Turning it off cancels a queued job.
func (s *Store) SetPublicEgressEnabled(ctx context.Context, agentID string, enabled bool, now time.Time, offlineTimeout time.Duration) (created bool, err error) {
	nowMS := now.UnixMilli()
	if !validStorageID(agentID, 128) || nowMS <= 0 {
		return false, ErrAgentNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	revoked, disabled, online, err := agentOnlineTx(ctx, tx, agentID, nowMS, offlineTimeout)
	if err != nil {
		return false, err
	}
	if revoked {
		return false, ErrAgentRevoked
	}
	row, err := readPublicEgressRowTx(ctx, tx, agentID)
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_public_egress_control(agent_id,enabled,updated_at) VALUES(?,?,?)
		ON CONFLICT(agent_id) DO UPDATE SET enabled=excluded.enabled,updated_at=excluded.updated_at`, agentID, boolInt(enabled), nowMS); err != nil {
		return false, err
	}
	if !enabled {
		if _, err := tx.ExecContext(ctx, `UPDATE probe_jobs SET status='expired' WHERE agent_id=? AND probe_type='public_egress' AND status='queued'`, agentID); err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	if row.enabled {
		return false, tx.Commit()
	}
	if err := cleanupExpiredJobsTx(ctx, tx, agentID, nowMS); err != nil {
		return false, err
	}
	pending, err := publicEgressPendingTx(ctx, tx, agentID)
	if err != nil {
		return false, err
	}
	var epoch int64
	var sessionID string
	if err := tx.QueryRowContext(ctx, `SELECT epoch,session_id FROM agent_state WHERE agent_id=?`, agentID).Scan(&epoch, &sessionID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	cooling := row.lastManualAt > 0 && row.lastManualAt+PublicEgressManualCooldown.Milliseconds() > nowMS
	if row.supported && online && !disabled && !pending && !cooling {
		jobID, err := insertPublicEgressJobTx(ctx, tx, agentID, nowMS)
		if err != nil && !errors.Is(err, ErrOutstandingJobsFull) {
			return false, err
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE agent_public_egress_control SET last_manual_at=?,last_job_id=?,next_check_at=0,session_epoch=?,session_id=? WHERE agent_id=?`, nowMS, jobID, epoch, sessionID, agentID)
			if err != nil {
				return false, err
			}
			return true, tx.Commit()
		}
	}
	// Nothing was dispatched: keep any existing schedule, never earlier than the end of the cooldown.
	next := row.nextCheckAt
	if next < nowMS {
		next = nowMS
		if cooling {
			next = row.lastManualAt + PublicEgressManualCooldown.Milliseconds()
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_public_egress_control SET next_check_at=? WHERE agent_id=?`, next, agentID); err != nil {
		return false, err
	}
	return false, tx.Commit()
}

// CreateManualPublicEgressJob is the "check now" button. The cooldown record is stored in the
// database, so it survives a Server restart.
func (s *Store) CreateManualPublicEgressJob(ctx context.Context, agentID string, now time.Time, offlineTimeout time.Duration) error {
	nowMS := now.UnixMilli()
	if !validStorageID(agentID, 128) || nowMS <= 0 {
		return ErrAgentNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	revoked, disabled, online, err := agentOnlineTx(ctx, tx, agentID, nowMS, offlineTimeout)
	if err != nil {
		return err
	}
	if revoked {
		return ErrAgentRevoked
	}
	if disabled {
		return ErrAgentDisabled
	}
	row, err := readPublicEgressRowTx(ctx, tx, agentID)
	if err != nil {
		return err
	}
	if !row.enabled {
		return ErrPublicEgressDisabled
	}
	if !row.supported {
		return ErrPublicEgressUnsupported
	}
	if !online {
		return ErrAgentOffline
	}
	if err := cleanupExpiredJobsTx(ctx, tx, agentID, nowMS); err != nil {
		return err
	}
	if pending, err := publicEgressPendingTx(ctx, tx, agentID); err != nil {
		return err
	} else if pending {
		return ErrPublicEgressPending
	}
	if until := row.lastManualAt + PublicEgressManualCooldown.Milliseconds(); row.lastManualAt > 0 && until > nowMS {
		return &PublicEgressCooldownError{Until: until}
	}
	jobID, err := insertPublicEgressJobTx(ctx, tx, agentID, nowMS)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_public_egress_control SET last_manual_at=?,last_job_id=?,next_check_at=0,updated_at=? WHERE agent_id=?`, nowMS, jobID, nowMS, agentID); err != nil {
		return err
	}
	return tx.Commit()
}

// EnsurePublicEgressJob is the scheduler. It runs from the ordinary claim endpoint, like Google
// Status. A check is due when the cycle (seven days, or a failure backoff step) ended or the Agent
// started a new session. Automatic checks ignore the manual cooldown but never overlap.
func (s *Store) EnsurePublicEgressJob(ctx context.Context, agentID string, epoch uint64, sessionID string, now time.Time) (bool, error) {
	nowMS := now.UnixMilli()
	if !validStorageID(agentID, 128) || nowMS <= 0 || epoch > math.MaxInt64 {
		return false, ErrAgentNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var revoked int
	var disabledAt sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT revoked,disabled_at FROM agents WHERE id=?`, agentID).Scan(&revoked, &disabledAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrAgentNotFound
		}
		return false, err
	}
	if revoked != 0 || disabledAt.Valid {
		return false, nil
	}
	row, err := readPublicEgressRowTx(ctx, tx, agentID)
	if err != nil {
		return false, err
	}
	if !row.enabled || !row.supported {
		return false, nil
	}
	if err := cleanupExpiredJobsTx(ctx, tx, agentID, nowMS); err != nil {
		return false, err
	}
	// A job that expired without a result is a failed attempt.
	if row.lastJobID != "" {
		var status string
		err := tx.QueryRowContext(ctx, `SELECT status FROM probe_jobs WHERE id=?`, row.lastJobID).Scan(&status)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		if errors.Is(err, sql.ErrNoRows) || status == "expired" {
			row.nextCheckAt, row.failureStep = nextAfterFailure(row.failureStep, nowMS)
			if _, err := tx.ExecContext(ctx, `UPDATE agent_public_egress_control SET last_job_id='',next_check_at=?,failure_step=? WHERE agent_id=?`, row.nextCheckAt, row.failureStep, agentID); err != nil {
				return false, err
			}
			row.lastJobID = ""
		}
	}
	pending, err := publicEgressPendingTx(ctx, tx, agentID)
	if err != nil {
		return false, err
	}
	newSession := row.sessionEpoch != int64(epoch) || row.sessionID != sessionID
	if pending {
		if newSession {
			if _, err := tx.ExecContext(ctx, `UPDATE agent_public_egress_control SET session_epoch=?,session_id=? WHERE agent_id=?`, int64(epoch), sessionID, agentID); err != nil {
				return false, err
			}
			return false, tx.Commit()
		}
		return false, tx.Commit()
	}
	if row.nextCheckAt > nowMS {
		// Not due yet. Only a reconnect can bring a check forward, and a crash-looping Agent must not turn
		// that into a stream of third-party requests: at most one reconnect-triggered check per spacing,
		// counted from the latest check of any kind (periodic, retry, manual or reconnect).
		if !newSession {
			return false, tx.Commit()
		}
		var lastCreated int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(created_at),0) FROM probe_jobs WHERE agent_id=? AND probe_type='public_egress'`, agentID).Scan(&lastCreated); err != nil {
			return false, err
		}
		if nowMS-lastCreated < PublicEgressReconnectSpacing.Milliseconds() {
			if _, err := tx.ExecContext(ctx, `UPDATE agent_public_egress_control SET session_epoch=?,session_id=? WHERE agent_id=?`, int64(epoch), sessionID, agentID); err != nil {
				return false, err
			}
			return false, tx.Commit()
		}
	}
	jobID, err := insertPublicEgressJobTx(ctx, tx, agentID, nowMS)
	if err != nil {
		if errors.Is(err, ErrOutstandingJobsFull) {
			return false, tx.Commit()
		}
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_public_egress_control SET session_epoch=?,session_id=?,last_job_id=?,next_check_at=0,updated_at=? WHERE agent_id=?`, int64(epoch), sessionID, jobID, nowMS, agentID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func nextAfterFailure(step int, nowMS int64) (next int64, newStep int) {
	if step < len(publicEgressRetrySteps) {
		return nowMS + publicEgressRetrySteps[step].Milliseconds(), step + 1
	}
	return nowMS + protocol.EgressJobInterval.Milliseconds(), 0
}

// savePublicEgressResultTx stores a finished check. The Server stamps the time and the validity
// itself. A result that arrives after the switch was turned off is dropped.
func savePublicEgressResultTx(ctx context.Context, tx *sql.Tx, agentID string, result protocol.PublicEgressResult, receivedAt int64) error {
	row, err := readPublicEgressRowTx(ctx, tx, agentID)
	if err != nil || !row.enabled {
		if err == nil && row.exists {
			_, err = tx.ExecContext(ctx, `UPDATE agent_public_egress_control SET last_job_id='' WHERE agent_id=?`, agentID)
		}
		return err
	}
	previous, err := readStoredEgressTx(ctx, tx, agentID)
	if err != nil {
		return err
	}
	stamp := func(o protocol.EgressObservation, old protocol.EgressObservation) protocol.EgressObservation {
		o.CheckedAt = receivedAt
		o.LastSuccess = old.LastSuccess
		ttl := protocol.EgressFailureTTL
		if o.Status == "available" {
			ttl = protocol.EgressJobSuccessTTL
			o.LastSuccess = &protocol.EgressSuccess{IP: o.IP, CheckedAt: receivedAt}
		}
		o.ValidUntil = receivedAt + ttl.Milliseconds()
		return o
	}
	state := EgressState{Version: 1, Supported: true, Source: "job", Epoch: previous.Epoch, Sequence: previous.Sequence,
		IPv4: stamp(result.IPv4, previous.IPv4), IPv6: stamp(result.IPv6, previous.IPv6)}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_public_egress(agent_id,payload) VALUES(?,?) ON CONFLICT(agent_id) DO UPDATE SET payload=excluded.payload`, agentID, string(data)); err != nil {
		return err
	}
	next, step := receivedAt+protocol.EgressJobInterval.Milliseconds(), 0
	if !result.Succeeded() {
		next, step = nextAfterFailure(row.failureStep, receivedAt)
	}
	_, err = tx.ExecContext(ctx, `UPDATE agent_public_egress_control SET last_job_id='',next_check_at=?,failure_step=?,updated_at=? WHERE agent_id=?`, next, step, receivedAt, agentID)
	if err != nil {
		return fmt.Errorf("record public egress schedule: %w", err)
	}
	return nil
}
