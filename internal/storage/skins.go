package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

const DefaultSkinID = "default"
const skinLimit = 20

var (
	ErrSkinNotFound = errors.New("skin not found")
	ErrSkinLimit    = errors.New("skin limit reached")
	ErrBuiltinSkin  = errors.New("built-in skin cannot be deleted")
)

type Skin struct {
	ID          string
	Name        string
	Author      string
	Description string
	Light       map[string]string
	Dark        map[string]string
	CreatedAt   int64
}

func scanSkin(row interface{ Scan(...any) error }) (Skin, error) {
	var skin Skin
	var light, dark string
	err := row.Scan(&skin.ID, &skin.Name, &skin.Author, &skin.Description, &light, &dark, &skin.CreatedAt)
	if err != nil {
		return skin, err
	}
	if err := json.Unmarshal([]byte(light), &skin.Light); err != nil {
		return skin, err
	}
	return skin, json.Unmarshal([]byte(dark), &skin.Dark)
}

const skinColumns = "id,name,author,description,light,dark,created_at"

func (s *Store) ListSkins(ctx context.Context) (string, []Skin, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()
	var active string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(active_id,?) FROM skin_settings WHERE singleton=1`, DefaultSkinID).Scan(&active); err != nil {
		return "", nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+skinColumns+` FROM skins ORDER BY created_at,id`)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	var skins []Skin
	for rows.Next() {
		skin, err := scanSkin(rows)
		if err != nil {
			return "", nil, err
		}
		skins = append(skins, skin)
	}
	return active, skins, rows.Err()
}

func (s *Store) GetSkin(ctx context.Context, id string) (Skin, error) {
	skin, err := scanSkin(s.db.QueryRowContext(ctx, `SELECT `+skinColumns+` FROM skins WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrSkinNotFound
	}
	return skin, err
}

func (s *Store) ActiveSkin(ctx context.Context) (Skin, error) {
	skin, err := scanSkin(s.db.QueryRowContext(ctx, `SELECT `+skinColumns+` FROM skins
		WHERE id=(SELECT active_id FROM skin_settings WHERE singleton=1)`))
	if errors.Is(err, sql.ErrNoRows) {
		return Skin{ID: DefaultSkinID}, nil
	}
	return skin, err
}

func (s *Store) CreateSkin(ctx context.Context, skin Skin, now time.Time) (Skin, error) {
	light, err := json.Marshal(skin.Light)
	if err != nil {
		return Skin{}, err
	}
	dark, err := json.Marshal(skin.Dark)
	if err != nil {
		return Skin{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Skin{}, err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM skins`).Scan(&count); err != nil {
		return Skin{}, err
	}
	if count >= skinLimit {
		return Skin{}, ErrSkinLimit
	}
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return Skin{}, err
	}
	skin.ID, skin.CreatedAt = hex.EncodeToString(key[:]), now.UnixMilli()
	if _, err := tx.ExecContext(ctx, `INSERT INTO skins(`+skinColumns+`) VALUES(?,?,?,?,?,?,?)`,
		skin.ID, skin.Name, skin.Author, skin.Description, string(light), string(dark), skin.CreatedAt); err != nil {
		return Skin{}, err
	}
	return skin, tx.Commit()
}

func (s *Store) SetActiveSkin(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE skin_settings
		SET active_id=NULLIF(?,?)
		WHERE singleton=1 AND (?=? OR EXISTS(SELECT 1 FROM skins WHERE id=?))`, id, DefaultSkinID, id, DefaultSkinID, id)
	return skinWriteResult(result, err)
}

func (s *Store) DeleteSkin(ctx context.Context, id string) error {
	if id == DefaultSkinID {
		return ErrBuiltinSkin
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE skin_settings SET active_id=NULL WHERE singleton=1 AND active_id=?`, id); err != nil {
		return err
	}
	if err := skinWriteResult(tx.ExecContext(ctx, `DELETE FROM skins WHERE id=?`, id)); err != nil {
		return err
	}
	return tx.Commit()
}

func skinWriteResult(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrSkinNotFound
	}
	return err
}
