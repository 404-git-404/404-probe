package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"time"

	"404-probe/internal/protocol"
)

var (
	ErrGoogleStatusUnsupported = errors.New("agent does not support google status")
	ErrGoogleStatusPending     = errors.New("google status check is already pending")
	ErrAgentOffline            = errors.New("agent is offline")
)

const (
	GoogleStatusInterval   = 24 * time.Hour
	GoogleStatusJobTimeout = 30 * time.Second
	googleStatusJobLife    = 10 * time.Minute
)

type GoogleStatusSnapshot struct {
	AgentID       string
	Result        protocol.GoogleStatusResult
	CheckedAt     int64
	UnknownStreak int
	NextDueAt     int64
}

func (s *Store) GoogleStatusCapability(ctx context.Context, agentID string) (bool, error) {
	var supported int
	err := s.db.QueryRowContext(ctx, `SELECT supported FROM agent_google_status_capabilities WHERE agent_id=?`, agentID).Scan(&supported)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return supported == 1, err
}

// NegotiateGoogleStatusCapability fences the write against a concurrent new
// report/session. The session check and capability write are one SQL statement.
func (s *Store) NegotiateGoogleStatusCapability(ctx context.Context, agentID string, supported bool, epoch uint64, sessionID string, now time.Time) (bool, error) {
	if epoch > math.MaxInt64 || now.UnixMilli() <= 0 {
		return false, ErrUnauthorized
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO agent_google_status_capabilities(agent_id,supported,updated_at)
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

func (s *Store) GoogleStatusPending(ctx context.Context, agentID string, now time.Time) (bool, error) {
	var pending int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM probe_jobs WHERE agent_id=? AND probe_type='google_status' AND status IN ('queued','leased') AND expires_at>?)`, agentID, now.UnixMilli()).Scan(&pending)
	return pending == 1, err
}

type googleStatusReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) GetGoogleStatus(ctx context.Context, agentID string) (GoogleStatusSnapshot, bool, error) {
	return readGoogleStatus(ctx, s.db, agentID)
}

func readGoogleStatus(ctx context.Context, db googleStatusReader, agentID string) (GoogleStatusSnapshot, bool, error) {
	var snapshot GoogleStatusSnapshot
	var youtubeRegion, youtubeError, searchError, signinError, geminiRegion, geminiError sql.NullString
	var sentToChina sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT agent_id,youtube_status,youtube_region,sent_to_china,youtube_error,
		search_status,search_error,signin_status,signin_error,gemini_status,gemini_region,gemini_error,
		checked_at,unknown_streak,next_due_at FROM agent_google_status WHERE agent_id=?`, agentID).Scan(
		&snapshot.AgentID, &snapshot.Result.YouTube.Status, &youtubeRegion, &sentToChina, &youtubeError,
		&snapshot.Result.Search.Status, &searchError, &snapshot.Result.SignIn.Status, &signinError,
		&snapshot.Result.Gemini.Status, &geminiRegion, &geminiError, &snapshot.CheckedAt, &snapshot.UnknownStreak, &snapshot.NextDueAt)
	if errors.Is(err, sql.ErrNoRows) {
		return GoogleStatusSnapshot{}, false, nil
	}
	if err != nil {
		return GoogleStatusSnapshot{}, false, err
	}
	snapshot.Result.YouTube.Region = youtubeRegion.String
	if sentToChina.Valid {
		value := sentToChina.Int64 == 1
		snapshot.Result.YouTube.SentToChina = &value
	}
	snapshot.Result.YouTube.Error.Category = youtubeError.String
	snapshot.Result.Search.Error.Category = searchError.String
	snapshot.Result.SignIn.Error.Category = signinError.String
	snapshot.Result.Gemini.Region = geminiRegion.String
	snapshot.Result.Gemini.Error.Category = geminiError.String
	if err := snapshot.Result.Validate(); err != nil || snapshot.CheckedAt <= 0 || snapshot.NextDueAt <= snapshot.CheckedAt || snapshot.UnknownStreak < 0 {
		return GoogleStatusSnapshot{}, false, ErrCorruptProbeData
	}
	return snapshot, true, nil
}

// EnsureGoogleStatusJob records capability and atomically creates the first or
// due low-frequency job. Calling it from the ordinary claim endpoint means no
// new lane or generic remote-operation surface is introduced.
func (s *Store) EnsureGoogleStatusJob(ctx context.Context, agentID string, now time.Time) (bool, error) {
	return s.createGoogleStatusJob(ctx, agentID, now, 0, true)
}

