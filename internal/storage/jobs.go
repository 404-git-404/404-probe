package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"404-probe/internal/protocol"
)

var (
	ErrJobNotFound      = errors.New("probe job not found")
	ErrLeaseLost        = errors.New("probe job lease lost")
	ErrJobExpired       = errors.New("probe job expired")
	ErrAttemptExhausted = errors.New("probe job attempt exhausted")
	ErrResultConflict   = errors.New("probe job result conflicts with stored result")
	ErrInvalidJobResult = errors.New("invalid probe job result")
	ErrCorruptProbeData = errors.New("stored probe data is corrupt")
)

type JobStatus string

const (
	JobStatusQueued   JobStatus = "queued"
	JobStatusLeased   JobStatus = "leased"
	JobStatusFinished JobStatus = "finished"
	JobStatusExpired  JobStatus = "expired"
)

type CreateOneShotJobParams struct {
	ID        string
	AgentID   string
	ProbeType protocol.ProbeType
	Config    protocol.ProbeConfig
	TimeoutMS int
	CreatedAt int64
	NotBefore int64
	ExpiresAt int64
}

type ProbeJobRecord struct {
	ID             string
	ScheduleID     string
	AgentID        string
	ProbeType      protocol.ProbeType
	Config         protocol.ProbeConfig
	TimeoutMS      int
	CreatedAt      int64
	ScheduledFor   int64
	NotBefore      int64
	ExpiresAt      int64
	Status         JobStatus
	Attempt        int64
	LeaseToken     string
	LeaseEpoch     uint64
	LeaseSessionID string
	LeasedAt       int64
	LeaseUntil     int64
	FinishedAt     int64
}

type ProbeResultRecord struct {
	JobID      string
	ReceivedAt int64
	Result     protocol.JobResult
	Hash       []byte
}

type SubmitResultAck struct {
	Duplicate bool
}

