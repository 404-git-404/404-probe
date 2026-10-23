package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"404-probe/internal/auth"
)

type m34Interval struct {
	slot, family, proto string
	revision            int64
	target              string
	endAt, configUntil  any
}

type m34Spec struct {
	name              string
	noConfig          bool
	revision          int64
	enabled           bool
	regionID, choices string
	lanes             any
	targets           map[string]struct {
		payload string
		retired any
	}
	slots                    []m34Slot
	intervals                []m34Interval
	revoked, alpine, history bool
}

type m34Slot struct {
	slot, family, proto, target string
	revision                    int64
}

const m34DefaultChoices = `[{"slot":"telecom","source":"catalog","protocol":"tcp"},{"slot":"unicom","source":"catalog","protocol":"tcp"},{"slot":"mobile","source":"catalog","protocol":"tcp"}]`

func m34Hex(n int) string { return fmt.Sprintf("%032x", n) }

// m34Open opens a schema 33 database (or 32 with the R23 column removed) without running migration 34.
func m34Open(t *testing.T, path string, from32 bool) *Store {
	t.Helper()
	withoutMigration34(t)
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if from32 {
		for _, q := range []string{`ALTER TABLE agent_google_status DROP COLUMN youtube_premium_notice`, `DELETE FROM schema_migrations WHERE version=33`} {
			if _, err := s.db.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
	}
	return s
}

func m34Seed(t *testing.T, s *Store, spec m34Spec, serial int) string {
	t.Helper()
	ctx := context.Background()
	id, err := auth.NewID()
	if err != nil {
		t.Fatal(err)
	}
	_, hash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddAgent(ctx, id, spec.name, hash, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %s: %v", spec.name, query, err)
		}
	}
	if spec.revoked {
		exec(`UPDATE agents SET revoked=1 WHERE id=?`, id)
	}
	if !spec.noConfig {
		var lanes any
		if spec.lanes != nil {
			lanes = spec.lanes
		}
		choices := spec.choices
		if choices == "" {
			choices = m34DefaultChoices
		}
		exec(`INSERT INTO quality_config(agent_id,revision,enabled,ipv6,choices,updated_at,region_id,v2_lanes) VALUES(?,?,?,?,?,?,?,?)`, id, spec.revision, spec.enabled, true, choices, 1000, spec.regionID, lanes)
	}
	ids := map[string]string{}
	n := 0
	for key, target := range spec.targets {
		n++
		tid := m34Hex(serial*100 + n)
		ids[key] = tid
		exec(`INSERT INTO quality_targets(id,agent_id,fingerprint,payload,created_at,retired_at) VALUES(?,?,?,?,?,?)`, tid, id, tid, target.payload, 1000, target.retired)
	}
	for _, slot := range spec.slots {
		var tid any
		if slot.target != "" {
			tid = ids[slot.target]
		}
		exec(`INSERT INTO quality_slots(agent_id,slot,family,protocol,revision,target_id,enabled) VALUES(?,?,?,?,?,?,1)`, id, slot.slot, slot.family, slot.proto, slot.revision, tid)
	}
	for _, iv := range spec.intervals {
		exec(`INSERT INTO quality_intervals(agent_id,slot,family,protocol,revision,target_id,start_at,end_at,config_from,config_until,v1_authorized) VALUES(?,?,?,?,?,?,?,?,?,?,1)`,
			id, iv.slot, iv.family, iv.proto, iv.revision, ids[iv.target], 2000, iv.endAt, iv.revision, iv.configUntil)
	}
	if spec.history {
		var anyTarget string
		for _, tid := range ids {
			anyTarget = tid
			break
		}
		exec(`INSERT INTO quality_raw(agent_id,target_id,epoch,session_id,seq,slot_revision,config_revision,sample_time,received_at,scheduled_at,finished_at,duration_ms,outcome,resolved_ip,raw_sent,raw_received,content_hash,attempt,success,latency,packet_sent,packet_received) VALUES(?,?,1,'s',1,1,1,3000,3000,3000,3020,20,'success','127.0.0.1',0,0,zeroblob(32),1,1,12,0,0)`, id, anyTarget)
		exec(`INSERT INTO quality_minute(agent_id,target_id,bucket,count,attempts,successes,latency_sum,sent,received) VALUES(?,?,0,1,1,1,12,0,0)`, id, anyTarget)
	}
	if spec.alpine {
		exec(`INSERT INTO quality_capability(agent_id,epoch,session_id,supported,excluded_reason) VALUES(?,1,'s',1,'platform_excluded:alpine')`, id)
	}
	return id
}

