package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

func DefaultYouTubeAdFreeRegions() []string {
	return []string{"AL", "MD", "AD", "SM", "MO", "KH", "LA", "MV", "MN", "CI", "MG", "ET", "RU", "MM", "CN"}
}

func encodeYouTubeAdFreeRegions(regions []string) (string, error) {
	if regions == nil {
		return "[]", nil
	}
	encoded, err := json.Marshal(regions)
	return string(encoded), err
}

func (s *Store) YouTubeAdFreeRegions(ctx context.Context) ([]string, error) {
	return readYouTubeAdFreeRegions(ctx, s.db)
}

func readYouTubeAdFreeRegions(ctx context.Context, query interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) ([]string, error) {
	rows, err := query.QueryContext(ctx, `SELECT region FROM web_youtube_ad_free_regions ORDER BY region`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	regions := []string{}
	for rows.Next() {
		var region string
		if err := rows.Scan(&region); err != nil {
			return nil, err
		}
		regions = append(regions, region)
	}
	return regions, rows.Err()
}

func (s *Store) SetYouTubeAdFreeRegions(ctx context.Context, regions []string, now time.Time) (bool, error) {
	encoded, err := encodeYouTubeAdFreeRegions(regions)
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	removed, err := tx.ExecContext(ctx, `DELETE FROM web_youtube_ad_free_regions WHERE region NOT IN (SELECT value FROM json_each(?))`, encoded)
	if err != nil {
		return false, err
	}
	added, err := tx.ExecContext(ctx, `INSERT INTO web_youtube_ad_free_regions(region,created_at)
		SELECT value,? FROM json_each(?) WHERE true ON CONFLICT(region) DO NOTHING`, now.Unix(), encoded)
	if err != nil {
		return false, err
	}
	removedCount, err := removed.RowsAffected()
	if err != nil {
		return false, err
	}
	addedCount, err := added.RowsAffected()
	if err != nil {
		return false, err
	}
	changed := removedCount+addedCount > 0
	if changed {
		// All dashboard catalog fields must advance together so an older
		// dashboard response can never overwrite a newly saved region list.
		result, err := tx.ExecContext(ctx, `UPDATE organization_meta SET revision=revision+1 WHERE id=1 AND revision<9007199254740991`)
		if err != nil {
			return false, err
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			return false, ErrOrganizationConflict
		}
	}
	return changed, tx.Commit()
}
