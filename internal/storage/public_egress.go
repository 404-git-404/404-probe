package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"404-probe/internal/protocol"
)

// Epoch and Sequence are internal ordering state and are never serialized to Web views.
// Status and validity timestamps are identical in API and SSE; the consumer
// evaluates expiry and offline lifecycle without changing the base status.
type EgressState struct {
	Version   int    `json:"version"`
	Supported bool   `json:"supported"`
	Epoch     string `json:"epoch"`
	Sequence  string `json:"sequence"`
	// Source is "job" for a Server-dispatched check and empty for telemetry attached by a v1.0.3 Agent.
	Source string                     `json:"source,omitempty"`
	IPv4   protocol.EgressObservation `json:"ipv4"`
	IPv6   protocol.EgressObservation `json:"ipv6"`
}

func migratePublicEgressTx(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{
		`CREATE TABLE agent_public_egress(agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,payload TEXT NOT NULL CHECK(length(payload)<=4096))`,
		`INSERT INTO schema_migrations(version,applied_at) VALUES(28,unixepoch())`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func blankEgressState() EgressState {
	return EgressState{Version: 1, IPv4: protocol.UnknownEgress("ipv4", "not_reported"), IPv6: protocol.UnknownEgress("ipv6", "not_reported")}
}

func publicEgressEnabledTx(ctx context.Context, tx *sql.Tx, agentID string) (bool, error) {
	var enabled int
	err := tx.QueryRowContext(ctx, `SELECT enabled FROM agent_public_egress_control WHERE agent_id=?`, agentID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return enabled == 1, err
}

func mergePublicEgress(previous, current State, r protocol.Report, received time.Time) EgressState {
	// A current Agent no longer attaches telemetry: what its jobs stored stays untouched.
	if r.PublicEgress == nil && previous.PublicEgress.Version == 1 && previous.PublicEgress.Source == "job" {
		return previous.PublicEgress
	}
	p := EgressState{Version: 1, Supported: r.PublicEgress != nil, Epoch: strconv.FormatUint(r.Epoch, 10), Sequence: strconv.FormatUint(r.Sequence, 10), IPv4: protocol.UnknownEgress("ipv4", "not_reported"), IPv6: protocol.UnknownEgress("ipv6", "not_reported")}
	if r.PublicEgress != nil {
		if r.PublicEgress.Version == 1 {
			p.IPv4 = r.PublicEgress.IPv4
			p.IPv6 = r.PublicEgress.IPv6
		} else {
			p.IPv4.Reason = "invalid_report"
			p.IPv6.Reason = "invalid_report"
		}
	}
	merge := func(next, old protocol.EgressObservation, family string) protocol.EgressObservation {
		next = protocol.NormalizeEgress(next, family, r.CollectedAt, received.UnixMilli())
		// An old cache sample inside a newer telemetry sequence must not undo
		// a newer observation. Missing/invalid optional data stays unknown.
		if p.Supported && previous.Epoch == current.Epoch && previous.SessionID == current.SessionID && next.Reason != "invalid_report" && old.CheckedAt > next.CheckedAt {
			next = old
		}
		if old.LastSuccess != nil && (next.LastSuccess == nil || old.LastSuccess.CheckedAt > next.LastSuccess.CheckedAt) {
			next.LastSuccess = old.LastSuccess
		}
		return next
	}
	p.IPv4 = merge(p.IPv4, previous.PublicEgress.IPv4, "ipv4")
	p.IPv6 = merge(p.IPv6, previous.PublicEgress.IPv6, "ipv6")
	return p
}

func writePublicEgressTx(ctx context.Context, tx *sql.Tx, s State) error {
	data, err := json.Marshal(s.PublicEgress)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_public_egress(agent_id,payload) VALUES(?,?) ON CONFLICT(agent_id) DO UPDATE SET payload=excluded.payload`, s.AgentID, string(data))
	return err
}

// readPublicEgressTx is what views see: nothing while the detection is switched off.
func readPublicEgressTx(ctx context.Context, tx *sql.Tx, id string) (EgressState, error) {
	enabled, err := publicEgressEnabledTx(ctx, tx, id)
	if err != nil || !enabled {
		return blankEgressState(), err
	}
	return readStoredEgressTx(ctx, tx, id)
}

func readStoredEgressTx(ctx context.Context, tx *sql.Tx, id string) (EgressState, error) {
	p := blankEgressState()
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT payload FROM agent_public_egress WHERE agent_id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	var saved EgressState
	if len(raw) <= 4096 && json.Unmarshal([]byte(raw), &saved) == nil && saved.Version == 1 {
		// Persisted data is validated too; corrupt optional data cannot make
		// the complete Agent detail unavailable.
		saved.IPv4 = protocol.NormalizeStoredEgress(saved.IPv4, "ipv4", time.Now().UnixMilli(), time.Now().UnixMilli())
		saved.IPv6 = protocol.NormalizeStoredEgress(saved.IPv6, "ipv6", time.Now().UnixMilli(), time.Now().UnixMilli())
		return saved, nil
	}
	return p, nil
}