func m34Catalog(slots []string, revision int64) ([]m34Slot, []m34Interval, map[string]struct {
	payload string
	retired any
}) {
	targets := map[string]struct {
		payload string
		retired any
	}{}
	var rows []m34Slot
	var intervals []m34Interval
	for _, slot := range slots {
		for _, family := range []string{"ipv4", "ipv6"} {
			key := slot + family
			targets[key] = struct {
				payload string
				retired any
			}{fmt.Sprintf(`{"slot":"%s","family":"%s","protocol":"tcp","source":"catalog","region":"x","host":"h.%s.example.com","port":80,"slot_revision":"","status":""}`, slot, family, slot), nil}
			rows = append(rows, m34Slot{slot, family, "tcp", key, revision})
			if family == "ipv4" {
				intervals = append(intervals, m34Interval{slot, family, "tcp", revision, key, nil, nil})
			}
		}
	}
	return rows, intervals, targets
}

func m34Specs() []m34Spec {
	all := []string{"telecom", "unicom", "mobile"}
	var specs []m34Spec
	slotsA, intervalsA, targetsA := m34Catalog(all, 5)
	province := m34Spec{name: "province", revision: 5, enabled: true, regionID: `["province","河北",null]`, slots: slotsA, intervals: intervalsA, targets: targetsA, history: true}
	specs = append(specs, province)
	city := province
	city.name, city.regionID = "city", `["city","广东","深圳"]`
	specs = append(specs, city)
	unknown := province
	unknown.name, unknown.regionID = "unknown province", `["province","香港",null]`
	specs = append(specs, unknown)
	none := province
	none.name, none.regionID = "unselected", ""
	specs = append(specs, none)
	paused := province
	paused.name, paused.enabled, paused.revision = "paused", false, 6
	paused.intervals = nil
	for _, slot := range all {
		paused.intervals = append(paused.intervals, m34Interval{slot, "ipv4", "tcp", 3, slot + "ipv4", int64(4000), int64(5)})
	}
	specs = append(specs, paused)
	fromChoices := province
	fromChoices.name, fromChoices.regionID = "choices region", ""
	fromChoices.choices = `[{"slot":"telecom","source":"catalog","protocol":"tcp","region":"[\"province\",\"江苏\",null]"},{"slot":"unicom","source":"catalog","protocol":"tcp"},{"slot":"mobile","source":"catalog","protocol":"tcp"}]`
	specs = append(specs, fromChoices)
	fromLanes := province
	fromLanes.name, fromLanes.regionID = "lanes region", ""
	fromLanes.lanes = `[{"slot":"mobile","family":"ipv4","protocol":"tcp","source":"catalog","region":"[\"city\",\"四川\",\"成都\"]","enabled":true}]`
	specs = append(specs, fromLanes)
	specs = append(specs, m34Spec{name: "enabled without targets", revision: 2, enabled: true})
	orphan := province
	orphan.name, orphan.noConfig = "no config row", true
	orphan.slots = append([]m34Slot(nil), slotsA...)
	orphan.slots[0].revision = 7
	specs = append(specs, orphan)
	unreadable := province
	unreadable.name, unreadable.choices, unreadable.regionID = "unreadable choices", "not json", ""
	specs = append(specs, unreadable)
	damaged := province
	damaged.name = "damaged payload"
	damaged.targets = map[string]struct {
		payload string
		retired any
	}{}
	for key, target := range targetsA {
		target.payload = "not json at all"
		damaged.targets[key] = target
	}
	specs = append(specs, damaged)
	retired := province
	retired.name = "already retired"
	retired.targets = map[string]struct {
		payload string
		retired any
	}{}
	for key, target := range targetsA {
		target.retired = int64(123)
		retired.targets[key] = target
	}
	retired.intervals = nil
	specs = append(specs, retired)
	alpine := province
	alpine.name, alpine.alpine = "alpine", true
	specs = append(specs, alpine)
	revoked := province
	revoked.name, revoked.revoked = "revoked", true
	specs = append(specs, revoked)
	return specs
}

type m34Snapshot struct {
	closedIntervals map[string]string
	retiredAt       map[string]string
	counts          map[string]int
}

