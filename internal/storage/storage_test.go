package storage

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
)

func testStore(t *testing.T, path string) (*Store, string, string) {
	t.Helper()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := auth.NewID()
	if err != nil {
		t.Fatal(err)
	}
	token, hash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddAgent(context.Background(), id, "alpha", hash, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	return s, id, token
}

func validReport(id string, epoch uint64, session, boot string, sequence, rx, tx uint64) protocol.Report {
	return protocol.Report{AgentID: id, Epoch: epoch, SessionID: session, Sequence: sequence, CollectedAt: time.Now().UnixMilli(), Hostname: "vps-1", OS: "debian 12", Arch: "amd64", BootID: boot, Uptime: 100, CPUPercent: 12, Load1: .1, Load5: .2, Load15: .3, RAMUsed: 50, RAMTotal: 100, RAMPercent: 50, SwapUsed: 0, SwapTotal: 0, SwapPercent: 0, DiskUsed: 20, DiskTotal: 100, DiskPercent: 20, RXBytes: rx, TXBytes: tx}
}

func TestMigrationAndAuthentication(t *testing.T) {
	s, id, token := testStore(t, ":memory:")
	defer s.Close()
	ctx := context.Background()
	version, err := s.SchemaVersion(ctx)
	if err != nil || version != currentSchemaVersion {
		t.Fatalf("version=%d err=%v", version, err)
	}
	got, err := s.Authenticate(ctx, token)
	if err != nil || got != id {
		t.Fatalf("authentication got=%q err=%v", got, err)
	}
	if _, err := s.Authenticate(ctx, "wrong"); err != ErrUnauthorized {
		t.Fatalf("wrong token error=%v", err)
	}
	agents, err := s.ListAgents(ctx)
	if err != nil || len(agents) != 1 {
		t.Fatalf("agents=%v err=%v", agents, err)
	}
}

func TestRevokedAuthenticationIsDistinctButUnauthorized(t *testing.T) {
	s, id, token := testStore(t, ":memory:")
	defer s.Close()
	ctx := context.Background()
	if revoked, err := s.RevokeAgent(ctx, id, time.Unix(200, 0)); err != nil || !revoked {
		t.Fatalf("revoke=%t err=%v", revoked, err)
	}
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrAgentRevoked) || !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked authentication error=%v", err)
	}
	if _, err := s.Authenticate(ctx, "wrong"); err != ErrUnauthorized || errors.Is(err, ErrAgentRevoked) {
		t.Fatalf("unknown authentication error=%v", err)
	}
	if _, _, _, err := s.ProcessReport(ctx, id, validReport(id, 1, "session", "boot", 1, 1, 1), time.Unix(201, 0)); !errors.Is(err, ErrAgentRevoked) {
		t.Fatalf("revoked report error=%v", err)
	}
}

func TestAgentDisableEnableLifecyclePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disabled.db")
	s, id, token := testStore(t, path)
	ctx := context.Background()
	disabledAt := time.Unix(200, 0)
	if changed, err := s.DisableAgent(ctx, id, disabledAt); err != nil || !changed {
		t.Fatalf("disable changed=%t err=%v", changed, err)
	}
	if changed, err := s.DisableAgent(ctx, id, disabledAt.Add(time.Second)); err != nil || changed {
		t.Fatalf("idempotent disable changed=%t err=%v", changed, err)
	}
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrAgentDisabled) || errors.Is(err, ErrAgentRevoked) {
		t.Fatalf("disabled authentication error=%v", err)
	}
	agents, err := s.ListAgents(ctx)
	if err != nil || len(agents) != 1 || agents[0].DisabledAt == nil || *agents[0].DisabledAt != disabledAt.UnixMilli() {
		t.Fatalf("disabled agents=%+v err=%v", agents, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrAgentDisabled) {
		t.Fatalf("reopened disabled authentication error=%v", err)
	}
	if changed, err := s.EnableAgent(ctx, id, disabledAt.Add(2*time.Second)); err != nil || !changed {
		t.Fatalf("enable changed=%t err=%v", changed, err)
	}
	if changed, err := s.EnableAgent(ctx, id, disabledAt.Add(3*time.Second)); err != nil || changed {
		t.Fatalf("idempotent enable changed=%t err=%v", changed, err)
	}
	if got, err := s.Authenticate(ctx, token); err != nil || got != id {
		t.Fatalf("enabled authentication id=%q err=%v", got, err)
	}
}

