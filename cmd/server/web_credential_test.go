package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/storage"
)

func TestWebCredentialStartupAndAdopt(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	database := filepath.Join(dir, "server.db")
	hashFile := filepath.Join(dir, "web-password.hash")
	store, err := openStore(database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(100, 0)
	writeHash := func(password, ending string) storage.WebCredential {
		t.Helper()
		hash, err := auth.HashPassword([]byte(password))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(hashFile, []byte(hash+ending), 0o400); err != nil {
			t.Fatal(err)
		}
		file, err := readWebPasswordFile(hashFile)
		if err != nil {
			t.Fatal(err)
		}
		return file
	}
	initial := writeHash("initial fixture password", "\n")
	if _, err := reconcileWebPasswordFile(ctx, store, hashFile, now); err != nil {
		t.Fatal(err)
	}
	if got, err := store.WebCredential(ctx); err != nil || got.Revision != 0 {
		t.Fatal("startup persisted the initial file credential", err)
	}
	webHash, err := auth.HashPassword([]byte("web fixture password"))
	if err != nil {
		t.Fatal(err)
	}
	if revision, err := store.ReplaceWebCredential(ctx, 0, webHash, initial.FileSHA256, now); err != nil || revision != 1 {
		t.Fatalf("web revision=%d err=%v", revision, err)
	}
	if _, err := reconcileWebPasswordFile(ctx, store, hashFile, now); err != nil {
		t.Fatal(err)
	}
	if got, err := store.WebCredential(ctx); err != nil || got.Revision != 1 || got.PasswordHash != webHash {
		t.Fatal("same password file replaced the database credential", err)
	}
	if err := os.Remove(hashFile); err != nil {
		if err := os.Chmod(hashFile, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(hashFile); err != nil {
			t.Fatal(err)
		}
	}
	replaced := writeHash("root fixture password", "\r\n")
	if _, err := reconcileWebPasswordFile(ctx, store, hashFile, now); err != nil {
		t.Fatal(err)
	}
	if got, err := store.WebCredential(ctx); err != nil || got.Revision != 2 || got.PasswordHash != replaced.PasswordHash || got.FileSHA256 != replaced.FileSHA256 {
		t.Fatal("startup did not adopt the replaced password file", err)
	}
	for range 2 {
		if err := run([]string{"web-credential", "adopt", "--db", database, "--hash-file", hashFile}); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := store.WebCredential(ctx); err != nil || got.Revision != 4 || got.FileSHA256 != replaced.FileSHA256 {
		t.Fatal("adopt failed to advance the revision", err)
	}
}