func m34Query(t *testing.T, s *Store, query string, args ...any) map[string]string {
	t.Helper()
	rows, err := s.db.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key string
		var value sql.NullString
		if err := rows.Scan(&key, &value); err != nil {
			t.Fatal(err)
		}
		out[key] = value.String
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func m34Snap(t *testing.T, s *Store) m34Snapshot {
	snap := m34Snapshot{counts: map[string]int{}}
	snap.closedIntervals = m34Query(t, s, `SELECT agent_id||'/'||slot||'/'||family||'/'||protocol||'/'||revision, end_at||'|'||config_until FROM quality_intervals WHERE end_at IS NOT NULL`)
	snap.retiredAt = m34Query(t, s, `SELECT id, retired_at FROM quality_targets WHERE retired_at IS NOT NULL`)
	for _, table := range []string{"quality_raw", "quality_minute", "quality_buckets", "quality_series", "quality_targets", "quality_gaps"} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		snap.counts[table] = n
	}
	return snap
}

func TestMigration34UserTargetsFromSchema33And32(t *testing.T) {
	for _, from32 := range []bool{false, true} {
		name := "from schema 33"
		if from32 {
			name = "from schema 32"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "m34.db")
			s := m34Open(t, path, from32)
			specs := m34Specs()
			ids := map[string]string{}
			revisions := map[string]int64{}
			for i, spec := range specs {
				ids[spec.name] = m34Seed(t, s, spec, i+1)
				revisions[spec.name] = spec.revision
			}
			revisions["no config row"] = 7
			if _, err := s.db.Exec(`UPDATE agent_google_status SET youtube_status='cn'`); err != nil {
				t.Fatal(err)
			}
			before := m34Snap(t, s)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			migration34Skip = false
			s, err := Open(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if version, err := s.SchemaVersion(context.Background()); err != nil || version < 34 || version != currentSchemaVersion {
				t.Fatalf("version=%d err=%v", version, err)
			}
			// The R23 column is there whichever schema the upgrade started from.
			var premium int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('agent_google_status') WHERE name='youtube_premium_notice'`).Scan(&premium); err != nil || premium != 1 {
				t.Fatalf("R23 column missing: %d %v", premium, err)
			}
			wantRegion := map[string][3]string{
				"province": {"河北", "河北", "河北"}, "city": {"广东", "广东", "广东"}, "unknown province": {"", "", ""}, "unselected": {"", "", ""},
				"paused": {"河北", "河北", "河北"}, "choices region": {"江苏", "", ""}, "lanes region": {"", "", "四川"},
				"enabled without targets": {"", "", ""}, "no config row": {"", "", ""}, "unreadable choices": {"", "", ""},
				"damaged payload": {"河北", "河北", "河北"}, "already retired": {"河北", "河北", "河北"}, "alpine": {"河北", "河北", "河北"}, "revoked": {"河北", "河北", "河北"},
			}
			for name, id := range ids {
				var revision, settings, interval, granted int64
				var enabled int
				var snapshot sql.NullString
				var regionID, choices string
				var lanes sql.NullString
				if err := s.db.QueryRow(`SELECT revision,settings_revision,interval_ms,enabled,targets_granted,capability_snapshot,region_id,choices,v2_lanes FROM quality_config WHERE agent_id=?`, id).Scan(&revision, &settings, &interval, &enabled, &granted, &snapshot, &regionID, &choices, &lanes); err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				old := revisions[name]
				if revision != old+1 || settings != old+1 || interval != 30000 || enabled != 0 || granted != 0 || snapshot.Valid || regionID != "" || lanes.Valid || choices != m34DefaultChoices {
					t.Errorf("%s: config revision=%d settings=%d interval=%d enabled=%d granted=%d legacy=(%q,%v,%q)", name, revision, settings, interval, enabled, granted, regionID, lanes, choices)
				}
				rows, err := s.db.Query(`SELECT slot,kind,region,isp,host,port,slot_enabled,ipv4_tcp,ipv4_icmp,ipv6_tcp,ipv6_icmp,updated_revision FROM quality_slot_settings WHERE agent_id=? ORDER BY CASE slot WHEN 'telecom' THEN 1 WHEN 'unicom' THEN 2 ELSE 3 END`, id)
				if err != nil {
					t.Fatal(err)
				}
				var seen int
				for rows.Next() {
					var slot, kind, region, isp, host string
					var port sql.NullInt64
					var flags [5]int
					var updated int64
					if err := rows.Scan(&slot, &kind, &region, &isp, &host, &port, &flags[0], &flags[1], &flags[2], &flags[3], &flags[4], &updated); err != nil {
						t.Fatal(err)
					}
					if kind != "builtin" || isp != slot || host != "" || port.Valid || flags != [5]int{} || updated != old+1 || region != wantRegion[name][seen] {
						t.Errorf("%s/%s: kind=%s region=%q isp=%q host=%q port=%v flags=%v updated=%d", name, slot, kind, region, isp, host, port, flags, updated)
					}
					seen++
				}
				rows.Close()
				if seen != 3 {
					t.Errorf("%s: %d slot settings", name, seen)
				}
				var open, pointing, enabledSlots, badRevision int
				if err := s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM quality_intervals WHERE agent_id=? AND end_at IS NULL),(SELECT COUNT(*) FROM quality_slots WHERE agent_id=? AND target_id IS NOT NULL),(SELECT COUNT(*) FROM quality_slots WHERE agent_id=? AND enabled<>0),(SELECT COUNT(*) FROM quality_slots WHERE agent_id=? AND revision<>?)`, id, id, id, id, old+1).Scan(&open, &pointing, &enabledSlots, &badRevision); err != nil {
					t.Fatal(err)
				}
				if open != 0 || pointing != 0 || enabledSlots != 0 || badRevision != 0 {
					t.Errorf("%s: open=%d pointing=%d enabled=%d wrongRevision=%d", name, open, pointing, enabledSlots, badRevision)
				}
			}
			// Intervals that were open end with the old measurement revision as config_until.
			var wrong int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM quality_intervals i JOIN quality_config c ON c.agent_id=i.agent_id WHERE i.config_until IS NOT NULL AND i.config_until>c.revision-1`).Scan(&wrong); err != nil || wrong != 0 {
				t.Fatalf("config_until above the old revision: %d %v", wrong, err)
			}
			after := m34Snap(t, s)
			for key, value := range before.closedIntervals {
				if after.closedIntervals[key] != value {
					t.Errorf("closed interval %s changed: %q -> %q", key, value, after.closedIntervals[key])
				}
			}
			for key, value := range before.retiredAt {
				if after.retiredAt[key] != value {
					t.Errorf("retired_at of %s changed: %q -> %q", key, value, after.retiredAt[key])
				}
			}
			if n := m34Query(t, s, `SELECT 'x', COUNT(*) FROM quality_targets WHERE retired_at IS NULL`)["x"]; n != "0" {
				t.Errorf("%s targets are still active, damaged payloads included", n)
			}
			for table, n := range before.counts {
				if after.counts[table] != n {
					t.Errorf("%s: %d -> %d rows", table, n, after.counts[table])
				}
			}
			var status string
			if err := s.db.QueryRow(`SELECT youtube_status FROM agent_google_status LIMIT 1`).Scan(&status); err != nil && !errors.Is(err, sql.ErrNoRows) {
				t.Fatal(err)
			}
			var violations int
			rows, err := s.db.Query(`PRAGMA foreign_key_check`)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				violations++
			}
			rows.Close()
			if violations != 0 {
				t.Fatalf("%d foreign key violations", violations)
			}
		})
	}
}

// Both starting points give the same network quality result.
func TestMigration34StartsFromEitherSchemaWithTheSameResult(t *testing.T) {
	dump := func(from32 bool) string {
		path := filepath.Join(t.TempDir(), "same.db")
		s := m34Open(t, path, from32)
		for i, spec := range m34Specs() {
			m34Seed(t, s, spec, i+1)
		}
		s.Close()
		migration34Skip = false
		s, err := Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		var out []string
		for _, query := range []string{
			`SELECT revision||'/'||settings_revision||'/'||enabled||'/'||interval_ms FROM quality_config ORDER BY revision,settings_revision`,
			`SELECT slot||'/'||kind||'/'||region||'/'||isp||'/'||updated_revision FROM quality_slot_settings ORDER BY updated_revision,slot,region`,
			`SELECT slot||'/'||family||'/'||revision||'/'||IFNULL(config_until,'') FROM quality_intervals ORDER BY revision,slot,family`,
		} {
			rows, err := s.db.Query(query)
			if err != nil {
				t.Fatal(err)
			}
			var lines []string
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					t.Fatal(err)
				}
				lines = append(lines, line)
			}
			rows.Close()
			out = append(out, strings.Join(lines, ";"))
		}
		return strings.Join(out, "\n")
	}
	if a, b := dump(false), dump(true); a != b {
		t.Fatalf("results differ:\n%s\n--\n%s", a, b)
	}
}

// A failure at any stage leaves the database as it was (the whole migrate() is one transaction),
// and a second start retries from the beginning without counting the revision twice.
func TestMigration34FailureInjectionAndRetry(t *testing.T) {
	for _, from32 := range []bool{false, true} {
		for _, stage := range []string{"structure", "devices", "validation"} {
			t.Run(fmt.Sprintf("from32=%t/%s", from32, stage), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "fail.db")
				s := m34Open(t, path, from32)
				var ids []string
				for i, spec := range m34Specs() {
					ids = append(ids, m34Seed(t, s, spec, i+1))
				}
				startVersion := 33
				if from32 {
					startVersion = 32
				}
				before := m34Snap(t, s)
				var openBefore int
				if err := s.db.QueryRow(`SELECT COUNT(*) FROM quality_intervals WHERE end_at IS NULL`).Scan(&openBefore); err != nil || openBefore == 0 {
					t.Fatalf("fixture has no open intervals: %d %v", openBefore, err)
				}
				s.Close()
				migration34Skip = false
				injected := errors.New("injected migration failure")
				migration34FailAt = func(at string) error {
					if at == stage {
						return injected
					}
					return nil
				}
				t.Cleanup(func() { migration34FailAt = nil })
				if _, err := Open(context.Background(), path); !errors.Is(err, injected) {
					t.Fatalf("Open error = %v", err)
				}
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				var version int
				var tables, premium, openAfter, configs int
				if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
					t.Fatal(err)
				}
				db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='quality_slot_settings'`).Scan(&tables)
				db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('agent_google_status') WHERE name='youtube_premium_notice'`).Scan(&premium)
				db.QueryRow(`SELECT COUNT(*) FROM quality_intervals WHERE end_at IS NULL`).Scan(&openAfter)
				db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('quality_config') WHERE name='settings_revision'`).Scan(&configs)
				db.Close()
				wantPremium := 1
				if from32 {
					wantPremium = 0
				}
				if version != startVersion || tables != 0 || configs != 0 || premium != wantPremium || openAfter != openBefore {
					t.Fatalf("failed migration changed the database: version=%d slot_settings=%d columns=%d premium=%d open=%d", version, tables, configs, premium, openAfter)
				}
				migration34FailAt = nil
				s, err = Open(context.Background(), path)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				if version, err := s.SchemaVersion(context.Background()); err != nil || version != currentSchemaVersion {
					t.Fatalf("retry version=%d err=%v", version, err)
				}
				var revision, settings int64
				if err := s.db.QueryRow(`SELECT revision,settings_revision FROM quality_config WHERE agent_id=?`, ids[0]).Scan(&revision, &settings); err != nil || revision != 6 || settings != 6 {
					t.Fatalf("revision counted twice: %d/%d err=%v", revision, settings, err)
				}
				after := m34Snap(t, s)
				for table, n := range before.counts {
					if after.counts[table] != n {
						t.Fatalf("%s: %d -> %d", table, n, after.counts[table])
					}
				}
			})
		}
	}
}

func TestMigration34IsNotRepeatedAndNewerSchemasAreRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repeat.db")
	s, id, _ := testStore(t, path)
	var revision int64
	if _, err := s.db.Exec(`INSERT INTO quality_config(agent_id,revision,enabled,ipv6,choices,updated_at,settings_revision) VALUES(?,9,0,0,?,1,9)`, id, m34DefaultChoices); err != nil {
		t.Fatal(err)
	}
	s.Close()
	for i := 0; i < 2; i++ {
		var err error
		s, err = Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow(`SELECT revision FROM quality_config WHERE agent_id=?`, id).Scan(&revision); err != nil || revision != 9 {
			t.Fatalf("restart %d changed the revision: %d %v", i, revision, err)
		}
		s.Close()
	}
	// A binary that knows only schema 33 has no way to open this database: it refuses newer versions.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(?,1)`, currentSchemaVersion+1); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(context.Background(), path); !errors.Is(err, ErrUnsupportedSchemaVersion) {
		t.Fatalf("newer schema accepted: %v", err)
	}
}
