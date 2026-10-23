package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"404-probe/internal/auth"
)

const m35MiB = int64(1) << 20

// m35Fingerprint hashes every table (rows, sorted) and every schema statement, apart from the skipped tables.
func m35Fingerprint(t *testing.T, db *sql.DB, skip ...string) map[string]string {
	t.Helper()
	skipped := map[string]bool{}
	for _, name := range skip {
		skipped[name] = true
	}
	names, err := db.Query(`SELECT name,COALESCE(sql,'') FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	type table struct{ name, ddl string }
	var tables []table
	for names.Next() {
		var item table
		if err := names.Scan(&item.name, &item.ddl); err != nil {
			t.Fatal(err)
		}
		if !skipped[item.name] {
			tables = append(tables, item)
		}
	}
	names.Close()
	out := map[string]string{}
	for _, item := range tables {
		rows, err := db.Query(`SELECT * FROM ` + item.name)
		if err != nil {
			t.Fatal(err)
		}
		columns, _ := rows.Columns()
		var lines []string
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			lines = append(lines, fmt.Sprintf("%v", values))
		}
		rows.Close()
		sort.Strings(lines)
		sum := sha256.Sum256([]byte(item.ddl + "\n" + strings.Join(lines, "\n")))
		out[item.name] = fmt.Sprintf("%d:%s", len(lines), hex.EncodeToString(sum[:8]))
	}
	return out
}

func m35Columns(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info('` + table + `')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out[name] = true
	}
	return out
}

func m35TableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func m35Version(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := schemaVersion(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func m35Clock(t *testing.T, at int64) {
	t.Helper()
	migration35Clock = func() time.Time { return time.UnixMilli(at) }
	t.Cleanup(func() { migration35Clock = time.Now })
}

type m35Plan struct {
	name       string
	mode       string
	usage      int64
	status     string // "" = partial
	cycleStart int64  // 0 = no active cycle
	before     *[2]int64
	after      *[2]int64
	end        *[2]int64 // agent_state totals; nil = no state row
	// expectation
	basis    string
	rx, tx   int64
	sinceAt  int64 // expected cycle_dir_since for backfill (the anchor minute); 0 = the migration clock
	noSince  bool
	noMinute bool
}

// m35Seed writes one device per plan with the fixtures of the case and returns the agent ids.
func m35Seed(t *testing.T, s *Store, plans []m35Plan) []string {
	t.Helper()
	ids := make([]string, len(plans))
	for i, p := range plans {
		id, err := auth.NewID()
		if err != nil {
			t.Fatal(err)
		}
		_, hash, err := auth.NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AddAgent(context.Background(), id, p.name, hash, time.Unix(100, 0)); err != nil {
			t.Fatal(err)
		}
		ids[i] = id
		if p.end != nil {
			tx, err := s.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err := writeStateTx(context.Background(), tx, State{AgentID: id, SessionID: "s", Hostname: "h", OS: "linux", Arch: "amd64", RXTotal: uint64(p.end[0]), TXTotal: uint64(p.end[1]), LastSeen: 1, CollectedAt: 1}); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
		minute := func(bucket int64, totals [2]int64) {
			if _, err := s.db.Exec(`INSERT INTO minute_metrics(agent_id,bucket,samples,cpu_sum,ram_sum,swap_sum,disk_sum,load1_sum,load5_sum,load15_sum,rx_rate_sum,tx_rate_sum,rx_total,tx_total) VALUES(?,?,1,0,0,0,0,0,0,0,0,0,?,?)`, id, bucket, totals[0], totals[1]); err != nil {
				t.Fatal(err)
			}
		}
		if p.cycleStart > 0 {
			if p.before != nil {
				minute(p.cycleStart-m35Minute*3, *p.before)
			}
			if p.after != nil {
				minute(p.cycleStart+m35Minute*2, *p.after)
			}
		}
		var start, end any
		if p.cycleStart > 0 {
			start, end = p.cycleStart, p.cycleStart+30*24*3600*1000
		}
		status := p.status
		if status == "" {
			status = "partial"
		}
		if _, err := s.db.Exec(`INSERT INTO agent_plans(agent_id,traffic_mode,cycle_kind,cycle_count,cycle_anchor,timezone,usage_bytes,usage_status,cycle_start,cycle_end,updated_at,observe_after) VALUES(?,?,?,?,?,?,?,?,?,?,?,0)`,
			id, nullable(p.mode), "monthly", 1, "2026-10-01", "UTC", p.usage, status, start, end, 1000); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

const m35Minute = int64(60000)

// m35Cases are the plans the migration has to place. cycleStart is the same for every case.
func m35Cases() []m35Plan {
	const start = int64(1_790_000_000_000) / 60000 * 60000
	anchor := [2]int64{5_000 * m35MiB, 9_000 * m35MiB}
	return []m35Plan{
		{name: "sum exact", mode: "sum", usage: 400 * m35MiB, cycleStart: start, before: &anchor, end: &[2]int64{anchor[0] + 300*m35MiB, anchor[1] + 100*m35MiB},
			basis: planDirBackfill, rx: 300 * m35MiB, tx: 100 * m35MiB, sinceAt: start - 3*m35Minute},
		{name: "sum positive residual", mode: "sum", usage: 401 * m35MiB, cycleStart: start, before: &anchor, end: &[2]int64{anchor[0] + 300*m35MiB, anchor[1] + 100*m35MiB},
			basis: planDirBackfill, rx: 300*m35MiB + (1*m35MiB*300*m35MiB)/(400*m35MiB), tx: 401*m35MiB - (300*m35MiB + (1*m35MiB*300*m35MiB)/(400*m35MiB)), sinceAt: start - 3*m35Minute},
		{name: "sum negative residual", mode: "sum", usage: 399 * m35MiB, cycleStart: start, before: &anchor, end: &[2]int64{anchor[0] + 300*m35MiB, anchor[1] + 100*m35MiB},
			basis: planDirBackfill, rx: 300*m35MiB - m35MiB*3/4, tx: 399*m35MiB - (300*m35MiB - m35MiB*3/4), sinceAt: start - 3*m35Minute},
		{name: "rx plan", mode: "rx", usage: 305 * m35MiB, cycleStart: start, before: &anchor, end: &[2]int64{anchor[0] + 300*m35MiB, anchor[1] + 100*m35MiB},
			basis: planDirBackfill, rx: 305 * m35MiB, tx: 100 * m35MiB, sinceAt: start - 3*m35Minute},
		{name: "tx plan", mode: "tx", usage: 95 * m35MiB, cycleStart: start, before: &anchor, end: &[2]int64{anchor[0] + 300*m35MiB, anchor[1] + 100*m35MiB},
			basis: planDirBackfill, rx: 300 * m35MiB, tx: 95 * m35MiB, sinceAt: start - 3*m35Minute},
		{name: "tolerance exceeded (observe_after like gap)", mode: "sum", usage: 500 * m35MiB, cycleStart: start, before: &anchor, end: &[2]int64{anchor[0] + 300*m35MiB, anchor[1] + 100*m35MiB},
			basis: planDirSince},
		{name: "calibrated", mode: "sum", usage: 400 * m35MiB, status: "calibrated", cycleStart: start, before: &anchor, end: &[2]int64{anchor[0] + 300*m35MiB, anchor[1] + 100*m35MiB},
			basis: planDirSince},
		{name: "zero vector with usage", mode: "sum", usage: 1 * m35MiB, cycleStart: start, before: &anchor, end: &[2]int64{anchor[0], anchor[1]},
			basis: planDirSince},
		{name: "zero vector without usage", mode: "sum", usage: 0, cycleStart: start, before: &anchor, end: &[2]int64{anchor[0], anchor[1]},
			basis: planDirBackfill, rx: 0, tx: 0, sinceAt: start - 3*m35Minute},
		{name: "no minute rows", mode: "sum", usage: 400 * m35MiB, cycleStart: start, end: &[2]int64{anchor[0] + 300*m35MiB, anchor[1] + 100*m35MiB},
			basis: planDirSince},
		{name: "no state row", mode: "sum", usage: 400 * m35MiB, cycleStart: start, before: &anchor,
			basis: planDirSince},
		{name: "counter went backwards", mode: "sum", usage: 400 * m35MiB, cycleStart: start, before: &anchor, end: &[2]int64{anchor[0] - 1, anchor[1] + 400*m35MiB},
			basis: planDirSince},
		{name: "cycle older than the kept history", mode: "sum", usage: 400 * m35MiB, cycleStart: start, after: &anchor, end: &[2]int64{anchor[0] + 300*m35MiB, anchor[1] + 100*m35MiB},
			basis: planDirBackfill, rx: 300 * m35MiB, tx: 100 * m35MiB, sinceAt: start + 2*m35Minute},
		{name: "no billing mode", mode: "", usage: 0, cycleStart: 0, basis: planDirExact, noSince: true},
		{name: "mode without cycle, no usage", mode: "rx", usage: 0, cycleStart: 0, basis: planDirExact, noSince: true},
		{name: "mode without cycle, with usage", mode: "rx", usage: 5, cycleStart: 0, basis: planDirSince},
	}
}

func TestMigration35BackfillsPlansFromMinuteData(t *testing.T) {
	const clock = int64(1_790_100_000_000)
	m35Clock(t, clock)
	path := filepath.Join(t.TempDir(), "m35.db")
	s, _, _ := testStore(t, path)
	rewindV107Fixture(t, s.db)
	cases := m35Cases()
	ids := m35Seed(t, s, cases)
	s.Close()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i, c := range cases {
		var rx, tx int64
		var basis string
		var since sql.NullInt64
		var usage int64
		if err := s.db.QueryRow(`SELECT cycle_rx_bytes,cycle_tx_bytes,cycle_dir_basis,cycle_dir_since,usage_bytes FROM agent_plans WHERE agent_id=?`, ids[i]).Scan(&rx, &tx, &basis, &since, &usage); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if usage != c.usage {
			t.Fatalf("%s: the migration changed usage_bytes %d -> %d", c.name, c.usage, usage)
		}
		if basis != c.basis {
			t.Fatalf("%s: basis %s, want %s", c.name, basis, c.basis)
		}
		wantRX, wantTX := c.rx, c.tx
		if c.basis == planDirSince || c.basis == planDirExact {
			wantRX, wantTX = 0, 0
		}
		if rx != wantRX || tx != wantTX {
			t.Fatalf("%s: counters %d/%d, want %d/%d", c.name, rx, tx, wantRX, wantTX)
		}
		switch {
		case c.basis == planDirExact:
			if since.Valid {
				t.Fatalf("%s: exact plans have no since mark", c.name)
			}
		case c.basis == planDirSince:
			if !since.Valid || since.Int64 != clock {
				t.Fatalf("%s: since %v, want the migration time", c.name, since)
			}
		default:
			if !since.Valid || since.Int64 != c.sinceAt {
				t.Fatalf("%s: since %v, want the anchor minute %d", c.name, since, c.sinceAt)
			}
		}
		if reason := planDirectionError(c.mode, basis, uint64(usage), uint64(rx), uint64(tx)); reason != "" {
			t.Fatalf("%s: invariant: %s", c.name, reason)
		}
	}
}

func TestMigration35SchemaIsComplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	s, id, _ := testStore(t, path)
	defer s.Close()
	if version, err := s.SchemaVersion(context.Background()); err != nil || version != 35 {
		t.Fatalf("schema %d %v", version, err)
	}
	plans := m35Columns(t, s.db, "agent_plans")
	for _, name := range []string{"cycle_rx_bytes", "cycle_tx_bytes", "cycle_dir_basis", "cycle_dir_since"} {
		if !plans[name] {
			t.Fatalf("agent_plans lacks %s", name)
		}
	}
	if !m35Columns(t, s.db, "quality_config")["slow_threshold_ms"] {
		t.Fatal("quality_config lacks slow_threshold_ms")
	}
	for _, table := range []string{"tcp_mini_state", "tcp_mini_high"} {
		if m35TableExists(t, s.db, table) {
			t.Fatalf("%s still exists on a new database", table)
		}
	}
	// A new database starts every counter at zero, "exact", with the default threshold.
	if _, err := s.db.Exec(`INSERT INTO agent_plans(agent_id,traffic_mode,usage_bytes,updated_at) VALUES(?,'sum',0,1)`, id); err != nil {
		t.Fatal(err)
	}
	var rx, tx int64
	var basis string
	var since sql.NullInt64
	if err := s.db.QueryRow(`SELECT cycle_rx_bytes,cycle_tx_bytes,cycle_dir_basis,cycle_dir_since FROM agent_plans WHERE agent_id=?`, id).Scan(&rx, &tx, &basis, &since); err != nil || rx != 0 || tx != 0 || basis != "exact" || since.Valid {
		t.Fatalf("defaults %d %d %s %v %v", rx, tx, basis, since, err)
	}
	if _, err := s.db.Exec(`UPDATE agent_plans SET cycle_dir_basis='other' WHERE agent_id=?`, id); err == nil {
		t.Fatal("cycle_dir_basis accepts a foreign value")
	}
	if _, err := s.db.Exec(`UPDATE agent_plans SET cycle_rx_bytes=-1 WHERE agent_id=?`, id); err == nil {
		t.Fatal("cycle_rx_bytes accepts a negative value")
	}
	var threshold string
	if err := s.db.QueryRow(`SELECT dflt_value FROM pragma_table_info('quality_config') WHERE name='slow_threshold_ms'`).Scan(&threshold); err != nil || threshold != "500" {
		t.Fatalf("threshold default %q %v", threshold, err)
	}
	if _, err := s.db.Exec(`INSERT INTO quality_config(agent_id,revision,enabled,ipv6,choices,updated_at) VALUES(?,1,0,0,'[]',1)`, id); err != nil {
		t.Fatal(err)
	}
	var stored int
	if err := s.db.QueryRow(`SELECT slow_threshold_ms FROM quality_config WHERE agent_id=?`, id).Scan(&stored); err != nil || stored != 500 {
		t.Fatalf("a configuration row starts at %d %v", stored, err)
	}
	for _, ok := range []int{50, 3000} {
		if _, err := s.db.Exec(`UPDATE quality_config SET slow_threshold_ms=? WHERE agent_id=?`, ok, id); err != nil {
			t.Fatalf("%d refused: %v", ok, err)
		}
	}
	for _, bad := range []int{49, 3001, 0, -1} {
		if _, err := s.db.Exec(`UPDATE quality_config SET slow_threshold_ms=? WHERE agent_id=?`, bad, id); err == nil {
			t.Fatalf("slow_threshold_ms accepts %d", bad)
		}
	}
}

func TestMigration35From34KeepsEverythingElseAndDropsTCPMini(t *testing.T) {
	m35Clock(t, 1_790_100_000_000)
	path := filepath.Join(t.TempDir(), "up.db")
	s, _, _ := testStore(t, path)
	rewindV107Fixture(t, s.db)
	cases := m35Cases()
	m35Seed(t, s, cases)
	var states int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tcp_mini_state`).Scan(&states); err != nil || states == 0 {
		t.Fatalf("fixture: tcp_mini_state rows %d %v", states, err)
	}
	before := m35Fingerprint(t, s.db, "tcp_mini_state", "tcp_mini_high", "agent_plans", "quality_config", "schema_migrations")
	s.Close()
	if v := m35Version(t, path); v != 34 {
		t.Fatalf("fixture schema %d", v)
	}
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	after := m35Fingerprint(t, s.db, "agent_plans", "quality_config", "schema_migrations")
	for table, want := range before {
		if after[table] != want {
			t.Errorf("%s changed by migration 35", table)
		}
	}
	for table := range after {
		if _, ok := before[table]; !ok {
			t.Errorf("migration 35 created %s", table)
		}
	}
	for _, table := range []string{"tcp_mini_state", "tcp_mini_high"} {
		if m35TableExists(t, s.db, table) {
			t.Fatalf("%s survived", table)
		}
	}
	if version, err := s.SchemaVersion(context.Background()); err != nil || version != 35 {
		t.Fatalf("schema %d %v", version, err)
	}
}

