package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"404-probe/internal/protocol"
)

const MaxCollectionPageLimit = 100

type CollectionPageKey struct {
	CreatedAt int64
	ID        string
}

type AgentQueryStatus string

const (
	AgentQueryStatusOnline  AgentQueryStatus = "online"
	AgentQueryStatusOffline AgentQueryStatus = "offline"
	AgentQueryStatusRevoked AgentQueryStatus = "revoked"
)

type AgentQuery struct {
	Status         AgentQueryStatus
	After          *CollectionPageKey
	Limit          int
	Now            time.Time
	OfflineTimeout time.Duration
}

type AgentSnapshot struct {
	Agent  Agent
	State  *State
	Online bool
}

type ProbeScheduleQuery struct {
	AgentID   string
	Enabled   *bool
	ProbeType protocol.ProbeType
	After     *CollectionPageKey
	Limit     int
}

type ProbeResultSummaryRecord struct {
	ReceivedAt    int64
	FinishedAt    int64
	DurationMS    float64
	Success       bool
	ErrorCategory string
}

type ProbeJobListRecord struct {
	Job           ProbeJobRecord
	ResultSummary *ProbeResultSummaryRecord
}

type ProbeJobQuery struct {
	AgentID        string
	ScheduleID     string
	ProbeType      protocol.ProbeType
	Status         JobStatus
	Success        *bool
	CreatedAfter   int64
	CreatedBefore  int64
	FinishedAfter  int64
	FinishedBefore int64
	After          *CollectionPageKey
	Limit          int
}

