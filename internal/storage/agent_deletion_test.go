package storage

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
)

func TestDeleteOfflineAgentErasesHistory(t *testing.T) {
	s := organizationStore(t)
	ctx := t.Context()
	now := time.Unix(2000, 0)
	id := strings.Repeat("a", 32)
	other := strings.Repeat("b", 32)
	enrollment, token := strings.Repeat("c", 43), strings.Repeat("d", 43)
	if _, err := s.CreateAgentEnrollmentToken(ctx, strings.Repeat("c", 32), "全部历史", auth.Hash(enrollment), now); err != nil {
		t.Fatal(err)
	}
	if err := s.RedeemAgentEnrollmentToken(ctx, auth.Hash(enrollment), "全部历史", id, auth.Hash(token), now); err != nil {
		t.Fatal(err)
	}
	organizationAgent(t, s, other)
	if _, err := s.db.Exec(`INSERT INTO server_local_agent VALUES(1,?)`, other); err != nil {
		t.Fatal(err)
	}
	report := validReport(id, 1, "session", "boot", 1, 100, 200)
	report.CollectedAt = now.UnixMilli()
	if _, accepted, reason, err := s.ProcessReport(ctx, id, report, now); err != nil || !accepted {
		t.Fatal(accepted, reason, err)
	}
	// Seed storage-level rows so the test covers every FK, including transient
	// and derived rows whose normal producers have independent test fixtures.
	fixtures := []string{
		`INSERT INTO agent_country_code_lookups(agent_id,operation_id,status,requested_epoch,requested_session_id,active_epoch,active_session_id,created_at,updated_at) VALUES(?1,?1,'requested',1,'session',1,'session',1,1)`,
		`INSERT OR REPLACE INTO agent_google_status_capabilities VALUES(?1,1,1)`,
		`INSERT INTO agent_google_status(agent_id,youtube_status,search_status,signin_status,gemini_status,checked_at,unknown_streak,next_due_at) VALUES(?1,'not_cn','ok','reachable','available',1,0,10)`,
		`INSERT OR REPLACE INTO agent_management_capabilities VALUES(?1,1,1,1,'session',1)`,
		`INSERT INTO groups(id,name,name_key,sort_order,revision) VALUES(?2,'group','group',0,1)`,
		`INSERT OR REPLACE INTO agent_organization VALUES(?1,?2,1)`,
		`INSERT INTO tags(id,name,name_key,revision) VALUES(?2,'tag','tag',1)`,
		`INSERT INTO agent_tags VALUES(?1,?2)`,
		`INSERT INTO agent_outbound_snapshots(agent_id,available,payload_json,checked_at) VALUES(?1,1,'{}',1)`,
		`INSERT INTO agent_plan_config(agent_id,revision) VALUES(?1,?2)`,
		`INSERT INTO agent_plan_renewal_requests VALUES(?1,?1,'apply',printf('%064d',0),'renewal_date','2026-01-01','2026-02-01',?1,?2,?1,1,2,3)`,
		`INSERT INTO agent_plans(agent_id,traffic_mode,quota_value,quota_unit,quota_bytes,updated_at) VALUES(?1,'sum','1','GB',1000000000,1)`,
		`INSERT INTO agent_presence_anchor VALUES(?1,'online')`,
		`INSERT INTO agent_public_egress VALUES(?1,'{}')`,
		`INSERT OR REPLACE INTO agent_public_egress_control(agent_id,enabled) VALUES(?1,1)`,
		`INSERT INTO agent_removal_operations(operation_id,agent_id,status,requested_epoch,requested_session_id,active_epoch,active_session_id,created_at,updated_at) VALUES(?1,?1,'requested',1,'session',1,'session',1,1)`,
		`INSERT INTO agent_security_batches(agent_id,batch_id,content_hash,epoch,session_id,window_start,window_end,collected_at,status,total_events,source_count,payload_json,received_at) VALUES(?1,'batch',zeroblob(32),1,'session',1,2,2,'complete',1,1,'{}',2)`,
		`INSERT OR REPLACE INTO agent_security_capabilities(agent_id,supported,status,epoch,session_id,updated_at) VALUES(?1,1,'complete',1,'session',2)`,
		`INSERT INTO agent_startup_disk_failures VALUES(?1,1)`,
		`INSERT INTO agent_system_check_settings VALUES(?1,'{}')`,
		`INSERT INTO agent_system_check_ignores VALUES(?1,'dns','127.0.0.53',1)`,
		`INSERT INTO agent_traffic_baselines VALUES(?1,1,1,1,?1)`,
		`INSERT INTO agent_traffic_reset_requests VALUES(?1,?1,1,1,1)`,
		`INSERT INTO agent_upgrade_operations(operation_id,agent_id,from_version,target_version,status,created_at,updated_at) VALUES(?1,?1,'v1.4.0','v1.4.1','succeeded',1,1)`,
		`INSERT INTO agent_youtube_region_manual VALUES(?1,'US',1,1)`,
		`INSERT INTO probe_schedules(id,agent_id,name,probe_type,config_json,timeout_ms,interval_seconds,next_run_at,created_at,updated_at) VALUES(?1,?1,'test','tcp_connect','{}',100,30,1,1,1)`,
		`INSERT INTO probe_jobs(id,schedule_id,agent_id,probe_type,config_json,timeout_ms,created_at,scheduled_for,not_before,expires_at,status) VALUES(?1,?1,?1,'tcp_connect','{}',100,1,1,1,2,'finished')`,
		`INSERT INTO probe_results(job_id,attempt,received_at,agent_epoch,session_id,agent_started_at,agent_finished_at,duration_ms,success,payload_json,result_hash) VALUES(?1,1,1,1,'session',1,1,0,1,'{}',zeroblob(32))`,
		`INSERT INTO quality_alert_cursor VALUES(?1,1)`,
		`INSERT INTO quality_alert_history(agent_id,slot,family,protocol,target_id,revision,start_at,end_at) VALUES(?1,'slot','ipv4','tcp',?1,1,1,2)`,
		`INSERT OR REPLACE INTO quality_capability(agent_id,epoch,session_id,supported) VALUES(?1,1,'session',1)`,
		`INSERT INTO quality_clock_estimates VALUES(?1,1,'session',1,1,1)`,
		`INSERT OR REPLACE INTO quality_config(agent_id,revision,enabled,ipv6,choices,updated_at) VALUES(?1,1,1,0,'{}',1)`,
		`INSERT INTO quality_gaps(agent_id,epoch,session_id,start_at,end_at,reason,dropped) VALUES(?1,1,'session',1,2,'buffer_full',1)`,
		`INSERT INTO quality_targets(id,agent_id,fingerprint,payload,created_at) VALUES(?1,?1,'target','{}',1)`,
		`INSERT INTO quality_intervals VALUES(?1,'slot','ipv4','tcp',1,?1,1,NULL,1,2,1)`,
		`INSERT INTO quality_minute VALUES(?1,?1,1,1,1,1,1,1,1)`,
		`INSERT INTO quality_raw(id,agent_id,target_id,epoch,session_id,seq,slot_revision,config_revision,sample_time,received_at,scheduled_at,finished_at,duration_ms,outcome,resolved_ip,raw_sent,raw_received,content_hash,attempt,success,packet_sent,packet_received) VALUES(601,?1,?1,1,'session',1,1,1,1,1,1,1,0,'success','192.0.2.1',0,0,zeroblob(32),1,1,0,0)`,
		`INSERT INTO quality_series VALUES(602,?1,?1,'semantic')`,
		`INSERT INTO quality_sessions VALUES(603,?1,1,'session')`,
		`INSERT INTO quality_facts(raw_id,target_id,series_id,session_key,seq,sample_time,config_revision,slot_revision,resolved_ip,eligible,interval_ms) VALUES(601,?1,602,603,1,1,1,1,'192.0.2.1',1,30000)`,
		`INSERT INTO quality_receipts VALUES(603,1,602,1,1,1,zeroblob(32),1)`,
		`INSERT INTO quality_retired VALUES(603,1,1)`,
		`INSERT INTO quality_buckets VALUES(602,3600000,0,zeroblob(32))`,
		`INSERT OR IGNORE INTO quality_fleet_dirty(series_id,day_start) VALUES(602,0)`,
		`INSERT INTO quality_fleet_rollups VALUES(602,86400000,0,zeroblob(32),NULL,0,zeroblob(32))`,
		`INSERT INTO quality_slot_settings(agent_id,slot,kind,updated_revision) VALUES(?1,'slot','custom',1)`,
		`INSERT INTO quality_slots VALUES(?1,'slot','ipv4','tcp',1,?1,1)`,
	}
	for _, fixture := range fixtures {
		args := []any{id}
		if strings.Contains(fixture, "?2") {
			args = append(args, other)
		} else if !strings.Contains(fixture, "?1") {
			args = nil
		}
		if _, err := s.db.Exec(fixture, args...); err != nil {
			t.Fatalf("seed %s: %v", fixture, err)
		}
	}
	direct := deletionTableNames(t, s, `SELECT m.name FROM sqlite_schema m JOIN pragma_table_info(m.name) p ON p.name='agent_id'
		WHERE m.type='table' AND m.name<>'server_local_agent'`)
	indirect := []string{"probe_results", "quality_buckets", "quality_facts", "quality_fleet_dirty", "quality_fleet_rollups", "quality_receipts", "quality_retired"}
	checkRows := func(want bool) {
		for _, table := range direct {
			if got := countRows(t, s, `SELECT count(*) FROM "`+table+`" WHERE agent_id='`+id+`'`); (got > 0) != want {
				t.Errorf("%s: got %d rows for device, want rows=%t", table, got, want)
			}
		}
		for _, table := range indirect {
			if got := countRows(t, s, `SELECT count(*) FROM "`+table+`"`); (got > 0) != want {
				t.Errorf("%s: got %d derived rows, want rows=%t", table, got, want)
			}
		}
	}
	checkRows(true)
	offline := now.Add(time.Minute)
	if err := s.DeleteOfflineAgent(ctx, id, "全部历史", now, 30*time.Second); !errors.Is(err, ErrAgentOnline) {
		t.Fatalf("delete online: %v", err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER abort_agent_delete BEFORE DELETE ON agents BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOfflineAgent(ctx, id, "全部历史", offline, 30*time.Second); err == nil {
		t.Fatal("injected delete succeeded")
	}
	checkRows(true)
	if gotID, err := s.Authenticate(ctx, token); err != nil || gotID != id {
		t.Fatalf("rollback lost credential: %s %v", gotID, err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER abort_agent_delete`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOfflineAgent(ctx, id, "全部历史", offline, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	checkRows(false)
	if got, err := s.ServerLocalAgent(ctx); err != nil || got != other {
		t.Fatalf("unrelated local Agent = %q, err = %v", got, err)
	}
	queries := deletionTableNames(t, s, `SELECT 'SELECT count(*) FROM '||printf('"%w"',m.name)||' WHERE '||printf('"%w"',p.name)||'=?' FROM sqlite_schema m JOIN pragma_table_info(m.name) p WHERE m.type='table'`)
	for _, query := range queries {
		var found int
		if err := s.db.QueryRow(query, id).Scan(&found); err != nil || found != 0 {
			t.Fatalf("database scan: %s, count=%d, err=%v", query, found, err)
		}
	}
	for _, q := range []string{`SELECT count(*) FROM pragma_foreign_key_check`, `SELECT count(*) FROM quality_totals WHERE value<>0`} {
		if got := countRows(t, s, q); got != 0 {
			t.Errorf("%s: %d", q, got)
		}
	}
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrAgentDeleted) {
		t.Fatalf("old credential: %v", err)
	}
	if err := s.RedeemAgentEnrollmentToken(ctx, auth.Hash(enrollment), "全部历史", id, auth.Hash(token), offline); !errors.Is(err, ErrEnrollmentInvalid) {
		t.Fatalf("enrollment replay: %v", err)
	}
	for _, table := range []string{"agents", "groups", "tags"} {
		if got := countRows(t, s, fmt.Sprintf(`SELECT count(*) FROM %s WHERE id='%s'`, table, other)); got != 1 {
			t.Errorf("unrelated row in %s: %d", table, got)
		}
	}
}

func TestMigration48PreservesExistingIgnores(t *testing.T) {
	s := organizationStore(t)
	ctx := t.Context()
	id := strings.Repeat("a", 32)
	organizationAgent(t, s, id)
	if _, err := s.db.Exec(`INSERT INTO agent_system_check_ignores VALUES (?,'time','time',123);
		DROP TABLE deleted_agent_credentials;
		ALTER TABLE agents DROP COLUMN paused_contact_at;
		DELETE FROM schema_migrations WHERE version=48`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	key := protocol.SystemCheckKey{Kind: "dns", Item: "::1"}
	if changed, err := s.ChangeSystemCheckIgnore(ctx, id, key, true, time.Now()); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if ignores, err := s.SystemCheckIgnores(ctx, id); err != nil || !ignores[key] || !ignores[protocol.SystemCheckKey{Kind: "time", Item: "time"}] {
		t.Fatal(ignores, err)
	}
}

func deletionTableNames(t *testing.T, s *Store, query string) []string {
	t.Helper()
	rows, err := s.db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return names
}
