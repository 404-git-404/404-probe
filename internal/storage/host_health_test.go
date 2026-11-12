package storage

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func TestMigration37RollsBackAndRetries(t *testing.T) {
	ctx := context.Background()
	for _, phase := range []string{"host_health_column", "youtube_region_table"} {
		t.Run(phase, func(t *testing.T) {
			s, id, _ := testStore(t, filepath.Join(t.TempDir(), "schema36.db"))
			defer s.Close()
			if _, ok, reason, err := s.ProcessReport(ctx, id, validReport(id, 1, "session", "boot", 1, 1, 2), time.Now()); !ok || err != nil {
				t.Fatalf("seed report: %s %v", reason, err)
			}
			rewindHostHealthFixture(t, s.db)
			before := m35Fingerprint(t, s.db)
			boom := errors.New("injected migration 37 failure")
			migration37FailAt = func(stage string) error {
				if stage == phase {
					return boom
				}
				return nil
			}
			t.Cleanup(func() { migration37FailAt = nil })
			if err := s.migrate(ctx); !errors.Is(err, boom) {
				t.Fatalf("expected rollback: %v", err)
			}
			if version, err := s.SchemaVersion(ctx); err != nil || version != 36 || !reflect.DeepEqual(before, m35Fingerprint(t, s.db)) {
				t.Fatalf("rollback changed schema or rows: version=%d err=%v", version, err)
			}
			migration37FailAt = nil
			if err := s.migrate(ctx); err != nil {
				t.Fatal(err)
			}
			if version, err := s.SchemaVersion(ctx); err != nil || version != 37 {
				t.Fatalf("schema=%d err=%v", version, err)
			}
			if _, err := s.db.Exec(`UPDATE agent_state SET host_health_json='invalid' WHERE agent_id=?`, id); err == nil {
				t.Fatal("accepted invalid JSON")
			}
			for _, code := range []string{"us", "US1", "1A", "中"} {
				if _, err := s.db.Exec(`INSERT INTO agent_youtube_region_manual VALUES(?,?,0,1)`, id, code); err == nil {
					t.Fatalf("accepted region %q", code)
				}
			}
			if _, err := s.db.Exec(`INSERT INTO agent_youtube_region_manual VALUES(?,'US',0,1)`, id); err != nil {
				t.Fatal(err)
			}
			for _, partial := range []string{`ALTER TABLE agent_state DROP COLUMN host_health_json`, `DROP TABLE agent_youtube_region_manual`} {
				if _, err := s.db.Exec(`DELETE FROM schema_migrations WHERE version=37; ` + partial); err != nil {
					t.Fatal(err)
				}
				if err := s.migrate(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.db.Exec(`INSERT INTO agent_youtube_region_manual VALUES(?,'JP',0,1)`, id); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`DELETE FROM agents WHERE id=?`, id); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := s.db.QueryRow(`SELECT count(*) FROM agent_youtube_region_manual`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("foreign key cascade: count=%d err=%v", count, err)
			}
		})
	}
}

func TestHostHealthPersistsFaultHistoryAndLastSystemName(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, id, _ := testStore(t, path)
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	now := time.Now()
	var sequence uint64
	post := func(osName string, health *protocol.HostHealth) State {
		t.Helper()
		sequence++
		r := validReport(id, 1, "session", "boot", sequence, 1, 2)
		r.OS, r.HostHealth, r.CollectedAt = osName, health, now.UnixMilli()
		state, accepted, reason, err := s.ProcessReport(ctx, id, r, now)
		if !accepted || err != nil {
			t.Fatalf("report %d: %s %v", sequence, reason, err)
		}
		return state
	}
	if got := post("debian 12", nil); got.HostHealth != nil {
		t.Fatal("legacy Agent acquired a synthetic health status")
	}
	if got := post(protocol.UnavailableSystemName, nil); got.OS != "debian 12" || got.HostHealth != nil {
		t.Fatal("negotiation overwrote the stored system name")
	}
	first := now.Add(time.Second).UnixMilli()
	now = time.UnixMilli(first)
	fault := &protocol.HostHealth{OSReadFailed: true, Disk: protocol.DiskHealth{Status: protocol.DiskError,
		Reasons: []protocol.DiskHealthReason{protocol.DiskWriteEIO}, StartedAt: first - 10, CheckedAt: first - 5}}
	if got := post("ignored", fault); got.OS != "debian 12" || got.HostHealth.FirstFaultAt != first || got.HostHealth.LastFaultAt != first {
		t.Fatalf("first fault: %+v", got.HostHealth)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if got := post("", fault); got.OS != "debian 12" || !got.HostHealth.Current.OSReadFailed || got.HostHealth.FirstFaultAt != first || got.HostHealth.LastFaultAt != now.UnixMilli() {
		t.Fatalf("persisted fault: %+v", got.HostHealth)
	}
	last := now.UnixMilli()
	now = now.Add(time.Second)
	if got := post("debian 13", &protocol.HostHealth{Disk: protocol.DiskHealth{Status: protocol.DiskOK, CheckedAt: now.UnixMilli()}}); got.OS != "debian 13" || got.HostHealth.FirstFaultAt != 0 || got.HostHealth.LastFaultAt != last {
		t.Fatalf("recovered fault: %+v", got.HostHealth)
	}
	got, err := s.GetAgentSnapshot(ctx, id, now, time.Minute)
	if err != nil || got.State == nil || got.State.HostHealth.LastFaultAt != last || got.State.OS != "debian 13" {
		t.Fatalf("stored state: %+v %v", got.State, err)
	}
}