func (s *Store) QueryAgents(ctx context.Context, query AgentQuery) ([]AgentSnapshot, *CollectionPageKey, error) {
	nowMillis, offlineMillis, err := validateAgentQuery(query)
	if err != nil {
		return nil, nil, err
	}
	clauses := make([]string, 0, 3)
	arguments := make([]any, 0, 6)
	joinState := false
	switch query.Status {
	case "":
	case AgentQueryStatusOnline:
		joinState = true
		clauses = append(clauses, `a.revoked=0`, `s.last_seen>0`, `s.last_seen>=?`)
		arguments = append(arguments, nowMillis-offlineMillis)
	case AgentQueryStatusOffline:
		joinState = true
		clauses = append(clauses, `a.revoked=0`, `(s.agent_id IS NULL OR s.last_seen<=0 OR s.last_seen<?)`)
		arguments = append(arguments, nowMillis-offlineMillis)
	case AgentQueryStatusRevoked:
		clauses = append(clauses, `a.revoked=1`)
	}
	if query.After != nil {
		clauses = append(clauses, `(a.created_at<? OR (a.created_at=? AND a.id<?))`)
		arguments = append(arguments, query.After.CreatedAt, query.After.CreatedAt, query.After.ID)
	}
	statement := `SELECT a.id,a.created_at FROM agents a`
	if joinState {
		statement += ` LEFT JOIN agent_state s ON s.agent_id=a.id`
	}
	statement += whereSQL(clauses) + ` ORDER BY a.created_at DESC,a.id DESC LIMIT ?`
	arguments = append(arguments, query.Limit+1)

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	candidates, err := queryCollectionCandidates(ctx, tx, statement, arguments...)
	if err != nil {
		return nil, nil, err
	}
	candidates, next := trimCollectionCandidates(candidates, query.Limit)
	records := make([]AgentSnapshot, 0, len(candidates))
	for _, candidate := range candidates {
		record, exists, err := readAgentSnapshotTx(ctx, tx, candidate.ID, nowMillis, offlineMillis)
		if err != nil {
			return nil, nil, err
		}
		if !exists || record.Agent.CreatedAt != candidate.CreatedAt {
			return nil, nil, fmt.Errorf("%w: listed agent changed inside snapshot", ErrCorruptProbeData)
		}
		records = append(records, record)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return records, next, nil
}

func (s *Store) GetAgentSnapshot(ctx context.Context, agentID string, now time.Time, offlineTimeout time.Duration) (AgentSnapshot, error) {
	nowMillis, offlineMillis, err := validateAgentSnapshotTime(now, offlineTimeout)
	if err != nil {
		return AgentSnapshot{}, err
	}
	if !validStorageID(agentID, 128) {
		return AgentSnapshot{}, ErrAgentNotFound
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return AgentSnapshot{}, err
	}
	defer tx.Rollback()
	record, exists, err := readAgentSnapshotTx(ctx, tx, agentID, nowMillis, offlineMillis)
	if err != nil {
		return AgentSnapshot{}, err
	}
	if !exists {
		return AgentSnapshot{}, ErrAgentNotFound
	}
	if err := tx.Commit(); err != nil {
		return AgentSnapshot{}, err
	}
	return record, nil
}

func (s *Store) QueryProbeSchedules(ctx context.Context, query ProbeScheduleQuery) ([]ProbeScheduleRecord, *CollectionPageKey, error) {
	if err := validateProbeScheduleQuery(query); err != nil {
		return nil, nil, err
	}
	clauses := make([]string, 0, 4)
	arguments := make([]any, 0, 7)
	if query.AgentID != "" {
		clauses = append(clauses, `agent_id=?`)
		arguments = append(arguments, query.AgentID)
	}
	if query.Enabled != nil {
		clauses = append(clauses, `enabled=?`)
		arguments = append(arguments, boolInt(*query.Enabled))
	}
	if query.ProbeType != "" {
		clauses = append(clauses, `probe_type=?`)
		arguments = append(arguments, string(query.ProbeType))
	}
	if query.After != nil {
		clauses = append(clauses, `(created_at<? OR (created_at=? AND id<?))`)
		arguments = append(arguments, query.After.CreatedAt, query.After.CreatedAt, query.After.ID)
	}
	statement := `SELECT id,created_at FROM probe_schedules` + whereSQL(clauses) +
		` ORDER BY created_at DESC,id DESC LIMIT ?`
	arguments = append(arguments, query.Limit+1)

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	candidates, err := queryCollectionCandidates(ctx, tx, statement, arguments...)
	if err != nil {
		return nil, nil, err
	}
	candidates, next := trimCollectionCandidates(candidates, query.Limit)
	records := make([]ProbeScheduleRecord, 0, len(candidates))
	for _, candidate := range candidates {
		record, exists, err := readScheduleTx(ctx, tx, candidate.ID)
		if err != nil {
			return nil, nil, err
		}
		if !exists || record.CreatedAt != candidate.CreatedAt {
			return nil, nil, fmt.Errorf("%w: listed schedule changed inside snapshot", ErrCorruptProbeData)
		}
		records = append(records, record)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return records, next, nil
}

func (s *Store) QueryProbeJobs(ctx context.Context, query ProbeJobQuery, now time.Time) ([]ProbeJobListRecord, *CollectionPageKey, error) {
	nowMillis := now.UnixMilli()
	if err := validateProbeJobQuery(query, nowMillis); err != nil {
		return nil, nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	if query.AgentID != "" {
		err = cleanupExpiredJobsTx(ctx, tx, query.AgentID, nowMillis)
	} else {
		err = cleanupAllExpiredJobsTx(ctx, tx, nowMillis)
	}
	if err != nil {
		return nil, nil, err
	}

	clauses := make([]string, 0, 10)
	arguments := make([]any, 0, 16)
	if query.AgentID != "" {
		clauses = append(clauses, `j.agent_id=?`)
		arguments = append(arguments, query.AgentID)
	}
	if query.ScheduleID != "" {
		clauses = append(clauses, `j.schedule_id=?`)
		arguments = append(arguments, query.ScheduleID)
	}
	if query.ProbeType != "" {
		clauses = append(clauses, `j.probe_type=?`)
		arguments = append(arguments, string(query.ProbeType))
	}
	terminalFilter := query.Success != nil || query.FinishedAfter != 0 || query.FinishedBefore != 0
	if query.Status != "" {
		clauses = append(clauses, `j.status=?`)
		arguments = append(arguments, string(query.Status))
	} else if terminalFilter {
		clauses = append(clauses, `j.status='finished'`)
	}
	if query.Success != nil {
		clauses = append(clauses, `EXISTS(SELECT 1 FROM probe_results result_filter WHERE result_filter.job_id=j.id AND result_filter.success=?)`)
		arguments = append(arguments, boolInt(*query.Success))
	}
	if query.CreatedAfter != 0 {
		clauses = append(clauses, `j.created_at>?`)
		arguments = append(arguments, query.CreatedAfter)
	}
	if query.CreatedBefore != 0 {
		clauses = append(clauses, `j.created_at<?`)
		arguments = append(arguments, query.CreatedBefore)
	}
	if query.FinishedAfter != 0 {
		clauses = append(clauses, `j.finished_at>?`)
		arguments = append(arguments, query.FinishedAfter)
	}
	if query.FinishedBefore != 0 {
		clauses = append(clauses, `j.finished_at<?`)
		arguments = append(arguments, query.FinishedBefore)
	}
	if query.After != nil {
		clauses = append(clauses, `(j.created_at<? OR (j.created_at=? AND j.id<?))`)
		arguments = append(arguments, query.After.CreatedAt, query.After.CreatedAt, query.After.ID)
	}
	statement := `SELECT j.id,j.created_at FROM probe_jobs j` + whereSQL(clauses) +
		` ORDER BY j.created_at DESC,j.id DESC LIMIT ?`
	arguments = append(arguments, query.Limit+1)
	candidates, err := queryCollectionCandidates(ctx, tx, statement, arguments...)
	if err != nil {
		return nil, nil, err
	}
	candidates, next := trimCollectionCandidates(candidates, query.Limit)
	records := make([]ProbeJobListRecord, 0, len(candidates))
	for _, candidate := range candidates {
		job, exists, err := readJobTx(ctx, tx, candidate.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrCorruptProbeData, err)
		}
		if !exists || job.CreatedAt != candidate.CreatedAt {
			return nil, nil, fmt.Errorf("%w: listed job changed inside snapshot", ErrCorruptProbeData)
		}
		result, err := readValidatedListedJobTx(ctx, tx, job)
		if err != nil {
			return nil, nil, err
		}
		record := ProbeJobListRecord{Job: job}
		if result != nil {
			record.ResultSummary = &ProbeResultSummaryRecord{
				ReceivedAt: result.ReceivedAt, FinishedAt: result.Result.FinishedAt,
				DurationMS: result.Result.DurationMS, Success: result.Result.Success,
				ErrorCategory: result.Result.ErrorCategory,
			}
		}
		records = append(records, record)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return records, next, nil
}

func validateAgentQuery(query AgentQuery) (int64, int64, error) {
	switch query.Status {
	case "", AgentQueryStatusOnline, AgentQueryStatusOffline, AgentQueryStatusRevoked:
	default:
		return 0, 0, errors.New("agent status filter is invalid")
	}
	if err := validateCollectionQuery(query.Limit, query.After); err != nil {
		return 0, 0, err
	}
	return validateAgentSnapshotTime(query.Now, query.OfflineTimeout)
}

func validateAgentSnapshotTime(now time.Time, offlineTimeout time.Duration) (int64, int64, error) {
	nowMillis := now.UnixMilli()
	offlineMillis := offlineTimeout.Milliseconds()
	if nowMillis <= 0 || offlineTimeout <= 0 || offlineMillis <= 0 {
		return 0, 0, errors.New("agent snapshot time and offline timeout are invalid")
	}
	return nowMillis, offlineMillis, nil
}

func validateProbeScheduleQuery(query ProbeScheduleQuery) error {
	if err := validateCollectionQuery(query.Limit, query.After); err != nil {
		return err
	}
	if query.AgentID != "" && !validStorageID(query.AgentID, 128) {
		return errors.New("agent ID filter is invalid")
	}
	if query.ProbeType != "" {
		if err := query.ProbeType.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func validateProbeJobQuery(query ProbeJobQuery, nowMillis int64) error {
	if nowMillis <= 0 {
		return errors.New("job snapshot timestamp is invalid")
	}
	if err := validateCollectionQuery(query.Limit, query.After); err != nil {
		return err
	}
	if query.AgentID != "" && !validStorageID(query.AgentID, 128) {
		return errors.New("agent ID filter is invalid")
	}
	if query.ScheduleID != "" && !validStorageID(query.ScheduleID, 128) {
		return errors.New("schedule ID filter is invalid")
	}
	if query.ProbeType != "" {
		if err := query.ProbeType.Validate(); err != nil {
			return err
		}
	}
	if query.Status != "" {
		switch query.Status {
		case JobStatusQueued, JobStatusLeased, JobStatusFinished, JobStatusExpired:
		default:
			return errors.New("job status filter is invalid")
		}
	}
	if (query.Success != nil || query.FinishedAfter != 0 || query.FinishedBefore != 0) &&
		query.Status != "" && query.Status != JobStatusFinished {
		return errors.New("result filters require finished job status")
	}
	for _, value := range []int64{query.CreatedAfter, query.CreatedBefore, query.FinishedAfter, query.FinishedBefore} {
		if value < 0 {
			return errors.New("job time filters must not be negative")
		}
	}
	if query.CreatedAfter != 0 && query.CreatedBefore != 0 && query.CreatedAfter >= query.CreatedBefore {
		return errors.New("created time range is invalid")
	}
	if query.FinishedAfter != 0 && query.FinishedBefore != 0 && query.FinishedAfter >= query.FinishedBefore {
		return errors.New("finished time range is invalid")
	}
	return nil
}

func validateCollectionQuery(limit int, after *CollectionPageKey) error {
	if limit <= 0 || limit > MaxCollectionPageLimit {
		return fmt.Errorf("limit must be between 1 and %d", MaxCollectionPageLimit)
	}
	if after != nil && (after.CreatedAt <= 0 || !validStorageID(after.ID, 128)) {
		return errors.New("collection page key is invalid")
	}
	return nil
}

func readAgentSnapshotTx(ctx context.Context, tx *sql.Tx, agentID string, nowMillis, offlineMillis int64) (AgentSnapshot, bool, error) {
	var record AgentSnapshot
	var revoked int
	err := tx.QueryRowContext(ctx, `SELECT id,name,revoked,created_at FROM agents WHERE id=?`, agentID).Scan(
		&record.Agent.ID, &record.Agent.Name, &revoked, &record.Agent.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentSnapshot{}, false, nil
	}
	if err != nil {
		return AgentSnapshot{}, false, err
	}
	if !validStorageID(record.Agent.ID, 128) || strings.TrimSpace(record.Agent.Name) == "" || record.Agent.CreatedAt <= 0 ||
		(revoked != 0 && revoked != 1) {
		return AgentSnapshot{}, false, fmt.Errorf("%w: stored agent is invalid", ErrCorruptProbeData)
	}
	record.Agent.Revoked = revoked == 1
	state, exists, err := readStateTx(ctx, tx, record.Agent.ID, record.Agent.Name)
	if err != nil {
		return AgentSnapshot{}, false, err
	}
	if exists {
		record.State = &state
		record.Online = !record.Agent.Revoked && state.LastSeen > 0 && state.LastSeen >= nowMillis-offlineMillis
	}
	return record, true, nil
}

func cleanupAllExpiredJobsTx(ctx context.Context, tx *sql.Tx, nowMillis int64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE probe_jobs SET
		status=CASE WHEN expires_at>? THEN 'queued' ELSE 'expired' END,
		lease_token=NULL,lease_epoch=NULL,lease_session_id=NULL,leased_at=NULL,lease_until=NULL
		WHERE status='leased' AND lease_until<=?`, nowMillis, nowMillis); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE probe_jobs SET status='expired'
		WHERE status='queued' AND expires_at<=?`, nowMillis); err != nil {
		return err
	}
	return nil
}

type collectionCandidate struct {
	ID        string
	CreatedAt int64
}

func queryCollectionCandidates(ctx context.Context, tx *sql.Tx, statement string, arguments ...any) ([]collectionCandidate, error) {
	rows, err := tx.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := make([]collectionCandidate, 0)
	for rows.Next() {
		var candidate collectionCandidate
		if err := rows.Scan(&candidate.ID, &candidate.CreatedAt); err != nil {
			return nil, err
		}
		if !validStorageID(candidate.ID, 128) || candidate.CreatedAt <= 0 {
			return nil, fmt.Errorf("%w: collection key is invalid", ErrCorruptProbeData)
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func trimCollectionCandidates(candidates []collectionCandidate, limit int) ([]collectionCandidate, *CollectionPageKey) {
	if len(candidates) <= limit {
		return candidates, nil
	}
	candidates = candidates[:limit]
	last := candidates[len(candidates)-1]
	return candidates, &CollectionPageKey{CreatedAt: last.CreatedAt, ID: last.ID}
}

func whereSQL(clauses []string) string {
	if len(clauses) == 0 {
		return ""
	}
	return ` WHERE ` + strings.Join(clauses, ` AND `)
}
