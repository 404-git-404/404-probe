package storage

import (
	"context"
	"testing"
	"time"
)

func TestYouTubeManualRegionCascadesOnAgentDelete(t *testing.T) {
	s, id, _ := testStore(t, ":memory:")
	defer s.Close()
	ctx := context.Background()
	if err := s.SetYouTubeManualRegion(ctx, id, "MO", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM agents WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	if region, err := s.YouTubeManualRegion(ctx, id); err != nil || region != "" {
		t.Fatalf("deleted agent region=%q err=%v", region, err)
	}
}
