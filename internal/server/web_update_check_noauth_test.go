package server

import (
	"404-probe/internal/storage"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUpdateCheckWithoutWebAuthenticationStaysDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update-check.json")
	original := []byte(`{"enabled":true,"latest":"v1.1.0","checked_at":123}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := storage.Open(t.Context(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, err := NewApp(s, 30*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown()
	if a.webAuth != nil || a.updateCheck.Snapshot().Enabled {
		t.Fatal("checker not disabled")
	}
	if _, err := a.updateCheck.SetEnabled(true); err == nil {
		t.Fatal("inactive checker enabled")
	}
	done := make(chan struct{})
	go func() { a.CleanupLoop(); close(done) }()
	a.Shutdown()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not stop")
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != string(original) {
		t.Fatal("saved setting changed", err)
	}
}
