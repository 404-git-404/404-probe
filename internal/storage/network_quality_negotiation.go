package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"404-probe/internal/protocol"
)

// QualityNegotiation is the evidence one accepted report gives about the Agent's user-target
// support (design 4.2, 4.3). The header and the capability are self-reported: they tell a current
// Agent from an old one, they do not prove a binary. The safety boundary is the Agent's address
// check before every connection.
type QualityNegotiation struct {
	Header    bool
	Caps      *protocol.QualityCapabilities
	Epoch     uint64
	SessionID string
	Sequence  uint64
}

func QualityNegotiationFromReport(r protocol.Report) QualityNegotiation {
	return QualityNegotiation{Header: r.QualityTargetsHeader, Caps: r.NetworkQualityV2, Epoch: r.Epoch, SessionID: r.SessionID, Sequence: r.Sequence}
}

// ReconcileQualityAuthorization records grant/revoke evidence on the device and recalculates which
// lanes may run. It is a separate transaction after the resource report committed: a failure here
// never undoes the report. The report's session and sequence are checked again inside the
// transaction, so a late request can neither overwrite a newer session nor a later revocation.
//
//	header + complete v2 capability + targets=user_v1  => granted, capability snapshot updated
//	no header                                          => revoked
//	anything else (restart's first report)             => unknown: nothing changes
func (s *Store) ReconcileQualityAuthorization(ctx context.Context, id string, evidence QualityNegotiation, now time.Time) error {
	grant := evidence.Header && evidence.Caps != nil && evidence.Caps.Targets == protocol.QualityTargetsUserV1
	revoke := !evidence.Header
	if !grant && !revoke {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if revoke {
		// Every report of an old Agent lands here; nothing was granted in nearly all of them.
		var granted bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM quality_config WHERE agent_id=? AND targets_granted=1)`, id).Scan(&granted); err != nil {
			return err
		}
		if !granted {
			return nil
		}
	}
	pressure := s.qualityPressure(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var epoch, sequence uint64
	var session string
	err = tx.QueryRowContext(ctx, `SELECT epoch,session_id,sequence FROM agent_state WHERE agent_id=?`, id).Scan(&epoch, &session, &sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if epoch != evidence.Epoch || session != evidence.SessionID || sequence != evidence.Sequence {
		return nil // a newer report exists; it carries its own evidence
	}
	if err := requireActiveAgentTx(ctx, tx, id); err != nil {
		return err
	}
	if err := requireNoAgentRemovalTx(ctx, tx, id); err != nil {
		return err
	}
	cfg, err := qualityLoadConfigTx(ctx, tx, id)
	if err != nil {
		return err
	}
	switch {
	case grant:
		wire, err := json.Marshal(evidence.Caps)
		if err != nil {
			return err
		}
		if !cfg.exists {
			defaultChoices, _ := json.Marshal(qualityDefaultChoices())
			if _, err := tx.ExecContext(ctx, `INSERT INTO quality_config(agent_id,revision,enabled,ipv6,choices,updated_at,settings_revision,interval_ms,targets_granted,capability_snapshot) VALUES(?,0,0,0,?,?,0,30000,1,?)`, id, string(defaultChoices), now.UnixMilli(), string(wire)); err != nil {
				return err
			}
		} else {
			var same bool
			if err := tx.QueryRowContext(ctx, `SELECT targets_granted=1 AND capability_snapshot=? FROM quality_config WHERE agent_id=?`, string(wire), id).Scan(&same); err != nil {
				return err
			}
			if !same {
				if _, err := tx.ExecContext(ctx, `UPDATE quality_config SET targets_granted=1,capability_snapshot=? WHERE agent_id=?`, string(wire), id); err != nil {
					return err
				}
			}
		}
	case cfg.exists && cfg.Granted:
		if _, err := tx.ExecContext(ctx, `UPDATE quality_config SET targets_granted=0 WHERE agent_id=?`, id); err != nil {
			return err
		}
	case !cfg.exists || !cfg.Granted:
		return nil // nothing was granted, nothing to take back
	}
	if _, err := qualityApplyTx(ctx, tx, id, nil, false, now, pressure); err != nil {
		return err
	}
	return tx.Commit()
}
