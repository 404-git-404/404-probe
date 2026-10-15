package storage

import (
	"404-probe/internal/protocol"
	"context"
	"time"
)

// Card configuration adds the saved lane choices to the web read model only.
// Disabled/manual lanes must survive region editing without inferred targets.
type QualityCardConfig struct {
	protocol.QualityConfig
	CardLanes []protocol.QualityLaneChoice `json:"card_lanes"`
}

func (s *Store) QualityCardConfiguration(ctx context.Context, id string) (QualityCardConfig, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var out QualityCardConfig
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=?)`, id).Scan(&exists); err != nil {
		return out, err
	}
	if !exists {
		return out, ErrAgentNotFound
	}
	out.QualityConfig, err = qualityConfigTx(ctx, tx, id, 2)
	if err != nil {
		return out, err
	}
	choices, _, _, _, err := qualityChoicesTx(ctx, tx, id)
	if err != nil {
		return out, err
	}
	out.CardLanes, err = qualityLaneChoicesTx(ctx, tx, id, choices)
	return out, err
}
