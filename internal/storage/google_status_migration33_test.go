package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"404-probe/internal/protocol"
)

func saveYouTubeForTest(t *testing.T, store *Store, agentID string, youtube protocol.YouTubeResult, at int64) {
	t.Helper()
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result := protocol.GoogleStatusResult{YouTube: youtube,
		Search: protocol.GoogleSearchResult{Status: protocol.GoogleSearchUnknown}, SignIn: protocol.GoogleSignInResult{Status: protocol.GoogleSignInUnknown}, Gemini: protocol.GeminiResult{Status: protocol.GeminiUnknown}}
	if err := saveGoogleStatusTx(context.Background(), tx, agentID, result, at); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestGoogleStatusPremiumNoticePersistence(t *testing.T) {
	store, agentID, _ := testStore(t, filepath.Join(t.TempDir(), "notice.db"))
	defer store.Close()
	yes, no := true, false
	for i, tc := range []struct {
		name   string
		result protocol.YouTubeResult
		want   *bool
	}{
		{"flagged host with the page message", protocol.YouTubeResult{Status: protocol.YouTubeCN, Region: "US", SentToChina: &yes, PremiumNotice: &yes}, &yes},
		{"flagged host without the message", protocol.YouTubeResult{Status: protocol.YouTubeCN, Region: "US", SentToChina: &yes, PremiumNotice: &no}, &no},
		{"older shape: Region CN and no notice", protocol.YouTubeResult{Status: protocol.YouTubeCN, Region: "CN", SentToChina: &yes}, nil},
		{"flagged host without any page region", protocol.YouTubeResult{Status: protocol.YouTubeCN, SentToChina: &yes, PremiumNotice: &yes}, &yes},
		{"normal region", protocol.YouTubeResult{Status: protocol.YouTubeNotCN, Region: "US", SentToChina: &no}, nil},
		{"unknown", protocol.YouTubeResult{Status: protocol.YouTubeUnknown, Error: protocol.GoogleServiceError{Category: "china_signal_unverified"}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saveYouTubeForTest(t, store, agentID, tc.result, int64(1000+i))
			snapshot, exists, err := store.GetGoogleStatus(context.Background(), agentID)
			if err != nil || !exists {
				t.Fatalf("exists=%t err=%v", exists, err)
			}
			got := snapshot.Result.YouTube
			if got.Status != tc.result.Status || got.Region != tc.result.Region || (got.PremiumNotice == nil) != (tc.want == nil) || (tc.want != nil && *got.PremiumNotice != *tc.want) {
				t.Fatalf("got=%+v want notice=%v", got, tc.want)
			}
		})
	}
}

func TestMigration33AddsNullablePremiumNotice(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "m33.db")
	store, agentID, _ := testStore(t, path)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// Rebuild the schema 32 shape: no premium notice column, an existing cn row, version 33 removed.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`ALTER TABLE agent_google_status DROP COLUMN youtube_premium_notice`,
		`DELETE FROM schema_migrations WHERE version=33`,
		`INSERT INTO agent_google_status(agent_id,youtube_status,youtube_region,sent_to_china,youtube_error,search_status,search_error,signin_status,signin_error,gemini_status,gemini_region,gemini_error,checked_at,unknown_streak,next_due_at)
			VALUES('` + agentID + `','cn','CN',1,NULL,'unknown',NULL,'unknown',NULL,'unknown',NULL,NULL,1000,0,90000000)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(statement, err)
		}
	}
	db.Close()
	for attempt := 0; attempt < 2; attempt++ { // the second open proves it is idempotent
		store, err = Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if version, err := store.SchemaVersion(ctx); err != nil || version < 33 || version != currentSchemaVersion {
			t.Fatalf("version=%d err=%v", version, err)
		}
		snapshot, exists, err := store.GetGoogleStatus(ctx, agentID)
		if err != nil || !exists || snapshot.Result.YouTube.Status != protocol.YouTubeCN || snapshot.Result.YouTube.Region != "CN" || snapshot.Result.YouTube.PremiumNotice != nil {
			t.Fatalf("old row after migration: %+v exists=%t err=%v", snapshot, exists, err)
		}
		store.Close()
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`UPDATE agent_google_status SET youtube_premium_notice=2`); err == nil {
		t.Fatal("the column accepted a value other than NULL, 0 and 1")
	}
	for _, value := range []string{"NULL", "0", "1"} {
		if _, err := store.db.Exec(`UPDATE agent_google_status SET youtube_premium_notice=` + value); err != nil {
			t.Fatal(value, err)
		}
	}
}
