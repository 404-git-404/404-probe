package storage

import (
	"404-probe/internal/protocol"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"
)

var ErrQualityConflict = errors.New("quality revision conflict")
var ErrQualityQuota = errors.New("quality quota")
var ErrQualityUnsupported = errors.New("quality unsupported")
var ErrQualityInvalid = errors.New("invalid quality config")

var ErrQualityStoragePressure = errors.New("quality storage pressure")

// qualityDefaultChoices is the legacy choices column. R07 no longer reads it for behavior; it only
// keeps the column well-formed for rows created after migration 34.
func qualityDefaultChoices() []protocol.QualityChoice {
	var choices []protocol.QualityChoice
	for _, slot := range []string{"telecom", "unicom", "mobile"} {
		choices = append(choices, protocol.QualityChoice{Slot: slot, Source: "catalog", Protocol: "tcp"})
	}
	return choices
}

// qualityChoicesTx returns the measurement revision (the legacy column set is unused after R07).
func qualityChoicesTx(ctx context.Context, tx *sql.Tx, id string) ([]protocol.QualityChoice, int64, bool, bool, error) {
	choices := qualityDefaultChoices()
	var revision int64
	var enabled, v6 bool
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT revision,enabled,ipv6,choices FROM quality_config WHERE agent_id=?`, id).Scan(&revision, &enabled, &v6, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return choices, 0, false, false, nil
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &choices)
	}
	return choices, revision, enabled, v6, err
}

// GetQualitySessionConfig builds the configuration one Agent session receives (design 4.2).
// The new format, with user-entered targets, goes only to a session that sent the negotiation
// header on this request and completed both negotiation steps. Everything else, including every
// old Agent, gets the old format with nothing enabled so that its own validation accepts it and
// it stops all lanes.
func (s *Store) GetQualitySessionConfig(ctx context.Context, id string, epoch uint64, session string, version int, header bool) (protocol.QualityConfig, error) {
	if epoch == 0 || epoch > math.MaxInt64 || session == "" || (version != 1 && version != 2) {
		return protocol.QualityConfig{}, ErrQualityUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	budget := s.qualityBudget(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.QualityConfig{}, err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=?)`, id).Scan(&exists); err != nil {
		return protocol.QualityConfig{}, err
	}
	if !exists {
		return protocol.QualityConfig{}, ErrAgentNotFound
	}
	if err := requireActiveAgentTx(ctx, tx, id); err != nil {
		return protocol.QualityConfig{}, err
	}
	if err := requireNoAgentRemovalTx(ctx, tx, id); err != nil {
		return protocol.QualityConfig{}, err
	}
	var supported, userTargets bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM quality_capability c JOIN agent_state s ON c.agent_id=s.agent_id AND c.epoch=s.epoch AND c.session_id=s.session_id WHERE c.agent_id=? AND c.epoch=? AND c.session_id=? AND c.supported=1),
		EXISTS(SELECT 1 FROM quality_capability c JOIN agent_state s ON c.agent_id=s.agent_id AND c.epoch=s.epoch AND c.session_id=s.session_id WHERE c.agent_id=? AND c.epoch=? AND c.session_id=? AND c.user_targets=1)`, id, epoch, session, id, epoch, session).Scan(&supported, &userTargets); err != nil {
		return protocol.QualityConfig{}, err
	}
	caps, _, excluded, err := qualityCapabilityTx(ctx, tx, id)
	if err != nil {
		return protocol.QualityConfig{}, err
	}
	if !supported || (version == 2 && caps == nil) {
		return protocol.QualityConfig{}, ErrQualityUnsupported
	}
	cfg, err := qualityLoadConfigTx(ctx, tx, id)
	if err != nil {
		return protocol.QualityConfig{}, err
	}
	out := protocol.QualityConfig{Version: version, Revision: strconv.FormatInt(cfg.Revision, 10), Supported: true, IntervalMS: protocol.QualityMinIntervalMS, JitterPercent: 20, TimeoutMS: 5000, Targets: []protocol.QualityTarget{}}
	if version == 2 {
		out.Semantics = caps.Semantics
	}
	if excluded != "" {
		if version == 2 {
			out.State = excluded
		}
		return out, nil
	}
	// Both halves are required: this session negotiated, and the device-level authorization was
	// recorded. If the recalculation after the report failed, the session still gets the stop
	// configuration, so not even a paused target carries a user address.
	if version != 2 || !header || !userTargets || !cfg.Granted || cfg.Snapshot == nil {
		if version == 2 {
			out.State = "agent_upgrade_required"
		}
		return out, nil
	}
	out.Capabilities = caps
	out.Enabled = cfg.Enabled
	out.IPv6 = true
	out.IntervalMS = cfg.IntervalMS
	out.State = "ready"
	rows, err := tx.QueryContext(ctx, `SELECT q.revision,t.id,t.payload,EXISTS(SELECT 1 FROM quality_intervals i WHERE i.agent_id=q.agent_id AND i.slot=q.slot AND i.family=q.family AND i.protocol=q.protocol AND i.target_id=q.target_id AND i.end_at IS NULL)
		FROM quality_slots q JOIN quality_targets t ON t.id=q.target_id AND t.agent_id=q.agent_id WHERE q.agent_id=? ORDER BY q.slot,q.family,q.protocol`, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var revision int64
		var targetID, payload string
		var open bool
		if err := rows.Scan(&revision, &targetID, &payload, &open); err != nil {
			return out, err
		}
		t, err := qualityTargetFromPayload(targetID, payload)
		if err != nil {
			return out, err
		}
		t.SlotRevision, t.Status = strconv.FormatInt(revision, 10), "paused"
		if open {
			t.Status = "active"
		}
		out.Targets = append(out.Targets, t)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	budget.apply(&out)
	return out, nil
}

func qualityCountTx(ctx context.Context, tx *sql.Tx, id, field string, local, global *int) error {
	// field is exclusively a static internal table-counter name, never user input.
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT `+field+` FROM quality_usage WHERE agent_id=?),0)`, id).Scan(local); err != nil {
		return err
	}
	return tx.QueryRowContext(ctx, `SELECT value FROM quality_totals WHERE kind=?`, field).Scan(global)
}