func TestRevokedAgentCannotBeEnabled(t *testing.T) {
	s, id, token := testStore(t, ":memory:")
	defer s.Close()
	ctx := context.Background()
	if changed, err := s.DisableAgent(ctx, id, time.Unix(200, 0)); err != nil || !changed {
		t.Fatalf("disable changed=%t err=%v", changed, err)
	}
	if changed, err := s.RevokeAgent(ctx, id, time.Unix(201, 0)); err != nil || !changed {
		t.Fatalf("revoke changed=%t err=%v", changed, err)
	}
	if changed, err := s.EnableAgent(ctx, id, time.Unix(202, 0)); !errors.Is(err, ErrAgentRevoked) || changed {
		t.Fatalf("enable revoked changed=%t err=%v", changed, err)
	}
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrAgentRevoked) || errors.Is(err, ErrAgentDisabled) {
		t.Fatalf("revoked precedence error=%v", err)
	}
}

func TestRejectsFutureSchemaWithoutSideEffects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v6.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`,
		`INSERT INTO schema_migrations(version,applied_at) VALUES(11,5000)`,
		`CREATE TABLE future_fixture (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO future_fixture(id,value) VALUES(1,'future-data')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	var objectsBefore int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`).Scan(&objectsBefore); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(context.Background(), path)
	if store != nil {
		store.Close()
		t.Fatal("future schema was opened")
	}
	if !errors.Is(err, ErrUnsupportedSchemaVersion) || !strings.Contains(err.Error(), "version 11") {
		t.Fatalf("future schema error=%v", err)
	}

	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	var appliedAt int64
	if err := db.QueryRow(`SELECT version,applied_at FROM schema_migrations`).Scan(&version, &appliedAt); err != nil || version != 11 || appliedAt != 5000 {
		t.Fatalf("migration metadata changed: version=%d applied_at=%d err=%v", version, appliedAt, err)
	}
	var fixtureValue string
	if err := db.QueryRow(`SELECT value FROM future_fixture WHERE id=1`).Scan(&fixtureValue); err != nil || fixtureValue != "future-data" {
		t.Fatalf("future fixture changed: value=%q err=%v", fixtureValue, err)
	}
	var objectsAfter int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`).Scan(&objectsAfter); err != nil || objectsAfter != objectsBefore {
		t.Fatalf("schema side effects: before=%d after=%d err=%v", objectsBefore, objectsAfter, err)
	}
	var agentsTable int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='agents'`).Scan(&agentsTable); err != nil || agentsTable != 0 {
		t.Fatalf("old binary created agents table: count=%d err=%v", agentsTable, err)
	}
	var journalMode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil || journalMode != "delete" {
		t.Fatalf("future schema journal mode changed: mode=%q err=%v", journalMode, err)
	}
}

func TestOpenExistingV4(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v4.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if version, err := store.SchemaVersion(context.Background()); err != nil || version != currentSchemaVersion {
		t.Fatalf("version=%d err=%v", version, err)
	}
}

func TestMultipleAgentsAreIsolated(t *testing.T) {
	s, id1, _ := testStore(t, ":memory:")
	defer s.Close()
	id2, _ := auth.NewID()
	token2, hash2, _ := auth.NewToken()
	if err := s.AddAgent(context.Background(), id2, "beta", hash2, time.Now()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Unix(1000, 0)
	if _, ok, _, err := s.ProcessReport(ctx, id1, validReport(id1, 1, "s1", "b1", 1, 100, 200), now); err != nil || !ok {
		t.Fatal(err)
	}
	if _, ok, _, err := s.ProcessReport(ctx, id1, validReport(id1, 1, "s1", "b1", 2, 150, 260), now.Add(time.Second)); err != nil || !ok {
		t.Fatal(err)
	}
	authenticated2, err := s.Authenticate(ctx, token2)
	if err != nil || authenticated2 != id2 {
		t.Fatal(err)
	}
	if _, ok, _, err := s.ProcessReport(ctx, id2, validReport(id2, 1, "s2", "b2", 1, 900, 800), now); err != nil || !ok {
		t.Fatal(err)
	}
	states, err := s.ListStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]uint64{}
	for _, state := range states {
		values[state.AgentID] = state.RXTotal
	}
	if values[id1] != 50 || values[id2] != 0 {
		t.Fatalf("crossed state: %#v", values)
	}
}

func TestEpochSequenceAndTraffic(t *testing.T) {
	s, id, _ := testStore(t, ":memory:")
	defer s.Close()
	ctx := context.Background()
	at := time.Unix(1000, 0)

	state, ok, _, err := s.ProcessReport(ctx, id, validReport(id, 7, "session-a", "boot-a", 1, 1000, 2000), at)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if state.RXTotal != 0 || state.TXTotal != 0 {
		t.Fatalf("first raw counters were not treated as baseline: %d/%d", state.RXTotal, state.TXTotal)
	}
	state, ok, _, err = s.ProcessReport(ctx, id, validReport(id, 7, "session-a", "boot-a", 2, 1500, 2400), at.Add(10*time.Second))
	if err != nil || !ok {
		t.Fatal(err)
	}
	if state.RXTotal != 500 || state.TXTotal != 400 || state.RXRate != 50 || state.TXRate != 40 {
		t.Fatalf("growth state=%+v", state)
	}

	duplicate, ok, reason, err := s.ProcessReport(ctx, id, validReport(id, 7, "session-a", "boot-a", 2, 9999, 9999), at.Add(11*time.Second))
	if err != nil || ok || reason != "duplicate or stale sequence" || duplicate.RXTotal != 500 {
		t.Fatalf("duplicate accepted=%t reason=%q state=%+v err=%v", ok, reason, duplicate, err)
	}
	lower, ok, reason, err := s.ProcessReport(ctx, id, validReport(id, 7, "session-a", "boot-a", 1, 9999, 9999), at.Add(11*time.Second))
	if err != nil || ok || reason != "duplicate or stale sequence" || lower.RXTotal != 500 {
		t.Fatalf("lower sequence accepted=%t reason=%q state=%+v err=%v", ok, reason, lower, err)
	}
	mismatch, ok, reason, err := s.ProcessReport(ctx, id, validReport(id, 7, "other-session", "boot-a", 3, 9999, 9999), at.Add(12*time.Second))
	if err != nil || ok || reason != "session does not match epoch" || mismatch.RXTotal != 500 {
		t.Fatalf("same epoch with other session accepted=%t reason=%q state=%+v err=%v", ok, reason, mismatch, err)
	}

	// A newer persistent epoch wins even though sequence restarts and the Agent's
	// display clock moved backwards.
	restarted := validReport(id, 8, "session-b", "boot-a", 1, 1600, 2500)
	restarted.CollectedAt = time.Unix(500, 0).UnixMilli()
	state, ok, _, err = s.ProcessReport(ctx, id, restarted, at.Add(20*time.Second))
	if err != nil || !ok {
		t.Fatal(err)
	}
	if state.Epoch != 8 || state.RXTotal != 600 || state.TXTotal != 500 {
		t.Fatalf("agent restart state=%+v", state)
	}

	late := validReport(id, 7, "session-a", "boot-a", 99, 9999, 9999)
	late.CollectedAt = time.Unix(2000, 0).UnixMilli()
	stale, ok, reason, err := s.ProcessReport(ctx, id, late, at.Add(21*time.Second))
	if err != nil || ok || reason != "stale epoch" || stale.Epoch != 8 || stale.RXTotal != 600 {
		t.Fatalf("old epoch accepted=%t reason=%q state=%+v err=%v", ok, reason, stale, err)
	}

	state, ok, _, err = s.ProcessReport(ctx, id, validReport(id, 8, "session-b", "boot-a", 2, 100, 80), at.Add(30*time.Second))
	if err != nil || !ok {
		t.Fatal(err)
	}
	if state.RXTotal != 700 || state.TXTotal != 580 || state.RXRate != 0 || state.TXRate != 0 {
		t.Fatalf("counter rollback state=%+v", state)
	}
	state, ok, _, err = s.ProcessReport(ctx, id, validReport(id, 8, "session-b", "boot-a", 3, 0, 0), at.Add(35*time.Second))
	if err != nil || !ok {
		t.Fatal(err)
	}
	if state.RXTotal != 700 || state.TXTotal != 580 || state.RXRate != 0 || state.TXRate != 0 {
		t.Fatalf("counter reset to zero state=%+v", state)
	}

	state, ok, _, err = s.ProcessReport(ctx, id, validReport(id, 9, "session-c", "boot-b", 1, 300, 400), at.Add(40*time.Second))
	if err != nil || !ok {
		t.Fatal(err)
	}
	if state.RXTotal != 1000 || state.TXTotal != 980 || state.RXRate != 0 || state.TXRate != 0 || state.BootID != "boot-b" {
		t.Fatalf("boot change state=%+v", state)
	}
}

func TestTrafficAndEpochSurviveServerRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe.db")
	s, id, _ := testStore(t, path)
	ctx := context.Background()
	at := time.Unix(1000, 0)
	if _, ok, _, err := s.ProcessReport(ctx, id, validReport(id, 20, "s1", "b1", 1, 1000, 1000), at); err != nil || !ok {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	state, ok, _, err := s.ProcessReport(ctx, id, validReport(id, 20, "s1", "b1", 2, 1200, 1300), at.Add(10*time.Second))
	if err != nil || !ok {
		t.Fatal(err)
	}
	if state.RXTotal != 200 || state.TXTotal != 300 {
		t.Fatalf("totals after server restart=%d/%d", state.RXTotal, state.TXTotal)
	}
	lower, ok, reason, err := s.ProcessReport(ctx, id, validReport(id, 20, "s1", "b1", 1, 9000, 9000), at.Add(11*time.Second))
	if err != nil || ok || reason != "duplicate or stale sequence" || lower.RXTotal != 200 || lower.TXTotal != 300 {
		t.Fatalf("lower sequence after restart accepted=%t reason=%q state=%+v err=%v", ok, reason, lower, err)
	}
	state, ok, _, err = s.ProcessReport(ctx, id, validReport(id, 21, "s2", "b1", 1, 1400, 1500), at.Add(12*time.Second))
	if err != nil || !ok || state.RXTotal != 400 || state.TXTotal != 500 {
		t.Fatalf("new epoch after restart state=%+v accepted=%t err=%v", state, ok, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	stale, ok, reason, err := s.ProcessReport(ctx, id, validReport(id, 20, "s1", "b1", 100, 9000, 9000), at.Add(20*time.Second))
	if err != nil || ok || reason != "stale epoch" || stale.RXTotal != 400 || stale.TXTotal != 500 {
		t.Fatalf("persisted epoch lost: accepted=%t reason=%q state=%+v err=%v", ok, reason, stale, err)
	}
	state, ok, _, err = s.ProcessReport(ctx, id, validReport(id, 22, "s3", "b2", 1, 300, 200), at.Add(30*time.Second))
	if err != nil || !ok || state.RXTotal != 700 || state.TXTotal != 700 {
		t.Fatalf("new boot after restart state=%+v accepted=%t err=%v", state, ok, err)
	}
}

func TestPermanentTrafficSQLiteBoundaryIsAtomic(t *testing.T) {
	s, id, _ := testStore(t, ":memory:")
	defer s.Close()
	ctx := context.Background()
	at := time.Unix(1000, 0)
	max := uint64(math.MaxInt64)
	if _, ok, _, err := s.ProcessReport(ctx, id, validReport(id, 1, "s1", "b1", 1, max, max), at); err != nil || !ok {
		t.Fatalf("baseline accepted=%t err=%v", ok, err)
	}
	state, ok, _, err := s.ProcessReport(ctx, id, validReport(id, 2, "s2", "b2", 1, max, max), at.Add(time.Second))
	if err != nil || !ok || state.RXTotal != max || state.TXTotal != max {
		t.Fatalf("max boundary state=%+v accepted=%t err=%v", state, ok, err)
	}
	if _, ok, _, err := s.ProcessReport(ctx, id, validReport(id, 3, "s3", "b3", 1, 1, 1), at.Add(2*time.Second)); err == nil || ok {
		t.Fatalf("MaxInt64+1 accepted=%t err=%v", ok, err)
	}
	states, err := s.ListStates(ctx)
	if err != nil || len(states) != 1 {
		t.Fatalf("states=%v err=%v", states, err)
	}
	if states[0].Epoch != 2 || states[0].RXTotal != max || states[0].TXTotal != max {
		t.Fatalf("overflow changed persisted state: %+v", states[0])
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := aggregateMinuteTx(ctx, tx, State{AgentID: id, RXTotal: max + 1}); err == nil {
		t.Fatal("minute aggregation accepted MaxInt64+1")
	}
}

func TestMigratesV1StateToV4(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`,
		`INSERT INTO schema_migrations(version,applied_at) VALUES(1,1)`,
		`CREATE TABLE agents (id TEXT PRIMARY KEY, name TEXT NOT NULL, token_hash BLOB NOT NULL UNIQUE, revoked INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE agent_sessions (agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE, session_id TEXT NOT NULL, started_at INTEGER NOT NULL, active INTEGER NOT NULL, first_seen INTEGER NOT NULL, PRIMARY KEY(agent_id,session_id))`,
		`CREATE TABLE agent_state (agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE, session_id TEXT NOT NULL, session_started_at INTEGER NOT NULL, sequence INTEGER NOT NULL, boot_id TEXT NOT NULL, hostname TEXT NOT NULL, os TEXT NOT NULL, arch TEXT NOT NULL, uptime INTEGER NOT NULL, cpu REAL NOT NULL, load1 REAL NOT NULL, load5 REAL NOT NULL, load15 REAL NOT NULL, ram_used INTEGER NOT NULL, ram_total INTEGER NOT NULL, ram_percent REAL NOT NULL, swap_used INTEGER NOT NULL, swap_total INTEGER NOT NULL, swap_percent REAL NOT NULL, disk_used INTEGER NOT NULL, disk_total INTEGER NOT NULL, disk_percent REAL NOT NULL, raw_rx INTEGER NOT NULL, raw_tx INTEGER NOT NULL, rx_rate REAL NOT NULL, tx_rate REAL NOT NULL, rx_total INTEGER NOT NULL, tx_total INTEGER NOT NULL, collected_at INTEGER NOT NULL, last_seen INTEGER NOT NULL)`,
		`INSERT INTO agents(id,name,token_hash,created_at,updated_at) VALUES('old-agent','old-name',x'01',1,1)`,
		`INSERT INTO agent_sessions(agent_id,session_id,started_at,active,first_seen) VALUES('old-agent','old-session',1000,1,1000)`,
		`INSERT INTO agent_state VALUES('old-agent','old-session',1000,5,'old-boot','old-host','linux','amd64',100,1,1,1,1,1,2,50,0,0,0,1,2,50,100,200,1,2,10,20,1000,1000)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatalf("prepare v1: %v\n%s", err, statement)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	version, err := s.SchemaVersion(context.Background())
	if err != nil || version != currentSchemaVersion {
		t.Fatalf("version=%d err=%v", version, err)
	}
	states, err := s.ListStates(context.Background())
	if err != nil || len(states) != 1 {
		t.Fatalf("states=%v err=%v", states, err)
	}
	state := states[0]
	if state.Epoch != 0 || state.SessionID != "old-session" || state.Sequence != 5 || state.RXBytes != 100 || state.TXBytes != 200 || state.RXTotal != 10 || state.TXTotal != 20 {
		t.Fatalf("v1 state not preserved: %+v", state)
	}
	var indexes int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_agents_active_token_hash'`).Scan(&indexes); err != nil || indexes != 1 {
		t.Fatalf("token index count=%d err=%v", indexes, err)
	}
}

func TestTokenCannotWriteAnotherAgent(t *testing.T) {
	s, id, _ := testStore(t, ":memory:")
	defer s.Close()
	other, _ := auth.NewID()
	_, ok, _, err := s.ProcessReport(context.Background(), id, validReport(other, 1, "s", "b", 1, 1, 1), time.Now())
	if err != ErrUnauthorized || ok {
		t.Fatalf("cross-agent write accepted=%t err=%v", ok, err)
	}
}
