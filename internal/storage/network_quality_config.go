package storage

import (
	"404-probe/internal/protocol"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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

// Resolver must be a pure local catalog lookup, never a network operation.
type QualityResolver func(protocol.QualityChoice, string) (protocol.QualityTarget, error)

func qualityDefaultChoices() []protocol.QualityChoice {
	var choices []protocol.QualityChoice
	for _, slot := range []string{"telecom", "unicom", "mobile"} {
		choices = append(choices, protocol.QualityChoice{Slot: slot, Source: "catalog", Protocol: "tcp"})
	}
	return choices
}
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
func qualityConfigTx(ctx context.Context, tx *sql.Tx, id string) (protocol.QualityConfig, error) {
	choices, rev, enabled, v6, err := qualityChoicesTx(ctx, tx, id)
	out := protocol.QualityConfig{Version: 1, Revision: strconv.FormatInt(rev, 10), Enabled: enabled, IPv6: v6, IntervalMS: 30000, JitterPercent: 20, TimeoutMS: 5000, Targets: []protocol.QualityTarget{}}
	if err != nil {
		return out, err
	}
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM quality_capability c JOIN agent_state s ON c.agent_id=s.agent_id AND c.epoch=s.epoch AND c.session_id=s.session_id WHERE c.agent_id=? AND c.supported=1)`, id).Scan(&out.Supported)
	if err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT q.slot,q.family,q.revision,t.payload FROM quality_slots q LEFT JOIN quality_targets t ON t.id=q.target_id WHERE q.agent_id=? ORDER BY q.slot,q.family`, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var slot, family string
		var sr int64
		var payload sql.NullString
		if err := rows.Scan(&slot, &family, &sr, &payload); err != nil {
			return out, err
		}
		if family == "ipv6" && !v6 {
			continue
		}
		t := protocol.QualityTarget{Slot: slot, Family: family, Status: "unavailable"}
		for _, c := range choices {
			if c.Slot == slot {
				t.Region = c.Region
				t.Source = c.Source
				t.Protocol = c.Protocol
				if !payload.Valid && c.Source == "manual" {
					// Saved input facts only: unavailable/empty ID is not a
					// resolved or authorized endpoint for this address family.
					t.Host = c.Host
					t.Port = c.Port
				}
			}
		}
		if payload.Valid {
			if err := json.Unmarshal([]byte(payload.String), &t); err != nil {
				return out, err
			}
			t.Status = "active"
			if !enabled || (family == "ipv6" && !v6) {
				t.Status = "paused"
			}
		}
		t.SlotRevision = strconv.FormatInt(sr, 10)
		out.Targets = append(out.Targets, t)
	}
	if len(out.Targets) == 0 {
		for _, c := range choices {
			out.Targets = append(out.Targets, protocol.QualityTarget{Slot: c.Slot, Family: "ipv4", SlotRevision: "0", Source: c.Source, Protocol: c.Protocol, Region: c.Region, Status: "unavailable"})
		}
	}
	return out, rows.Err()
}
func (s *Store) GetQualityConfig(ctx context.Context, id string) (protocol.QualityConfig, error) {
	return s.getQualityConfig(ctx, id, 0, "")
}
func (s *Store) GetQualitySessionConfig(ctx context.Context, id string, epoch uint64, session string) (protocol.QualityConfig, error) {
	if epoch == 0 || epoch > math.MaxInt64 || session == "" {
		return protocol.QualityConfig{}, ErrQualityUnsupported
	}
	return s.getQualityConfig(ctx, id, int64(epoch), session)
}
func (s *Store) getQualityConfig(ctx context.Context, id string, epoch int64, session string) (protocol.QualityConfig, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
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
	config, err := qualityConfigTx(ctx, tx, id)
	if err != nil {
		return config, err
	}
	if epoch > 0 {
		if err := requireActiveAgentTx(ctx, tx, id); err != nil {
			return config, err
		}
		if err := requireNoAgentRemovalTx(ctx, tx, id); err != nil {
			return config, err
		}
		var supported bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM quality_capability c JOIN agent_state s ON c.agent_id=s.agent_id AND c.epoch=s.epoch AND c.session_id=s.session_id WHERE c.agent_id=? AND c.epoch=? AND c.session_id=? AND c.supported=1)`, id, epoch, session).Scan(&supported); err != nil {
			return config, err
		}
		if !supported {
			return config, ErrQualityUnsupported
		}
	}
	return config, nil
}

func (s *Store) UpdateQualityConfig(ctx context.Context, id string, update protocol.QualityConfigUpdate, resolve QualityResolver, now time.Time) (protocol.QualityConfig, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if update.ExpectedRevision != "0" {
		if _, err := protocol.QualityInteger(update.ExpectedRevision); err != nil {
			return protocol.QualityConfig{}, ErrQualityInvalid
		}
	}
	pressure := s.qualityPressure(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.QualityConfig{}, err
	}
	defer tx.Rollback()
	if err := requireActiveAgentTx(ctx, tx, id); err != nil {
		return protocol.QualityConfig{}, err
	}
	if err := requireNoAgentRemovalTx(ctx, tx, id); err != nil {
		return protocol.QualityConfig{}, err
	}
	choices, rev, enabled, v6, err := qualityChoicesTx(ctx, tx, id)
	if err != nil {
		return protocol.QualityConfig{}, err
	}
	if update.ExpectedRevision != strconv.FormatInt(rev, 10) {
		return protocol.QualityConfig{}, ErrQualityConflict
	}
	if rev == math.MaxInt64 || len(update.Choices) > 3 || resolve == nil {
		return protocol.QualityConfig{}, ErrQualityInvalid
	}
	seen := map[string]bool{}
	for _, c := range update.Choices {
		index := -1
		for i, old := range choices {
			if old.Slot == c.Slot {
				index = i
			}
		}
		if index < 0 || seen[c.Slot] || len(c.Region) > 120 || len(c.Host) > 253 || (c.Protocol != "tcp" && c.Protocol != "icmp") || (c.Source != "catalog" && c.Source != "manual") {
			return protocol.QualityConfig{}, ErrQualityInvalid
		}
		seen[c.Slot] = true
		choices[index] = c
	}
	newJSON, _ := json.Marshal(choices)
	oldChoices, _, _, _, _ := qualityChoicesTx(ctx, tx, id)
	oldJSON, _ := json.Marshal(oldChoices)
	if rev > 0 && string(newJSON) == string(oldJSON) && enabled == update.Enabled && v6 == update.IPv6 {
		return qualityConfigTx(ctx, tx, id)
	}
	newRev := rev + 1
	for _, choice := range choices {
		for _, family := range []string{"ipv4", "ipv6"} {
			target, err := resolve(choice, family)
			if err != nil {
				return protocol.QualityConfig{}, ErrQualityInvalid
			}
			target.Slot = choice.Slot
			target.Family = family
			target.Protocol = choice.Protocol
			target.Source = choice.Source
			target.Region = choice.Region
			target.SlotRevision = ""
			target.Status = ""
			target.ID = ""
			var targetID string
			if family == "ipv6" && !update.IPv6 {
				target.Host = ""
			}
			if target.Host != "" {
				fingerprintBytes, _ := json.Marshal(struct {
					Agent  string
					Target protocol.QualityTarget
				}{id, target})
				digest := sha256.Sum256(fingerprintBytes)
				fingerprint := hex.EncodeToString(digest[:])
				targetID = fingerprint[:32]
				target.ID = targetID
				var exists bool
				if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM quality_targets WHERE agent_id=? AND fingerprint=?)`, id, fingerprint).Scan(&exists); err != nil {
					return protocol.QualityConfig{}, err
				}
				if !exists {
					if pressure {
						return protocol.QualityConfig{}, ErrQualityStoragePressure
					}
					var local, global int
					if err := qualityCountTx(ctx, tx, id, "targets", &local, &global); err != nil {
						return protocol.QualityConfig{}, err
					}
					if local >= qualityTargetDevice || global >= qualityTargetGlobal {
						return protocol.QualityConfig{}, ErrQualityQuota
					}
					payload, _ := json.Marshal(target)
					if _, err := tx.ExecContext(ctx, `INSERT INTO quality_targets(id,agent_id,fingerprint,payload,created_at) VALUES(?,?,?,?,?)`, targetID, id, fingerprint, string(payload), now.UnixMilli()); err != nil {
						return protocol.QualityConfig{}, err
					}
				}
			}
			var oldID sql.NullString
			var oldRev int64
			err = tx.QueryRowContext(ctx, `SELECT target_id,revision FROM quality_slots WHERE agent_id=? AND slot=? AND family=?`, id, choice.Slot, family).Scan(&oldID, &oldRev)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return protocol.QualityConfig{}, err
			}
			var oldActive bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM quality_intervals WHERE agent_id=? AND slot=? AND family=? AND end_at IS NULL)`, id, choice.Slot, family).Scan(&oldActive); err != nil {
				return protocol.QualityConfig{}, err
			}
			active := targetID != "" && update.Enabled && (family == "ipv4" || update.IPv6)
			if oldRev > 0 && oldID.String == targetID && oldActive == active {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE quality_intervals SET end_at=?,config_until=? WHERE agent_id=? AND slot=? AND family=? AND end_at IS NULL`, now.UnixMilli(), newRev-1, id, choice.Slot, family); err != nil {
				return protocol.QualityConfig{}, err
			}
			var targetArg any
			if targetID != "" {
				targetArg = targetID
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO quality_slots(agent_id,slot,family,revision,target_id) VALUES(?,?,?,?,?) ON CONFLICT(agent_id,slot,family) DO UPDATE SET revision=excluded.revision,target_id=excluded.target_id`, id, choice.Slot, family, newRev, targetArg); err != nil {
				return protocol.QualityConfig{}, err
			}
			if active {
				if pressure {
					return protocol.QualityConfig{}, ErrQualityStoragePressure
				}
				var local, global int
				if err := qualityCountTx(ctx, tx, id, "intervals", &local, &global); err != nil {
					return protocol.QualityConfig{}, err
				}
				if local >= qualityIntervalDevice {
					return protocol.QualityConfig{}, ErrQualityQuota
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO quality_intervals(agent_id,slot,family,revision,target_id,start_at,config_from) VALUES(?,?,?,?,?,?,?)`, id, choice.Slot, family, newRev, targetID, now.UnixMilli(), newRev); err != nil {
					return protocol.QualityConfig{}, err
				}
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE quality_targets SET retired_at=CASE WHEN EXISTS(SELECT 1 FROM quality_slots s WHERE s.target_id=quality_targets.id) THEN NULL ELSE COALESCE(retired_at,?) END WHERE agent_id=?`, now.UnixMilli(), id); err != nil {
		return protocol.QualityConfig{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO quality_config(agent_id,revision,enabled,ipv6,choices,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(agent_id) DO UPDATE SET revision=excluded.revision,enabled=excluded.enabled,ipv6=excluded.ipv6,choices=excluded.choices,updated_at=excluded.updated_at`, id, newRev, update.Enabled, update.IPv6, string(newJSON), now.UnixMilli()); err != nil {
		return protocol.QualityConfig{}, err
	}
	out, err := qualityConfigTx(ctx, tx, id)
	if err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.QualityConfig{}, err
	}
	return out, nil
}

func qualityCountTx(ctx context.Context, tx *sql.Tx, id, field string, local, global *int) error {
	// field is exclusively a static internal table-counter name, never user input.
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT `+field+` FROM quality_usage WHERE agent_id=?),0)`, id).Scan(local); err != nil {
		return err
	}
	return tx.QueryRowContext(ctx, `SELECT value FROM quality_totals WHERE kind=?`, field).Scan(global)
}
