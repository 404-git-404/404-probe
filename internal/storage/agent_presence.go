package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type PresenceEvent struct {
	At   int64  `json:"at"`
	Kind string `json:"kind"`
}

type PresenceHistory struct {
	Start         int64           `json:"start"`
	End           int64           `json:"end"`
	InitialState  string          `json:"initial_state"`
	OnlineMS      int64           `json:"online_ms"`
	OfflineMS     int64           `json:"offline_ms"`
	PausedMS      int64           `json:"paused_ms"`
	UnknownMS     int64           `json:"unknown_ms"`
	OnlinePercent *float64        `json:"online_percent"`
	Events        []PresenceEvent `json:"events"`
}

func recordPresenceReportTx(ctx context.Context, tx *sql.Tx, id string, received time.Time, timeout time.Duration) error {
	at := received.UnixMilli()
	var deadline int64
	err := tx.QueryRowContext(ctx, `SELECT offline_at FROM agent_presence WHERE agent_id=?`, id).Scan(&deadline)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if deadline > 0 && deadline < at {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_presence_events(agent_id,at,kind) VALUES(?,?,'offline')`, id, deadline); err != nil {
			return err
		}
	}
	if deadline < at {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_presence_events(agent_id,at,kind) VALUES(?,?,'online')`, id, at); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_presence VALUES(?,?)
		ON CONFLICT(agent_id) DO UPDATE SET offline_at=MAX(offline_at,excluded.offline_at)`, id, at+timeout.Milliseconds())
	return err
}

// SweepPresence records missed deadlines even when the Agent never reconnects.
// It preserves the state at the retention boundary for duration calculations.
func (s *Store) SweepPresence(ctx context.Context, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	at, cutoff := now.UnixMilli(), now.Add(-QualityHistoryRetention).UnixMilli()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_presence_events(agent_id,at,kind)
		SELECT agent_id,offline_at,'offline' FROM agent_presence WHERE offline_at<?`, at); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_presence WHERE offline_at<?`, at); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_presence_events(agent_id,at,kind)
		SELECT id,?,'paused' FROM agents a WHERE disabled_at IS NOT NULL AND revoked=0
		AND NOT EXISTS(SELECT 1 FROM agent_presence_events e WHERE e.agent_id=a.id)
		AND NOT EXISTS(SELECT 1 FROM agent_presence_anchor h WHERE h.agent_id=a.id)`, at); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_presence_anchor(agent_id,kind)
		SELECT e.agent_id,e.kind FROM agent_presence_events e WHERE e.at<? AND e.id=(
			SELECT p.id FROM agent_presence_events p WHERE p.agent_id=e.agent_id AND p.at<? ORDER BY p.at DESC,p.id DESC LIMIT 1
		) ON CONFLICT(agent_id) DO UPDATE SET kind=excluded.kind`, cutoff, cutoff); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_presence_events WHERE at<?`, cutoff); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AgentPresenceHistory(ctx context.Context, id string, start, end int64, now time.Time) (PresenceHistory, error) {
	out := PresenceHistory{Start: start, End: end, Events: []PresenceEvent{}, InitialState: "unknown"}
	if _, err := networkStatusResolution(start, end, now); err != nil {
		return out, err
	}
	tx, err := s.beginNetworkStatusRead(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var exists bool
	var deadline int64
	var state string
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=?),
		COALESCE((SELECT offline_at FROM agent_presence WHERE agent_id=?),0),
		COALESCE((SELECT kind FROM agent_presence_events WHERE agent_id=? AND at<? ORDER BY at DESC,id DESC LIMIT 1),
			(SELECT kind FROM agent_presence_anchor WHERE agent_id=?),'unknown')`, id, id, id, start, id).Scan(&exists, &deadline, &state)
	if err != nil {
		return out, err
	}
	if !exists {
		return out, ErrAgentNotFound
	}
	if deadline > 0 && deadline < start {
		state = "offline"
	}
	out.InitialState = presenceState(state)
	rows, err := tx.QueryContext(ctx, `SELECT at,kind FROM agent_presence_events WHERE agent_id=? AND at>=? AND at<? ORDER BY at,id`, id, start, end)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var event PresenceEvent
		if err := rows.Scan(&event.At, &event.Kind); err != nil {
			rows.Close()
			return out, err
		}
		out.Events = append(out.Events, event)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	// A read between the deadline and the sweeper must already count the Agent
	// offline. The pending deadline always follows its latest persisted event.
	if deadline >= start && deadline < end {
		out.Events = append(out.Events, PresenceEvent{At: deadline, Kind: "offline"})
	}
	at := start
	for _, event := range out.Events {
		out.addDuration(state, event.At-at)
		at, state = event.At, event.Kind
	}
	out.addDuration(state, end-at)
	if observed := out.OnlineMS + out.OfflineMS; observed > 0 {
		percent := 100 * float64(out.OnlineMS) / float64(observed)
		out.OnlinePercent = &percent
	}
	return out, tx.Commit()
}

func presenceState(kind string) string {
	if kind == "resumed" {
		return "offline"
	}
	return kind
}

func (h *PresenceHistory) addDuration(kind string, duration int64) {
	switch presenceState(kind) {
	case "online":
		h.OnlineMS += duration
	case "offline":
		h.OfflineMS += duration
	case "paused":
		h.PausedMS += duration
	default:
		h.UnknownMS += duration
	}
}
