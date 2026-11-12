package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *Store) YouTubeManualRegion(ctx context.Context, id string) (string, error) {
	var region string
	err := s.db.QueryRowContext(ctx, `SELECT region FROM agent_youtube_region_manual WHERE agent_id=?`, id).Scan(&region)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return region, err
}

func (s *Store) SetYouTubeManualRegion(ctx context.Context, id, region string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `INSERT INTO agent_youtube_region_manual(agent_id,region,updated_at,revision)
		SELECT id,?,?,1 FROM agents WHERE id=? AND revoked=0
		ON CONFLICT(agent_id) DO UPDATE SET region=excluded.region,updated_at=excluded.updated_at,
		revision=agent_youtube_region_manual.revision+1
		WHERE agent_youtube_region_manual.region<>excluded.region`, region, now.UnixMilli(), id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 0 {
		return err
	}
	return requireYouTubeRegionAgent(ctx, s.db, id)
}

func (s *Store) ClearYouTubeManualRegion(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM agent_youtube_region_manual WHERE agent_id=?`, id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 0 {
		return err
	}
	return requireYouTubeRegionAgent(ctx, s.db, id)
}

func requireYouTubeRegionAgent(ctx context.Context, db *sql.DB, id string) error {
	var found bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=? AND revoked=0)`, id).Scan(&found); err != nil {
		return err
	}
	if !found {
		return ErrAgentNotFound
	}
	return nil
}