func (s *Store) CreateManualGoogleStatusJob(ctx context.Context, agentID string, now time.Time, offlineTimeout time.Duration) (bool, error) {
	return s.createGoogleStatusJob(ctx, agentID, now, offlineTimeout, false)
}

func (s *Store) createGoogleStatusJob(ctx context.Context, agentID string, now time.Time, offlineTimeout time.Duration, automatic bool) (bool, error) {
	nowMS := now.UnixMilli()
	if !validStorageID(agentID, 128) || nowMS <= 0 {
		return false, ErrAgentNotFound
	}
	jobID, err := googleStatusJobID()
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var revoked int
	var disabledAt sql.NullInt64
	var lastSeen sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT a.revoked,a.disabled_at,s.last_seen FROM agents a LEFT JOIN agent_state s ON s.agent_id=a.id WHERE a.id=?`, agentID).Scan(&revoked, &disabledAt, &lastSeen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrAgentNotFound
		}
		return false, err
	}
	if revoked != 0 {
		return false, ErrAgentRevoked
	}
	if disabledAt.Valid {
		return false, ErrAgentDisabled
	}
	var supported int
	if err := tx.QueryRowContext(ctx, `SELECT supported FROM agent_google_status_capabilities WHERE agent_id=?`, agentID).Scan(&supported); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrGoogleStatusUnsupported
		}
		return false, err
	}
	if supported != 1 {
		return false, ErrGoogleStatusUnsupported
	}
	if !automatic && (offlineTimeout <= 0 || !lastSeen.Valid || lastSeen.Int64 < nowMS-offlineTimeout.Milliseconds()) {
		return false, ErrAgentOffline
	}
	if err := cleanupExpiredJobsTx(ctx, tx, agentID, nowMS); err != nil {
		return false, err
	}
	if err := expireYouTubeObservationTx(ctx, tx, agentID, nowMS); err != nil {
		return false, err
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM probe_jobs WHERE agent_id=? AND probe_type='google_status' AND status IN ('queued','leased'))`, agentID).Scan(&pending); err != nil {
		return false, err
	}
	if pending != 0 {
		if automatic {
			return false, tx.Commit()
		}
		return false, ErrGoogleStatusPending
	}
	if automatic {
		var nextDue sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT next_due_at FROM agent_google_status WHERE agent_id=?`, agentID).Scan(&nextDue); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		if nextDue.Valid && nextDue.Int64 > nowMS {
			return false, tx.Commit()
		}
	}
	var outstanding int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM probe_jobs WHERE agent_id=? AND status IN ('queued','leased')`, agentID).Scan(&outstanding); err != nil {
		return false, err
	}
	if outstanding >= MaxOutstandingJobsPerAgent {
		return false, ErrOutstandingJobsFull
	}
	expires := nowMS + googleStatusJobLife.Milliseconds()
	if expires <= nowMS || expires > math.MaxInt64 {
		return false, errors.New("google status job timestamp overflow")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO probe_jobs(id,origin,schedule_id,agent_id,probe_type,config_json,timeout_ms,created_at,scheduled_for,not_before,expires_at,status,attempt)
		VALUES(?,'manual',NULL,?,'google_status','{}',?,?,?,?,?,'queued',0)`, jobID, agentID, int(GoogleStatusJobTimeout/time.Millisecond), nowMS, nowMS, nowMS, expires)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func saveGoogleStatusTx(ctx context.Context, tx *sql.Tx, agentID string, result protocol.GoogleStatusResult, checkedAt int64, diagnostic ...string) error {
	if err := result.Validate(); err != nil {
		return err
	}
	if len(diagnostic) > 0 && result.YouTube.Status == protocol.YouTubeUnknown {
		reason := strings.TrimPrefix(diagnostic[0], "youtube_")
		if strings.HasPrefix(diagnostic[0], "youtube_") && protocol.YouTubeDiagnosticCategory(reason) && protocol.CompatibleYouTubeCategory(reason) == result.YouTube.Error.Category {
			result.YouTube.Error.Category = reason
		}
	}
	var prior int
	var priorAt int64
	if err := tx.QueryRowContext(ctx, `SELECT unknown_streak,checked_at FROM agent_google_status WHERE agent_id=?`, agentID).Scan(&prior, &priorAt); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if checkedAt <= priorAt {
		return nil
	} // A late result must not resurrect a superseded region.
	streak := 0
	next := checkedAt + GoogleStatusInterval.Milliseconds()
	if result.HasRetainedUnknown() {
		streak = prior + 1
		retry := time.Hour
		if streak == 1 {
			retry = 10 * time.Minute
		} else if streak == 2 {
			retry = 30 * time.Minute
		}
		next = checkedAt + retry.Milliseconds()
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_google_status(agent_id,youtube_status,youtube_region,sent_to_china,youtube_error,
		search_status,search_error,signin_status,signin_error,gemini_status,gemini_region,gemini_error,checked_at,unknown_streak,next_due_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(agent_id) DO UPDATE SET
		youtube_status=excluded.youtube_status,youtube_region=excluded.youtube_region,sent_to_china=excluded.sent_to_china,youtube_error=excluded.youtube_error,
		search_status=excluded.search_status,search_error=excluded.search_error,signin_status=excluded.signin_status,signin_error=excluded.signin_error,
		gemini_status=excluded.gemini_status,gemini_region=excluded.gemini_region,gemini_error=excluded.gemini_error,
		checked_at=excluded.checked_at,unknown_streak=excluded.unknown_streak,next_due_at=excluded.next_due_at`,
		agentID, result.YouTube.Status, nullableText(result.YouTube.Region), nullableBool(result.YouTube.SentToChina), nullableText(result.YouTube.Error.Category),
		result.Search.Status, nullableText(result.Search.Error.Category), result.SignIn.Status, nullableText(result.SignIn.Error.Category),
		result.Gemini.Status, nullableText(result.Gemini.Region), nullableText(result.Gemini.Error.Category), checkedAt, streak, next)
	return err
}