func (s *Store) CreateOneShotJob(ctx context.Context, params CreateOneShotJobParams) error {
	if !validStorageID(params.ID, 128) || !validStorageID(params.AgentID, 128) {
		return errors.New("job ID and agent ID are required and must be reasonably sized")
	}
	if err := params.ProbeType.Validate(); err != nil {
		return err
	}
	configJSON, err := protocol.MarshalProbeConfig(params.ProbeType, params.Config)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if params.TimeoutMS < protocol.MinProbeTimeoutMS || params.TimeoutMS > protocol.MaxProbeTimeoutMS {
		return fmt.Errorf("timeout_ms must be between %d and %d", protocol.MinProbeTimeoutMS, protocol.MaxProbeTimeoutMS)
	}
	if params.CreatedAt <= 0 || params.NotBefore < params.CreatedAt || params.ExpiresAt <= params.NotBefore {
		return errors.New("job timestamps are invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireActiveAgentTx(ctx, tx, params.AgentID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO probe_jobs(
		id,schedule_id,agent_id,probe_type,config_json,timeout_ms,created_at,scheduled_for,not_before,expires_at,status,attempt
	) VALUES(?,NULL,?,?,?,?,?,?,?,?, 'queued',0)`,
		params.ID, params.AgentID, string(params.ProbeType), string(configJSON), params.TimeoutMS,
		params.CreatedAt, params.CreatedAt, params.NotBefore, params.ExpiresAt)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ClaimJob leases at most one Job for an Agent. A retry from the same active
// epoch/session replays the existing lease, including its token and attempt.
func (s *Store) ClaimJob(ctx context.Context, agentID string, request protocol.ClaimRequest, now time.Time, leaseDuration time.Duration) (*protocol.Job, error) {
	if !validStorageID(agentID, 128) {
		return nil, ErrUnauthorized
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	leaseMillis := leaseDuration.Milliseconds()
	if leaseMillis <= 0 {
		return nil, errors.New("lease duration must be positive")
	}
	nowMillis := now.UnixMilli()
	if nowMillis <= 0 || nowMillis > math.MaxInt64-leaseMillis {
		return nil, errors.New("lease timestamp exceeds SQLite integer range")
	}
	leaseUntil := nowMillis + leaseMillis

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := requireActiveAgentTx(ctx, tx, agentID); err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `UPDATE probe_jobs SET
		status=CASE WHEN expires_at>? THEN 'queued' ELSE 'expired' END,
		lease_token=NULL,lease_epoch=NULL,lease_session_id=NULL,leased_at=NULL,lease_until=NULL
		WHERE agent_id=? AND status='leased' AND lease_until<=?`, nowMillis, agentID, nowMillis); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE probe_jobs SET status='expired'
		WHERE agent_id=? AND status='queued' AND expires_at<=?`, agentID, nowMillis); err != nil {
		return nil, err
	}

	existing, exists, err := readActiveLeaseTx(ctx, tx, agentID, nowMillis)
	if err != nil {
		return nil, err
	}
	if exists {
		if existing.LeaseEpoch == request.AgentEpoch && existing.LeaseSessionID == request.SessionID {
			job, err := existing.protocolJob()
			if err != nil {
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return &job, nil
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}

	placeholders := make([]string, len(request.SupportedProbeTypes))
	arguments := make([]any, 0, 3+len(request.SupportedProbeTypes))
	arguments = append(arguments, agentID, nowMillis, nowMillis)
	for i, probeType := range request.SupportedProbeTypes {
		placeholders[i] = "?"
		arguments = append(arguments, string(probeType))
	}
	query := `SELECT id,attempt FROM probe_jobs
		WHERE agent_id=? AND status='queued' AND not_before<=? AND expires_at>?
		AND probe_type IN (` + strings.Join(placeholders, ",") + `)
		ORDER BY not_before,created_at,id LIMIT 1`
	var jobID string
	var attempt int64
	if err := tx.QueryRowContext(ctx, query, arguments...).Scan(&jobID, &attempt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return nil, nil
		}
		return nil, err
	}
	if attempt == math.MaxInt64 {
		result, err := tx.ExecContext(ctx, `UPDATE probe_jobs SET
			status='expired',lease_token=NULL,lease_epoch=NULL,lease_session_id=NULL,leased_at=NULL,lease_until=NULL
			WHERE id=? AND status='queued' AND attempt=?`, jobID, attempt)
		if err != nil {
			return nil, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errors.New("attempt exhaustion lost a concurrent update")
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, ErrAttemptExhausted
	}
	leaseToken, err := newLeaseToken()
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE probe_jobs SET
		status='leased',attempt=attempt+1,lease_token=?,lease_epoch=?,lease_session_id=?,leased_at=?,lease_until=?
		WHERE id=? AND status='queued' AND attempt<?`, leaseToken, int64(request.AgentEpoch), request.SessionID, nowMillis, leaseUntil, jobID, int64(math.MaxInt64))
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errors.New("claim lost a concurrent update")
	}
	record, exists, err := readJobTx(ctx, tx, jobID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrJobNotFound
	}
	job, err := record.protocolJob()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &job, nil
}

func (s *Store) SubmitJobResult(ctx context.Context, agentID, jobID string, result protocol.JobResult, received time.Time) (SubmitResultAck, error) {
	if !validStorageID(agentID, 128) || !validStorageID(jobID, 128) {
		return SubmitResultAck{}, ErrUnauthorized
	}
	receivedAt := received.UnixMilli()
	if receivedAt <= 0 {
		return SubmitResultAck{}, errors.New("received timestamp is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SubmitResultAck{}, err
	}
	defer tx.Rollback()
	if err := requireActiveAgentTx(ctx, tx, agentID); err != nil {
		return SubmitResultAck{}, err
	}
	job, exists, err := readJobTx(ctx, tx, jobID)
	if err != nil {
		return SubmitResultAck{}, err
	}
	if !exists {
		return SubmitResultAck{}, ErrJobNotFound
	}
	if job.AgentID != agentID {
		return SubmitResultAck{}, ErrUnauthorized
	}
	if job.Status == JobStatusExpired {
		return SubmitResultAck{}, ErrJobExpired
	}
	if result.Attempt != job.Attempt || !sameSecret(result.LeaseToken, job.LeaseToken) {
		return SubmitResultAck{}, ErrLeaseLost
	}
	if err := result.Validate(job.ProbeType); err != nil {
		return SubmitResultAck{}, err
	}
	if err := validateResultAgainstConfig(job, result); err != nil {
		return SubmitResultAck{}, err
	}
	payload, hash, err := protocol.CanonicalResult(job.ProbeType, result)
	if err != nil {
		return SubmitResultAck{}, err
	}

	if job.Status == JobStatusFinished {
		var storedHash []byte
		if err := tx.QueryRowContext(ctx, `SELECT result_hash FROM probe_results WHERE job_id=?`, jobID).Scan(&storedHash); err != nil {
			return SubmitResultAck{}, err
		}
		if !bytes.Equal(storedHash, hash[:]) {
			return SubmitResultAck{}, ErrResultConflict
		}
		if err := tx.Commit(); err != nil {
			return SubmitResultAck{}, err
		}
		return SubmitResultAck{Duplicate: true}, nil
	}
	if job.Status != JobStatusLeased || job.LeaseUntil <= receivedAt {
		return SubmitResultAck{}, ErrLeaseLost
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO probe_results(
		job_id,attempt,received_at,agent_epoch,session_id,agent_started_at,agent_finished_at,duration_ms,
		success,resolved_ip,error_category,error_message,payload_json,result_hash
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		jobID, result.Attempt, receivedAt, int64(result.AgentEpoch), result.SessionID,
		result.StartedAt, result.FinishedAt, result.DurationMS, boolInt(result.Success), nullableText(result.ResolvedIP),
		nullableText(result.ErrorCategory), nullableText(result.ErrorMessage), string(payload), hash[:])
	if err != nil {
		return SubmitResultAck{}, err
	}
	update, err := tx.ExecContext(ctx, `UPDATE probe_jobs SET status='finished',finished_at=?
		WHERE id=? AND status='leased' AND attempt=? AND lease_token=?`, receivedAt, jobID, result.Attempt, result.LeaseToken)
	if err != nil {
		return SubmitResultAck{}, err
	}
	changed, err := update.RowsAffected()
	if err != nil {
		return SubmitResultAck{}, err
	}
	if changed != 1 {
		return SubmitResultAck{}, ErrLeaseLost
	}
	if err := tx.Commit(); err != nil {
		return SubmitResultAck{}, err
	}
	return SubmitResultAck{}, nil
}

func (s *Store) GetProbeJob(ctx context.Context, jobID string) (ProbeJobRecord, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ProbeJobRecord{}, err
	}
	defer tx.Rollback()
	record, exists, err := readJobTx(ctx, tx, jobID)
	if err != nil {
		return ProbeJobRecord{}, err
	}
	if !exists {
		return ProbeJobRecord{}, ErrJobNotFound
	}
	if err := tx.Commit(); err != nil {
		return ProbeJobRecord{}, err
	}
	return record, nil
}

func (s *Store) GetProbeResult(ctx context.Context, jobID string) (ProbeResultRecord, error) {
	var record ProbeResultRecord
	var attempt int64
	var epoch int64
	var session string
	var startedAt, finishedAt int64
	var duration float64
	var success int
	var resolvedIP, category, message sql.NullString
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT job_id,attempt,received_at,agent_epoch,session_id,
		agent_started_at,agent_finished_at,duration_ms,success,resolved_ip,error_category,error_message,payload_json,result_hash
		FROM probe_results WHERE job_id=?`, jobID).Scan(
		&record.JobID, &attempt, &record.ReceivedAt, &epoch, &session, &startedAt, &finishedAt, &duration,
		&success, &resolvedIP, &category, &message, &payload, &record.Hash)
	if errors.Is(err, sql.ErrNoRows) {
		return ProbeResultRecord{}, ErrJobNotFound
	}
	if err != nil {
		return ProbeResultRecord{}, err
	}
	if epoch < 0 {
		return ProbeResultRecord{}, fmt.Errorf("%w: probe_results.agent_epoch is negative", ErrCorruptProbeData)
	}
	job, err := s.GetProbeJob(ctx, jobID)
	if err != nil {
		return ProbeResultRecord{}, err
	}
	typedPayload, err := protocol.DecodeProbeResult(job.ProbeType, []byte(payload))
	if err != nil {
		return ProbeResultRecord{}, err
	}
	record.Result = protocol.JobResult{
		ProtocolVersion: protocol.JobProtocolVersion, LeaseToken: job.LeaseToken, Attempt: attempt,
		AgentEpoch: uint64(epoch), SessionID: session, StartedAt: startedAt, FinishedAt: finishedAt,
		DurationMS: duration, Success: success != 0, ResolvedIP: resolvedIP.String,
		ErrorCategory: category.String, ErrorMessage: message.String, Result: typedPayload,
	}
	return record, nil
}

func readActiveLeaseTx(ctx context.Context, tx *sql.Tx, agentID string, nowMillis int64) (ProbeJobRecord, bool, error) {
	var jobID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM probe_jobs
		WHERE agent_id=? AND status='leased' AND lease_until>? ORDER BY leased_at,id LIMIT 1`, agentID, nowMillis).Scan(&jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return ProbeJobRecord{}, false, nil
	}
	if err != nil {
		return ProbeJobRecord{}, false, err
	}
	return readJobTx(ctx, tx, jobID)
}

func readJobTx(ctx context.Context, tx *sql.Tx, jobID string) (ProbeJobRecord, bool, error) {
	var record ProbeJobRecord
	var scheduleID, leaseToken, leaseSession sql.NullString
	var leaseEpoch, leasedAt, leaseUntil, finishedAt sql.NullInt64
	var probeType, configJSON, status string
	err := tx.QueryRowContext(ctx, `SELECT id,schedule_id,agent_id,probe_type,config_json,timeout_ms,
		created_at,scheduled_for,not_before,expires_at,status,attempt,lease_token,lease_epoch,
		lease_session_id,leased_at,lease_until,finished_at FROM probe_jobs WHERE id=?`, jobID).Scan(
		&record.ID, &scheduleID, &record.AgentID, &probeType, &configJSON, &record.TimeoutMS,
		&record.CreatedAt, &record.ScheduledFor, &record.NotBefore, &record.ExpiresAt, &status, &record.Attempt,
		&leaseToken, &leaseEpoch, &leaseSession, &leasedAt, &leaseUntil, &finishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ProbeJobRecord{}, false, nil
	}
	if err != nil {
		return ProbeJobRecord{}, false, err
	}
	record.ScheduleID = scheduleID.String
	record.ProbeType = protocol.ProbeType(probeType)
	record.Status = JobStatus(status)
	record.LeaseToken = leaseToken.String
	if leaseEpoch.Valid {
		if leaseEpoch.Int64 < 0 {
			return ProbeJobRecord{}, false, errors.New("stored lease epoch is negative")
		}
		record.LeaseEpoch = uint64(leaseEpoch.Int64)
	}
	record.LeaseSessionID = leaseSession.String
	record.LeasedAt = leasedAt.Int64
	record.LeaseUntil = leaseUntil.Int64
	record.FinishedAt = finishedAt.Int64
	config, err := protocol.DecodeProbeConfig(record.ProbeType, []byte(configJSON))
	if err != nil {
		return ProbeJobRecord{}, false, fmt.Errorf("decode stored job config: %w", err)
	}
	record.Config = config
	return record, true, nil
}

func (r ProbeJobRecord) protocolJob() (protocol.Job, error) {
	job := protocol.Job{
		ProtocolVersion: protocol.JobProtocolVersion, JobID: r.ID, ProbeType: r.ProbeType, Config: r.Config,
		CreatedAt: r.CreatedAt, NotBefore: r.NotBefore, ExpiresAt: r.ExpiresAt, TimeoutMS: r.TimeoutMS,
		Attempt: r.Attempt, LeaseToken: r.LeaseToken, LeaseExpiresAt: r.LeaseUntil,
	}
	if err := job.Validate(); err != nil {
		return protocol.Job{}, fmt.Errorf("stored job is invalid: %w", err)
	}
	return job, nil
}

func requireActiveAgentTx(ctx context.Context, tx *sql.Tx, agentID string) error {
	var revoked int
	err := tx.QueryRowContext(ctx, `SELECT revoked FROM agents WHERE id=?`, agentID).Scan(&revoked)
	if errors.Is(err, sql.ErrNoRows) || revoked != 0 {
		return ErrUnauthorized
	}
	return err
}

func validateResultAgainstConfig(job ProbeJobRecord, result protocol.JobResult) error {
	if job.ProbeType == protocol.ProbeTypeICMPPing && result.Result.ICMPPing.Sent != job.Config.ICMPPing.Count {
		return fmt.Errorf("%w: ICMP sent count does not match job config", ErrInvalidJobResult)
	}
	return nil
}

func newLeaseToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func sameSecret(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func validStorageID(value string, max int) bool {
	return value != "" && len(value) <= max && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
