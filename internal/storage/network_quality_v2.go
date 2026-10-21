package storage

import (
	"404-probe/internal/protocol"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

func qualityCapabilityTx(ctx context.Context, tx *sql.Tx, id string) (*protocol.QualityCapabilities, bool, string, error) {
	var raw sql.NullString
	var supported bool
	var reason string
	err := tx.QueryRowContext(ctx, `SELECT c.v2_details,c.supported,c.excluded_reason FROM quality_capability c JOIN agent_state s ON c.agent_id=s.agent_id AND c.epoch=s.epoch AND c.session_id=s.session_id WHERE c.agent_id=?`, id).Scan(&raw, &supported, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, "", nil
	}
	if err != nil {
		return nil, false, "", err
	}
	if !raw.Valid {
		return nil, supported, reason, nil
	}
	var caps protocol.QualityCapabilities
	if err := json.Unmarshal([]byte(raw.String), &caps); err != nil {
		return nil, false, "", err
	}
	if err := caps.Validate(); err != nil {
		return nil, false, "", err
	}
	return &caps, supported, reason, nil
}
func qualityExcludedTx(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	_, _, reason, err := qualityCapabilityTx(ctx, tx, id)
	return reason != "", err
}
func qualityLaneSupported(c *protocol.QualityCapabilities, family, proto string) bool {
	if c == nil || c.ExcludedReason != "" {
		return false
	}
	for _, l := range c.Lanes {
		if l.Family == family && l.Protocol == proto {
			return l.Supported
		}
	}
	return false
}