func TestMigration35IsIdempotentAndRepeatableOnAnOperatorCopy(t *testing.T) {
	m35Clock(t, 1_790_100_000_000)
	path := filepath.Join(t.TempDir(), "again.db")
	s, _, _ := testStore(t, path)
	rewindV107Fixture(t, s.db)
	m35Seed(t, s, m35Cases())
	s.Close()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	first := m35Fingerprint(t, s.db, "schema_migrations")
	// Run the migration step a second time on the migrated database: every structural step is skipped,
	// the backfill recomputes the same values and the tables stay gone.
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM schema_migrations WHERE version=35`); err != nil {
		t.Fatal(err)
	}
	if err := migrateV107Tx(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	second := m35Fingerprint(t, s.db, "schema_migrations")
	for table, want := range first {
		if second[table] != want {
			t.Errorf("%s differs after a second run", table)
		}
	}
	s.Close()
	s, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestMigration35FailureAtEveryStageLeavesSchema34Untouched(t *testing.T) {
	m35Clock(t, 1_790_100_000_000)
	for _, stage := range []string{"structure", "backfill", "retire", "validation"} {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fail.db")
			s, _, _ := testStore(t, path)
			rewindV107Fixture(t, s.db)
			m35Seed(t, s, m35Cases())
			before := m35Fingerprint(t, s.db)
			s.Close()

			boom := errors.New("injected " + stage)
			migration35FailAt = func(at string) error {
				if at == stage {
					return boom
				}
				return nil
			}
			t.Cleanup(func() { migration35FailAt = nil })
			if s, err := Open(context.Background(), path); err == nil {
				s.Close()
				t.Fatal("the injected failure was ignored")
			} else if !errors.Is(err, boom) {
				t.Fatalf("unexpected error: %v", err)
			}
			if v := m35Version(t, path); v != 34 {
				t.Fatalf("failed migration left schema %d", v)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			after := m35Fingerprint(t, db)
			db.Close()
			for table, want := range before {
				if after[table] != want {
					t.Errorf("%s changed by a failed migration", table)
				}
			}
			for table := range after {
				if _, ok := before[table]; !ok {
					t.Errorf("a failed migration left %s", table)
				}
			}

			migration35FailAt = nil
			s, err = Open(context.Background(), path)
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			defer s.Close()
			if version, err := s.SchemaVersion(context.Background()); err != nil || version != 35 {
				t.Fatalf("retry schema %d %v", version, err)
			}
		})
	}
}

// A database below schema 29 walks the whole chain: the historical TCP Mini migration creates the tables
// and migration 35 removes them again.
func TestMigration35OldDatabaseWalksTheWholeChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, _, _ := testStore(t, path)
	dropQualityFixtureSchema(t, s.db)
	if m35TableExists(t, s.db, "tcp_mini_state") || m35TableExists(t, s.db, "quality_config") {
		t.Fatal("fixture: the database should be below schema 23")
	}
	s.Close()
	if v := m35Version(t, path); v >= 29 {
		t.Fatalf("fixture schema %d", v)
	}
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	defer s.Close()
	if version, err := s.SchemaVersion(context.Background()); err != nil || version != 35 {
		t.Fatalf("schema %d %v", version, err)
	}
	if m35TableExists(t, s.db, "tcp_mini_state") || m35TableExists(t, s.db, "tcp_mini_high") {
		t.Fatal("tcp_mini tables survived the chain")
	}
	if !m35Columns(t, s.db, "agent_plans")["cycle_dir_basis"] || !m35Columns(t, s.db, "quality_config")["slow_threshold_ms"] {
		t.Fatal("schema 35 columns missing after the chain")
	}
}

func TestReconcilePlanDirectionsFixturesOfTheReview(t *testing.T) {
	type tc struct {
		name            string
		mode            string
		usage, rxB, txB uint64
		wantOK          bool
		wantRX, wantTX  uint64
	}
	const maxInt = uint64(math.MaxInt64)
	for _, c := range []tc{
		{"B5 zero vector with usage", "sum", uint64(m35MiB), 0, 0, false, 0, 0},
		{"B5 zero vector without usage", "sum", 0, 0, 0, true, 0, 0},
		{"B5 (1,1,1): floor of a negative residual", "sum", 1, 1, 1, true, 0, 1},
		{"B5 (2,1,1): exact", "sum", 2, 1, 1, true, 1, 1},
		{"B5 (3,1,1): floor of a positive residual", "sum", 3, 1, 1, true, 1, 2},
		{"all traffic in one direction", "sum", 10, 0, 10, true, 0, 10},
		{"rx plan keeps the real tx", "rx", 100, 90, 7, true, 100, 7},
		{"tx plan keeps the real rx", "tx", 100, 7, 90, true, 7, 100},
		{"rx plan outside tolerance", "rx", uint64(100 * m35MiB), uint64(10 * m35MiB), 0, false, 0, 0},
		{"near MaxInt64, exact", "sum", maxInt, maxInt / 2, maxInt - maxInt/2, true, maxInt / 2, maxInt - maxInt/2},
		{"near MaxInt64, usage below the minute data", "sum", maxInt - 1000, maxInt / 2, maxInt - maxInt/2, true, (maxInt-1000)/2 - 1, 0},
		{"minute data sum above MaxInt64 but each direction legal", "sum", maxInt, maxInt, 1, true, maxInt - 1, 1},
		{"unknown mode", "none", 1, 1, 1, false, 0, 0},
	} {
		rx, tx, ok := reconcilePlanDirections(c.mode, c.usage, c.rxB, c.txB)
		if ok != c.wantOK {
			t.Errorf("%s: ok=%v, want %v", c.name, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if c.name == "near MaxInt64, usage below the minute data" {
			if rx+tx != c.usage || rx > c.usage {
				t.Errorf("%s: %d+%d != %d", c.name, rx, tx, c.usage)
			}
			continue
		}
		if rx != c.wantRX || tx != c.wantTX {
			t.Errorf("%s: %d/%d, want %d/%d", c.name, rx, tx, c.wantRX, c.wantTX)
		}
		if reason := planDirectionError(c.mode, planDirBackfill, c.usage, rx, tx); reason != "" {
			t.Errorf("%s: invariant: %s", c.name, reason)
		}
	}
}

// Exhaustive small values: for every (usage, rx, tx) the share is within [0, usage], adds up exactly and
// equals floor(usage*rx/(rx+tx)) as the review's closed form says.
func TestReconcilePlanDirectionsSmallValuesExhaustive(t *testing.T) {
	for usage := uint64(0); usage <= 24; usage++ {
		for rxB := uint64(0); rxB <= 24; rxB++ {
			for txB := uint64(0); txB <= 24; txB++ {
				rx, tx, ok := reconcilePlanDirections("sum", usage, rxB, txB)
				if rxB+txB == 0 {
					if ok != (usage == 0) {
						t.Fatalf("zero vector (%d): ok=%v", usage, ok)
					}
					continue
				}
				if !ok {
					t.Fatalf("(%d,%d,%d) inside the tolerance was refused", usage, rxB, txB)
				}
				if rx+tx != usage {
					t.Fatalf("(%d,%d,%d): %d+%d", usage, rxB, txB, rx, tx)
				}
				if want := usage * rxB / (rxB + txB); rx != want {
					t.Fatalf("(%d,%d,%d): rx %d, want floor %d", usage, rxB, txB, rx, want)
				}
			}
		}
	}
}
