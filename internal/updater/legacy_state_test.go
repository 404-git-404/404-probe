package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyLocalMigrationJournalIsReadAndRetiredOnWrite(t *testing.T) {
	dir := t.TempDir()
	data, err := os.ReadFile("testdata/operation-local-migration.json")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "operation.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(EngineConfig{StateDirectory: dir,
		LiveBinary: filepath.Join(dir, "live"), StagedBinary: filepath.Join(dir, "staged"), PreviousBinary: filepath.Join(dir, "previous"),
		Inspect: func(string) (CandidateBuildInfo, error) {
			t.Fatal("constructor inspected executable")
			return CandidateBuildInfo{}, nil
		},
		ServiceCommand: func(context.Context, string) error { t.Fatal("constructor changed service"); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	want := State{SourceVersion: "v0.9.3", OperationID: "0123456789abcdef0123456789abcdef", TargetVersion: "v1.0.1", ReleaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: "succeeded", UpdatedAt: 1}
	if engine.state != want {
		t.Fatalf("state=%+v, want %+v", engine.state, want)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(data, after) {
		t.Fatal("read changed journal", err)
	}
	if err := engine.persistLocked(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(after, []byte("local_migration")) {
		t.Fatal("retired key persisted")
	}
	got, err := readStateFile(path)
	if err != nil || got != want {
		t.Fatalf("round trip=%+v err=%v", got, err)
	}
	want.LocalMigration = true
	wire, err := json.Marshal(want)
	if err != nil || bytes.Contains(wire, []byte("local_migration")) {
		t.Fatal("deprecated flag can be written", err)
	}
}