func nullableBool(value *bool) any {
	if value == nil {
		return nil
	}
	return boolInt(*value)
}

func googleStatusJobID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

// Expose expiration even before an Agent's next claim performs queue cleanup.
// No old successful region survives a newer attempt that expired without a result.
func (s *Store) GetGoogleStatusAt(ctx context.Context, agentID string, now time.Time) (GoogleStatusSnapshot, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return GoogleStatusSnapshot{}, false, err
	}
	defer tx.Rollback()
	snapshot, exists, err := readGoogleStatus(ctx, tx, agentID)
	if err != nil {
		return snapshot, exists, err
	}
	var expired sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT MAX(expires_at) FROM probe_jobs WHERE agent_id=? AND probe_type='google_status' AND status IN ('queued','leased','expired') AND expires_at<=?`, agentID, now.UnixMilli()).Scan(&expired)
	if err != nil {
		return snapshot, exists, err
	}
	if expired.Valid && expired.Int64 > snapshot.CheckedAt {
		snapshot = expiredYouTubeSnapshot(agentID, snapshot, expired.Int64)
		exists = true
	}
	return snapshot, exists, tx.Commit()
}

func expiredYouTubeSnapshot(agentID string, prior GoogleStatusSnapshot, at int64) GoogleStatusSnapshot {
	streak := prior.UnknownStreak + 1
	retry := time.Hour
	if streak == 1 {
		retry = 10 * time.Minute
	} else if streak == 2 {
		retry = 30 * time.Minute
	}
	return GoogleStatusSnapshot{AgentID: agentID, CheckedAt: at, UnknownStreak: streak, NextDueAt: at + retry.Milliseconds(), Result: protocol.GoogleStatusResult{
		YouTube: protocol.YouTubeResult{Status: protocol.YouTubeUnknown, Error: protocol.GoogleServiceError{Category: "job_expired"}},
		Search:  protocol.GoogleSearchResult{Status: protocol.GoogleSearchUnknown}, SignIn: protocol.GoogleSignInResult{Status: protocol.GoogleSignInUnknown}, Gemini: protocol.GeminiResult{Status: protocol.GeminiUnknown}}}
}

func expireYouTubeObservationTx(ctx context.Context, tx *sql.Tx, agentID string, now int64) error {
	var expired sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(expires_at) FROM probe_jobs WHERE agent_id=? AND probe_type='google_status' AND status='expired' AND expires_at<=?`, agentID, now).Scan(&expired); err != nil {
		return err
	}
	if !expired.Valid {
		return nil
	}
	result := expiredYouTubeSnapshot(agentID, GoogleStatusSnapshot{}, expired.Int64).Result
	return saveGoogleStatusTx(ctx, tx, agentID, result, expired.Int64)
}
